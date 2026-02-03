package upgradenodeinprogress

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	unipv1alpha1 "github.com/Azure/cluster-health-monitor/apis/upgradenodeinprogresses/v1alpha1"
)

const (
	// ConditionTypeHealthy is the condition type used to indicate a healthy state.
	ConditionTypeHealthy = "Healthy"

	// HealthSignalSource identifies this controller as the source of HealthSignal
	HealthSignalSource = "ClusterHealthMonitor"

	// HealthSignalNameSuffix is appended to UpgradeNodeInProgress name to form HealthSignal name
	HealthSignalNameSuffix = "-" + HealthSignalSource
)

// UpgradeNodeInProgressReconciler reconciles an UpgradeNodeInProgress object
// and updates the corresponding HealthSignal status
type UpgradeNodeInProgressReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=health.aks.io,resources=upgradenodeinprogresses,verbs=get;list;watch
// +kubebuilder:rbac:groups=health.aks.io,resources=healthsignals,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=health.aks.io,resources=healthsignals/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clusterhealthmonitor.azure.com,resources=checknodehealths,verbs=get;list;watch;create;update;patch;delete

// SetupWithManager sets up the controller with the Manager
func (r *UpgradeNodeInProgressReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&unipv1alpha1.UpgradeNodeInProgress{}).
		Watches(&chmv1alpha1.CheckNodeHealth{}, handler.EnqueueRequestsFromMapFunc(r.mapCheckNodeHealthToUpgradeNodeInProgress)).
		Complete(r)
}

// mapCheckNodeHealthToUpgradeNodeInProgress maps CheckNodeHealth events to UpgradeNodeInProgress reconcile requests
// Ownership chain: UpgradeNodeInProgress → HealthSignal → CheckNodeHealth
// This function traces back: CheckNodeHealth → HealthSignal (owner) → UpgradeNodeInProgress (owner)
func (r *UpgradeNodeInProgressReconciler) mapCheckNodeHealthToUpgradeNodeInProgress(ctx context.Context, obj client.Object) []ctrl.Request {
	cnh, ok := obj.(*chmv1alpha1.CheckNodeHealth)
	if !ok {
		return nil
	}

	// Find the HealthSignal owner of this CheckNodeHealth
	var hsOwnerRef *metav1.OwnerReference
	for i := range cnh.OwnerReferences {
		if cnh.OwnerReferences[i].Kind == "HealthSignal" {
			hsOwnerRef = &cnh.OwnerReferences[i]
			break
		}
	}
	if hsOwnerRef == nil {
		return nil
	}

	// Get the HealthSignal to find its UpgradeNodeInProgress owner
	hs := &unipv1alpha1.HealthSignal{}
	if err := r.Get(ctx, client.ObjectKey{Name: hsOwnerRef.Name}, hs); err != nil {
		klog.V(1).ErrorS(err, "Failed to get HealthSignal for mapping", "healthSignal", hsOwnerRef.Name)
		return nil
	}

	// Find the UpgradeNodeInProgress owner of the HealthSignal
	for _, ownerRef := range hs.OwnerReferences {
		if ownerRef.Kind == "UpgradeNodeInProgress" {
			return []ctrl.Request{
				{NamespacedName: client.ObjectKey{Name: ownerRef.Name}},
			}
		}
	}

	return nil
}

