// Package dnscheck provides a checker for DNS.
package dnscheck

import (
	"context"
	"errors"
	"fmt"

	"github.com/miekg/dns"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
)

func Register() {
	checker.RegisterChecker(config.CheckTypeDNS, BuildDNSChecker)
}

const (
	coreDNSNamespace   = "kube-system"
	coreDNSServiceName = "kube-dns"
	resolvConfPath     = "/etc/resolv.conf"
	localDNSIP         = "169.254.10.11"
)

// DNSChecker implements the Checker interface for DNS checks.
type DNSChecker struct {
	name       string
	config     *config.DNSConfig
	kubeClient kubernetes.Interface
	resolver   resolver
}

// BuildDNSChecker creates a new DNSChecker instance.
// If the DNSType is LocalDNS, it checks if LocalDNS IP is enabled before creating the checker.
func BuildDNSChecker(checkerConfig *config.CheckerConfig, kubeClient kubernetes.Interface) (checker.Checker, error) {
	// If this is a LocalDNS checker, check if LocalDNS IP is enabled.
	switch checkerConfig.DNSConfig.Target {
	case config.DNSCheckTargetLocalDNS:
		enabled, err := isLocalDNSEnabled()
		if err != nil {
			klog.ErrorS(err, "Failed to check LocalDNS IP")
			return nil, fmt.Errorf("failed to create LocalDNS checker: %w", err)
		}
		if !enabled {
			klog.InfoS("LocalDNS is not enabled", "name", checkerConfig.Name)
			return nil, checker.ErrSkipChecker
		}
	}

	chk := &DNSChecker{
		name:       checkerConfig.Name,
		config:     checkerConfig.DNSConfig,
		kubeClient: kubeClient,
		resolver:   &defaultResolver{},
	}
	klog.InfoS("Built DNSChecker",
		"name", chk.name,
		"config", chk.config,
	)
	return chk, nil
}

func (c DNSChecker) Name() string {
	return c.name
}

func (c DNSChecker) Type() config.CheckerType {
	return config.CheckTypeDNS
}

func (c DNSChecker) Run(ctx context.Context) {
	switch c.config.Target {
	case config.DNSCheckTargetCoreDNS:
		result, err := c.checkCoreDNS(ctx)
		checker.RecordResult(c, result, err)
		return
	case config.DNSCheckTargetLocalDNS:
		result, err := c.checkLocalDNS(ctx)
		checker.RecordResult(c, result, err)
		return
	case config.DNSCheckTargetCoreDNSPerPod:
		c.checkCoreDNSPerPod(ctx)
	}
}

// checkCoreDNS queries CoreDNS service and pods.
// If all queries succeed, the check is considered healthy.
func (c DNSChecker) checkCoreDNS(ctx context.Context) (*checker.Result, error) {
	// Check CoreDNS service.
	svcIP, err := getCoreDNSSvcIP(ctx, c.kubeClient)
	if errors.Is(err, errServiceNotReady) {
		return checker.Unhealthy(ErrCodeServiceNotReady, "CoreDNS service is not ready"), nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := c.resolver.lookupHost(ctx, svcIP, c.config.Domain, c.config.QueryTimeout); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return checker.Unhealthy(ErrCodeServiceTimeout, "CoreDNS service query timed out"), nil
		}
		return checker.Unhealthy(ErrCodeServiceError, fmt.Sprintf("CoreDNS service query error: %s", err)), nil
	}

	// Check CoreDNS pods.
	dnsEndpoints, err := getCoreDNSEndpoints(ctx, c.kubeClient)
	if errors.Is(err, errPodsNotReady) {
		return checker.Unhealthy(ErrCodePodsNotReady, "CoreDNS Pods are not ready"), nil
	}
	if err != nil {
		return nil, err
	}

	for _, dnsEndpoint := range dnsEndpoints {
		for _, ip := range dnsEndpoint.Addresses {
			if _, err := c.resolver.lookupHost(ctx, ip, c.config.Domain, c.config.QueryTimeout); err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					return checker.Unhealthy(ErrCodePodTimeout, "CoreDNS pod query timed out"), nil
				}
				return checker.Unhealthy(ErrCodePodError, fmt.Sprintf("CoreDNS pod query error: %s", err)), nil
			}
		}
	}

	return checker.Healthy(), nil
}

