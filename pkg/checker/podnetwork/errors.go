package podnetwork

import "errors"

// Error codes for PodNetwork checker results
const (
	// ErrorCodeCoreDNSPodsRetrievalFailed indicates failure to retrieve CoreDNS pods from API server
	ErrorCodeCoreDNSPodsRetrievalFailed = "CoreDNSPodsRetrievalFailed"

	// ErrorCodePodConnectivityFailure indicates pod-to-pod network connectivity issues
	ErrorCodePodConnectivityFailure = "PodConnectivityFailure"

	// ErrorCodeClusterDNSServiceFailure indicates cluster DNS service connectivity issues
	ErrorCodeClusterDNSServiceFailure = "ClusterDNSServiceFailure"

	// ErrorCodeCompleteNetworkFailure indicates both pod-to-pod and cluster DNS connectivity failed
	ErrorCodeCompleteNetworkFailure = "CompleteNetworkFailure"
)

// Predefined error variables
var (
	// ErrNoCoreDNSPods indicates no CoreDNS pods are available for testing
	ErrNoCoreDNSPods = errors.New("no CoreDNS pods available for network testing")

	// ErrAPIServerConnection indicates failure to connect to Kubernetes API server
	ErrAPIServerConnection = errors.New("failed to connect to Kubernetes API server")

	// ErrAllCoreDNSPodsFailed indicates all CoreDNS pod connections failed
	ErrAllCoreDNSPodsFailed = errors.New("all CoreDNS pod connections failed")

	// ErrClusterDNSServiceFailed indicates cluster DNS service query failed
	ErrClusterDNSServiceFailed = errors.New("cluster DNS service query failed")

	// ErrInsufficientCoreDNSPods indicates insufficient CoreDNS pods for conclusive testing
	ErrInsufficientCoreDNSPods = errors.New("insufficient CoreDNS pods for conclusive network testing")

	// ErrDNSQueryTimeout indicates DNS query timed out
	ErrDNSQueryTimeout = errors.New("DNS query timed out")
)