package upgradenodeinprogress

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	healthv1alpha1 "github.com/Azure/aks-health-signal/api/health/v1alpha1"
	upgradev1alpha1 "github.com/Azure/aks-health-signal/api/upgrade/v1alpha1"
	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

const (
	// ConditionTypeHealthy is the condition type used to indicate a healthy state.
	ConditionTypeHealthy = "Healthy"

	// HealthSignalSource identifies this controller as the source of HealthSignal
	HealthSignalSource = "ClusterHealthMonitor"

	// UpgradeNodeInProgressKind is the kind string for UpgradeNodeInProgress resources
	UpgradeNodeInProgressKind = "UpgradeNodeInProgress"

	// healthSignalOwnerUIDIndex is the field index for HealthSignal by owner UID
	healthSignalOwnerUIDIndex = ".metadata.ownerReferences.healthSignal.uid"

	// checkNodeHealthOwnerUIDIndex is the field index for CheckNodeHealth by owner UID
	checkNodeHealthOwnerUIDIndex = ".metadata.ownerReferences.checkNodeHealth.uid"
)

// UpgradeNodeInProgressReconciler reconciles an UpgradeNodeInProgress object
// and updates the corresponding HealthSignal status
type UpgradeNodeInProgressReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=upgrade.aks.io,resources=upgradenodeinprogresses,verbs=get;list;watch
// +kubebuilder:rbac:groups=health.aks.io,resources=healthsignals,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=health.aks.io,resources=healthsignals/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clusterhealthmonitor.azure.com,resources=checknodehealths,verbs=get;list;watch;create;update;patch;delete

