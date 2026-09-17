package gpu

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

// ncclReportJSON is a -J report captured verbatim from the GPU image on Standard_ND96isr_H100_v5.
func ncclReportJSON(t *testing.T) string {
	t.Helper()

	b, err := os.ReadFile("testdata/nccl_report.json")
	if err != nil {
		t.Fatalf("reading the captured report: %v", err)
	}
	return string(b)
}

// Captured from the GPU image on Standard_ND96isr_H100_v5 (8x H100) under mpirun, with an
// unparseable size argument. Every rank reports the fault, so the message repeats.
const ncclBadArgOutput = `invalid size specified for 'minbytes'
invalid size specified for 'minbytes'
invalid size specified for 'minbytes'
invalid size specified for 'minbytes'
invalid size specified for 'minbytes'
invalid size specified for 'minbytes'
--------------------------------------------------------------------------
Primary job  terminated normally, but 1 process returned
a non-zero exit code. Per user-direction, the job has been aborted.
--------------------------------------------------------------------------
invalid size specified for 'minbytes'
`

// Captured asking each rank for more GPUs than the node has. The counts differ per rank because
// nccl-tests multiplies by the rank index, and the banner is pushed off the top by the failures.
const ncclRankFailureOutput = `# Writing JSON output to /tmp/all_reduce.json
# nccl-tests version 2.19.7 nccl-headers=22304 nccl-library=22304
# Collective test starting: all_reduce_perf
# nThread 1 nGpus 99 minBytes 1048576 maxBytes 1048576 step: 2(factor) warmup iters: 1 iters: 20 agg iters: 1 validation: 1 graph: 0 unalign: 0
#
# Using devices
Invalid number of GPUs: 297 requested but only 8 were found.
 .. nccl-badgpus pid 49: Test failure common.cu:1454
Please check the number of processes and GPUs per process.
--------------------------------------------------------------------------
Primary job  terminated normally, but 1 process returned
a non-zero exit code. Per user-direction, the job has been aborted.
--------------------------------------------------------------------------
 .. nccl-badgpus pid 61: Test failure common.cu:1454
 .. nccl-badgpus pid 50: Test failure common.cu:1454
 .. nccl-badgpus pid 51: Test failure common.cu:1454
 .. nccl-badgpus pid 54: Test failure common.cu:1454
Invalid number of GPUs: 792 requested but only 8 were found.
Please check the number of processes and GPUs per process.
Invalid number of GPUs: 396 requested but only 8 were found.
Please check the number of processes and GPUs per process.
Invalid number of GPUs: 495 requested but only 8 were found.
Please check the number of processes and GPUs per process.
Invalid number of GPUs: 594 requested but only 8 were found.
Please check the number of processes and GPUs per process.
--------------------------------------------------------------------------
mpirun detected that one or more processes exited with non-zero status, thus causing
the job to be terminated. The first process to do so was:

  Process name: [[32573,1],2]
  Exit code:    5
--------------------------------------------------------------------------
`

// Captured requesting more ranks than the mapping allows. mpirun rejects the job before any rank
// starts, so nothing from nccl-tests appears at all.
const ncclLaunchFailureOutput = `--------------------------------------------------------------------------
Your job has requested more processes than the ppr for
this topology can support:

  App: /usr/local/bin/all_reduce_perf
  Number of procs:  99
  PPR: 8:node

Please revise the conflict and try again.
--------------------------------------------------------------------------
`

// An unprofiled SKU has no threshold to compare against, so the benchmark must not run.
func TestNCCLCheckerUnknownSKU(t *testing.T) {
	t.Parallel()

	got, err := NewNCCLChecker(Config{SKU: "unrecognized_sku"}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run() returned error %v, want nil", err)
	}
	if got.Status != checker.StatusUnknown {
		t.Errorf("Status = %q, want %q", got.Status, checker.StatusUnknown)
	}
	if got.Detail.Code != ErrorCodeUnknownSKU {
		t.Errorf("Code = %q, want %q", got.Detail.Code, ErrorCodeUnknownSKU)
	}
}

func TestParseNCCLResult(t *testing.T) {
	t.Parallel()

	report := ncclReportJSON(t)

	tests := []struct {
		name        string
		reportJSON  string
		output      string
		sku         string
		execErr     error
		wantStatus  checker.Status
		wantCode    string
		wantMessage string
	}{
		{
			name:        "healthy above threshold",
			reportJSON:  report,
			sku:         h100SKU,
			wantStatus:  checker.StatusHealthy,
			wantMessage: "bus bandwidth 480.294 GB/s (>= 460.000 GB/s threshold",
		},
		{
			name:       "bandwidth below threshold",
			reportJSON: strings.Replace(report, "480.294297", "300.000", 1),
			sku:        h100SKU,
			wantStatus: checker.StatusUnhealthy,
			wantCode:   ErrorCodeNcclLowBandwidth,
		},
		{
			name:        "correctness failure wins over passing bandwidth",
			reportJSON:  strings.Replace(report, `"count": 0`, `"count": 3`, 1),
			sku:         h100SKU,
			wantStatus:  checker.StatusUnhealthy,
			wantCode:    ErrorCodeNcclCorrectness,
			wantMessage: "3 out-of-bounds values",
		},
		{
			// The tool dies before writing a report, so stdout is the only explanation.
			name:        "argument error is a tool failure",
			output:      ncclBadArgOutput,
			sku:         h100SKU,
			execErr:     errors.New("exit status 255"),
			wantStatus:  checker.StatusUnhealthy,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: "invalid size specified for 'minbytes'",
		},
		{
			name:        "rank mismatch is a tool failure",
			output:      ncclRankFailureOutput,
			sku:         h100SKU,
			execErr:     errors.New("exit status 5"),
			wantStatus:  checker.StatusUnhealthy,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: "Invalid number of GPUs",
		},
		{
			// mpirun can reject the job before any rank starts, leaving no nccl-tests output at
			// all; the launcher's complaint is then the only thing to report.
			name:        "launcher rejection is a tool failure",
			output:      ncclLaunchFailureOutput,
			sku:         h100SKU,
			execErr:     errors.New("exit status 1"),
			wantStatus:  checker.StatusUnhealthy,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: "requested more processes than the ppr",
		},
		{
			// A report with no measurements must not read as a zero-bandwidth failure.
			name:        "report without results is a tool failure",
			reportJSON:  `{"results": [], "average_bus_bandwidth": {"bandwidth": 0}}`,
			sku:         h100SKU,
			wantStatus:  checker.StatusUnhealthy,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: "nccl-tests produced no report",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			profile, _ := profileFor(tt.sku)
			got := parseNCCLResult(tt.reportJSON, tt.output, tt.sku, profile, tt.execErr)

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
