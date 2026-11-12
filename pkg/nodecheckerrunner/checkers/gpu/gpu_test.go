package gpu

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

const (
	nd96isr_h100_v5 = "Standard_ND96isr_H100_v5"
	nv72ads_a10_v5  = "Standard_NV72ads_A10_v5"
)

func TestConfigWithDefaults(t *testing.T) {
	t.Parallel()

	got := Config{}.withDefaults()
	if got.ToolTimeout != defaultToolTimeout {
		t.Errorf("ToolTimeout = %v, want %v", got.ToolTimeout, defaultToolTimeout)
	}

	explicit := Config{ToolTimeout: time.Minute}.withDefaults()
	if explicit.ToolTimeout != time.Minute {
		t.Errorf("withDefaults() overwrote explicit values: %+v", explicit)
	}
}

func TestNewCheckers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sku  string
		want []string
	}{
		{
			name: "sku with every threshold runs every checker",
			sku:  nd96isr_h100_v5,
			want: []string{NCCLAllReduceCheckerName, HostBandwidthCheckerName, PeerBandwidthCheckerName},
		},
		{
			name: "sku without a p2p threshold skips peer bandwidth",
			sku:  nv72ads_a10_v5,
			want: []string{NCCLAllReduceCheckerName, HostBandwidthCheckerName},
		},
		{
			name: "sku with single gpu runs only host bandwidth",
			sku:  "Standard_NC40ads_H100_v5",
			want: []string{HostBandwidthCheckerName},
		},
		{
			name: "unknown sku runs every checker",
			sku:  "unrecognized_sku",
			want: []string{NCCLAllReduceCheckerName, HostBandwidthCheckerName, PeerBandwidthCheckerName},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got []string
			for _, c := range NewCheckers(Config{SKU: tt.sku}) {
				got = append(got, c.Name())
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("NewCheckers() = %v, want %v", got, tt.want)
			}
			if names := CheckerNames(tt.sku); !slices.Equal(names, tt.want) {
				t.Errorf("CheckerNames() = %v, want %v", names, tt.want)
			}
		})
	}
}

// When the controller sets a skip reason, every checker the SKU would run still reports it under the
// same name. None of them may touch the GPUs; nvidia-smi is absent here, so a checker that tried
// would report ToolFailed instead.
func TestNewCheckersWithSkipReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		// wantCode is what every checker reports.
		wantCode string
	}{
		{name: "nd96isr_h100_v5", cfg: Config{SKU: nd96isr_h100_v5, SkipReason: ErrorCodeGPUsNotClaimable}, wantCode: ErrorCodeGPUsNotClaimable},
		{name: "nv72ads_a10_v5", cfg: Config{SKU: nv72ads_a10_v5, SkipReason: ErrorCodeGPUsNotClaimable}, wantCode: ErrorCodeGPUsNotClaimable},
		{name: "nc40ads_h100_v5", cfg: Config{SKU: "Standard_NC40ads_H100_v5", SkipReason: ErrorCodeGPUsNotClaimable}, wantCode: ErrorCodeGPUsNotClaimable},
		{name: "nc80adis_h100_v5", cfg: Config{SKU: "Standard_NC80adis_H100_v5", SkipReason: ErrorCodeGPUsNotClaimable}, wantCode: ErrorCodeGPUsNotClaimable},
		{name: "unknown sku", cfg: Config{SKU: "unrecognized_sku", SkipReason: ErrorCodeGPUsNotClaimable}, wantCode: ErrorCodeUnknownSKU},
		{name: "missing sku", cfg: Config{}, wantCode: ErrorCodeNodeSKUNotAvailable},
		{name: "missing sku at readiness deadline", cfg: Config{SkipReason: ErrorCodeNodeSKUNotAvailable}, wantCode: ErrorCodeNodeSKUNotAvailable},
		{name: "missing sku and gpus", cfg: Config{SkipReason: ErrorCodeGPUsNotClaimable}, wantCode: ErrorCodeNodeSKUNotAvailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checkers := NewCheckers(tt.cfg)
			names := make([]string, 0, len(checkers))
			for _, c := range checkers {
				names = append(names, c.Name())

				got, err := c.Run(context.Background())
				if err != nil {
					t.Fatalf("%s Run() returned error %v, want nil", c.Name(), err)
				}
				if got.Status != checker.StatusUnknown {
					t.Errorf("%s Status = %q, want %q", c.Name(), got.Status, checker.StatusUnknown)
				}
				if got.Detail.Code != tt.wantCode {
					t.Errorf("%s Code = %q, want %q", c.Name(), got.Detail.Code, tt.wantCode)
				}
				if tt.wantCode == ErrorCodeNodeSKUNotAvailable && !strings.Contains(got.Detail.Message, "node.kubernetes.io/instance-type") {
					t.Errorf("%s must explain the missing node label, got %q", c.Name(), got.Detail.Message)
				}
			}
			if want := CheckerNames(tt.cfg.SKU); !slices.Equal(names, want) {
				t.Errorf("checkers = %v, want the names the SKU runs %v", names, want)
			}
		})
	}
}
