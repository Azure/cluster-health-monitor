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

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

const (
	// CheckNodeHealthLabel is the label key used to identify check node health pods
	CheckNodeHealthLabel = "clusterhealthmonitor.azure.com/checknodehealth"

	// NodeLabel is the label key used to identify which node the pod is checking
	NodeLabel = "clusterhealthmonitor.azure.com/node"
)

// CheckNodeHealthReconciler reconciles a CheckNodeHealth object
type CheckNodeHealthReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	CheckerPodLabel     string // Label to identify health check pods
	CheckerPodImage     string // Image for the health check pod
	CheckerPodNamespace string // Namespace to create pods in
}

// +kubebuilder:rbac:groups=clusterhealthmonitor.azure.com,resources=checknodehealths,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clusterhealthmonitor.azure.com,resources=checknodehealths/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete,namespace=kube-system

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
	// Fetch the CheckNodeHealth instance
	cnh := &chmv1alpha1.CheckNodeHealth{}
	if err := r.Get(ctx, req.NamespacedName, cnh); err != nil {
		// Resource not found, probably deleted
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	klog.InfoS("Reconciling CheckNodeHealth", "name", cnh.Name, "node", cnh.Spec.NodeRef.Name)

	// Check if already completed - if so, cleanup pod and skip
	if isCompleted(cnh) {
		klog.InfoS("CheckNodeHealth already completed", "name", cnh.Name)
		return ctrl.Result{}, nil
	}

	// Check if pod exists and get its status, or create one if it doesn't exist
	pod, err := r.ensureHealthCheckPod(ctx, cnh)
	if err != nil {
		klog.ErrorS(err, "Failed to ensure health check pod")
		return ctrl.Result{}, err
	}

	// Mark the CheckNodeHealth as started
	if err := r.markStarted(ctx, cnh); err != nil {
		klog.ErrorS(err, "Failed to mark as started", "name", cnh.Name)
		return ctrl.Result{}, err
	}

	// Determine the overall result based on pod status
	return r.determineCheckResult(ctx, cnh, pod)
}

func (r *CheckNodeHealthReconciler) determineCheckResult(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, pod *corev1.Pod) (ctrl.Result, error) {
	// Check if pod succeeded or failed (completed)
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		klog.InfoS("Health check pod completed, marking as completed", "phase", pod.Status.Phase)

		if err := r.markCompleted(ctx, cnh); err != nil {
			klog.ErrorS(err, "Failed to mark as completed")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	if pod.Status.Phase == corev1.PodPending {
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
			Reason:             "unknown",
			LastTransitionTime: now,
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
	// TODO: In real implementation, set condition based on actual check results
	cnh.Status.Conditions = []metav1.Condition{
		{
			Type:               "Healthy",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "ChecksPassed",
			Message:            "Health checks completed successfully",
		},
	}

	if err := r.Status().Update(ctx, cnh); err != nil {
		return fmt.Errorf("failed to patch status: %w", err)
	}

	return nil
}

func isCompleted(cnh *chmv1alpha1.CheckNodeHealth) bool {
	return cnh.Status.FinishedAt != nil
}
