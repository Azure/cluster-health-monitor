package gpu

import (
	"errors"
	"strings"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

const ncclHealthyOutput = `
# Collective test starting: all_reduce_perf
#
#                                                              out-of-place
#       size         count      type   redop    root     time   algbw   busbw #wrong
17179869184    4294967296     float     sum      -1  62123.4  276.55  483.96      0
# Out of bounds values : 0 OK
# Avg bus bandwidth    : 483.96
#
`

const ncclCorruptOutput = `
17179869184    4294967296     float     sum      -1  62123.4  276.55  120.10      3
# Out of bounds values : 3 FAILED
# Avg bus bandwidth    : 120.10
`

const nvbandwidthOutput = `
nvbandwidth Version: v0.10
Built from Git version: v0.10

CUDA Runtime Version: 12060
Device 0: NVIDIA H100 80GB HBM3

Running host_to_device_memcpy_ce.
memcpy CE CPU(row) <-> GPU(column) bandwidth (GB/s)
           0         1
 0     55.17     54.02
SUM host_to_device_memcpy_ce 109.19

Running device_to_host_memcpy_ce.
memcpy CE CPU(row) <-> GPU(column) bandwidth (GB/s)
           0         1
 0     52.11     51.90
SUM device_to_host_memcpy_ce 104.01

Running device_to_device_memcpy_read_ce.
memcpy CE GPU(row) -> GPU(column) bandwidth (GB/s)
           0         1
 0       N/A    340.55
 1    339.87       N/A
SUM device_to_device_memcpy_read_ce 680.42
`

func TestParseNCCLResult(t *testing.T) {
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
			name:        "healthy above threshold",
			output:      ncclHealthyOutput,
			sku:         "Standard_ND96isr_H100_v5",
			wantStatus:  checker.StatusHealthy,
			wantMessage: "483.960 GB/s (>= 460.000 GB/s threshold",
		},
		{
			name:       "bandwidth below threshold",
			output:     strings.ReplaceAll(ncclHealthyOutput, "483.96", "300.00"),
			sku:        "Standard_ND96isr_H100_v5",
			wantStatus: checker.StatusUnhealthy,
			wantCode:   ErrorCodeNcclLowBandwidth,
		},
		{
			name:       "correctness failure wins over bandwidth",
			output:     ncclCorruptOutput,
			sku:        "Standard_ND96isr_H100_v5",
			wantStatus: checker.StatusUnhealthy,
			wantCode:   ErrorCodeNcclCorrectness,
		},
		{
			name:        "unknown sku reports measurement without verdict",
			output:      strings.ReplaceAll(ncclHealthyOutput, "483.96", "10.00"),
			sku:         "Standard_ND96isr_H200_v5",
			wantStatus:  checker.StatusHealthy,
			wantMessage: "no threshold configured",
		},
		{
			name:        "no summary is a tool failure",
			output:      "some early CUDA error\n",
			sku:         "Standard_ND96isr_H100_v5",
			execErr:     errors.New("exit status 1"),
			wantStatus:  checker.StatusUnhealthy,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: "exit status 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNCCLResult(tt.output, tt.sku, tt.execErr)

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

func TestParseBandwidthResult(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		sku         string
		wantStatus  checker.Status
		wantCode    string
		wantMessage string
	}{
		{
			name:       "all testcases above threshold",
			output:     nvbandwidthOutput,
			sku:        "Standard_ND96isr_H100_v5",
			wantStatus: checker.StatusHealthy,
			// The device-to-device minimum ignores the N/A diagonal.
			wantMessage: "device_to_device_memcpy_read_ce: min 339.870 GB/s",
		},
		{
			name:       "one pair below p2p threshold",
			output:     strings.Replace(nvbandwidthOutput, "339.87", "120.00", 1),
			sku:        "Standard_ND96isr_H100_v5",
			wantStatus: checker.StatusUnhealthy,
			wantCode:   ErrorCodeGpuLowBandwidth,
		},
		{
			name:        "unknown sku reports measurements without verdict",
			output:      nvbandwidthOutput,
			sku:         "Standard_ND96isr_H200_v5",
			wantStatus:  checker.StatusHealthy,
			wantMessage: "no threshold configured",
		},
		{
			name:       "no matrices is a tool failure",
			output:     "nvbandwidth Version: v0.10\nno CUDA devices found\n",
			sku:        "Standard_ND96isr_H100_v5",
			wantStatus: checker.StatusUnhealthy,
			wantCode:   ErrorCodeToolFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBandwidthResult(tt.output, tt.sku, nil)

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

func TestParseNvbandwidthMatricesPreservesOrder(t *testing.T) {
	mins, order := parseNvbandwidthMatrices(nvbandwidthOutput)

	wantOrder := []string{nvbwHostToDevice, nvbwDeviceToHost, nvbwDeviceToDevice}
	if len(order) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
	for i, want := range wantOrder {
		if order[i] != want {
			t.Errorf("order[%d] = %q, want %q", i, order[i], want)
		}
	}

	if got := mins[nvbwHostToDevice]; got != 54.02 {
		t.Errorf("host_to_device min = %v, want 54.02", got)
	}
	if got := mins[nvbwDeviceToDevice]; got != 339.87 {
		t.Errorf("device_to_device min = %v, want 339.87", got)
	}
}

func TestNormalizeSKU(t *testing.T) {
	for _, sku := range []string{"Standard_ND96isr_H100_v5", "nd96isr_h100_v5", "STANDARD_ND96ISR_H100_V5"} {
		if _, ok := gpuThresholdsFor(sku); !ok {
			t.Errorf("gpuThresholdsFor(%q) found no thresholds", sku)
		}
	}
	if _, ok := gpuThresholdsFor("Standard_D8d_v5"); ok {
		t.Error("gpuThresholdsFor(Standard_D8d_v5) unexpectedly found thresholds")
	}
}
