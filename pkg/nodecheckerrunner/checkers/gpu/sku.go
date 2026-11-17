package gpu

import "strings"

// skuProfile is the per-SKU expectation for a GPU node.
type skuProfile struct {
	ExpectedGPUs    int     // GPU/vGPU devices the SKU should expose; `check_gpu_count <n>`
	NcclBusGBps     float64 // NCCL all-reduce bus bandwidth; `check_nccl_allreduce <busbw>`
	NcclMessageSize string  // NcclMessageSize is `check_nccl_allreduce <_> <_> <size>`.
	NcclTopoFile    string  // Names the NCCL topology file in topoDir.
	BwPCIeGBps      float64 // host<->device memcpy, both directions; `check_gpu_bw <pcie>`
	BwP2PGBps       float64 // device-to-device memcpy read; `check_gpu_bw <p2p>`
}

// skuProfiles is keyed by normalizeSKU. When GPU checks are enabled, SKUs absent from this map
// are treated as unknown and checks do not run.
var skuProfiles = map[string]skuProfile{
	// NVadsA10_v5 sizes and GPU shares are documented at:
	// https://learn.microsoft.com/azure/virtual-machines/sizes/gpu-accelerated/nvadsa10v5-series
	// NV6/NV12/NV18 have 1/6, 1/3, and 1/2 of an A10 (4/8/12 GiB); each is
	// expected to expose one vGPU. NV36ads/NV36adms have one full 24 GiB A10.
	// NCCL all-reduce and peer bandwidth do not apply to a single GPU/vGPU.
	// Fractional H2D/D2H floors scale NV72's provisional 10 GB/s floor by GPU
	// share as a bring-up heuristic, not a measured or Azure-guaranteed bandwidth.
	// TODO: Validate GPU visibility and H2D/D2H floors on all five new A10 sizes.
	"nv6ads_a10_v5": {
		ExpectedGPUs: 1,
		BwPCIeGBps:   10.0 / 6,
	},
	"nv12ads_a10_v5": {
		ExpectedGPUs: 1,
		BwPCIeGBps:   10.0 / 3,
	},
	"nv18ads_a10_v5": {
		ExpectedGPUs: 1,
		BwPCIeGBps:   10.0 / 2,
	},
	"nv36ads_a10_v5": {
		ExpectedGPUs: 1,
		BwPCIeGBps:   10.0,
	},
	"nv36adms_a10_v5": {
		ExpectedGPUs: 1,
		BwPCIeGBps:   10.0,
	},
	// NV72 has two 24 GiB A10 GPUs without NVLink; the 4G message fits in memory.
	// Initial real-AKS samples measured 2.03-2.31 GB/s, so use 1.5 GB/s as a
	// conservative bring-up floor with headroom for host variance.
	"nv72ads_a10_v5": {
		ExpectedGPUs: 2,
		// TODO: Replace this provisional floor after validating a larger sample across NV72 hosts and A10 topologies.
		NcclBusGBps:     1.5,
		NcclMessageSize: "4G",
		BwPCIeGBps:      10.0, // TODO: set a validated NV72 H2D/D2H PCIe floor from more real measurements.
	},
	// Sourced from https://github.com/Azure/azurehpc-health-checks/blob/main/conf/nd96isr_h100_v5.conf.
	"nd96isr_h100_v5": {
		ExpectedGPUs:    8,
		NcclBusGBps:     460.0,
		NcclMessageSize: "16G",
		NcclTopoFile:    "ndv5-topo.xml",
		BwPCIeGBps:      48.0,
		BwP2PGBps:       335.0,
	},
}

// normalizeSKU lowercases a VM size and strips the "standard_" prefix so both the node label
// form (Standard_ND96isr_H100_v5) and the bare form resolve to the same key.
func normalizeSKU(sku string) string {
	return strings.TrimPrefix(strings.ToLower(sku), "standard_")
}

func profileFor(sku string) (skuProfile, bool) {
	v, ok := skuProfiles[normalizeSKU(sku)]
	return v, ok
}

// ProfiledSKU returns the normalized form of sku, and whether the GPU checks have a profile for it.
// Only profiled SKUs are returned as true, so callers can rely on the set of values being the
// profiles defined here.
func ProfiledSKU(sku string) (string, bool) {
	normalized := normalizeSKU(sku)
	_, ok := skuProfiles[normalized]
	return normalized, ok
}
