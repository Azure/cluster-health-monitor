package controller

import (
	"context"
	"fmt"

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

// CheckNodeHealthReconciler reconciles a CheckNodeHealth object
type CheckNodeHealthReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
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

	// Check if pod exists and get its status
	podName := getHealthCheckPodName(cnh)
	pod := &corev1.Pod{}
	err := r.Get(ctx, client.ObjectKey{
		Name:      podName,
		Namespace: r.PodNamespace,
	}, pod)

	if err != nil {
		if apierrors.IsNotFound(err) {
			// Pod doesn't exist, create it
			if err := r.ensureHealthCheckPod(ctx, cnh); err != nil {
				logger.Error(err, "Failed to ensure health check pod")
				return ctrl.Result{}, err
			}
			// Pod created, will reconcile again when pod status changes
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Pod exists, check its status
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

	// Pod is still running (Pending or Running), will reconcile again when pod status changes
	logger.V(1).Info("Health check pod not finished yet", "phase", pod.Status.Phase)
	return ctrl.Result{}, nil
}

func (r *CheckNodeHealthReconciler) ensureHealthCheckPod(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) error {
	logger := log.FromContext(ctx)
	podName := getHealthCheckPodName(cnh)

	// Check if pod already exists
	existingPod := &corev1.Pod{}
	err := r.Get(ctx, client.ObjectKey{
		Name:      podName,
		Namespace: r.PodNamespace,
	}, existingPod)

	if err == nil {
		// Pod already exists
		logger.V(1).Info("Health check pod already exists", "pod", podName)
		return nil
	}

	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get pod: %w", err)
	}

	// Create the pod
	pod := r.buildHealthCheckPod(cnh, podName)

	// Set CheckNodeHealth as owner
	if err := controllerutil.SetControllerReference(cnh, pod, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}

	logger.Info("Creating health check pod", "pod", podName, "node", cnh.Spec.NodeRef.Name)
	if err := r.Create(ctx, pod); err != nil {
		return fmt.Errorf("failed to create pod: %w", err)
	}

	// Set StartedAt timestamp when pod is created
	now := metav1.Now()
	cnh.Status.StartedAt = &now
	cnh.Status.Conditions = []metav1.Condition{
		{
			Type:               "Healthy",
			Status:             metav1.ConditionUnknown,
			LastTransitionTime: metav1.Now(),
		},
	}
	if err := r.Status().Update(ctx, cnh); err != nil {
		logger.Error(err, "Failed to update StartedAt timestamp")
		// Don't return error here, pod creation succeeded
	}

	return nil
}

func (r *CheckNodeHealthReconciler) buildHealthCheckPod(cnh *chmv1alpha1.CheckNodeHealth, podName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: r.PodNamespace,
			Labels: map[string]string{
				"app":                "cluster-health-monitor",
				"checknodehealth":    cnh.Name,
				"chm.azure.com/node": cnh.Spec.NodeRef.Name,
			},
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
}

func (r *CheckNodeHealthReconciler) cleanupPod(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) error {
	podName := getHealthCheckPodName(cnh)

	pod := &corev1.Pod{}
	err := r.Get(ctx, client.ObjectKey{
		Name:      podName,
		Namespace: r.PodNamespace,
	}, pod)

	if apierrors.IsNotFound(err) {
		// Pod already deleted
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to get pod: %w", err)
	}

	// Delete the pod
	if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete pod: %w", err)
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
			Reason:             "ChecksPassed",
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
