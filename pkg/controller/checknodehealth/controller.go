package checknodehealth

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

const (
	// PodPendingTimeout is the maximum time a pod can stay in Pending state
	// before being marked as failed
	PodPendingTimeout = time.Minute

	// CheckNodeHealthFinalizer is the finalizer used to ensure proper cleanup
	CheckNodeHealthFinalizer = "checknodehealth.clusterhealthmonitor.azure.com/finalizer"

	// CheckNodeHealthLabel is the label key used to identify check node health pods
	CheckNodeHealthLabel = "clusterhealthmonitor.azure.com/checknodehealth"

	// NodeLabel is the label key used to identify which node the pod is checking
	NodeLabel = "clusterhealthmonitor.azure.com/node"

	// Condition reasons for CheckNodeHealth
	ReasonCheckStarted = "CheckStarted"
	ReasonCheckPassed  = "CheckPassed"
	ReasonCheckFailed  = "CheckFailed"
	ReasonCheckUnknown = "CheckUnknown"
)

// CheckNodeHealthReconciler reconciles a CheckNodeHealth object
type CheckNodeHealthReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	CheckerPodImage     string // Image for the health check pod
	CheckerPodNamespace string // Namespace to create pods in
}

// +kubebuilder:rbac:groups=clusterhealthmonitor.azure.com,resources=checknodehealths,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clusterhealthmonitor.azure.com,resources=checknodehealths/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clusterhealthmonitor.azure.com,resources=checknodehealths/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete,namespace=kube-system

// SetupWithManager sets up the controller with the Manager
func (r *CheckNodeHealthReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&chmv1alpha1.CheckNodeHealth{}).
		Owns(&corev1.Pod{}). // Watch pods created by this controller
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
		}).
		Complete(r)
}

