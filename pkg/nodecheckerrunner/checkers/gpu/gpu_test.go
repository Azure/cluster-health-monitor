package gpu

import (
	"slices"
	"testing"
	"time"
)

const (
	h100SKU = "Standard_ND96isr_H100_v5"
	a10SKU  = "Standard_NV72ads_A10_v5"
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
			sku:  h100SKU,
			want: []string{NCCLAllReduceCheckerName, HostBandwidthCheckerName, PeerBandwidthCheckerName},
		},
		{
			name: "sku without a p2p threshold skips peer bandwidth",
			sku:  a10SKU,
			want: []string{NCCLAllReduceCheckerName, HostBandwidthCheckerName},
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
