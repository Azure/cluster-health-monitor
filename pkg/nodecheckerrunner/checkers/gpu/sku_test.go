package gpu

import "testing"

func TestProfileFor(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		sku      string
		wantGPUs int
	}{
		{sku: "Standard_ND96isr_H100_v5", wantGPUs: 8},
		{sku: "nd96isr_h100_v5", wantGPUs: 8},
		{sku: "STANDARD_ND96ISR_H100_V5", wantGPUs: 8},
		{sku: "Standard_NV36ads_A10_v5", wantGPUs: 1},
		{sku: "nv36ads_a10_v5", wantGPUs: 1},
		{sku: "STANDARD_NV36ADS_A10_V5", wantGPUs: 1},
		{sku: "Standard_NV36adms_A10_v5", wantGPUs: 1},
		{sku: "nv36adms_a10_v5", wantGPUs: 1},
		{sku: "STANDARD_NV36ADMS_A10_V5", wantGPUs: 1},
		{sku: "Standard_NV72ads_A10_v5", wantGPUs: 2},
	} {
		profile, ok := profileFor(tt.sku)
		if !ok {
			t.Errorf("profileFor(%q) found no profile", tt.sku)
			continue
		}
		if profile.ExpectedGPUs != tt.wantGPUs {
			t.Errorf("profileFor(%q).ExpectedGPUs = %d, want %d", tt.sku, profile.ExpectedGPUs, tt.wantGPUs)
		}
	}

	// Fractional A10 sizes need separate GPU-visibility and bandwidth validation.
	for _, sku := range []string{"Standard_D8d_v5", "Standard_NV6ads_A10_v5", "Standard_NV12ads_A10_v5", "Standard_NV18ads_A10_v5"} {
		if _, ok := profileFor(sku); ok {
			t.Errorf("profileFor(%q) unexpectedly found a profile", sku)
		}
	}
}

func TestA10Profiles(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		sku  string
		want skuProfile
	}{
		{
			sku:  "Standard_NV36ads_A10_v5",
			want: skuProfile{ExpectedGPUs: 1, BwPCIeGBps: 10.0},
		},
		{
			sku:  "Standard_NV36adms_A10_v5",
			want: skuProfile{ExpectedGPUs: 1, BwPCIeGBps: 10.0},
		},
		{
			sku: "Standard_NV72ads_A10_v5",
			want: skuProfile{
				ExpectedGPUs:    2,
				NcclBusGBps:     1.5,
				NcclMessageSize: "4G",
				BwPCIeGBps:      10.0,
			},
		},
	} {
		t.Run(tt.sku, func(t *testing.T) {
			t.Parallel()

			got, ok := profileFor(tt.sku)
			if !ok || got != tt.want {
				t.Errorf("profileFor(%q) = (%+v, %v), want (%+v, true)", tt.sku, got, ok, tt.want)
			}
		})
	}
}

func TestProfiledSKU(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		sku            string
		wantNormalized string
		wantProfiled   bool
	}{
		{sku: "Standard_ND96isr_H100_v5", wantNormalized: "nd96isr_h100_v5", wantProfiled: true},
		{sku: "nd96isr_h100_v5", wantNormalized: "nd96isr_h100_v5", wantProfiled: true},
		{sku: "Standard_NV36ads_A10_v5", wantNormalized: "nv36ads_a10_v5", wantProfiled: true},
		{sku: "nv36ads_a10_v5", wantNormalized: "nv36ads_a10_v5", wantProfiled: true},
		{sku: "STANDARD_NV36ADS_A10_V5", wantNormalized: "nv36ads_a10_v5", wantProfiled: true},
		{sku: "Standard_NV36adms_A10_v5", wantNormalized: "nv36adms_a10_v5", wantProfiled: true},
		{sku: "nv36adms_a10_v5", wantNormalized: "nv36adms_a10_v5", wantProfiled: true},
		{sku: "STANDARD_NV36ADMS_A10_V5", wantNormalized: "nv36adms_a10_v5", wantProfiled: true},
		{sku: "Standard_NV72ads_A10_v5", wantNormalized: "nv72ads_a10_v5", wantProfiled: true},
		{sku: "Standard_NV6ads_A10_v5", wantNormalized: "nv6ads_a10_v5", wantProfiled: false},
		{sku: "Standard_NV12ads_A10_v5", wantNormalized: "nv12ads_a10_v5", wantProfiled: false},
		{sku: "Standard_NV18ads_A10_v5", wantNormalized: "nv18ads_a10_v5", wantProfiled: false},
		{sku: "Standard_D8d_v5", wantNormalized: "d8d_v5", wantProfiled: false},
		{sku: "", wantNormalized: "", wantProfiled: false},
	} {
		normalized, profiled := ProfiledSKU(tt.sku)
		if normalized != tt.wantNormalized || profiled != tt.wantProfiled {
			t.Errorf("ProfiledSKU(%q) = (%q, %v), want (%q, %v)", tt.sku, normalized, profiled, tt.wantNormalized, tt.wantProfiled)
		}
	}
}
