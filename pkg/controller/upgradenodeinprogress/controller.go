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
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	healthv1alpha1 "github.com/Azure/aks-health-signal/api/health/v1alpha1"
	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

const (
	// ConditionTypeHealthy is the condition type used to indicate a healthy state.
	ConditionTypeHealthy = "Healthy"

	// HealthSignalSource identifies this controller as the source of HealthSignal
	HealthSignalSource = "ClusterHealthMonitor"

	// HealthCheckRequestKind is the kind string for HealthCheckRequest resources
	HealthCheckRequestKind = "HealthCheckRequest"

	// HealthSignalKind is the kind string for HealthSignal resources
	HealthSignalKind = "HealthSignal"

	// healthSignalOwnerUIDIndex is the field index for HealthSignal by owner UID
	healthSignalOwnerUIDIndex = ".metadata.ownerReferences.healthSignal.uid"

	// checkNodeHealthOwnerUIDIndex is the field index for CheckNodeHealth by owner UID
	checkNodeHealthOwnerUIDIndex = ".metadata.ownerReferences.checkNodeHealth.uid"
)

// HealthCheckRequestReconciler reconciles a HealthCheckRequest object
// and updates the corresponding HealthSignal status
type HealthCheckRequestReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=health.aks.io,resources=healthcheckrequests,verbs=get;list;watch
// +kubebuilder:rbac:groups=health.aks.io,resources=healthsignals,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=health.aks.io,resources=healthsignals/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clusterhealthmonitor.azure.com,resources=checknodehealths,verbs=get;list;watch;create;update;patch;delete

// SetupWithManager sets up the controller with the Manager
func (r *HealthCheckRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Set up field indexer to find HealthSignals by owner UID, so that we can efficiently find the HealthSignal associated with a HealthCheckRequest
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &healthv1alpha1.HealthSignal{}, healthSignalOwnerUIDIndex, func(obj client.Object) []string {
		hs := obj.(*healthv1alpha1.HealthSignal)
		var uids []string
		for _, ref := range hs.OwnerReferences {
			if ref.Kind == HealthCheckRequestKind {
				uids = append(uids, string(ref.UID))
			}
		}
		return uids
	}); err != nil {
		return err
	}

	// Set up field indexer to find CheckNodeHealths by owner UID, so that we can efficiently find the CheckNodeHealth associated with a HealthSignal
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &chmv1alpha1.CheckNodeHealth{}, checkNodeHealthOwnerUIDIndex, func(obj client.Object) []string {
		cnh := obj.(*chmv1alpha1.CheckNodeHealth)
		var uids []string
		for _, ref := range cnh.OwnerReferences {
			if ref.Kind == HealthSignalKind {
				uids = append(uids, string(ref.UID))
			}
		}
		return uids
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&healthv1alpha1.HealthCheckRequest{}).
		Watches(&chmv1alpha1.CheckNodeHealth{}, handler.EnqueueRequestsFromMapFunc(r.mapCheckNodeHealthToHealthCheckRequest)).
		Complete(r)
}

// mapCheckNodeHealthToHealthCheckRequest maps CheckNodeHealth events to HealthCheckRequest reconcile requests
// Ownership chain: HealthCheckRequest → HealthSignal → CheckNodeHealth
// This function traces back: CheckNodeHealth → HealthSignal (owner) → HealthCheckRequest (owner)
func (r *HealthCheckRequestReconciler) mapCheckNodeHealthToHealthCheckRequest(ctx context.Context, obj client.Object) []ctrl.Request {
	cnh, ok := obj.(*chmv1alpha1.CheckNodeHealth)
	if !ok {
		return nil
	}

	// Find the HealthSignal owner of this CheckNodeHealth
	hsOwnerRef, found := lo.Find(cnh.OwnerReferences, func(ref metav1.OwnerReference) bool {
		return ref.Kind == HealthSignalKind
	})
	if !found {
		return nil
	}

	// Get the HealthSignal to find its HealthCheckRequest owner
	hs := &healthv1alpha1.HealthSignal{}
	if err := r.Get(ctx, client.ObjectKey{Name: hsOwnerRef.Name}, hs); err != nil {
		klog.V(1).ErrorS(err, "Failed to get HealthSignal for mapping", "healthSignal", hsOwnerRef.Name)
		return nil
	}

	// Find the HealthCheckRequest owner of the HealthSignal
	for _, ownerRef := range hs.OwnerReferences {
		if ownerRef.Kind == HealthCheckRequestKind {
			return []ctrl.Request{
				{NamespacedName: client.ObjectKey{Name: ownerRef.Name}},
			}
		}
	}

	return nil
}

