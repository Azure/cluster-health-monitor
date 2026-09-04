package gpu

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

const (
	// Error codes recorded on GPU check results.
	ErrorCodeToolFailed       = "GpuCheckToolFailed"
	ErrorCodeInsufficientGPUs = "InsufficientGPUs"
	ErrorCodeNcclCorrectness  = "NcclCorrectnessError"
	ErrorCodeNcclLowBandwidth = "NcclBandwidthBelowThreshold"
	ErrorCodeGpuLowBandwidth  = "GpuBandwidthBelowThreshold"

	// nvbandwidth testcase names, mirroring AzNHC's check_gpu_bw selection.
	nvbwHostToDevice   = "host_to_device_memcpy_ce"
	nvbwDeviceToHost   = "device_to_host_memcpy_ce"
	nvbwDeviceToDevice = "device_to_device_memcpy_read_ce"

	// maxResultMessageLen bounds messages well under the CRD's 32768 limit.
	maxResultMessageLen = 4000
)

var (
	// avgBusBandwidthRe matches nccl-tests' summary line, e.g. "# Avg bus bandwidth : 480.18".
	avgBusBandwidthRe = regexp.MustCompile(`Avg bus bandwidth\s*:\s*([0-9]+(?:\.[0-9]+)?)`)
	// outOfBoundsRe matches the correctness summary, e.g. "# Out of bounds values : 0 OK".
	outOfBoundsRe = regexp.MustCompile(`Out of bounds values\s*:\s*([0-9]+)`)
	// nvbwRunningRe matches nvbandwidth's per-testcase banner, e.g. "Running host_to_device_memcpy_ce."
	nvbwRunningRe = regexp.MustCompile(`^Running\s+(\S+?)\.?$`)
)

// gpuThresholds are the per-SKU pass/fail bandwidth floors, mirroring the AzNHC conf values
// (check_nccl_allreduce <nccl>, check_gpu_bw <pcie> <p2p>).
type gpuThresholds struct {
	NcclBusGBps float64 // NCCL all-reduce bus bandwidth
	PCIeGBps    float64 // host<->device memcpy, applies to both directions
	P2PGBps     float64 // device-to-device memcpy read
}

// gpuThresholdsBySKU is keyed by normalizeSKU. SKUs absent from this map are measured and
// reported without a bandwidth verdict.
var gpuThresholdsBySKU = map[string]gpuThresholds{
	// From AzNHC nd96isr_h100_v5.conf: `check_nccl_allreduce 460.0 ...` and `check_gpu_bw 48 335`.
	"nd96isr_h100_v5": {NcclBusGBps: 460.0, PCIeGBps: 48.0, P2PGBps: 335.0},
}

// normalizeSKU lowercases a VM size and strips the "standard_" prefix so both the node label
// form (Standard_ND96isr_H100_v5) and the bare form resolve to the same key.
func normalizeSKU(sku string) string {
	return strings.TrimPrefix(strings.ToLower(sku), "standard_")
}

func gpuThresholdsFor(sku string) (gpuThresholds, bool) {
	v, ok := gpuThresholdsBySKU[normalizeSKU(sku)]
	return v, ok
}

// parseNCCLResult maps nccl-tests output to a check result. A tool that exited non-zero is still
// parsed: a partial run that reported out-of-bounds values is more actionable than the exit code.
func parseNCCLResult(output, sku string, execErr error) *checker.Result {
	thresholds, haveThresholds := gpuThresholdsFor(sku)

	busbw, haveBusbw := 0.0, false
	if m := avgBusBandwidthRe.FindStringSubmatch(output); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			busbw, haveBusbw = v, true
		}
	}

	outOfBounds, haveCorrectness := 0, false
	if m := outOfBoundsRe.FindStringSubmatch(output); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			outOfBounds, haveCorrectness = v, true
		}
	}

	useThreshold := haveThresholds && thresholds.NcclBusGBps > 0

	switch {
	case haveCorrectness && outOfBounds > 0:
		return checker.Unhealthy(ErrorCodeNcclCorrectness,
			fmt.Sprintf("NCCL all-reduce reported %d out-of-bounds values", outOfBounds))
	case !haveBusbw:
		return checker.Unhealthy(ErrorCodeToolFailed, truncateMessage(fmt.Sprintf(
			"nccl-tests produced no bandwidth summary (%s)\n%s", execErrString(execErr), lastLines(output, 20))))
	case useThreshold && busbw < thresholds.NcclBusGBps:
		return checker.Unhealthy(ErrorCodeNcclLowBandwidth, fmt.Sprintf(
			"bus bandwidth %.3f GB/s below %.3f GB/s threshold for %s", busbw, thresholds.NcclBusGBps, sku))
	case useThreshold:
		return healthy(fmt.Sprintf("bus bandwidth %.3f GB/s (>= %.3f GB/s threshold for %s), out-of-bounds values %d",
			busbw, thresholds.NcclBusGBps, sku, outOfBounds))
	default:
		return healthy(fmt.Sprintf("bus bandwidth %.3f GB/s (no threshold configured for %q), out-of-bounds values %d",
			busbw, sku, outOfBounds))
	}
}