// SetupWithManager sets up the controller with the Manager
func (r *UpgradeNodeInProgressReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Set up field indexer to find HealthSignals by owner UID
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &healthv1alpha1.HealthSignal{}, healthSignalOwnerUIDIndex, func(obj client.Object) []string {
		hs := obj.(*healthv1alpha1.HealthSignal)
		var uids []string
		for _, ref := range hs.OwnerReferences {
			if ref.Kind == UpgradeNodeInProgressKind {
				uids = append(uids, string(ref.UID))
			}
		}
		return uids
	}); err != nil {
		return err
	}

	// Set up field indexer to find CheckNodeHealths by owner UID
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &chmv1alpha1.CheckNodeHealth{}, checkNodeHealthOwnerUIDIndex, func(obj client.Object) []string {
		cnh := obj.(*chmv1alpha1.CheckNodeHealth)
		var uids []string
		for _, ref := range cnh.OwnerReferences {
			if ref.Kind == "HealthSignal" {
				uids = append(uids, string(ref.UID))
			}
		}
		return uids
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&upgradev1alpha1.UpgradeNodeInProgress{}).
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
	hsOwnerRef, found := lo.Find(cnh.OwnerReferences, func(ref metav1.OwnerReference) bool {
		return ref.Kind == "HealthSignal"
	})
	if !found {
		return nil
	}

	// Get the HealthSignal to find its UpgradeNodeInProgress owner
	hs := &healthv1alpha1.HealthSignal{}
	if err := r.Get(ctx, client.ObjectKey{Name: hsOwnerRef.Name}, hs); err != nil {
		klog.V(1).ErrorS(err, "Failed to get HealthSignal for mapping", "healthSignal", hsOwnerRef.Name)
		return nil
	}

	// Find the UpgradeNodeInProgress owner of the HealthSignal
	for _, ownerRef := range hs.OwnerReferences {
		if ownerRef.Kind == UpgradeNodeInProgressKind {
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
	unip := &upgradev1alpha1.UpgradeNodeInProgress{}
	if err := r.Get(ctx, req.NamespacedName, unip); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	klog.InfoS("Reconciling UpgradeNodeInProgress", "name", unip.Name, "node", unip.Spec.NodeRef.Name)

	// Skip if being deleted - Kubernetes GC will clean up HealthSignal and CheckNodeHealth via OwnerReference
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

	if cnh.Status.FinishedAt == nil {
		klog.V(1).InfoS("CheckNodeHealth still in progress", "name", cnh.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Check if CheckNodeHealth has completed
	// Copy results from CheckNodeHealth to HealthSignal
	if err := r.syncHealthSignalStatus(ctx, healthSignal, cnh); err != nil {
		klog.ErrorS(err, "Failed to sync HealthSignal status")
		return ctrl.Result{}, err
	}
	klog.InfoS("CheckNodeHealth completed, synced to HealthSignal", "name", unip.Name)
	return ctrl.Result{}, nil
}

// ensureHealthSignal creates or retrieves the HealthSignal for the given UpgradeNodeInProgress
func (r *UpgradeNodeInProgressReconciler) ensureHealthSignal(ctx context.Context, unip *upgradev1alpha1.UpgradeNodeInProgress) (*healthv1alpha1.HealthSignal, error) {
	// Find HealthSignal owned by this UpgradeNodeInProgress using the field index
	healthSignalList := &healthv1alpha1.HealthSignalList{}
	if err := r.List(ctx, healthSignalList, client.MatchingFields{healthSignalOwnerUIDIndex: string(unip.UID)}); err != nil {
		return nil, fmt.Errorf("failed to list HealthSignals by owner UID: %w", err)
	}

	if len(healthSignalList.Items) > 0 {
		// Return the first (and should be only) HealthSignal owned by this UpgradeNodeInProgress
		return &healthSignalList.Items[0], nil
	}

	// Create new HealthSignal with naming convention: {unipName}-{source} (lowercase for RFC 1123)
	healthSignalName := strings.ToLower(fmt.Sprintf("%s-%s", unip.Name, HealthSignalSource))
	healthSignal := &healthv1alpha1.HealthSignal{
		ObjectMeta: metav1.ObjectMeta{
			Name: healthSignalName,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: upgradev1alpha1.GroupVersion.String(),
					Kind:       UpgradeNodeInProgressKind,
					Name:       unip.Name,
					UID:        unip.UID,
				},
			},
		},
		Spec: healthv1alpha1.HealthSignalSpec{
			Source: corev1.ObjectReference{
				Name: HealthSignalSource,
			},
			Type: healthv1alpha1.NodeHealth,
			Target: &corev1.ObjectReference{
				Kind: "Node",
				Name: unip.Spec.NodeRef.Name,
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
func (r *UpgradeNodeInProgressReconciler) ensureCheckNodeHealth(ctx context.Context, hs *healthv1alpha1.HealthSignal, nodeName string) (*chmv1alpha1.CheckNodeHealth, error) {
	// Find CheckNodeHealth owned by this HealthSignal using the field index
	cnhList := &chmv1alpha1.CheckNodeHealthList{}
	if err := r.List(ctx, cnhList, client.MatchingFields{checkNodeHealthOwnerUIDIndex: string(hs.UID)}); err != nil {
		return nil, fmt.Errorf("failed to list CheckNodeHealths by owner UID: %w", err)
	}

	if len(cnhList.Items) > 0 {
		return &cnhList.Items[0], nil
	}

	cnhName := hs.Name
	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{
			Name: cnhName,
		},
		Spec: chmv1alpha1.CheckNodeHealthSpec{
			NodeRef: chmv1alpha1.NodeReference{
				Name: nodeName,
			},
		},
	}

	// Set HealthSignal as the owner (Controller=true) - this blocks deletion of CheckNodeHealth until HealthSignal is deleted
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
func (r *UpgradeNodeInProgressReconciler) syncHealthSignalStatus(ctx context.Context, hs *healthv1alpha1.HealthSignal, cnh *chmv1alpha1.CheckNodeHealth) error {
	// Copy conditions from CheckNodeHealth to HealthSignal
	hs.Status.Conditions = make([]metav1.Condition, len(cnh.Status.Conditions))
	copy(hs.Status.Conditions, cnh.Status.Conditions)

	if err := r.Status().Update(ctx, hs); err != nil {
		return fmt.Errorf("failed to update HealthSignal status: %w", err)
	}

	klog.InfoS("Synced HealthSignal status from CheckNodeHealth", "name", hs.Name)
	return nil
}