// checkLocalDNS queries the LocalDNS server.
// If the query succeeds, the check is considered healthy.
func (c DNSChecker) checkLocalDNS(ctx context.Context) (*checker.Result, error) {
	if _, err := c.resolver.lookupHost(ctx, localDNSIP, c.config.Domain, c.config.QueryTimeout); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return checker.Unhealthy(ErrCodeLocalDNSTimeout, "LocalDNS query timed out"), nil
		}
		return checker.Unhealthy(ErrCodeLocalDNSError, fmt.Sprintf("LocalDNS query error: %s", err)), nil
	}

	return checker.Healthy(), nil
}

// checkCoreDNSPods queries CoreDNS pods and return a result for each of the pods.
func (c DNSChecker) checkCoreDNSPerPod(ctx context.Context) {
	endpoints, err := getCoreDNSEndpoints(ctx, c.kubeClient)
	if err != nil {
		// TODO: record error with a separate metric
		klog.Errorf("Failed to get CoreDNS endpoints: %s", err)
		return
	}

	for _, endpoint := range endpoints {
		if endpoint.TargetRef == nil || len(endpoint.TargetRef.Name) == 0 {
			// TODO: record error with a separate metric
			if endpoint.NodeName != nil {
				klog.Errorf("CoreDNS pod name missing in endpoint's targetRef on node %s", *endpoint.NodeName)
			} else {
				klog.Error("CoreDNS pod name missing in endpoint's targetRef")
			}
			continue
		}

		// Query CoreDNS endpoint.
		podname := endpoint.TargetRef.Name
		err := c.queryEndpoint(ctx, endpoint)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				checker.RecordCoreDNSPodResult(c, podname, checker.Unhealthy(ErrCodePodTimeout, "CoreDNS pod query timed out"), nil)
			} else {
				checker.RecordCoreDNSPodResult(c, podname, nil, err)
			}
		} else {
			checker.RecordCoreDNSPodResult(c, podname, checker.Healthy(), nil)
		}
	}
}

func (c DNSChecker) queryEndpoint(ctx context.Context, endpoint discoveryv1.Endpoint) error {
	for _, ip := range endpoint.Addresses {
		if _, err := c.resolver.lookupHost(ctx, ip, c.config.Domain, c.config.QueryTimeout); err != nil {
			return err
		}
	}
	return nil
}

// getCoreDNSSvcIP returns the ClusterIP of the CoreDNS service in the cluster as a DNSTarget.
func getCoreDNSSvcIP(ctx context.Context, kubeClient kubernetes.Interface) (string, error) {
	svc, err := kubeClient.CoreV1().Services(coreDNSNamespace).Get(ctx, coreDNSServiceName, metav1.GetOptions{})

	if err != nil && apierrors.IsNotFound(err) {
		return "", errServiceNotReady
	}
	if err != nil {
		return "", fmt.Errorf("failed to get CoreDNS service: %w", err)
	}

	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == "None" {
		return "", errServiceNotReady
	}

	return svc.Spec.ClusterIP, nil
}

// getCoreDNSEndpoints returns all CoreDNS pod endpoints in the cluster.
func getCoreDNSEndpoints(ctx context.Context, kubeClient kubernetes.Interface) ([]discoveryv1.Endpoint, error) {
	endpointSliceList, err := kubeClient.DiscoveryV1().EndpointSlices(coreDNSNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + coreDNSServiceName,
	})
	if err != nil && apierrors.IsNotFound(err) {
		return nil, errPodsNotReady
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get CoreDNS pod IPs: %w", err)
	}

	var endpoints []discoveryv1.Endpoint
	for _, endpointSlice := range endpointSliceList.Items {
		for _, ep := range endpointSlice.Endpoints {
			// According to Kubernetes docs: "A nil value should be interpreted as 'true'".
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}

			endpoints = append(endpoints, ep)
		}
	}

	if len(endpoints) == 0 {
		return nil, errPodsNotReady
	}

	return endpoints, nil
}

// isLocalDNSEnabled reads /etc/resolv.conf and checks if the localDNSIP exists.
func isLocalDNSEnabled() (bool, error) {
	config, err := dns.ClientConfigFromFile(resolvConfPath)
	if err != nil {
		return false, fmt.Errorf("failed to parse %s: %w", resolvConfPath, err)
	}

	for _, server := range config.Servers {
		if server == localDNSIP {
			return true, nil
		}
	}

	return false, nil
}