// parseBandwidthResult maps nvbandwidth output to a check result. Each testcase matrix is reduced
// to its minimum measured value, matching AzNHC's behavior of failing when any device pair falls
// below expectation.
func parseBandwidthResult(output, sku string, execErr error) *checker.Result {
	thresholds, haveThresholds := gpuThresholdsFor(sku)

	mins, order := parseNvbandwidthMatrices(output)
	if len(order) == 0 {
		return checker.Unhealthy(ErrorCodeToolFailed, truncateMessage(fmt.Sprintf(
			"nvbandwidth produced no results (%s)\n%s", execErrString(execErr), lastLines(output, 20))))
	}

	unhealthy := false
	lines := make([]string, 0, len(order))
	for _, testcase := range order {
		min := mins[testcase]

		threshold := 0.0
		if haveThresholds {
			switch testcase {
			case nvbwHostToDevice, nvbwDeviceToHost:
				threshold = thresholds.PCIeGBps
			case nvbwDeviceToDevice:
				threshold = thresholds.P2PGBps
			}
		}

		switch {
		case threshold > 0 && min < threshold:
			unhealthy = true
			lines = append(lines, fmt.Sprintf("%s: min %.3f GB/s below %.3f GB/s threshold", testcase, min, threshold))
		case threshold > 0:
			lines = append(lines, fmt.Sprintf("%s: min %.3f GB/s (>= %.3f GB/s threshold)", testcase, min, threshold))
		default:
			lines = append(lines, fmt.Sprintf("%s: min %.3f GB/s (no threshold configured for %q)", testcase, min, sku))
		}
	}

	message := truncateMessage(strings.Join(lines, "\n"))
	if unhealthy {
		return checker.Unhealthy(ErrorCodeGpuLowBandwidth, message)
	}
	return healthy(message)
}

// parseNvbandwidthMatrices returns the minimum measured value per testcase, preserving the
// order the testcases were reported in. nvbandwidth prints a banner line, then a column
// header, then a bandwidth matrix whose first column is the row index; "N/A" cells are skipped.
func parseNvbandwidthMatrices(body string) (map[string]float64, []string) {
	mins := map[string]float64{}
	var order []string
	current := ""
	skipColumnHeader := false

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if m := nvbwRunningRe.FindStringSubmatch(trimmed); m != nil {
			current = m[1]
			skipColumnHeader = true
			continue
		}
		if current == "" || strings.Contains(trimmed, "memcpy") || strings.HasPrefix(trimmed, "SUM") ||
			strings.HasPrefix(trimmed, "COEFFICIENT_OF_VARIATION") {
			continue
		}
		if skipColumnHeader {
			skipColumnHeader = false
			continue
		}

		fields := strings.Fields(trimmed)
		for _, f := range fields[1:] {
			v, err := strconv.ParseFloat(f, 64)
			if err != nil || v <= 0 {
				continue
			}
			if cur, ok := mins[current]; !ok || v < cur {
				if !ok {
					order = append(order, current)
				}
				mins[current] = v
			}
		}
	}
	return mins, order
}

// healthy builds a passing result that still carries its measurements.
func healthy(message string) *checker.Result {
	return &checker.Result{
		Status: checker.StatusHealthy,
		Detail: checker.Detail{Message: message},
	}
}

func execErrString(err error) string {
	if err == nil {
		return "tool exited 0"
	}
	return err.Error()
}

// lastLines returns the trailing n non-empty lines of s, for surfacing failure context.
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// truncateMessage bounds a message to maxResultMessageLen, keeping the tail.
func truncateMessage(msg string) string {
	if len(msg) > maxResultMessageLen {
		return msg[len(msg)-maxResultMessageLen:]
	}
	return msg
}
