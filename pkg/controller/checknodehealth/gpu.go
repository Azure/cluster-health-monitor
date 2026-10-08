package checknodehealth

import (
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/gpu"
)

const (
	// nvidiaGPUResourceName is the extended resource name for NVIDIA GPUs. Only present on nodes with
	// the nvidia device plugin. By default, this only includes fully managed GPU nodes on AKS.
	nvidiaGPUResourceName corev1.ResourceName = "nvidia.com/gpu"

	// gpuAcceleratorLabel marks a node as having GPUs even when the device plugin is absent. This is
	// present on both driver-only and fully managed GPU nodes on AKS.
	gpuAcceleratorLabel = "kubernetes.azure.com/accelerator"

	// instanceTypeLabel carries the node's VM SKU.
	instanceTypeLabel = "node.kubernetes.io/instance-type"

	// GPUPodTimeout is the budget for a GPU checker pod. It has to cover a cold pull of the GPU
	// checker pod image on top of the benchmarks themselves.
	GPUPodTimeout = 15 * time.Minute
)

const (
	CheckerNcclAllReduce    = gpu.NCCLAllReduceCheckerName
	CheckerGpuHostBandwidth = gpu.HostBandwidthCheckerName
	CheckerGpuPeerBandwidth = gpu.PeerBandwidthCheckerName
)

// gpuCheckerNames are every check a GPU node can report on top of baseCheckerNames. Only the ones
// gpu.CheckerNames returns for the node's SKU run on it.
var gpuCheckerNames = []string{CheckerNcclAllReduce, CheckerGpuHostBandwidth, CheckerGpuPeerBandwidth}

// gpuNodeInfo describes the GPU capabilities of a CheckNodeHealth's target node.
type gpuNodeInfo struct {
	// isGPUNode decides which Node health conditions the result is published as. See NodeConditionNodeHealthy.
	isGPUNode bool
	// gpuCount is the node's allocatable gpu extended resource, not how many the node physically has.
	// Only the device plugin publishes it, so driver-only pools report zero while still having GPUs.
	gpuCount int64
	sku      string
}

// gpuNodeInfoFrom derives the GPU capabilities of a CheckNodeHealth's target node. A node counts as
// a GPU node when it advertises allocatable nvidia GPUs or carries the accelerator label with value
// "nvidia". AMD GPUs are currently not supported. A nil node is treated as non-GPU.
//
// The node must be read through the uncached reader so allocatable GPU resources and labels are
// current.
func gpuNodeInfoFrom(node *corev1.Node) gpuNodeInfo {
	if node == nil {
		return gpuNodeInfo{}
	}

	var allocatable int64
	if q, ok := node.Status.Allocatable[nvidiaGPUResourceName]; ok {
		allocatable = q.Value()
	}

	return gpuNodeInfo{
		isGPUNode: allocatable > 0 || strings.EqualFold(node.Labels[gpuAcceleratorLabel], "nvidia"),
		gpuCount:  allocatable,
		sku:       node.Labels[instanceTypeLabel],
	}
}

// hasNvidiaDevicePlugin reports whether the NVIDIA device plugin advertises the node's GPUs.
func (i gpuNodeInfo) hasNvidiaDevicePlugin() bool {
	return i.gpuCount > 0
}
