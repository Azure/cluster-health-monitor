package gpu

import "testing"

func TestProfileFor(t *testing.T) {
	t.Parallel()

	for _, sku := range []string{"Standard_ND96isr_H100_v5", "nd96isr_h100_v5", "STANDARD_ND96ISR_H100_V5"} {
		p, ok := profileFor(sku)
		if !ok {
			t.Errorf("profileFor(%q) found no profile", sku)
			continue
		}
		if p.ExpectedGPUs != 8 {
			t.Errorf("profileFor(%q).ExpectedGPUs = %d, want 8", sku, p.ExpectedGPUs)
		}
	}
	if _, ok := profileFor("Standard_D8d_v5"); ok {
		t.Error("profileFor(Standard_D8d_v5) unexpectedly found a profile")
	}
}
