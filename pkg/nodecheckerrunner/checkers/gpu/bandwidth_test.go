package gpu

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

// nvbandwidthHostOutput is captured verbatim from the GPU image on Standard_ND96isr_H100_v5, running
// nvbandwidth with the host path's testcases the way the host bandwidth checker does.
func nvbandwidthHostOutput(t *testing.T) string {
	return readTestdata(t, "testdata/nvbandwidth_host_report.json")
}

// nvbandwidthPeerOutput was captured the same way with the peer path's testcases.
func nvbandwidthPeerOutput(t *testing.T) string {
	return readTestdata(t, "testdata/nvbandwidth_peer_report.json")
}

// nvbandwidthErrorStatusOutput was captured the same way with an unrecognized testcase name.
func nvbandwidthErrorStatusOutput(t *testing.T) string {
	return readTestdata(t, "testdata/nvbandwidth_error.json")
}

func readTestdata(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the captured output: %v", err)
	}
	return string(b)
}

func TestParseBandwidthResult(t *testing.T) {
	t.Parallel()

	h100, ok := profileFor(h100SKU)
	if !ok {
		t.Fatalf("profile for SKU %q not found", h100SKU)
	}

	tests := []struct {
		name        string
		output      string
		testcases   []string
		threshold   float64
		execErr     error
		wantStatus  checker.Status
		wantCode    string
		wantMessage string
	}{
		{
			name:        "host path above threshold",
			output:      nvbandwidthHostOutput(t),
			testcases:   hostBandwidth.testcases,
			threshold:   hostBandwidth.threshold(h100),
			wantStatus:  checker.StatusHealthy,
			wantMessage: "device_to_host_memcpy_ce: min 55.120 GB/s at GPU 7 (>= 48.000 GB/s threshold)",
		},
		{
			name:       "peer path above threshold",
			output:     nvbandwidthPeerOutput(t),
			testcases:  peerBandwidth.testcases,
			threshold:  peerBandwidth.threshold(h100),
			wantStatus: checker.StatusHealthy,
			// The device-to-device minimum ignores the N/A diagonal and names the pair.
			wantMessage: "device_to_device_memcpy_read_ce: min 394.967 GB/s at GPU 3 -> GPU 7",
		},
		{
			name:        "one pair below the peer path threshold",
			output:      strings.Replace(nvbandwidthPeerOutput(t), "394.967", "120.00", 1),
			testcases:   peerBandwidth.testcases,
			threshold:   peerBandwidth.threshold(h100),
			wantStatus:  checker.StatusUnhealthy,
			wantCode:    ErrorCodeLowBandwidth,
			wantMessage: "min 120.000 GB/s at GPU 3 -> GPU 7 below 335.000 GB/s threshold",
		},
		{
			name:        "one gpu below the host path threshold",
			output:      strings.Replace(nvbandwidthHostOutput(t), "55.5898", "20.00", 1),
			testcases:   hostBandwidth.testcases,
			threshold:   hostBandwidth.threshold(h100),
			wantStatus:  checker.StatusUnhealthy,
			wantCode:    ErrorCodeLowBandwidth,
			wantMessage: "host_to_device_memcpy_ce: min 20.000 GB/s at GPU 5 below 48.000 GB/s threshold",
		},
		{
			name:        "passing measurements with a dirty exit is a tool failure",
			output:      nvbandwidthHostOutput(t),
			testcases:   hostBandwidth.testcases,
			threshold:   hostBandwidth.threshold(h100),
			execErr:     errors.New("exit status 1"),
			wantStatus:  checker.StatusUnknown,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: "did not exit cleanly (exit status 1)",
		},
		{
			name:        "error status is a tool failure even on a zero exit",
			output:      nvbandwidthErrorStatusOutput(t),
			testcases:   []string{"not_a_real_testcase"},
			threshold:   1,
			wantStatus:  checker.StatusUnknown,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: `not_a_real_testcase: no measurement reported (status "ERROR")`,
		},
		{
			name:        "a requested testcase missing from the report is a tool failure",
			output:      nvbandwidthErrorStatusOutput(t),
			testcases:   hostBandwidth.testcases,
			threshold:   hostBandwidth.threshold(h100),
			wantStatus:  checker.StatusUnknown,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: "host_to_device_memcpy_ce: not reported",
		},
		{
			name:        "output that is not a report is a tool failure",
			output:      "nvbandwidth: symbol lookup error: undefined symbol\n",
			testcases:   hostBandwidth.testcases,
			threshold:   hostBandwidth.threshold(h100),
			wantStatus:  checker.StatusUnknown,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: "nvbandwidth produced no results",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parseBandwidthResult(tt.output, tt.testcases, tt.threshold, tt.execErr)

			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q (message: %s)", got.Status, tt.wantStatus, got.Detail.Message)
			}
			if got.Detail.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", got.Detail.Code, tt.wantCode)
			}
			if tt.wantMessage != "" && !strings.Contains(got.Detail.Message, tt.wantMessage) {
				t.Errorf("Message = %q, want it to contain %q", got.Detail.Message, tt.wantMessage)
			}
		})
	}
}

