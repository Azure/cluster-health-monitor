package nodeprovisioning

const (
	// This is the error code of the PodStartupCheckers's result.
	errCodePodCreationError           = "pod_creation_error"
	errCodePodCreationTimeout         = "pod_creation_timeout"
	errCodePodStartupDurationExceeded = "pod_startup_duration_exceeded"
	errCodeRequestFailed              = "request_failed"
	errCodeRequestTimeout             = "request_timeout"
)
