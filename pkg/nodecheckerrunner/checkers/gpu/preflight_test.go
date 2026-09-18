package gpu

import (
	"errors"
	"strings"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

func TestPreflightChecker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		sku         string
		found       int
		detectErr   error
		wantStatus  checker.Status
		wantCode    string
		wantMessage string
	}{
		{
			name:        "all expected gpus present",
			sku:         h100SKU,
			found:       8,
			wantStatus:  checker.StatusHealthy,
			wantMessage: "found 8 of 8 GPUs expected",
		},
		{
			name:        "missing gpu reports unhealthy",
			sku:         h100SKU,
			found:       7,
			wantStatus:  checker.StatusUnhealthy,
			wantCode:    ErrorCodeUnexpectedGPUCount,
			wantMessage: "found 7 of 8 GPUs expected",
		},
		{
			name:        "more gpus than expected reports unhealthy",
			sku:         h100SKU,
			found:       9,
			wantStatus:  checker.StatusUnhealthy,
			wantCode:    ErrorCodeUnexpectedGPUCount,
			wantMessage: "found 9 of 8 GPUs expected",
		},
		{
			name:        "unrecognized sku reports unknown",
			sku:         "unrecognized_sku",
			found:       4,
			wantStatus:  checker.StatusUnknown,
			wantCode:    ErrorCodeUnknownSKU,
			wantMessage: "is not recognized or supported",
		},
		{
			name:        "detection failure reports unknown",
			sku:         h100SKU,
			detectErr:   errors.New("exec: nvidia-smi not found"),
			wantStatus:  checker.StatusUnknown,
			wantCode:    ErrorCodeToolFailed,
			wantMessage: "could not determine GPU count",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := evaluateCount(tt.sku, tt.found, tt.detectErr)
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
