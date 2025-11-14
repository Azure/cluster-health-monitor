package checknodehealth

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

const (
	// PodPendingTimeout is the maximum time a pod can stay in Pending state
	// before being marked as failed
	PodPendingTimeout = 10 * time.Minute
)

// CheckNodeHealthReconciler reconciles a CheckNodeHealth object
type CheckNodeHealthReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	PodLabel     string // Label to identify health check pods
	PodImage     string // Image for the health check pod
	PodNamespace string // Namespace to create pods in
}

// +kubebuilder:rbac:groups=chm.azure.com,resources=checknodehealths,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=chm.azure.com,resources=checknodehealths/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=chm.azure.com,resources=checknodehealths/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

// SetupWithManager sets up the controller with the Manager
func (r *CheckNodeHealthReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&chmv1alpha1.CheckNodeHealth{}).
		Owns(&corev1.Pod{}). // Watch pods created by this controller
		Complete(r)
}

// Reconcile is part of the main kubernetes reconciliation loop
// This controller creates a pod on the target node to execute health checks.
// The pod updates the CheckNodeHealth status when checks complete.
func (r *CheckNodeHealthReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the CheckNodeHealth instance
	cnh := &chmv1alpha1.CheckNodeHealth{}
	if err := r.Get(ctx, req.NamespacedName, cnh); err != nil {
		// Resource not found, probably deleted
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("Reconciling CheckNodeHealth", "name", cnh.Name, "node", cnh.Spec.NodeRef.Name)

	// Check if already completed - if so, cleanup pod and skip
	if isCompleted(cnh) {
		logger.V(1).Info("CheckNodeHealth already completed")
		// Clean up the pod if it still exists
		if err := r.cleanupPod(ctx, cnh); err != nil {
			logger.Error(err, "Failed to cleanup pod")
		}
		return ctrl.Result{}, nil
	}

	// Check if pod exists and get its status, or create one if it doesn't exist
	pod, err := r.ensureHealthCheckPod(ctx, cnh)
	if err != nil {
		logger.Error(err, "Failed to ensure health check pod")
		return ctrl.Result{}, err
	}

	// Mark the CheckNodeHealth as started
	if err := r.markStarted(ctx, cnh); err != nil {
		logger.Error(err, "Failed to mark as started")
		return ctrl.Result{}, err
	}

	// Determine the overall result based on pod status
	return r.determineCheckResult(ctx, cnh, pod)
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

// determineCheckResult determines the overall result of the CheckNodeHealth based on pod status
func (r *CheckNodeHealthReconciler) determineCheckResult(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, pod *corev1.Pod) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check if pod succeeded
	if pod.Status.Phase == corev1.PodSucceeded {
		logger.Info("Health check pod succeeded, marking as completed")
		if err := r.markCompleted(ctx, cnh); err != nil {
			logger.Error(err, "Failed to mark as completed")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Check if pod failed
	if pod.Status.Phase == corev1.PodFailed {
		logger.Info("Health check pod failed, marking as failed")
		if err := r.markFailed(ctx, cnh, "Pod failed to execute health checks"); err != nil {
			logger.Error(err, "Failed to mark as failed")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Check if pod is stuck in Pending state for too long
	if pod.Status.Phase == corev1.PodPending {
		if r.isPodPendingTimeout(cnh, pod) {
			message := fmt.Sprintf("Pod stuck in Pending state for more than %v", PodPendingTimeout)
			logger.Info("Health check pod pending timeout, marking as failed", "timeout", PodPendingTimeout)
			if err := r.markFailed(ctx, cnh, message); err != nil {
				logger.Error(err, "Failed to mark as failed")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		// Pod is still pending but within timeout, requeue after a reasonable interval
		logger.V(1).Info("Health check pod still pending", "phase", pod.Status.Phase)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Pod is still running, will reconcile again when pod status changes
	logger.V(1).Info("Health check pod not finished yet", "phase", pod.Status.Phase)
	return ctrl.Result{}, nil
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

func (r *CheckNodeHealthReconciler) markStarted(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) error {
	// Only update status if StartedAt is not already set
	if cnh.Status.StartedAt != nil {
		return nil
	}

	now := metav1.Now()
	cnh.Status.StartedAt = &now
	cnh.Status.Conditions = []metav1.Condition{
		{
			Type:               "Healthy",
			Status:             metav1.ConditionUnknown,
			Reason:             "unknown",
			LastTransitionTime: metav1.Now(),
		},
	}

	if err := r.Status().Update(ctx, cnh); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

func (r *CheckNodeHealthReconciler) markCompleted(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) error {
	now := metav1.Now()
	cnh.Status.FinishedAt = &now
	cnh.Status.Conditions = []metav1.Condition{
		{
			Type:               "Healthy",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "ChecksPasseddd",
			Message:            "Health checks completed successfully",
		},
	}

	if err := r.Status().Update(ctx, cnh); err != nil {
		return fmt.Errorf("failed to patch status: %w", err)
	}

	return nil
}

func (r *CheckNodeHealthReconciler) markFailed(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, message string) error {
	now := metav1.Now()
	cnh.Status.FinishedAt = &now
	cnh.Status.Conditions = []metav1.Condition{
		{
			Type:               "Healthy",
			Status:             metav1.ConditionFalse,
			LastTransitionTime: metav1.Now(),
			Reason:             "ResourceUnavailable",
			Message:            message,
		},
	}

	if err := r.Status().Update(ctx, cnh); err != nil {
		return fmt.Errorf("failed to patch status: %w", err)
	}

	return nil
}

func getHealthCheckPodName(cnh *chmv1alpha1.CheckNodeHealth) string {
	return fmt.Sprintf("health-check-%s", cnh.Name)
}

func isCompleted(cnh *chmv1alpha1.CheckNodeHealth) bool {
	return cnh.Status.FinishedAt != nil
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
