package gpu

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"k8s.io/klog/v2"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

const (
	// mpirunPath is where the GPU image installs OpenMPI.
	mpirunPath = "/opt/openmpi/bin/mpirun"

	// topoDir is where the GPU image places the NCCL topology files.
	topoDir = "/usr/local/share/topofiles"
)

// ncclReport is what the results are unmarshaled into. Fields we do not read are omitted.
type ncclReport struct {
	Results []struct {
		Size int64 `json:"size"`
	} `json:"results"`
	OutOfBounds struct {
		Count int `json:"count"`
	} `json:"out_of_bounds"`
	AverageBusBandwidth struct {
		Bandwidth float64 `json:"bandwidth"`
	} `json:"average_bus_bandwidth"`
}

// NCCLChecker measures all-reduce collective bandwidth and correctness across the node's GPUs.
type NCCLChecker struct {
	cfg Config
}

func NewNCCLChecker(cfg Config) *NCCLChecker {
	return &NCCLChecker{cfg: cfg.withDefaults()}
}

func (c *NCCLChecker) Name() string {
	return "NcclAllReduce"
}

// Run runs the all-reduce benchmark and maps its report to a check result.
func (c *NCCLChecker) Run(ctx context.Context) (*checker.Result, error) {
	// Nothing to judge the measurement against, so the benchmark does not make sense to run.
	profile, ok := profileFor(c.cfg.SKU)
	if !ok || profile.NcclBusGBps == 0 {
		return unknownSKU(c.cfg.SKU), nil
	}

	// GPU preflight checker is expected to have already confirmed the node exposes this many GPUs.
	gpuCount := profile.ExpectedGPUs

	// All-reduce bus bandwidth is algbw * 2(n-1)/n, which is zero for a single rank. Running the
	// benchmark anyway would produce a definitionally-zero measurement and a false failure.
	if gpuCount < 2 {
		return &checker.Result{
			Status: checker.StatusUnknown,
			Detail: checker.Detail{
				Code:    ErrorCodeInsufficientGPUs,
				Message: fmt.Sprintf("NCCL all-reduce needs at least 2 GPUs, %s has %d", c.cfg.SKU, gpuCount),
			},
		}, nil
	}

	// nccl-tests only emits its JSON report to a file. Creating a directory because it cannot overwrite an existing path.
	dir, err := os.MkdirTemp("", "nccl")
	if err != nil {
		return &checker.Result{
			Status: checker.StatusUnknown,
			Detail: checker.Detail{
				Code:    ErrorCodeToolFailed,
				Message: fmt.Sprintf("could not create a directory for the nccl-tests report: %v", err),
			},
		}, nil
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			klog.ErrorS(err, "Could not remove the nccl-tests report directory", "path", dir)
		}
	}()
	reportPath := filepath.Join(dir, "all_reduce.json")

	output, err := runTool(ctx, mpirunPath, c.cfg.ToolTimeout, ncclArgs(gpuCount, reportPath, profile)...)

	report, readErr := os.ReadFile(reportPath)
	if readErr != nil {
		klog.ErrorS(readErr, "Could not read the nccl-tests report", "path", reportPath)
	}
	return parseNCCLResult(string(report), output, c.cfg.SKU, profile, err), nil
}

// ncclArgs sets up the all-reduce in a similar way as AzNHC's check_nccl_allreduce does.
func ncclArgs(gpuCount int, reportPath string, profile skuProfile) []string {
	args := []string{
		// Using "isolated" launcher because every rank runs in this container. OpenMPI otherwise
		// defaults to launching them over ssh, which the distroless image does not ship.
		"-mca", "plm", "isolated",
		"-np", strconv.Itoa(gpuCount),
		"--map-by", fmt.Sprintf("ppr:%d:node", gpuCount),
		"-bind-to", "numa",
		"-mca", "coll_hcoll_enable", "0",
		"-x", "LD_LIBRARY_PATH",
		"-x", "CUDA_DEVICE_ORDER=PCI_BUS_ID",
		"-x", "NCCL_IB_PCI_RELAXED_ORDERING=1",
	}
	if profile.NcclTopoFile != "" {
		args = append(args, "-x", "NCCL_TOPO_FILE="+filepath.Join(topoDir, profile.NcclTopoFile))
	}
	return append(args,
		toolsDir+"/all_reduce_perf",
		"-b", profile.NcclMessageSize, "-e", profile.NcclMessageSize,
		"-f", "2", "-g", "1", "-c", "1",
		"-J", reportPath,
	)
}

// parseNCCLResult maps nccl-tests' JSON report to a check result. The tool's stdout is still
// passed in because a run that dies before writing the report explains itself only there.
func parseNCCLResult(reportJSON, output, sku string, profile skuProfile, execErr error) *checker.Result {
	var report ncclReport
	if err := json.Unmarshal([]byte(reportJSON), &report); err != nil || len(report.Results) == 0 {
		return checker.Unhealthy(ErrorCodeToolFailed, truncateMessage(fmt.Sprintf(
			"nccl-tests produced no report (%s)\n%s", execErrString(execErr), output)))
	}

	busbw := report.AverageBusBandwidth.Bandwidth
	outOfBounds := report.OutOfBounds.Count

	switch {
	case outOfBounds > 0:
		return checker.Unhealthy(ErrorCodeNcclCorrectness,
			fmt.Sprintf("NCCL all-reduce reported %d out-of-bounds values", outOfBounds))
	case busbw < profile.NcclBusGBps:
		return checker.Unhealthy(ErrorCodeNcclLowBandwidth, fmt.Sprintf(
			"bus bandwidth %.3f GB/s below %.3f GB/s threshold for %s", busbw, profile.NcclBusGBps, sku))
	}
	return healthy(fmt.Sprintf("bus bandwidth %.3f GB/s (>= %.3f GB/s threshold for %s)",
		busbw, profile.NcclBusGBps, sku))
}
