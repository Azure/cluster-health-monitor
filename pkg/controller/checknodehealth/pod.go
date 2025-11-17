package checknodehealth

import (
	"context"
	"fmt"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *CheckNodeHealthReconciler) cleanupPod(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) error {
	logger := log.FromContext(ctx)

	// Find all pods with the specific label that matches this CR
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(r.PodNamespace),
		client.MatchingLabels{r.PodLabel: cnh.Name},
	}

	if err := r.List(ctx, podList, listOpts...); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	// Delete all matching pods
	for _, pod := range podList.Items {
		logger.Info("Deleting health check pod", "pod", pod.Name, "cr", cnh.Name)
		if err := r.Delete(ctx, &pod); err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to delete pod", "pod", pod.Name)
			return fmt.Errorf("failed to delete pod %s: %w", pod.Name, err)
		}
	}

	if len(podList.Items) > 0 {
		logger.Info("Cleaned up health check pods", "count", len(podList.Items), "cr", cnh.Name)
	}

	return nil
}

func (r *CheckNodeHealthReconciler) buildHealthCheckPod(cnh *chmv1alpha1.CheckNodeHealth) (*corev1.Pod, error) {
	podName := getHealthCheckPodName(cnh)
	labels := map[string]string{
		"app":                "cluster-health-monitor",
		"checknodehealth":    cnh.Name,
		"chm.azure.com/node": cnh.Spec.NodeRef.Name,
	}

	// Add the configurable pod label for identification
	if r.PodLabel != "" {
		labels[r.PodLabel] = cnh.Name
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: r.PodNamespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeName:      cnh.Spec.NodeRef.Name, // Schedule on specific node
			Containers: []corev1.Container{
				{
					Name:    "health-checker",
					Image:   r.PodImage,
					Command: []string{"/bin/sh", "-c"},
					Args:    []string{"sleep 10"},
				},
			},
		},
	}

	// Set CheckNodeHealth as owner
	if err := controllerutil.SetControllerReference(cnh, pod, r.Scheme); err != nil {
		// This shouldn't fail in normal circumstances, but if it does,
		// we'll return a pod without owner reference rather than nil
		// The caller should handle this gracefully
		return nil, err
	}

	return pod, nil
}

func (r *CheckNodeHealthReconciler) ensureHealthCheckPod(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) (*corev1.Pod, error) {
	logger := log.FromContext(ctx)

	// Check if pods already exist using label selector
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(r.PodNamespace),
		client.MatchingLabels{r.PodLabel: cnh.Name},
	}

	if err := r.List(ctx, podList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list existing pods: %w", err)
	}

	if len(podList.Items) > 0 {
		// Pod already exists, return the first one
		pod := &podList.Items[0]
		if len(podList.Items) > 1 {
			logger.Info("Multiple health check pods found, using first one", "count", len(podList.Items))
		}
		logger.V(1).Info("Health check pod already exists", "pod", pod.Name)
		return pod, nil
	}

	// Create the pod
	pod, err := r.buildHealthCheckPod(cnh)
	if err != nil {
		return nil, fmt.Errorf("failed to build health check pod: %w", err)
	}

	logger.Info("Creating health check pod", "pod", pod.Name, "node", cnh.Spec.NodeRef.Name)
	if err := r.Create(ctx, pod); err != nil {
		return nil, fmt.Errorf("failed to create pod: %w", err)
	}

	return pod, nil
}

// isPodPendingTimeout checks if the pod has been pending for too long
func (r *CheckNodeHealthReconciler) isPodPendingTimeout(cnh *chmv1alpha1.CheckNodeHealth, pod *corev1.Pod) bool {
	// If StartedAt is not set, we can't determine timeout
	if cnh.Status.StartedAt == nil {
		return false
	}

	// Check if the pod has been pending since StartedAt
	pendingDuration := time.Since(cnh.Status.StartedAt.Time)
	return pendingDuration > PodPendingTimeout
}

func getHealthCheckPodName(cnh *chmv1alpha1.CheckNodeHealth) string {
	return fmt.Sprintf("health-check-%s", cnh.Name)
}