// Reconcile is part of the main kubernetes reconciliation loop.
// This controller watches HealthCheckRequest, creates CheckNodeHealth CR,
// and copies results to HealthSignal.
func (r *HealthCheckRequestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the HealthCheckRequest instance
	hcr := &healthv1alpha1.HealthCheckRequest{}
	if err := r.Get(ctx, req.NamespacedName, hcr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	klog.InfoS("Reconciling HealthCheckRequest", "name", hcr.Name, "target", hcr.Spec.TargetRef.Name)

	// Skip if being deleted - Kubernetes GC will clean up HealthSignal and CheckNodeHealth via OwnerReference
	if hcr.DeletionTimestamp != nil {
		klog.InfoS("HealthCheckRequest is being deleted, skipping", "name", hcr.Name)
		return ctrl.Result{}, nil
	}

	// Ensure HealthSignal exists for this HealthCheckRequest
	healthSignal, err := r.ensureHealthSignal(ctx, hcr)
	if err != nil {
		klog.ErrorS(err, "Failed to ensure HealthSignal")
		return ctrl.Result{}, err
	}

	// Ensure CheckNodeHealth exists to perform the actual health check
	// CheckNodeHealth is owned by HealthSignal
	cnh, err := r.ensureCheckNodeHealth(ctx, healthSignal, hcr.Spec.TargetRef.Name)
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
	klog.InfoS("CheckNodeHealth completed, synced to HealthSignal", "name", hcr.Name)
	return ctrl.Result{}, nil
}

// ensureHealthSignal creates or retrieves the HealthSignal for the given HealthCheckRequest
func (r *HealthCheckRequestReconciler) ensureHealthSignal(ctx context.Context, hcr *healthv1alpha1.HealthCheckRequest) (*healthv1alpha1.HealthSignal, error) {
	// Find HealthSignal owned by this HealthCheckRequest using the field index
	healthSignalList := &healthv1alpha1.HealthSignalList{}
	if err := r.List(ctx, healthSignalList, client.MatchingFields{healthSignalOwnerUIDIndex: string(hcr.UID)}); err != nil {
		return nil, fmt.Errorf("failed to list HealthSignals by owner UID: %w", err)
	}

	if len(healthSignalList.Items) > 0 {
		// Return the first (and should be only) HealthSignal owned by this HealthCheckRequest
		return &healthSignalList.Items[0], nil
	}

	// Create new HealthSignal with naming convention: {hcrName}-{source} (lowercase for RFC 1123)
	healthSignalName := strings.ToLower(fmt.Sprintf("%s-%s", hcr.Name, HealthSignalSource))
	healthSignal := &healthv1alpha1.HealthSignal{
		ObjectMeta: metav1.ObjectMeta{
			Name: healthSignalName,
			// don't use SetControllerReference here because we don't want block deletion of HealthSignal when HealthCheckRequest is deleted
			// We only want Kubernetes GC to handle cleanup of HealthSignal
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         healthv1alpha1.GroupVersion.String(),
					Kind:               HealthCheckRequestKind,
					Name:               hcr.Name,
					UID:                hcr.UID,
					BlockOwnerDeletion: ptr.To(false),
				},
			},
		},
		Spec: healthv1alpha1.HealthSignalSpec{
			Type: healthv1alpha1.NodeHealth,
			TargetRef: &corev1.ObjectReference{
				Kind: "Node",
				Name: hcr.Spec.TargetRef.Name,
			},
		},
	}

	if err := r.Create(ctx, healthSignal); err != nil {
		return nil, fmt.Errorf("failed to create HealthSignal: %w", err)
	}

	klog.InfoS("Created HealthSignal", "name", healthSignalName, "source", HealthSignalSource, "node", hcr.Spec.TargetRef.Name)
	return healthSignal, nil
}

// ensureCheckNodeHealth creates or retrieves the CheckNodeHealth for the given HealthSignal
// CheckNodeHealth is owned by HealthSignal (Controller=true)
func (r *HealthCheckRequestReconciler) ensureCheckNodeHealth(ctx context.Context, hs *healthv1alpha1.HealthSignal, nodeName string) (*chmv1alpha1.CheckNodeHealth, error) {
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
func (r *HealthCheckRequestReconciler) syncHealthSignalStatus(ctx context.Context, hs *healthv1alpha1.HealthSignal, cnh *chmv1alpha1.CheckNodeHealth) error {
	// Copy conditions from CheckNodeHealth to HealthSignal
	hs.Status.Conditions = make([]metav1.Condition, len(cnh.Status.Conditions))
	copy(hs.Status.Conditions, cnh.Status.Conditions)

	if err := r.Status().Update(ctx, hs); err != nil {
		return fmt.Errorf("failed to update HealthSignal status: %w", err)
	}

	klog.InfoS("Synced HealthSignal status from CheckNodeHealth", "name", hs.Name)
	return nil
}
