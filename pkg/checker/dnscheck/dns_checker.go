// Package dnscheck provides a checker for DNS.
package dnscheck

import (
	"context"
	"errors"
	"fmt"

	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

func Register() {
	checker.RegisterChecker(config.CheckTypeDNS, BuildDNSChecker)
}

const (
	CoreDNSNamespace   = "kube-system"
	CoreDNSServiceName = "kube-dns"
)

// DNSChecker implements the Checker interface for DNS checks.
type DNSChecker struct {
	name       string
	config     *config.DNSConfig
	kubeClient kubernetes.Interface
	resolver   resolver
}

// BuildDNSChecker creates a new DNSChecker instance.
func BuildDNSChecker(config *config.CheckerConfig) (checker.Checker, error) {
	if err := config.DNSConfig.ValidateDNSConfig(); err != nil {
		return nil, err
	}

	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	return &DNSChecker{
		name:       config.Name,
		config:     config.DNSConfig,
		kubeClient: client,
		resolver:   &defaultResolver{},
	}, nil
}

func (c DNSChecker) Name() string {
	return c.name
}

func (c DNSChecker) Type() config.CheckerType {
	return config.CheckTypeDNS
}

// Run executes the DNS check.
// It queries the CoreDNS service and pods for the configured domain.
// If LocalDNS is configured, it should also query that.
// If all queries succeed, the check is considered healthy.
func (c DNSChecker) Run(ctx context.Context) (*types.Result, error) {
	domain := c.config.Domain

	svcIP, err := getCoreDNSSvcIP(ctx, c.kubeClient)
	if errors.Is(err, errServiceNotReady) {
		return types.Unhealthy(errCodeServiceNotReady, "CoreDNS service is not ready"), nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := c.resolver.lookupHost(ctx, svcIP, domain); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.Unhealthy(errCodeServiceTimeout, "CoreDNS service query timed out"), nil
		}
		return types.Unhealthy(errCodeServiceError, fmt.Sprintf("CoreDNS service query error: %s", err)), nil
	}

	podIPs, err := getCoreDNSPodIPs(ctx, c.kubeClient)
	if errors.Is(err, errPodsNotReady) {
		return types.Unhealthy(errCodePodsNotReady, "CoreDNS Pods are not ready"), nil
	}

	for _, ip := range podIPs {
		if _, err := c.resolver.lookupHost(ctx, ip, domain); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return types.Unhealthy(errCodePodTimeout, "CoreDNS pod query timed out"), nil
			}
			return types.Unhealthy(errCodePodError, fmt.Sprintf("CoreDNS pod query error: %s", err)), nil
		}
	}

	// TODO: Get LocalDNS IP.

	return types.Healthy(), nil
}

// getCoreDNSSvcIP returns the ClusterIP of the CoreDNS service in the cluster as a DNSTarget.
func getCoreDNSSvcIP(ctx context.Context, kubeClient kubernetes.Interface) (string, error) {
	svc, err := kubeClient.CoreV1().Services(CoreDNSNamespace).Get(ctx, CoreDNSServiceName, metav1.GetOptions{})

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
	endpointSliceList, err := kubeClient.DiscoveryV1().EndpointSlices(CoreDNSNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + CoreDNSServiceName,
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
