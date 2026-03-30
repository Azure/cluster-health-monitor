package podstartup

const (
	// This is the error code of the PodStartupCheckers's result.
	ErrCodePodCreationError           = "PodCreationError"
	ErrCodePodCreationTimeout         = "PodCreationTimeout"
	ErrCodePodStartupDurationExceeded = "PodStartupDurationExceeded"
	ErrCodeRequestFailed              = "RequestFailed"
	ErrCodeRequestTimeout             = "RequestTimeout"
	ErrCodeStorageClassNotFound       = "StorageClassNotFound"
)
