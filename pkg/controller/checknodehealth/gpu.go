package checknodehealth

import (
	"context"
	"fmt"
	"strings"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	nvidiaGPUResourceName corev1.ResourceName = "nvidia.com/gpu"
	gpuAcceleratorLabel   string              = "kubernetes.azure.com/accelerator"
)

// reconcileGPUChecks checks if the node is a GPU node and if so runs the GPU check runner. It returns true if the GPU checks are completed
// or not applicable, and false if they are still in progress.
func (r *CheckNodeHealthReconciler) reconcileGPUChecks(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) (bool, error) {
	if !r.EnableGPUChecks {
		klog.V(3).InfoS("Skipping GPU checks because they are disabled", "checkNodeHealth", cnh.Name, "node", cnh.Spec.NodeRef.Name)
		return true, nil
	}

	node := &corev1.Node{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: cnh.Spec.NodeRef.Name}, node); err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(3).InfoS("Skipping GPU checks because the target node no longer exists", "checkNodeHealth", cnh.Name, "node", cnh.Spec.NodeRef.Name)
			return true, nil
		}
		return false, fmt.Errorf("failed to get node %s: %w", cnh.Spec.NodeRef.Name, err)
	}
	if !isSupportedGPUNode(node) {
		klog.V(3).InfoS("Skipping GPU checks because the target is not a GPU node", "checkNodeHealth", cnh.Name, "node", node.Name)
		return true, nil
	}
	if r.GPUCheckRunner == nil {
		return false, fmt.Errorf("GPU checks are enabled but no GPU check runner is configured")
	}
	return r.GPUCheckRunner.Reconcile(ctx, cnh, node)
}

// isSupportedGPUNode determines if a node is a GPU node for which health checks are supported. Currently only nvidia GPUs will be supported
// so this will return false for other types such as AMD. Support for these may be added in the future.
func isSupportedGPUNode(node *corev1.Node) bool {
	// TODO add checks for specific supported skus.

	// GPU pools with a device plugin advertise the resource. Driver-only pools contain only the accelerator label.
	_, advertisesNvidiaGPUResource := node.Status.Allocatable[nvidiaGPUResourceName]
	return advertisesNvidiaGPUResource || strings.EqualFold(node.Labels[gpuAcceleratorLabel], "nvidia")
}
