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

// bandwidthPath is a set of nvbandwidth testcases measured against one of the SKU's thresholds.
type bandwidthPath struct {
	checkerName string
	testcases   []string
	threshold   func(skuProfile) float64
}

var (
	// hostBandwidth covers copies between host memory and each GPU.
	hostBandwidth = bandwidthPath{
		checkerName: HostBandwidthCheckerName,
		testcases:   []string{nvbwHostToDevice, nvbwDeviceToHost},
		threshold:   func(p skuProfile) float64 { return p.BwPCIeGBps },
	}
	// peerBandwidth covers copies between every pair of GPUs.
	peerBandwidth = bandwidthPath{
		checkerName: PeerBandwidthCheckerName,
		testcases:   []string{nvbwDeviceToDevice},
		threshold:   func(p skuProfile) float64 { return p.BwP2PGBps },
	}
)

// BandwidthChecker measures the copy bandwidth of one bandwidthPath, mirroring part of AzNHC check_gpu_bw.
type BandwidthChecker struct {
	cfg  Config
	path bandwidthPath
}

// NewHostBandwidthChecker measures host<->device copy bandwidth.
func NewHostBandwidthChecker(cfg Config) *BandwidthChecker {
	return &BandwidthChecker{cfg: cfg.withDefaults(), path: hostBandwidth}
}

// NewPeerBandwidthChecker measures device<->device copy bandwidth.
func NewPeerBandwidthChecker(cfg Config) *BandwidthChecker {
	return &BandwidthChecker{cfg: cfg.withDefaults(), path: peerBandwidth}
}

func (c *BandwidthChecker) Name() string {
	return c.path.checkerName
}

func (c *BandwidthChecker) appliesTo(profile skuProfile) bool {
	return c.path.threshold(profile) > 0
}

func (c *BandwidthChecker) Run(ctx context.Context) (*checker.Result, error) {
	profile, ok := profileFor(c.cfg.SKU)
	if !ok || !c.appliesTo(profile) {
		return unsupportedSKU(c.cfg.SKU), nil
	}
	if result := preflight(ctx, c.cfg); result != nil {
		return result, nil
	}
	args := append([]string{"-t"}, c.path.testcases...)
	args = append(args, "-i", "10", "--format", "json")
	output, execErr := runTool(ctx, toolsDir+"/nvbandwidth", c.cfg.ToolTimeout, args...)
	return parseBandwidthResult(output, c.path.testcases, c.path.threshold(profile), execErr), nil
}

// parseBandwidthResult maps nvbandwidth output to a check result for the given testcases. It reports
// unhealthy if any measured value is below the threshold, and a tool failure if any of the testcases
// has no measurement.
func parseBandwidthResult(output string, testcases []string, threshold float64, execErr error) *checker.Result {
	reported, err := parseNvbandwidthReport(output)
	if err != nil || len(reported) == 0 {
		return toolFailed(fmt.Sprintf(
			"nvbandwidth produced no results (%s)\n%s", execErrString(execErr), output))
	}
	byName := lo.KeyBy(reported, func(t nvbandwidthTestcase) string { return t.Name })

	lowBandwidth := false
	missingMeasurement := false
	lines := make([]string, 0, len(testcases))
	for _, name := range testcases {
		testcase, found := byName[name]
		if !found {
			missingMeasurement = true
			lines = append(lines, fmt.Sprintf("%s: not reported", name))
			continue
		}
		min, ok := testcase.slowest()
		if testcase.Status != nvbwStatusPassed || !ok {
			missingMeasurement = true
			lines = append(lines, fmt.Sprintf("%s: no measurement reported (status %q)", name, testcase.Status))
			continue
		}
		where := min.where(name)

		if min.value < threshold {
			lowBandwidth = true
			lines = append(lines, fmt.Sprintf("%s: min %.3f GB/s%s below %.3f GB/s threshold", name, min.value, where, threshold))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: min %.3f GB/s%s (>= %.3f GB/s threshold)", name, min.value, where, threshold))
	}

	message := truncateMessage(strings.Join(lines, "\n"))
	switch {
	case missingMeasurement:
		return toolFailed(message)
	case execErr != nil:
		return toolFailed(fmt.Sprintf(
			"nvbandwidth reported measurements but did not exit cleanly (%s)\n%s", execErrString(execErr), message))
	case lowBandwidth:
		return checker.Unhealthy(ErrorCodeLowBandwidth, message)
	}
	return healthy(message)
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
