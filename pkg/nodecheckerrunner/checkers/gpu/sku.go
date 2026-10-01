package gpu

import "strings"

// skuProfile is the per-SKU expectation for a GPU node.
type skuProfile struct {
	ExpectedGPUs    int     // GPUs the SKU should expose; `check_gpu_count <n>`
	NcclBusGBps     float64 // NCCL all-reduce bus bandwidth; `check_nccl_allreduce <busbw>`
	NcclMessageSize string  // NcclMessageSize is `check_nccl_allreduce <_> <_> <size>`.
	NcclTopoFile    string  // Names the NCCL topology file in topoDir.
	BwPCIeGBps      float64 // host<->device memcpy, both directions; `check_gpu_bw <pcie>`
	BwP2PGBps       float64 // device-to-device memcpy read; `check_gpu_bw <p2p>`
}

// skuProfiles is keyed by normalizeSKU. When GPU checks are enabled, SKUs absent from this map
// are treated as unknown and checks do not run.
var skuProfiles = map[string]skuProfile{
	// NV72 has two 24 GiB A10 GPUs without NVLink; the 4G message fits in memory.
	// Its throughput floors are temporary bring-up values, not yet validated on A10.
	"nv72ads_a10_v5": {
		ExpectedGPUs:    2,
		NcclBusGBps:     10.0, // TODO: set a validated NV72 NCCL bus-bandwidth floor from real measurements.
		NcclMessageSize: "4G",
		BwPCIeGBps:      10.0, // TODO: set a validated NV72 H2D/D2H PCIe floor from real measurements.
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