// Reconcile is part of the main kubernetes reconciliation loop
// This controller creates a pod on the target node to execute health checks.
// The pod updates the CheckNodeHealth status when checks complete.
func (r *CheckNodeHealthReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the CheckNodeHealth instance
	cnh := &chmv1alpha1.CheckNodeHealth{}
	if err := r.Get(ctx, req.NamespacedName, cnh); err != nil {
		// Resource not found, probably deleted
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	klog.InfoS("Reconciling CheckNodeHealth", "name", cnh.Name, "node", cnh.Spec.NodeRef.Name)

	// Handle deletion
	if cnh.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, cnh)
	}

	// Ensure finalizer is present
	if !controllerutil.ContainsFinalizer(cnh, CheckNodeHealthFinalizer) {
		controllerutil.AddFinalizer(cnh, CheckNodeHealthFinalizer)
		if err := r.Update(ctx, cnh); err != nil {
			klog.ErrorS(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		klog.V(1).InfoS("Added finalizer, continuing with reconcile")
	}

	// Check if already completed - if so, start cleanup process
	if isCompleted(cnh) {
		klog.V(1).InfoS("CheckNodeHealth already completed, starting cleanup")
		return r.handleCompletion(ctx, cnh)
	}

	// Check if pod exists and get its status, or create one if it doesn't exist
	pod, err := r.ensureHealthCheckPod(ctx, cnh)
	if err != nil {
		klog.ErrorS(err, "Failed to ensure health check pod")
		return ctrl.Result{}, err
	}

	// Mark the CheckNodeHealth as started
	if err := r.markStarted(ctx, cnh); err != nil {
		klog.ErrorS(err, "Failed to mark as started")
		return ctrl.Result{}, err
	}

	// Determine the overall result based on pod status
	return r.determineCheckResult(ctx, cnh, pod)
}

// determineCheckResult determines the overall result of the CheckNodeHealth based on pod status
func (r *CheckNodeHealthReconciler) determineCheckResult(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, pod *corev1.Pod) (ctrl.Result, error) {
	if err := r.updatePodstartCheckerResult(ctx, cnh, pod); err != nil {
		klog.ErrorS(err, "Failed to update PodStartup check result")
		return ctrl.Result{}, err
	}

	// Check if pod succeeded or failed (completed)
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		klog.InfoS("Health check pod completed, marking as completed", "phase", pod.Status.Phase)

		// Step 1: Mark as completed first (this records the result)
		if err := r.markCompleted(ctx, cnh, pod); err != nil {
			klog.ErrorS(err, "Failed to mark as completed")
			return ctrl.Result{}, err
		}

		// Step 2: Delete the pod
		if err := r.cleanupPod(ctx, cnh); err != nil {
			klog.ErrorS(err, "Failed to cleanup completed pod, will retry")
			// Don't return error - let reconcile handle cleanup via handleCompletion
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		klog.InfoS("Successfully marked as completed and deleted pod")
		return ctrl.Result{}, nil
	}

	// Check if pod is stuck in Pending state for too long
	if pod.Status.Phase == corev1.PodPending {
		if r.isPodPendingTimeout(cnh, pod) {
			message := fmt.Sprintf("Pod stuck in Pending state for more than %v", PodPendingTimeout)
			klog.InfoS("Health check pod pending timeout, marking as failed", "timeout", PodPendingTimeout)

			// Step 1: Mark as failed first (this records the result)
			if err := r.markFailed(ctx, cnh, message); err != nil {
				klog.ErrorS(err, "Failed to mark as failed")
				return ctrl.Result{}, err
			}

			// Step 2: Delete the stuck pod
			if err := r.cleanupPod(ctx, cnh); err != nil {
				klog.ErrorS(err, "Failed to cleanup timed out pod, will retry")
				// Don't return error - let reconcile handle cleanup via handleCompletion
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			return ctrl.Result{}, nil
		}
		// Pod is still pending but within timeout, requeue after a reasonable interval
		klog.V(1).InfoS("Health check pod still pending", "phase", pod.Status.Phase)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Pod is still running, will reconcile again when pod status changes
	klog.V(1).InfoS("Health check pod not finished yet", "phase", pod.Status.Phase)
	return ctrl.Result{}, nil
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
			Reason:             ReasonCheckStarted,
			LastTransitionTime: now,
		},
	}

	if err := r.Status().Update(ctx, cnh); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

func (r *CheckNodeHealthReconciler) markCompleted(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, pod *corev1.Pod) error {
	now := metav1.Now()
	cnh.Status.FinishedAt = &now

	// Determine the overall health status based on the new logic
	healthyStatus, reason, message := r.determineHealthyCondition(cnh, pod)

	cnh.Status.Conditions = []metav1.Condition{
		{
			Type:               "Healthy",
			Status:             healthyStatus,
			LastTransitionTime: now,
			Reason:             reason,
			Message:            message,
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
			Reason:             ReasonCheckFailed,
			Message:            message,
		},
	}

	if err := r.Status().Update(ctx, cnh); err != nil {
		return fmt.Errorf("failed to patch status: %w", err)
	}

	return nil
}

// determineHealthyCondition determines the Healthy condition status based on pod exit codes and Results
func (r *CheckNodeHealthReconciler) determineHealthyCondition(cnh *chmv1alpha1.CheckNodeHealth, pod *corev1.Pod) (metav1.ConditionStatus, string, string) {
	// Rule 1: Check if any container exit code == 10, or any Result.Status == "Unknown"
	if r.hasContainerExitCode10(pod) {
		return metav1.ConditionUnknown, ReasonCheckUnknown, "Health check pod failed to connect to API server to update status"
	}

	if r.hasUnknownResult(cnh) {
		return metav1.ConditionUnknown, ReasonCheckUnknown, "At least one health check result has Unknown status"
	}

	// Rule 2: Check if any Result.Status == "Unhealthy"
	if r.hasUnhealthyResult(cnh) {
		return metav1.ConditionFalse, ReasonCheckFailed, "At least one health check result is Unhealthy"
	}

	// Rule 3: All Results.Status == "Healthy" (or no results yet)
	if r.allResultsHealthy(cnh) {
		return metav1.ConditionTrue, ReasonCheckPassed, "All health checks completed successfully"
	}

	// Default case - should not happen if logic is correct
	return metav1.ConditionUnknown, ReasonCheckUnknown, "Unable to determine health status"
}

// hasContainerExitCode10 checks if any container in the pod exited with code 10
func (r *CheckNodeHealthReconciler) hasContainerExitCode10(pod *corev1.Pod) bool {
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.State.Terminated != nil && containerStatus.State.Terminated.ExitCode == 10 {
			return true
		}
	}
	return false
}

// hasUnknownResult checks if any result has Unknown status
func (r *CheckNodeHealthReconciler) hasUnknownResult(cnh *chmv1alpha1.CheckNodeHealth) bool {
	for _, result := range cnh.Status.Results {
		if result.Status == chmv1alpha1.CheckStatusUnknown {
			return true
		}
	}
	return false
}

// hasUnhealthyResult checks if any result has Unhealthy status
func (r *CheckNodeHealthReconciler) hasUnhealthyResult(cnh *chmv1alpha1.CheckNodeHealth) bool {
	for _, result := range cnh.Status.Results {
		if result.Status == chmv1alpha1.CheckStatusUnhealthy {
			return true
		}
	}
	return false
}

// allResultsHealthy checks if all results have Healthy status (or there are no results)
func (r *CheckNodeHealthReconciler) allResultsHealthy(cnh *chmv1alpha1.CheckNodeHealth) bool {
	// If no results, consider as healthy (checks haven't populated results yet)
	if len(cnh.Status.Results) == 0 {
		return true
	}

	// All results must be healthy
	for _, result := range cnh.Status.Results {
		if result.Status != chmv1alpha1.CheckStatusHealthy {
			return false
		}
	}
	return true
}

func isCompleted(cnh *chmv1alpha1.CheckNodeHealth) bool {
	return cnh.Status.FinishedAt != nil
}

// handleDeletion handles the deletion of CheckNodeHealth resources with proper cleanup
func (r *CheckNodeHealthReconciler) handleDeletion(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) (ctrl.Result, error) {
	klog.InfoS("Handling CheckNodeHealth deletion", "name", cnh.Name)

	// Clean up the pod
	if err := r.cleanupPod(ctx, cnh); err != nil {
		klog.ErrorS(err, "Failed to cleanup pod during deletion")
		// Return error to retry - don't remove finalizer yet
		return ctrl.Result{}, err
	}

	// Remove finalizer to allow deletion
	controllerutil.RemoveFinalizer(cnh, CheckNodeHealthFinalizer)
	if err := r.Update(ctx, cnh); err != nil {
		klog.ErrorS(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}

	klog.InfoS("CheckNodeHealth deletion completed", "name", cnh.Name)
	return ctrl.Result{}, nil
}

// handleCompletion handles completed checks by cleaning up any remaining pods
// This handles the case where pod deletion failed in determineCheckResult
// For completed checks, ensure any remaining pods are cleaned up
func (r *CheckNodeHealthReconciler) handleCompletion(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) (ctrl.Result, error) {
	if err := r.cleanupPod(ctx, cnh); err != nil {
		klog.ErrorS(err, "Failed to cleanup remaining pods")
		return ctrl.Result{}, err
	}

	klog.V(1).InfoS("CheckNodeHealth completion cleanup finished")
	return ctrl.Result{}, nil
}