// A path the SKU has no threshold for must not run, e.g. device-to-device bandwidth on a SKU without NVLink.
func TestBandwidthCheckerWithoutThreshold(t *testing.T) {
	t.Parallel()

	for _, sku := range []string{a10SKU, "unrecognized_sku"} {
		got, err := NewPeerBandwidthChecker(Config{SKU: sku}).Run(context.Background())
		if err != nil {
			t.Fatalf("%s: Run() returned error %v, want nil", sku, err)
		}
		if got.Status != checker.StatusUnknown || got.Detail.Code != ErrorCodeUnknownSKU {
			t.Errorf("%s: result = %q/%q, want %q/%q", sku, got.Status, got.Detail.Code, checker.StatusUnknown, ErrorCodeUnknownSKU)
		}
	}
}

func TestNvbandwidthSlowest(t *testing.T) {
	t.Parallel()

	var testcases []nvbandwidthTestcase
	for _, output := range []string{nvbandwidthHostOutput(t), nvbandwidthPeerOutput(t)} {
		reported, err := parseNvbandwidthReport(output)
		if err != nil {
			t.Fatalf("parseNvbandwidthReport() error = %v", err)
		}
		testcases = append(testcases, reported...)
	}

	want := []struct {
		name string
		cell bwCell
	}{
		{nvbwHostToDevice, bwCell{value: 55.5519, row: 0, col: 3}},
		{nvbwDeviceToHost, bwCell{value: 55.1197, row: 0, col: 7}},
		{nvbwDeviceToDevice, bwCell{value: 394.967, row: 3, col: 7}},
	}
	if len(testcases) != len(want) {
		t.Fatalf("got %d testcases, want %d", len(testcases), len(want))
	}
	for i, w := range want {
		if testcases[i].Name != w.name {
			t.Errorf("testcases[%d].Name = %q, want %q", i, testcases[i].Name, w.name)
		}
		got, ok := testcases[i].slowest()
		if !ok {
			t.Errorf("testcases[%d].slowest() reported no measurement", i)
			continue
		}
		if got != w.cell {
			t.Errorf("testcases[%d].slowest() = %+v, want %+v", i, got, w.cell)
		}
	}
}

// A testcase with nothing measurable must not be mistaken for a zero measurement, which would be
// reported as a bandwidth failure rather than a tool failure.
func TestNvbandwidthSlowestNoMeasurement(t *testing.T) {
	t.Parallel()

	for name, matrix := range map[string][][]string{
		"no matrix":  nil,
		"all N/A":    {{"N/A", "N/A"}},
		"all zeroes": {{"0", "0.00"}},
	} {
		if _, ok := (nvbandwidthTestcase{Matrix: matrix}).slowest(); ok {
			t.Errorf("%s: slowest() reported a measurement", name)
		}
	}
}
