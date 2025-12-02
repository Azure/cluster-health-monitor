package checknodehealth

import (
	"context"
	"fmt"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// maxPodNameLength is the maximum allowed length for Kubernetes pod names
	maxPodNameLength = 253
	// podNamePrefix is the prefix used for health check pod names
	podNamePrefix = "check-node-health-"
)

func (r *CheckNodeHealthReconciler) cleanupPod(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) error {
	// Find all pods with the specific label that matches this CR
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(r.CheckerPodNamespace),
		client.MatchingLabels{CheckNodeHealthLabel: cnh.Name},
	}

	if err := r.List(ctx, podList, listOpts...); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	// Delete all matching pods
	for _, pod := range podList.Items {
		klog.InfoS("Deleting health check pod", "pod", pod.Name, "cr", cnh.Name)
		if err := r.Delete(ctx, &pod); err != nil && !apierrors.IsNotFound(err) {
			klog.ErrorS(err, "Failed to delete pod", "pod", pod.Name)
			return fmt.Errorf("failed to delete pod %s: %w", pod.Name, err)
		}
	}

	if len(podList.Items) > 0 {
		klog.InfoS("Cleaned up health check pods", "count", len(podList.Items), "cr", cnh.Name)
	}

	return nil
}

func (r *CheckNodeHealthReconciler) ensureHealthCheckPod(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) (*corev1.Pod, error) {
	// Check if pods already exist using label selector
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(r.CheckerPodNamespace),
		client.MatchingLabels{CheckNodeHealthLabel: cnh.Name},
	}

	if err := r.List(ctx, podList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list existing pods: %w", err)
	}

	if len(podList.Items) > 0 {
		// Pod already exists, return the first one
		pod := &podList.Items[0]
		if len(podList.Items) > 1 {
			klog.InfoS("Multiple health check pods found, using first one", "count", len(podList.Items))
		}
		klog.V(1).InfoS("Health check pod already exists", "pod", pod.Name)
		return pod, nil
	}

	// Create the pod
	pod, err := r.buildHealthCheckPod(cnh)
	if err != nil {
		return nil, fmt.Errorf("failed to build health check pod: %w", err)
	}

	klog.InfoS("Creating health check pod", "pod", pod.Name, "node", cnh.Spec.NodeRef.Name)
	if err := r.Create(ctx, pod); err != nil {
		return nil, fmt.Errorf("failed to create pod: %w", err)
	}

	// Refetch the pod to get the CreationTimestamp set by the API server
	createdPod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{Name: pod.Name, Namespace: pod.Namespace}, createdPod); err != nil {
		return nil, fmt.Errorf("failed to get created pod: %w", err)
	}

	return createdPod, nil
}

func (r *CheckNodeHealthReconciler) buildHealthCheckPod(cnh *chmv1alpha1.CheckNodeHealth) (*corev1.Pod, error) {
	podName := generateHealthCheckPodName(cnh)
	labels := map[string]string{
		"app":                "cluster-health-monitor",
		CheckNodeHealthLabel: cnh.Name,
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: r.CheckerPodNamespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeName:      cnh.Spec.NodeRef.Name, // Schedule on specific node
			Containers: []corev1.Container{
				{
					Name:  "node-health-checker",
					Image: r.CheckerPodImage,

					//TODO: this is placeholder command for test; replace with actual health check logic
					Command: []string{"/bin/sh", "-c"},
					Args:    []string{"sleep 10"},
				},
			},
		},
	}

	// Set CheckNodeHealth as owner reference to establish parent-child relationship
	// This enables automatic pod cleanup when the CheckNodeHealth CR is deleted (garbage collection)
	// and allows the controller to receive pod events for reconciliation
	if err := controllerutil.SetControllerReference(cnh, pod, r.Scheme); err != nil {
		// This shouldn't fail in normal circumstances, but if it does,
		// we'll return an error rather than creating a pod without proper ownership
		return nil, err
	}

	return pod, nil
}

