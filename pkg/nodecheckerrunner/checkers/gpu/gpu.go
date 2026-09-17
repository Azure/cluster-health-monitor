// Package gpu runs the intrusive GPU benchmarks as ordinary node checkers. It only works inside
// the GPU image variant, which ships the benchmark binaries and the CUDA runtime libraries.
package gpu

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

const (
	// toolsDir is where the GPU image places the benchmark binaries.
	toolsDir = "/usr/local/bin"

	// defaultToolTimeout bounds a single benchmark. It must stay below the controller's GPU pod
	// timeout so the pod reports its own failure instead of being killed and reported for.
	defaultToolTimeout = 5 * time.Minute

	// Where the NVIDIA container runtime injects nvidia-smi.
	nvidiaSMIPath = "/usr/bin/nvidia-smi"

	// maxCapturedOutput bounds the tool output retained in memory. Only the tail is kept, so a
	// chatty tool cannot grow the checker's heap.
	maxCapturedOutput = 256 * 1024

	// maxResultMessageLen bounds a result message. Most are short summaries, but a tool failure
	// can append the raw output which has no inherent bound.
	maxResultMessageLen = 4000
)

// Config is shared by the GPU checkers.
type Config struct {
	// SKU is the node's VM size, used to select the expected GPU count and bandwidth thresholds.
	SKU string
	// ToolTimeout bounds each individual benchmark.
	ToolTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.ToolTimeout == 0 {
		c.ToolTimeout = defaultToolTimeout
	}
	return c
}

// Checker is the subset of the runner's NodeChecker that this package implements.
type Checker interface {
	Name() string
	Run(ctx context.Context) (*checker.Result, error)
}

// NewCheckers returns the intrusive benchmarks. They assume the node passed NewPreflightChecker,
// so the caller must not run them otherwise.
func NewCheckers(cfg Config) []Checker {
	return []Checker{NewNCCLChecker(cfg), NewBandwidthChecker(cfg)}
}

// Skipped reports a check that a failed preflight kept from running.
func Skipped(reason string) *checker.Result {
	return &checker.Result{
		Status: checker.StatusUnknown,
		Detail: checker.Detail{Code: ErrorCodePreflightFailed, Message: reason},
	}
}

// detectGPUCount counts the GPUs nvidia-smi reports.
func detectGPUCount(ctx context.Context, timeout time.Duration) (int, error) {
	output, err := runTool(ctx, nvidiaSMIPath, timeout, "-L")
	if err != nil {
		klog.ErrorS(err, "Failed to detect GPU count with nvidia-smi")
		return 0, err
	}

	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "GPU ") {
			count++
		}
	}
	klog.InfoS("Detected GPU count from nvidia-smi", "gpuCount", count)
	return count, nil
}

// runTool executes a benchmark binary and returns its stdout and stderr. The output is bounded and the tail is kept when it's too large.
// Output is returned even on error; the parsers extract what the tool managed to report.
func runTool(ctx context.Context, path string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	klog.InfoS("Running GPU benchmark", "tool", path, "args", args, "timeout", timeout)

	out := &tailWriter{max: maxCapturedOutput}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = os.Environ()
	cmd.Stdout = out
	cmd.Stderr = out

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("%s exceeded timeout %s", path, timeout)
	}

	klog.InfoS("GPU benchmark finished", "tool", path, "error", err)
	return out.String(), err
}

// healthy builds a passing result that still carries its measurements.
func healthy(message string) *checker.Result {
	return &checker.Result{
		Status: checker.StatusHealthy,
		Detail: checker.Detail{Message: message},
	}
}

// unknownSKU is returned instead of running a benchmark when the node's SKU has no profile.
func unknownSKU(sku string) *checker.Result {
	return &checker.Result{
		Status: checker.StatusUnknown,
		Detail: checker.Detail{
			Code:    ErrorCodeUnknownSKU,
			Message: fmt.Sprintf("GPU SKU %q is not recognized or supported, so the check did not run", sku),
		},
	}
}

// execErrString describes how a tool exited. Helps differentiate tool runtime errors from crashes.
func execErrString(err error) string {
	if err == nil {
		return "tool exited 0"
	}
	return err.Error()
}

// truncateMessage bounds a message to maxResultMessageLen, keeping the tail.
func truncateMessage(msg string) string {
	if len(msg) > maxResultMessageLen {
		return msg[len(msg)-maxResultMessageLen:]
	}
	return msg
}
