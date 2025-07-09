// Package dnscheck provides a checker for DNS.
package dnscheck

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/miekg/dns"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
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
	name         string
	config       *config.DNSConfig
	kubeClient   kubernetes.Interface
	resolver     resolver
	queryTimeout time.Duration
}

// BuildDNSChecker creates a new DNSChecker instance.
// If the DNSType is LocalDNS, it checks if LocalDNS IP is enabled before creating the checker.
func BuildDNSChecker(config *config.CheckerConfig, kubeClient kubernetes.Interface) (checker.Checker, error) {
	// If this is a LocalDNS checker, check if LocalDNS IP is enabled.
	if config.DNSConfig.CheckLocalDNS {
		enabled, err := isLocalDNSEnabled()
		if err != nil {
			klog.ErrorS(err, "Failed to check LocalDNS IP")
			return nil, fmt.Errorf("failed to create LocalDNS checker: %w", err)
		}
		if !enabled {
			klog.InfoS("LocalDNS is not enabled", "name", config.Name)
			return nil, checker.ErrSkipChecker
		}
	}

	queryTimeout := config.DNSConfig.QueryTimeout
	if queryTimeout <= 0 {
		queryTimeout = 2 * time.Second // Default timeout
	}

	chk := &DNSChecker{
		name:         config.Name,
		config:       config.DNSConfig,
		kubeClient:   kubeClient,
		resolver:     &defaultResolver{},
		queryTimeout: queryTimeout,
	}
	klog.InfoS("Built DNSChecker",
		"name", chk.name,
		"config", chk.config,
		"queryTimeout", chk.queryTimeout,
	)
	return chk, nil
}

func (c DNSChecker) Name() string {
	return c.name
}

func (c DNSChecker) Type() config.CheckerType {
	return config.CheckTypeDNS
}

// Run executes the DNS check.
// It will check either CoreDNS or LocalDNS for the configured domain.
func (c DNSChecker) Run(ctx context.Context) (*types.Result, error) {
	if c.config.CheckLocalDNS {
		return c.checkLocalDNS(ctx)
	} else {
		return c.checkCoreDNS(ctx)
	}
}

// checkCoreDNS queries CoreDNS service and pods.
// If all queries succeed, the check is considered healthy.
func (c DNSChecker) checkCoreDNS(ctx context.Context) (*types.Result, error) {
	// Check CoreDNS service.
	svcIP, err := getCoreDNSSvcIP(ctx, c.kubeClient)
	if errors.Is(err, errServiceNotReady) {
		return types.Unhealthy(errCodeServiceNotReady, "CoreDNS service is not ready"), nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := c.resolver.lookupHost(ctx, svcIP, c.config.Domain, c.queryTimeout); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.Unhealthy(errCodeServiceTimeout, "CoreDNS service query timed out"), nil
		}
		return types.Unhealthy(errCodeServiceError, fmt.Sprintf("CoreDNS service query error: %s", err)), nil
	}

	// Check CoreDNS pods.
	podIPs, err := getCoreDNSPodIPs(ctx, c.kubeClient)
	if errors.Is(err, errPodsNotReady) {
		return types.Unhealthy(errCodePodsNotReady, "CoreDNS Pods are not ready"), nil
	}
	if err != nil {
		return nil, err
	}

	for _, ip := range podIPs {
		if _, err := c.resolver.lookupHost(ctx, ip, c.config.Domain, c.queryTimeout); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return types.Unhealthy(errCodePodTimeout, "CoreDNS pod query timed out"), nil
			}
			return types.Unhealthy(errCodePodError, fmt.Sprintf("CoreDNS pod query error: %s", err)), nil
		}
	}

	return types.Healthy(), nil
}

// checkLocalDNS queries the LocalDNS server.
// If the query succeeds, the check is considered healthy.
func (c DNSChecker) checkLocalDNS(ctx context.Context) (*types.Result, error) {
	if _, err := c.resolver.lookupHost(ctx, localDNSIP, c.config.Domain, c.queryTimeout); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.Unhealthy(errCodeLocalDNSTimeout, "LocalDNS query timed out"), nil
		}
		return types.Unhealthy(errCodeLocalDNSError, fmt.Sprintf("LocalDNS query error: %s", err)), nil
	}

	return types.Healthy(), nil
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

// getCoreDNSPodIPs returns the IPs of all CoreDNS pods in the cluster as DNSTargets.
func getCoreDNSPodIPs(ctx context.Context, kubeClient kubernetes.Interface) ([]string, error) {
	endpointSliceList, err := kubeClient.DiscoveryV1().EndpointSlices(coreDNSNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + coreDNSServiceName,
	})
	if err != nil && apierrors.IsNotFound(err) {
		return nil, errPodsNotReady
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get CoreDNS pod IPs: %w", err)
	}

	var podIPs []string
	for _, endpointSlice := range endpointSliceList.Items {
		for _, ep := range endpointSlice.Endpoints {
			// According to Kubernetes docs: "A nil value should be interpreted as 'true'".
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}

			podIPs = append(podIPs, ep.Addresses...)
		}
	}

	if len(podIPs) == 0 {
		return nil, errPodsNotReady
	}

	return podIPs, nil
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
