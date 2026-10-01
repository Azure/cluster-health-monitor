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
