package dnscheck

import "errors"

const (
	// This is the error code of the DNSChecker's result.
	errCodeServiceNotReady = "service_not_ready"
	errCodePodsNotReady    = "pods_not_ready"
	errCodeServiceTimeout  = "service_timeout"
	errCodePodTimeout      = "pod_timeout"
	errCodeServiceError    = "service_error"
	errCodePodError        = "pod_error"
	errCodeLocalDNSTimeout = "local_dns_timeout"
	errCodeLocalDNSError   = "local_dns_error"
)

// This is the error list used by the DNSChecker.
var (
	errServiceNotReady = errors.New("service not ready")
	errPodsNotReady    = errors.New("pods not ready")
)
