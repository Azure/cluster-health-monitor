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
		{sku: "Standard_NV72ads_A10_v5", wantGPUs: 2},
		{sku: "Standard_NC40ads_H100_v5", wantGPUs: 1},
		{sku: "nc40ads_h100_v5", wantGPUs: 1},
		{sku: "STANDARD_NC40ADS_H100_V5", wantGPUs: 1},
		{sku: "Standard_NC80adis_H100_v5", wantGPUs: 2},
		{sku: "nc80adis_h100_v5", wantGPUs: 2},
		{sku: "STANDARD_NC80ADIS_H100_V5", wantGPUs: 2},
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

	if _, ok := profileFor("Standard_D8d_v5"); ok {
		t.Error("profileFor(Standard_D8d_v5) unexpectedly found a profile")
	}
}

func TestNCH100Profiles(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		sku  string
		want skuProfile
	}{
		{
			sku: "Standard_NC40ads_H100_v5",
			want: skuProfile{
				ExpectedGPUs: 1,
				BwPCIeGBps:   48.0,
			},
		},
		{
			sku: "Standard_NC80adis_H100_v5",
			want: skuProfile{
				ExpectedGPUs:    2,
				NcclBusGBps:     208.0,
				NcclMessageSize: "16G",
				BwPCIeGBps:      48.0,
				BwP2PGBps:       335.0,
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
		{sku: "Standard_NV72ads_A10_v5", wantNormalized: "nv72ads_a10_v5", wantProfiled: true},
		{sku: "Standard_NC40ads_H100_v5", wantNormalized: "nc40ads_h100_v5", wantProfiled: true},
		{sku: "nc40ads_h100_v5", wantNormalized: "nc40ads_h100_v5", wantProfiled: true},
		{sku: "STANDARD_NC40ADS_H100_V5", wantNormalized: "nc40ads_h100_v5", wantProfiled: true},
		{sku: "Standard_NC80adis_H100_v5", wantNormalized: "nc80adis_h100_v5", wantProfiled: true},
		{sku: "nc80adis_h100_v5", wantNormalized: "nc80adis_h100_v5", wantProfiled: true},
		{sku: "STANDARD_NC80ADIS_H100_V5", wantNormalized: "nc80adis_h100_v5", wantProfiled: true},
		{sku: "Standard_D8d_v5", wantNormalized: "d8d_v5", wantProfiled: false},
		{sku: "", wantNormalized: "", wantProfiled: false},
	} {
		normalized, profiled := ProfiledSKU(tt.sku)
		if normalized != tt.wantNormalized || profiled != tt.wantProfiled {
			t.Errorf("ProfiledSKU(%q) = (%q, %v), want (%q, %v)", tt.sku, normalized, profiled, tt.wantNormalized, tt.wantProfiled)
		}
	}
}
