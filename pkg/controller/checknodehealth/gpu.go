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

	// DefaultGPUWait is how long a GPU node without any GPUs the checks can claim is given to get some
	// before its GPU checks start.
	//
	// This exists because after a node boots, the a CNH can be created for it before something like
	// the NVIDIA device plugin has registered its GPUs. This provides a grace period so that the checks
	// do not return Unknown due to no GPUs being found/available.
	//
	// The 2 minute time was chosen after rebooting and scaling up/down some A10 and H100 nodes using
	// Nvidia device plugin. The GPUs reached the Node 10-33s after the CheckNodeHealth was created.
	DefaultGPUWait = 2 * time.Minute

	// gpuPollInterval is how often the node is re-read while waiting for claimable GPUs.
	gpuPollInterval = 30 * time.Second
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
	// claimableGPUs is how many of the node's GPUs a checker pod can get exclusive use of. The checks
	// claim all of them and judge the count against the SKU, so a lost GPU shows up as a count failure.
	claimableGPUs int64
	sku           string
}

// gpuNodeInfoFrom derives the GPU capabilities of a CheckNodeHealth's target node. A node counts as
// a GPU node when it has claimable GPUs or carries the accelerator label with value "nvidia". AMD
// GPUs are currently not supported. A nil node is treated as non-GPU.
//
// The node must be read through the uncached reader so GPU resources and labels are current.
func gpuNodeInfoFrom(node *corev1.Node) gpuNodeInfo {
	if node == nil {
		return gpuNodeInfo{}
	}

	claimable := nvidiaClaimableGPUs(node)
	return gpuNodeInfo{
		isGPUNode:     claimable > 0 || strings.EqualFold(node.Labels[gpuAcceleratorLabel], "nvidia"),
		claimableGPUs: claimable,
		sku:           node.Labels[instanceTypeLabel],
	}
}

// nvidiaClaimableGPUs returns the NVIDIA GPUs a pod can be given exclusive use of: the ones the NVIDIA
// device plugin advertises as healthy. A driver-only node has no device plugin, so it has none.
func nvidiaClaimableGPUs(node *corev1.Node) int64 {
	if q, ok := node.Status.Allocatable[nvidiaGPUResourceName]; ok {
		return q.Value()
	}
	return 0
}

// gpuChecks is what the checker pod is told about the GPU checks.
type gpuChecks struct {
	sku string
	// gpus is how many GPUs the checker pod claims and benchmarks.
	gpus int64
	// skipReason, when set, is the error code every GPU check reports instead of running. The checker
	// pod gets no GPUs then.
	skipReason string
}
