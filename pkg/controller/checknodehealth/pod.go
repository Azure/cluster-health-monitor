package checknodehealth

import (
	"context"
	"fmt"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	corev1 "k8s.io/api/core/v1"
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

	return pod, nil
}

func (r *CheckNodeHealthReconciler) buildHealthCheckPod(cnh *chmv1alpha1.CheckNodeHealth) (*corev1.Pod, error) {
	podName := getHealthCheckPodName(cnh)
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

func getHealthCheckPodName(cnh *chmv1alpha1.CheckNodeHealth) string {
	desiredName := fmt.Sprintf("%s%s", podNamePrefix, cnh.Name)

	// If the name is too long, truncate it
	if len(desiredName) > maxPodNameLength {
		desiredName = desiredName[:maxPodNameLength]
	}

	return desiredName
}
