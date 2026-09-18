package gpu

import (
	"context"
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

// preflight reports whether the node is fit to benchmark, returning nil when it is. Each benchmark
// calls this before running its tool so it does not report a verdict against a node whose GPUs do
// not match the profile its thresholds came from.
func preflight(ctx context.Context, cfg Config) *checker.Result {
	// The count comes from nvidia-smi rather than the device plugin because it has to be the
	// devices the benchmarks will run on, not what the node advertises.
	count, err := detectGPUCount(ctx, cfg.ToolTimeout)
	if result := evaluateCount(cfg.SKU, count, err); result.Status != checker.StatusHealthy {
		return result
	}
	return nil
}

func evaluateCount(sku string, count int, detectErr error) *checker.Result {
	// A profile without an expected count is a malformed entry rather than a node fault, so it is
	// reported the same way as a SKU we do not recognize.
	profile, ok := profileFor(sku)
	if !ok || profile.ExpectedGPUs == 0 {
		return unsupportedSKU(sku)
	}

	if detectErr != nil {
		return toolFailed(fmt.Sprintf("could not determine GPU count: %v", detectErr))
	}

	if count != profile.ExpectedGPUs {
		return checker.Unhealthy(ErrorCodeUnexpectedGPUCount,
			fmt.Sprintf("found %d of %d GPUs expected for %s", count, profile.ExpectedGPUs, sku))
	}
	return healthy(fmt.Sprintf("found %d of %d GPUs expected for %s", count, profile.ExpectedGPUs, sku))
}
