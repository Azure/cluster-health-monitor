package podnetwork

// Error codes for PodNetwork checker results
const (
	// ErrorCodeNetworkConnectivityFailed indicates pod network connectivity issues
	// This covers both pod-to-pod connectivity failures and cluster DNS service failures
	ErrorCodeNetworkConnectivityFailed = "NetworkConnectivityFailed"
)
