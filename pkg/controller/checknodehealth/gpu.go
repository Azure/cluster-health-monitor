package checknodehealth

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
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
)

// gpuNodeInfo describes the GPU capabilities of a CheckNodeHealth's target node.
type gpuNodeInfo struct {
	isGPUNode bool
	// gpuCount is the allocatable GPU count, which is zero on driver-only pools even though
	// the node has GPUs.
	gpuCount int64
	sku      string
}

// gpuNodeInfo derives GPU capabilities from the target Node already read by Reconcile.
// A node counts as a GPU node when it advertises allocatable NVIDIA GPUs or carries the
// accelerator label with value "nvidia". AMD GPUs are currently not supported.
func (r *CheckNodeHealthReconciler) gpuNodeInfo(node *corev1.Node) gpuNodeInfo {
	if !r.EnableGPUChecks {
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
