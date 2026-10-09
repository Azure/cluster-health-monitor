package gpu

import (
	"context"
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

// preflight decides whether a benchmark can run, returning the SKU profile it is judged against when
// it can, and otherwise the result to report instead. Every benchmark calls this before running its
// tool, so all the reasons one does not run are decided here, in order:
//   - the SKU has no profile, or no threshold for this benchmark: nothing to judge a measurement by
//   - the controller set a skip reason: there are no GPUs to benchmark
//   - the GPU count does not match the profile the thresholds came from
func preflight(ctx context.Context, cfg Config, c Checker) (skuProfile, *checker.Result) {
	profile, ok := profileFor(cfg.SKU)
	if !ok || !c.appliesTo(profile) {
		return skuProfile{}, unsupportedSKU(cfg.SKU)
	}

	if cfg.SkipReason != "" {
		return skuProfile{}, skipped(cfg.SkipReason)
	}

	// The count comes from nvidia-smi rather than the device plugin because it has to be the
	// devices the benchmarks will run on, not what the node advertises.
	count, err := detectGPUCount(ctx, cfg.ToolTimeout)
	if result := evaluateCount(cfg.SKU, count, err); result.Status != checker.StatusHealthy {
		return skuProfile{}, result
	}
	return profile, nil
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
