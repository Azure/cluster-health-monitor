package upgradenodeinprogress

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	unipv1alpha1 "github.com/Azure/cluster-health-monitor/apis/upgradenodeinprogresses/v1alpha1"
)

// UpgradeNodeInProgressReconciler reconciles an UpgradeNodeInProgress object
// and updates the corresponding HealthSignal status
type UpgradeNodeInProgressReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=health.aks.io,resources=upgradenodeinprogresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=health.aks.io,resources=healthsignals,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=health.aks.io,resources=healthsignals/status,verbs=get;update;patch

// SetupWithManager sets up the controller with the Manager
func (r *UpgradeNodeInProgressReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&unipv1alpha1.UpgradeNodeInProgress{}).
		Complete(r)
}

// Reconcile is part of the main kubernetes reconciliation loop.
// This controller watches UpgradeNodeInProgress and creates/updates corresponding HealthSignal resources.
func (r *UpgradeNodeInProgressReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the UpgradeNodeInProgress instance
	unip := &unipv1alpha1.UpgradeNodeInProgress{}
	if err := r.Get(ctx, req.NamespacedName, unip); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	klog.InfoS("Reconciling UpgradeNodeInProgress", "name", unip.Name, "node", unip.Spec.NodeRef.Name)

	// TODO: Handle deletion
	// TODO: Ensure HealthSignal exists for this node
	// TODO: Create checker pod on the target node
	// TODO: Update HealthSignal status based on checker results

	return ctrl.Result{}, nil
}
