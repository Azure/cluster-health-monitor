// Package dnscheck provides a checker for DNS.
package dnscheck

import (
	"context"
	"fmt"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

// DNSChecker implements the Checker interface for DNS checks.
type DNSChecker struct {
	name   string
	config *config.DNSConfig
}

const (
	CoreDNSNamespace   = "kube-system"
	CoreDNSServiceName = "kube-dns"
)

// DNSTargetType defines the type of DNS target.
type DNSTargetType string

const (
	CoreDNSService DNSTargetType = "service"
	CoreDNSPod     DNSTargetType = "pod"
)

// DNSTarget represents a DNS target with its IP and type.
type DNSTarget struct {
	IP   string
	Type DNSTargetType
}

// BuildDNSChecker creates a new DNSChecker instance.
func BuildDNSChecker(name string, config *config.DNSConfig) (*DNSChecker, error) {
	if name == "" {
		return nil, fmt.Errorf("checker name cannot be empty")
	}
	if err := config.ValidateDNSConfig(); err != nil {
		return nil, err
	}

	return &DNSChecker{
		name:   name,
		config: config,
	}, nil
}

func (c DNSChecker) Name() string {
	return c.name
}

func (c DNSChecker) Run(ctx context.Context) types.Result {
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return types.Result{
			Status: types.StatusUnknown,
			ErrorDetail: &types.ErrorDetail{
				Code:    "IN_CLUSTER_CONFIG_ERROR",
				Message: fmt.Sprintf("failed to get in-cluster config: %v", err),
			},
		}
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return types.Result{
			Status: types.StatusUnknown,
			ErrorDetail: &types.ErrorDetail{
				Code:    "K8S_CLIENT_ERROR",
				Message: fmt.Sprintf("failed to create Kubernetes client: %v", err),
			},
		}
	}

	coreDNSServiceTarget, err := getCoreDNSServiceIP(ctx, clientset)
	if err != nil {
		return types.Result{
			Status: types.StatusUnknown,
			ErrorDetail: &types.ErrorDetail{
				Code:    "COREDNS_SERVICE_IP_ERROR",
				Message: fmt.Sprintf("failed to get CoreDNS service IP: %v", err),
			},
		}
	}

	coreDNSPodTargets, err := getCoreDNSPodIPs(ctx, clientset)
	if err != nil {
		return types.Result{
			Status: types.StatusUnknown,
			ErrorDetail: &types.ErrorDetail{
				Code:    "COREDNS_POD_IPS_ERROR",
				Message: fmt.Sprintf("failed to get CoreDNS pod IPs: %v", err),
			},
		}
	}

	// TODO: Get LocalDNS IP.

	dnsTargets := make(map[DNSTarget]struct{})
	dnsTargets[coreDNSServiceTarget] = struct{}{}
	for _, target := range coreDNSPodTargets {
		dnsTargets[target] = struct{}{}
	}

	// TODO: Implement the DNS checking logic here
	return types.Result{
		Status: types.StatusUnknown,
		ErrorDetail: &types.ErrorDetail{
			Code:    "NOT_IMPLEMENTED",
			Message: "DNSChecker not implemented yet",
		},
	}
}

// getCoreDNSServiceIP returns the ClusterIP of the CoreDNS service in the cluster as a DNSTarget.
func getCoreDNSServiceIP(ctx context.Context, clientset kubernetes.Interface) (DNSTarget, error) {
	service, err := clientset.CoreV1().Services(CoreDNSNamespace).Get(ctx, CoreDNSServiceName, metav1.GetOptions{})
	if err != nil {
		return DNSTarget{}, fmt.Errorf("failed to get CoreDNS service: %w", err)
	}

	if service.Spec.ClusterIP == "" {
		return DNSTarget{}, fmt.Errorf("CoreDNS service has no ClusterIP")
	}

	return DNSTarget{
		IP:   service.Spec.ClusterIP,
		Type: CoreDNSService,
	}, nil
}

// getCoreDNSPodIPs returns the IPs of all CoreDNS pods in the cluster as DNSTargets.
func getCoreDNSPodIPs(ctx context.Context, clientset kubernetes.Interface) ([]DNSTarget, error) {
	endpointSliceList, err := clientset.DiscoveryV1().EndpointSlices(CoreDNSNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + CoreDNSServiceName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list CoreDNS endpoint slices: %w", err)
	}

	var dnsTargets []DNSTarget
	for _, endpointSlice := range endpointSliceList.Items {
		for _, endpoint := range endpointSlice.Endpoints {
			// According to Kubernetes docs: "A nil value should be interpreted as 'true'".
			if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
				continue
			}

			for _, address := range endpoint.Addresses {
				dnsTargets = append(dnsTargets, DNSTarget{
					IP:   address,
					Type: CoreDNSPod,
				})
			}
		}
	}

	if len(dnsTargets) == 0 {
		return nil, fmt.Errorf("no ready CoreDNS pod endpoints found")
	}

	return dnsTargets, nil
}
