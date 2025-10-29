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

// CheckHealthMonitorReconciler reconciles a CheckHealthMonitor object
type CheckHealthMonitorReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	PodImage      string // Image for the health check pod
	PodNamespace  string // Namespace to create pods in
	ConfigMapName string // ConfigMap with checker config
}

// +kubebuilder:rbac:groups=chm.azure.com,resources=checkhealthmonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=chm.azure.com,resources=checkhealthmonitors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=chm.azure.com,resources=checkhealthmonitors/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

// SetupWithManager sets up the controller with the Manager
func (r *CheckHealthMonitorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&chmv1alpha1.CheckHealthMonitor{}).
		Owns(&corev1.Pod{}). // Watch pods created by this controller
		Complete(r)
}

// Reconcile is part of the main kubernetes reconciliation loop
// This controller creates a pod on the target node to execute health checks.
// The pod updates the CheckHealthMonitor status when checks complete.
func (r *CheckHealthMonitorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the CheckHealthMonitor instance
	chm := &chmv1alpha1.CheckHealthMonitor{}
	if err := r.Get(ctx, req.NamespacedName, chm); err != nil {
		// Resource not found, probably deleted
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("Reconciling CheckHealthMonitor", "name", chm.Name, "node", chm.Spec.NodeRef.Name)

	// Check if already completed - if so, cleanup pod and skip
	if isCompleted(chm) {
		logger.V(1).Info("CheckHealthMonitor already completed")
		// Clean up the pod if it still exists
		if err := r.cleanupPod(ctx, chm); err != nil {
			logger.Error(err, "Failed to cleanup pod")
		}
		return ctrl.Result{}, nil
	}

	// Check if pod exists and get its status
	podName := getHealthCheckPodName(chm)
	pod := &corev1.Pod{}
	err := r.Get(ctx, client.ObjectKey{
		Name:      podName,
		Namespace: r.PodNamespace,
	}, pod)

	if err != nil {
		if apierrors.IsNotFound(err) {
			// Pod doesn't exist, create it
			if err := r.ensureHealthCheckPod(ctx, chm); err != nil {
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
		if err := r.markCompleted(ctx, chm); err != nil {
			logger.Error(err, "Failed to mark as completed")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Check if pod failed
	if pod.Status.Phase == corev1.PodFailed {
		logger.Info("Health check pod failed, marking as failed")
		if err := r.markFailed(ctx, chm, "Pod failed to execute health checks"); err != nil {
			logger.Error(err, "Failed to mark as failed")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Pod is still running (Pending or Running), will reconcile again when pod status changes
	logger.V(1).Info("Health check pod not finished yet", "phase", pod.Status.Phase)
	return ctrl.Result{}, nil
}

func (r *CheckHealthMonitorReconciler) ensureHealthCheckPod(ctx context.Context, chm *chmv1alpha1.CheckHealthMonitor) error {
	logger := log.FromContext(ctx)
	podName := getHealthCheckPodName(chm)

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
	pod := r.buildHealthCheckPod(chm, podName)

	// Set CheckHealthMonitor as owner
	if err := controllerutil.SetControllerReference(chm, pod, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}

	logger.Info("Creating health check pod", "pod", podName, "node", chm.Spec.NodeRef.Name)
	if err := r.Create(ctx, pod); err != nil {
		return fmt.Errorf("failed to create pod: %w", err)
	}

	return nil
}

func (r *CheckHealthMonitorReconciler) buildHealthCheckPod(chm *chmv1alpha1.CheckHealthMonitor, podName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: r.PodNamespace,
			Labels: map[string]string{
				"app":                "cluster-health-monitor",
				"checkhealthmonitor": chm.Name,
				"chm.azure.com/node": chm.Spec.NodeRef.Name,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeName:      chm.Spec.NodeRef.Name, // Schedule on specific node
			Containers: []corev1.Container{
				{
					Name:  "health-checker",
					Image: r.PodImage,
					Args: []string{
						"--config=/etc/cluster-health-monitor/config.yaml",
						fmt.Sprintf("--cr-name=%s", chm.Name),
						fmt.Sprintf("--cr-namespace=%s", chm.Namespace),
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "config",
							MountPath: "/etc/cluster-health-monitor",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: r.ConfigMapName,
							},
						},
					},
				},
			},
			ServiceAccountName: "cluster-health-monitor", // Needs permissions to update CR status
		},
	}
}

func (r *CheckHealthMonitorReconciler) cleanupPod(ctx context.Context, chm *chmv1alpha1.CheckHealthMonitor) error {
	podName := getHealthCheckPodName(chm)

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

func (r *CheckHealthMonitorReconciler) markCompleted(ctx context.Context, chm *chmv1alpha1.CheckHealthMonitor) error {
	chm.Status.Conditions = []metav1.Condition{
		{
			Type:               string(chmv1alpha1.CheckHealthMonitorConditionCompleted),
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "ChecksCompleted",
			Message:            "Health checks completed successfully",
		},
	}

	if err := r.Status().Update(ctx, chm); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

func (r *CheckHealthMonitorReconciler) markFailed(ctx context.Context, chm *chmv1alpha1.CheckHealthMonitor, message string) error {
	chm.Status.Conditions = []metav1.Condition{
		{
			Type:               string(chmv1alpha1.CheckHealthMonitorConditionFailed),
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "ChecksFailed",
			Message:            message,
		},
	}

	if err := r.Status().Update(ctx, chm); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

func getHealthCheckPodName(chm *chmv1alpha1.CheckHealthMonitor) string {
	return fmt.Sprintf("health-check-%s", chm.Name)
}

func isCompleted(chm *chmv1alpha1.CheckHealthMonitor) bool {
	for _, cond := range chm.Status.Conditions {
		if cond.Type == string(chmv1alpha1.CheckHealthMonitorConditionCompleted) && cond.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}
