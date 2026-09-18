package gpu

// Error codes for GPU checker results.
const (
	// ErrorCodeToolFailed indicates a benchmark binary produced no usable measurement.
	ErrorCodeToolFailed = "GpuCheckToolFailed"

	// ErrorCodeInsufficientGPUs indicates the node exposed too few GPUs for the check to be meaningful.
	ErrorCodeInsufficientGPUs = "InsufficientGPUs"

	// ErrorCodeUnexpectedGPUCount indicates the node exposed a different number of GPUs than its SKU
	// is expected to have.
	ErrorCodeUnexpectedGPUCount = "UnexpectedGPUCount"

	// ErrorCodeUnknownSKU indicates the node's SKU has no configured profile, so no GPU check can
	// produce a verdict.
	ErrorCodeUnknownSKU = "UnknownGpuSKU"

	// ErrorCodeNcclCorrectness indicates the all-reduce returned incorrect data.
	ErrorCodeNcclCorrectness = "NcclCorrectnessError"

	// ErrorCodeNcclLowBandwidth indicates all-reduce bus bandwidth fell below the SKU's threshold.
	ErrorCodeNcclLowBandwidth = "NcclBandwidthBelowThreshold"

	// ErrorCodeGpuLowBandwidth indicates a memcpy bandwidth fell below the SKU's threshold.
	ErrorCodeGpuLowBandwidth = "GpuBandwidthBelowThreshold"
)
