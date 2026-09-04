package checknodehealth

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/cnhstatus"
)

const (
	// GPUResourceName is the extended resource name for NVIDIA GPUs.
	GPUResourceName corev1.ResourceName = "nvidia.com/gpu"

	// GPUPodTimeout bounds the checker pod on GPU nodes. The intrusive benchmarks are
	// long-running compared to PodTimeout, and the pod bounds each tool below this.
	GPUPodTimeout = 20 * time.Minute

	// nvidiaAcceleratorLabel marks a node as having NVIDIA GPUs even when the device plugin is
	// absent (e.g. driver-only pools) and no nvidia.com/gpu is advertised as allocatable.
	nvidiaAcceleratorLabel = "kubernetes.azure.com/accelerator"

	// instanceTypeLabel carries the node's VM SKU, used to select bandwidth thresholds.
	instanceTypeLabel = "node.kubernetes.io/instance-type"

	// gpuShmSize sizes /dev/shm, which NCCL needs for intra-node transports. The 64Mi default
	// is far too small.
	gpuShmSize = "8Gi"

	// checkerUID is the non-root user the checker images run as.
	checkerUID int64 = 65532

	// Error codes the controller records when the checker pod cannot report for itself.
	ErrorCodeGPUTimeout   = "GpuCheckTimeout"
	ErrorCodeGPUPodFailed = "GpuCheckPodFailed"
)

// GPUCheckResults are the results the checker pod is expected to report on a GPU node. The
// controller fills these in itself when the pod fails or times out before reporting.
var GPUCheckResults = []string{"NcclAllReduce", "GpuBandwidth"}

// gpuNodeInfo describes the GPU capabilities of a CheckNodeHealth's target node.
type gpuNodeInfo struct {
	isGPUNode bool
	gpuCount  int64
	sku       string
}

// gpuNodeInfoFor reads the target node through the uncached reader so allocatable GPU
// resources and labels are current. A node counts as a GPU node when it advertises
// allocatable GPUs or carries the NVIDIA accelerator label.
func (r *CheckNodeHealthReconciler) gpuNodeInfoFor(ctx context.Context, nodeName string) (gpuNodeInfo, error) {
	if !r.EnableGPUChecks {
		return gpuNodeInfo{}, nil
	}

	node := &corev1.Node{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return gpuNodeInfo{}, fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	var allocatable int64
	if q, ok := node.Status.Allocatable[GPUResourceName]; ok {
		allocatable = q.Value()
	}

	return gpuNodeInfo{
		isGPUNode: allocatable > 0 || node.Labels[nvidiaAcceleratorLabel] == "nvidia",
		gpuCount:  allocatable,
		sku:       node.Labels[instanceTypeLabel],
	}, nil
}

// podTimeoutFor returns the deadline for the checker pod on this node.
func podTimeoutFor(info gpuNodeInfo) time.Duration {
	if info.isGPUNode {
		return GPUPodTimeout
	}
	return PodTimeout
}

// fillMissingGPUResults records a terminal Unknown result for every expected GPU result the
// pod did not report. A pod that never ran cannot report on itself.
func (r *CheckNodeHealthReconciler) fillMissingGPUResults(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, errorCode, message string) error {
	defaults := make([]chmv1alpha1.CheckResult, 0, len(GPUCheckResults))
	for _, name := range GPUCheckResults {
		defaults = append(defaults, chmv1alpha1.CheckResult{
			Name:      name,
			Status:    chmv1alpha1.CheckStatusUnknown,
			ErrorCode: errorCode,
			Message:   message,
		})
	}

	added, err := cnhstatus.FillMissingResults(ctx, r.Client, cnh.Name, defaults...)
	if err != nil {
		return fmt.Errorf("failed to record missing GPU results: %w", err)
	}
	for _, result := range added {
		cnhstatus.UpsertResult(&cnh.Status, result)
	}
	return nil
}

// podFailureMessage summarizes why a pod failed, preferring container termination detail.
func podFailureMessage(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
			return fmt.Sprintf("container %s exited %d: %s %s", cs.Name, t.ExitCode, t.Reason, t.Message)
		}
	}
	if pod.Status.Message != "" {
		return pod.Status.Message
	}
	return string(pod.Status.Phase)
}
