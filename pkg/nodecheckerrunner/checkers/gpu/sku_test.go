package gpu

import "testing"

func TestProfileFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		skus            []string
		wantGPUs        int
		wantNCCL        float64
		wantMessageSize string
		wantPCIe        float64
		wantP2P         float64
	}{
		{
			name:            "H100 NDv5",
			skus:            []string{"Standard_ND96isr_H100_v5", "nd96isr_h100_v5", "STANDARD_ND96ISR_H100_V5"},
			wantGPUs:        8,
			wantNCCL:        460,
			wantMessageSize: "16G",
			wantPCIe:        48,
			wantP2P:         335,
		},
		{
			name:            "A10 NV72",
			skus:            []string{"Standard_NV72ads_A10_v5", "nv72ads_a10_v5", "STANDARD_NV72ADS_A10_V5"},
			wantGPUs:        2,
			wantNCCL:        10,
			wantMessageSize: "4G",
			wantPCIe:        10,
			wantP2P:         0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, vmSize := range tt.skus {
				profile, ok := profileFor(vmSize)
				if !ok {
					t.Errorf("profileFor(%q) found no profile", vmSize)
					continue
				}
				if profile.ExpectedGPUs != tt.wantGPUs ||
					profile.NcclBusGBps != tt.wantNCCL ||
					profile.NcclMessageSize != tt.wantMessageSize ||
					profile.BwPCIeGBps != tt.wantPCIe ||
					profile.BwP2PGBps != tt.wantP2P {
					t.Errorf("profileFor(%q) = %+v, want GPUs=%d NCCL=%v size=%s PCIe=%v P2P=%v",
						vmSize, profile, tt.wantGPUs, tt.wantNCCL, tt.wantMessageSize, tt.wantPCIe, tt.wantP2P)
				}
			}
		})
	}

	if _, ok := profileFor("Standard_D8d_v5"); ok {
		t.Error("profileFor(Standard_D8d_v5) unexpectedly found a profile")
	}
}

func TestA10BandwidthTestcases(t *testing.T) {
	t.Parallel()

	profile, ok := profileFor("Standard_NV72ads_A10_v5")
	if !ok {
		t.Fatal("A10 profile not found")
	}
	got := nvbwTestcasesFor(profile)
	want := []string{nvbwHostToDevice, nvbwDeviceToHost}
	if len(got) != len(want) {
		t.Fatalf("nvbwTestcasesFor(A10) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("testcase[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