// Reconcile is part of the main kubernetes reconciliation loop.
// This controller watches UpgradeNodeInProgress, creates CheckNodeHealth CR,
// and copies results to HealthSignal.
func (r *UpgradeNodeInProgressReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the UpgradeNodeInProgress instance
	unip := &unipv1alpha1.UpgradeNodeInProgress{}
	if err := r.Get(ctx, req.NamespacedName, unip); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	klog.InfoS("Reconciling UpgradeNodeInProgress", "name", unip.Name, "node", unip.Spec.NodeRef.Name)

	// Skip if being deleted - Kubernetes GC will clean up CheckNodeHealth via OwnerReference
	if unip.DeletionTimestamp != nil {
		klog.InfoS("UpgradeNodeInProgress is being deleted, skipping", "name", unip.Name)
		return ctrl.Result{}, nil
	}

	// Ensure HealthSignal exists for this UpgradeNodeInProgress
	healthSignal, err := r.ensureHealthSignal(ctx, unip)
	if err != nil {
		klog.ErrorS(err, "Failed to ensure HealthSignal")
		return ctrl.Result{}, err
	}

	// Ensure CheckNodeHealth exists to perform the actual health check
	// CheckNodeHealth is owned by HealthSignal
	cnh, err := r.ensureCheckNodeHealth(ctx, healthSignal, unip.Spec.NodeRef.Name)
	if err != nil {
		klog.ErrorS(err, "Failed to ensure CheckNodeHealth")
		return ctrl.Result{}, err
	}

	// Check if CheckNodeHealth has completed
	if cnh.Status.FinishedAt != nil {
		// Copy results from CheckNodeHealth to HealthSignal
		if err := r.syncHealthSignalStatus(ctx, healthSignal, cnh); err != nil {
			klog.ErrorS(err, "Failed to sync HealthSignal status")
			return ctrl.Result{}, err
		}
		klog.InfoS("CheckNodeHealth completed, synced to HealthSignal", "name", unip.Name)
		return ctrl.Result{}, nil
	}

	// CheckNodeHealth is still in progress, requeue to check later
	klog.V(1).InfoS("CheckNodeHealth still in progress", "name", cnh.Name)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// ensureHealthSignal creates or retrieves the HealthSignal for the given UpgradeNodeInProgress
func (r *UpgradeNodeInProgressReconciler) ensureHealthSignal(ctx context.Context, unip *unipv1alpha1.UpgradeNodeInProgress) (*unipv1alpha1.HealthSignal, error) {
	// Use naming convention: {unipName}-{source}
	healthSignalName := fmt.Sprintf("%s-%s", unip.Name, HealthSignalSource)

	healthSignal := &unipv1alpha1.HealthSignal{}
	err := r.Get(ctx, client.ObjectKey{Name: healthSignalName}, healthSignal)
	if err == nil {
		return healthSignal, nil
	}

	if client.IgnoreNotFound(err) != nil {
		return nil, fmt.Errorf("failed to get HealthSignal: %w", err)
	}

	// Create new HealthSignal
	healthSignal = &unipv1alpha1.HealthSignal{
		ObjectMeta: metav1.ObjectMeta{
			Name: healthSignalName,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: unipv1alpha1.SchemeGroupVersion.String(),
					Kind:       "UpgradeNodeInProgress",
					Name:       unip.Name,
					UID:        unip.UID,
				},
			},
		},
		Spec: unipv1alpha1.HealthSignalSpec{
			Source: HealthSignalSource,
			Type:   unipv1alpha1.HealthSignalTypeNodeHealth,
			Target: unipv1alpha1.HealthSignalTarget{
				NodeName: unip.Spec.NodeRef.Name,
			},
		},
	}

	if err := r.Create(ctx, healthSignal); err != nil {
		return nil, fmt.Errorf("failed to create HealthSignal: %w", err)
	}

	klog.InfoS("Created HealthSignal", "name", healthSignalName, "source", HealthSignalSource, "node", unip.Spec.NodeRef.Name)
	return healthSignal, nil
}

// ensureCheckNodeHealth creates or retrieves the CheckNodeHealth for the given HealthSignal
// CheckNodeHealth is owned by HealthSignal (Controller=true)
func (r *UpgradeNodeInProgressReconciler) ensureCheckNodeHealth(ctx context.Context, hs *unipv1alpha1.HealthSignal, nodeName string) (*chmv1alpha1.CheckNodeHealth, error) {
	// Use the same name as HealthSignal for CheckNodeHealth
	cnhName := hs.Name

	cnh := &chmv1alpha1.CheckNodeHealth{}
	err := r.Get(ctx, client.ObjectKey{Name: cnhName}, cnh)
	if err == nil {
		return cnh, nil
	}

	if client.IgnoreNotFound(err) != nil {
		return nil, fmt.Errorf("failed to get CheckNodeHealth: %w", err)
	}

	// Create new CheckNodeHealth
	cnh = &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{
			Name: cnhName,
		},
		Spec: chmv1alpha1.CheckNodeHealthSpec{
			NodeRef: chmv1alpha1.NodeReference{
				Name: nodeName,
			},
		},
	}

	// Set HealthSignal as the owner (Controller=true) - this enables:
	// 1. Automatic garbage collection when HealthSignal is deleted
	// 2. Tracing back to UpgradeNodeInProgress via HealthSignal's owner
	if err := controllerutil.SetControllerReference(hs, cnh, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference: %w", err)
	}

	if err := r.Create(ctx, cnh); err != nil {
		return nil, fmt.Errorf("failed to create CheckNodeHealth: %w", err)
	}

	klog.InfoS("Created CheckNodeHealth", "name", cnhName, "node", nodeName, "owner", hs.Name)
	return cnh, nil
}

// syncHealthSignalStatus copies the status from CheckNodeHealth to HealthSignal
func (r *UpgradeNodeInProgressReconciler) syncHealthSignalStatus(ctx context.Context, hs *unipv1alpha1.HealthSignal, cnh *chmv1alpha1.CheckNodeHealth) error {
	// Check if already synced (FinishedAt is set)
	if hs.Status.FinishedAt != nil {
		return nil
	}

	// Copy timing information
	hs.Status.StartedAt = cnh.Status.StartedAt
	hs.Status.FinishedAt = cnh.Status.FinishedAt

	// Copy conditions from CheckNodeHealth to HealthSignal
	hs.Status.Condition = make([]metav1.Condition, len(cnh.Status.Conditions))
	copy(hs.Status.Condition, cnh.Status.Conditions)

	if err := r.Status().Update(ctx, hs); err != nil {
		return fmt.Errorf("failed to update HealthSignal status: %w", err)
	}

	klog.InfoS("Synced HealthSignal status from CheckNodeHealth", "name", hs.Name)
	return nil
}
