// Package gpu runs the intrusive GPU benchmarks as ordinary node checkers. It only works inside
// the GPU image variant, which ships the benchmark binaries and the CUDA runtime.
package gpu

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

const (
	// DefaultToolsDir is where the GPU image places the benchmark binaries.
	DefaultToolsDir = "/usr/local/bin"

	// DefaultToolTimeout bounds a single benchmark. It must stay below the controller's GPU pod
	// timeout so the pod reports its own failure instead of being killed and reported for.
	DefaultToolTimeout = 8 * time.Minute

	// nvidiaSMIPath is injected by the NVIDIA container runtime rather than shipped in the image.
	nvidiaSMIPath = "/usr/bin/nvidia-smi"

	// maxCapturedOutput bounds the per-tool output held in memory.
	maxCapturedOutput = 256 * 1024
)

// Config is shared by the GPU checkers.
type Config struct {
	// SKU is the node's VM size, used to select bandwidth thresholds.
	SKU string
	// GPUCount is the number of GPUs granted to the pod. Zero means unknown, which happens on
	// driver-only pools where no device plugin advertises nvidia.com/gpu.
	GPUCount int
	// ToolsDir holds the benchmark binaries.
	ToolsDir string
	// ToolTimeout bounds each individual benchmark.
	ToolTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.ToolsDir == "" {
		c.ToolsDir = DefaultToolsDir
	}
	if c.ToolTimeout == 0 {
		c.ToolTimeout = DefaultToolTimeout
	}
	return c
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

// Run measures at a single large message size, as AzNHC does. Sweeping up from small sizes drags
// the reported average bus bandwidth far below the achievable peak, making it incomparable to the
// per-SKU threshold. It never returns an error: failures are encoded in the result so the runner
// does not retry an expensive benchmark.
func (c *NCCLChecker) Run(ctx context.Context) (*checker.Result, error) {
	count := c.cfg.GPUCount
	if count < 1 {
		count = detectGPUCount(ctx, c.cfg.ToolTimeout)
		klog.InfoS("Detected GPU count from nvidia-smi", "gpuCount", count)
	}

	// All-reduce bus bandwidth is algbw * 2(n-1)/n, which is zero for a single rank. Running the
	// benchmark anyway would produce a definitionally-zero measurement and a false failure.
	if count < 2 {
		return &checker.Result{
			Status: checker.StatusUnknown,
			Detail: checker.Detail{
				Code:    ErrorCodeInsufficientGPUs,
				Message: fmt.Sprintf("NCCL all-reduce needs at least 2 GPUs, found %d", count),
			},
		}, nil
	}

	output, err := runTool(ctx, c.cfg.ToolsDir+"/all_reduce_perf", c.cfg.ToolTimeout,
		"-b", "16G", "-e", "16G", "-f", "2", "-g", strconv.Itoa(count))
	return parseNCCLResult(output, c.cfg.SKU, err), nil
}

// BandwidthChecker measures host<->device PCIe and device-to-device NVLink read bandwidth,
// mirroring AzNHC check_gpu_bw.
type BandwidthChecker struct {
	cfg Config
}

func NewBandwidthChecker(cfg Config) *BandwidthChecker {
	return &BandwidthChecker{cfg: cfg.withDefaults()}
}

func (c *BandwidthChecker) Name() string {
	return "GpuBandwidth"
}

func (c *BandwidthChecker) Run(ctx context.Context) (*checker.Result, error) {
	output, err := runTool(ctx, c.cfg.ToolsDir+"/nvbandwidth", c.cfg.ToolTimeout,
		"-t", nvbwHostToDevice, nvbwDeviceToHost, nvbwDeviceToDevice, "-i", "10")
	return parseBandwidthResult(output, c.cfg.SKU, err), nil
}

// detectGPUCount counts the GPUs nvidia-smi reports, for pools with no device plugin.
func detectGPUCount(ctx context.Context, timeout time.Duration) int {
	output, err := runTool(ctx, nvidiaSMIPath, timeout, "-L")
	if err != nil {
		klog.ErrorS(err, "Failed to detect GPU count with nvidia-smi")
		return 0
	}

	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "GPU ") {
			count++
		}
	}
	return count
}

// runTool executes a benchmark binary and returns its combined output, bounded to the tail.
// Output is returned even on error; the parsers extract what the tool managed to report.
func runTool(ctx context.Context, path string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	klog.InfoS("Running GPU benchmark", "tool", path, "args", args, "timeout", timeout)

	// Tee to stdout so the raw benchmark output stays visible in the pod logs.
	var buf bytes.Buffer
	sink := io.MultiWriter(&buf, os.Stdout)
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = os.Environ()
	cmd.Stdout = sink
	cmd.Stderr = sink

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("%s exceeded timeout %s", path, timeout)
	}

	output := buf.String()
	if len(output) > maxCapturedOutput {
		output = output[len(output)-maxCapturedOutput:]
	}
	klog.InfoS("GPU benchmark finished", "tool", path, "error", err)
	return output, err
}
