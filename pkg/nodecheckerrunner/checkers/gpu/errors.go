package gpu

// Error codes for GPU checker results.
const (
	// ErrorCodeToolFailed indicates a benchmark binary produced no usable measurement.
	ErrorCodeToolFailed = "ToolFailed"

	// ErrorCodeInsufficientGPUs indicates the node exposed too few GPUs for the check to be meaningful.
	ErrorCodeInsufficientGPUs = "InsufficientGPUs"

	// ErrorCodeUnexpectedGPUCount indicates the node exposed a different number of GPUs than its SKU
	// is expected to have.
	ErrorCodeUnexpectedGPUCount = "UnexpectedGPUCount"

	// ErrorCodeUnknownSKU indicates the node's SKU has no configured profile, so no GPU check can
	// produce a verdict.
	ErrorCodeUnknownSKU = "UnknownSKU"

	// ErrorCodeCorrectness indicates a benchmark returned incorrect data.
	ErrorCodeCorrectness = "CorrectnessError"

	// ErrorCodeLowBandwidth indicates a measured bandwidth fell below the SKU's threshold.
	ErrorCodeLowBandwidth = "BandwidthBelowThreshold"
)
