package gpu

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/samber/lo"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

const (
	// nvbandwidth testcase names, mirroring AzNHC's check_gpu_bw selection.
	nvbwHostToDevice   = "host_to_device_memcpy_ce"
	nvbwDeviceToHost   = "device_to_host_memcpy_ce"
	nvbwDeviceToDevice = "device_to_device_memcpy_read_ce"

	// nvbwStatusPassed is the testcase status nvbandwidth reports for a completed measurement.
	// A testcase it could not run is still listed, with a different status and no matrix.
	nvbwStatusPassed = "Passed"
)

// nvbandwidthReport is the part of nvbandwidth's --format json output that we consume.
type nvbandwidthReport struct {
	Body struct {
		Testcases []nvbandwidthTestcase `json:"testcases"`
	} `json:"nvbandwidth"`
}

type nvbandwidthTestcase struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Matrix cells are strings because nvbandwidth writes "N/A" for pairs it did not measure such as the device-to-device diagonal.
	Matrix [][]string `json:"bandwidth_matrix"`
}

// BandwidthChecker measures host<->device PCIe and device-to-device NVLink read bandwidth, mirroring AzNHC check_gpu_bw.
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
	profile, ok := profileFor(c.cfg.SKU)
	testcases := nvbwTestcasesFor(profile)
	if !ok || len(testcases) == 0 {
		return unknownSKU(c.cfg.SKU), nil
	}

	args := append([]string{"-t"}, testcases...)
	args = append(args, "-i", "10", "--format", "json")
	output, err := runTool(ctx, toolsDir+"/nvbandwidth", c.cfg.ToolTimeout, args...)
	return parseBandwidthResult(output, profile, err), nil
}

// nvbwTestcasesFor returns the testcases that can be evaluated for a given sku.
func nvbwTestcasesFor(profile skuProfile) []string {
	var testcases []string
	if profile.BwPCIeGBps > 0 {
		testcases = append(testcases, nvbwHostToDevice, nvbwDeviceToHost)
	}
	if profile.BwP2PGBps > 0 {
		testcases = append(testcases, nvbwDeviceToDevice)
	}
	return testcases
}

// parseBandwidthResult maps nvbandwidth output to a check result. Test will report unhealthy if any value is below the threshold.
func parseBandwidthResult(output string, profile skuProfile, execErr error) *checker.Result {
	testcases, err := parseNvbandwidthReport(output)
	if err != nil || len(testcases) == 0 {
		return checker.Unhealthy(ErrorCodeToolFailed, truncateMessage(fmt.Sprintf(
			"nvbandwidth produced no results (%s)\n%s", execErrString(execErr), output)))
	}

	lowBandwidth := false
	toolFailed := false
	lines := make([]string, 0, len(testcases))
	for _, testcase := range testcases {
		min, ok := testcase.slowest()
		if testcase.Status != nvbwStatusPassed || !ok {
			toolFailed = true
			lines = append(lines, fmt.Sprintf("%s: no measurement reported (status %q)", testcase.Name, testcase.Status))
			continue
		}
		where := min.where(testcase.Name)
		threshold := nvbwThresholdFor(profile, testcase.Name)

		if min.value < threshold {
			lowBandwidth = true
			lines = append(lines, fmt.Sprintf("%s: min %.3f GB/s%s below %.3f GB/s threshold", testcase.Name, min.value, where, threshold))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: min %.3f GB/s%s (>= %.3f GB/s threshold)", testcase.Name, min.value, where, threshold))
	}

	message := truncateMessage(strings.Join(lines, "\n"))
	switch {
	case toolFailed:
		return checker.Unhealthy(ErrorCodeToolFailed, message)
	case lowBandwidth:
		return checker.Unhealthy(ErrorCodeGpuLowBandwidth, message)
	}
	return healthy(message)
}

// nvbwThresholdFor is the floor for a testcase.
func nvbwThresholdFor(profile skuProfile, testcase string) float64 {
	switch testcase {
	case nvbwHostToDevice, nvbwDeviceToHost:
		return profile.BwPCIeGBps
	case nvbwDeviceToDevice:
		return profile.BwP2PGBps
	}
	return 0
}

// parseNvbandwidthReport decodes nvbandwidth's JSON output.
func parseNvbandwidthReport(output string) ([]nvbandwidthTestcase, error) {
	var report nvbandwidthReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return nil, err
	}
	return report.Body.Testcases, nil
}

// slowest returns the lowest measured cell in the testcase's matrix.
func (t nvbandwidthTestcase) slowest() (bwCell, bool) {
	// Unparseable cells are dropped, which is how the "N/A" device-to-device diagonal is skipped.
	cells := lo.FlatMap(t.Matrix, func(row []string, r int) []bwCell {
		return lo.FilterMap(row, func(cell string, c int) (bwCell, bool) {
			value, err := strconv.ParseFloat(cell, 64)
			return bwCell{value: value, row: r, col: c}, err == nil && value > 0
		})
	})
	if len(cells) == 0 {
		return bwCell{}, false
	}
	return lo.MinBy(cells, func(a, b bwCell) bool { return a.value < b.value }), true
}

// bwCell is one measurement from an nvbandwidth matrix, carrying the row and column it came from
// so a failure can name the devices involved.
type bwCell struct {
	value float64
	row   int
	col   int
}

// where describes the cell. nvbandwidth's host<->device matrices index GPUs by column, while the
// device-to-device matrix indexes them by both.
func (m bwCell) where(testcase string) string {
	if testcase == nvbwDeviceToDevice {
		return fmt.Sprintf(" at GPU %d -> GPU %d", m.row, m.col)
	}
	return fmt.Sprintf(" at GPU %d", m.col)
}
