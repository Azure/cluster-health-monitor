package gpu

import (
	"context"
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

// PreflightChecker gates the intrusive benchmarks.
type PreflightChecker struct {
	cfg Config
}

func NewPreflightChecker(cfg Config) *PreflightChecker {
	return &PreflightChecker{cfg: cfg.withDefaults()}
}

func (c *PreflightChecker) Name() string {
	return "GpuPreflight"
}

func (c *PreflightChecker) Run(ctx context.Context) (*checker.Result, error) {
	// The count comes from nvidia-smi rather than the device plugin because it has to be the
	// devices the benchmarks will run on, not what the node advertises.
	count, err := detectGPUCount(ctx, c.cfg.ToolTimeout)
	return evaluateCount(c.cfg.SKU, count, err), nil
}

func evaluateCount(sku string, count int, detectErr error) *checker.Result {
	// A profile without an expected count is a malformed entry rather than a node fault, so it is
	// reported the same way as a SKU we do not recognize.
	profile, ok := profileFor(sku)
	if !ok || profile.ExpectedGPUs == 0 {
		return unknownSKU(sku)
	}

	if detectErr != nil {
		return &checker.Result{
			Status: checker.StatusUnknown,
			Detail: checker.Detail{
				Code:    ErrorCodeToolFailed,
				Message: fmt.Sprintf("could not determine GPU count: %v", detectErr),
			},
		}
	}

	if count < profile.ExpectedGPUs {
		return checker.Unhealthy(ErrorCodeMissingGPUs,
			fmt.Sprintf("found %d of %d GPUs expected for %s", count, profile.ExpectedGPUs, sku))
	}
	return healthy(fmt.Sprintf("found %d of %d GPUs expected for %s", count, profile.ExpectedGPUs, sku))
}
