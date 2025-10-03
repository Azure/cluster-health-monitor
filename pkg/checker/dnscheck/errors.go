package dnscheck

import "errors"

const (
	// This is the error code of the DNSChecker's result.
	ErrCodeServiceNotReady = "ServiceNotReady"
	ErrCodePodsNotReady    = "PodsNotReady"
	ErrCodeServiceTimeout  = "ServiceTimeout"
	ErrCodePodTimeout      = "PodTimeout"
	ErrCodeServiceError    = "ServiceError"
	ErrCodePodError        = "PodError"
	ErrCodePodNameMissing  = "PodNameMissing"
	ErrCodeLocalDNSTimeout = "LocalDNSTimeout"
	ErrCodeLocalDNSError   = "LocalDNSError"
)

// This is the error list used by the DNSChecker.
var (
	errServiceNotReady = errors.New("service not ready")
	errPodsNotReady    = errors.New("pods not ready")
)
