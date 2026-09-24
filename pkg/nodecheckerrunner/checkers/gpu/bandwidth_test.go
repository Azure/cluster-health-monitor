package gpu

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

// nvbandwidthOutput is a --format json report captured verbatim from the GPU image on Standard_ND96isr_H100_v5.
func nvbandwidthOutput(t *testing.T) string {
	return readTestdata(t, "testdata/nvbandwidth_report.json")
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

func TestNvbwTestcasesFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		profile skuProfile
		want    []string
	}{
		{
			name:    "both thresholds configured returns all testcases",
			profile: skuProfile{BwPCIeGBps: 48, BwP2PGBps: 335},
			want:    []string{nvbwHostToDevice, nvbwDeviceToHost, nvbwDeviceToDevice},
		},
		{
			// Some skus have no p2p threshold configured.
			name:    "no p2p threshold drops the device-to-device testcase",
			profile: skuProfile{BwPCIeGBps: 9},
			want:    []string{nvbwHostToDevice, nvbwDeviceToHost},
		},
		{
			name:    "no floors at all returns no testcases",
			profile: skuProfile{},
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := nvbwTestcasesFor(tt.profile)
			if len(got) != len(tt.want) {
				t.Fatalf("nvbwTestcasesFor() = %v, want %v", got, tt.want)
			}
			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("nvbwTestcasesFor()[%d] = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

func TestParseBandwidthResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		output      string
		sku         string
		execErr     error
		wantStatus  checker.Status
		wantCode    string
		wantMessage string
	}{
		{
			name:       "all testcases above threshold",
			output:     nvbandwidthOutput(t),
			sku:        h100SKU,
			wantStatus: checker.StatusHealthy,
			// The device-to-device minimum ignores the N/A diagonal and names the pair.
			wantMessage: "device_to_device_memcpy_read_ce: min 394.921 GB/s at GPU 2 -> GPU 3",
		},
		{
			name:        "one pair below the p2p threshold",
			output:      strings.Replace(nvbandwidthOutput(t), "394.921", "120.00", 1),
			sku:         h100SKU,
			wantStatus:  checker.StatusUnhealthy,
			wantCode:    ErrorCodeLowBandwidth,
			wantMessage: "min 120.000 GB/s at GPU 2 -> GPU 3 below 335.000 GB/s threshold",
		},
		{

			name:        "one gpu below the pcie threshold",
			output:      strings.Replace(nvbandwidthOutput(t), "55.5542", "20.00", 1),
			sku:         h100SKU,
			wantStatus:  checker.StatusUnhealthy,
			wantCode:    ErrorCodeLowBandwidth,
			wantMessage: "host_to_device_memcpy_ce: min 20.000 GB/s at GPU 5 below 48.000 GB/s threshold",
		},
		{
			name:        "passing measurements with a dirty exit is a tool failure",
			output:      nvbandwidthOutput(t),
			sku:         h100SKU,
			execErr:     errors.New("exit status 1"),
			wantStatus:  checker.StatusUnknown,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: "did not exit cleanly (exit status 1)",
		},
		{
			name:        "error status is a tool failure even on a zero exit",
			output:      nvbandwidthErrorStatusOutput(t),
			sku:         h100SKU,
			wantStatus:  checker.StatusUnknown,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: `not_a_real_testcase: no measurement reported (status "ERROR")`,
		},
		{
			name:        "output that is not a report is a tool failure",
			output:      "nvbandwidth: symbol lookup error: undefined symbol\n",
			sku:         h100SKU,
			wantStatus:  checker.StatusUnknown,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: "nvbandwidth produced no results",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			profile, ok := profileFor(tt.sku)
			if !ok {
				t.Errorf("profile for SKU %q not found", tt.sku)
			}

			got := parseBandwidthResult(tt.output, profile, tt.execErr)

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

func TestNvbandwidthSlowest(t *testing.T) {
	t.Parallel()

	testcases, err := parseNvbandwidthReport(nvbandwidthOutput(t))
	if err != nil {
		t.Fatalf("parseNvbandwidthReport() error = %v", err)
	}

	want := []struct {
		name string
		cell bwCell
	}{
		{nvbwHostToDevice, bwCell{value: 55.5542, row: 0, col: 5}},
		{nvbwDeviceToHost, bwCell{value: 55.162, row: 0, col: 1}},
		{nvbwDeviceToDevice, bwCell{value: 394.921, row: 2, col: 3}},
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