func (r *CheckNodeHealthReconciler) updatePodstartCheckerResult(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, pod *corev1.Pod) error {
	// PodStartup checker evaluates whether containers can successfully start on the node.

	// Case 1: All containers have started successfully
	// - Containers are running OR have terminated after starting (e.g., CrashLoopBackOff)
	// - Even if containers crash after starting, PodStartup is Healthy because the container
	//   runtime successfully started them. The crash is an application(Checker) issue, not a node issue.
	if r.areAllContainersStarted(pod) {
		return r.markPodStartupResult(ctx, cnh, chmv1alpha1.CheckStatusHealthy, "All containers started successfully")
	}

	// Case 2: Pod timeout
	// - We cannot rely on container status to detect startup failures because the container runtime
	//   will automatically retry failed containers (e.g., image pull failures, resource constraints)
	// - The only reliable way to detect pod startup failure is by waiting for a timeout
	// - If the pod remains in Pending state beyond the timeout, it indicates a persistent node-level
	//   issue preventing container startup
	if pod.Status.Phase == corev1.PodPending && r.isPodTimeout(pod) {
		return r.markPodStartupResult(ctx, cnh, chmv1alpha1.CheckStatusUnhealthy, "Pod stuck in Pending state - timeout exceeded")
	}

	// Case 3: Still initializing or container runtime is retrying
	// - Containers are being pulled, created, or retried by the runtime
	// - No action needed yet, wait for containers to start or timeout
	return nil
}

// areAllContainersStarted checks if all containers have started successfully
func (r *CheckNodeHealthReconciler) areAllContainersStarted(pod *corev1.Pod) bool {
	// If no container statuses available yet, containers haven't started
	if len(pod.Status.ContainerStatuses) == 0 {
		return false
	}

	// Check each container status
	for _, containerStatus := range pod.Status.ContainerStatuses {
		// Container has started if it's currently running OR if it terminated after starting
		hasStarted := containerStatus.State.Running != nil ||
			(containerStatus.State.Terminated != nil && !containerStatus.State.Terminated.StartedAt.IsZero())

		if !hasStarted {
			return false
		}
	}
	return true
}

// markPodStartupResult marks the CheckNodeHealth with a PodStartup check result
func (r *CheckNodeHealthReconciler) markPodStartupResult(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, status chmv1alpha1.CheckStatus, message string) error {
	// Create or update the PodStartup result
	result := chmv1alpha1.CheckResult{
		Name:    "PodStartup",
		Status:  status,
		Message: message,
	}

	// Update or append the result
	r.updateCheckResult(cnh, result)

	// Update the status
	if err := r.Status().Update(ctx, cnh); err != nil {
		return fmt.Errorf("failed to update CheckNodeHealth status: %w", err)
	}

	klog.InfoS("PodStartup check result recorded", "cr", cnh.Name, "status", status, "message", message)
	return nil
}

// updateCheckResult updates or appends a check result to the CheckNodeHealth status
func (r *CheckNodeHealthReconciler) updateCheckResult(cnh *chmv1alpha1.CheckNodeHealth, newResult chmv1alpha1.CheckResult) {
	// Find existing result for this checker
	for i, result := range cnh.Status.Results {
		if result.Name == newResult.Name {
			// Update existing result
			cnh.Status.Results[i] = newResult
			return
		}
	}

	// Append new result if not found
	cnh.Status.Results = append(cnh.Status.Results, newResult)
}

// isPodTimeout checks if the pod has been running for too long without completing
func (r *CheckNodeHealthReconciler) isPodTimeout(pod *corev1.Pod) bool {
	duration := time.Since(pod.CreationTimestamp.Time)
	return duration > PodTimeout
}

func generateHealthCheckPodName(cnh *chmv1alpha1.CheckNodeHealth) string {
	desiredName := fmt.Sprintf("%s%s", podNamePrefix, cnh.Name)

	// If the name is too long, truncate it
	if len(desiredName) > maxPodNameLength {
		desiredName = desiredName[:maxPodNameLength]
	}

	return desiredName
}
