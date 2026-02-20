package node

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

const (
	// AnnotationLastBootID is the annotation key used to store the last observed bootID on a node.
	AnnotationLastBootID = "checknodehealth.clusterhealthmonitor.azure.com/last-boot-id"

	// cnhRebootPrefix is the prefix used for CheckNodeHealth CR names triggered by node reboot.
	cnhRebootPrefix = "reboot-"

	// maxCNHNameLength is the maximum allowed length for CheckNodeHealth CR names.
	maxCNHNameLength = 253

	// NewNodeThreshold is the maximum age of a node to be considered "new".
	// Nodes created within this duration will trigger a CheckNodeHealth on
	// first observation (no prior bootID annotation), while older nodes will
	// only have their annotation initialized without a health check.
	NewNodeThreshold = 5 * time.Minute
)

// NodeRebootReconciler watches Node objects and creates CheckNodeHealth CRs
// when a node reboot is detected via a change in bootID.
type NodeRebootReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeRebootReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}, builder.WithPredicates(r.nodeRebootPredicate())).
		Complete(r)
}

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch

// Reconcile detects node reboots by comparing the node's current bootID
// against the last-seen bootID stored in an annotation. When a reboot is
// detected, it creates a CheckNodeHealth CR with a deterministic name
// derived from the node name and bootID.
func (r *NodeRebootReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	node := &corev1.Node{}
	if err := r.Get(ctx, req.NamespacedName, node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	currentBootID := node.Status.NodeInfo.BootID
	if currentBootID == "" {
		klog.V(1).InfoS("Node has no bootID yet, skipping", "node", node.Name)
		return ctrl.Result{}, nil
	}

	lastBootID := node.Annotations[AnnotationLastBootID]

	// First time seeing this node — no prior bootID annotation exists.
	// If the node was created recently it is genuinely new, so run a health
	// check. Otherwise it is an existing node observed for the first time
	// after a controller (re)start — only initialize the annotation to avoid
	// triggering a spurious CheckNodeHealth for every node in the cluster.
	if lastBootID == "" {
		if time.Since(node.CreationTimestamp.Time) < NewNodeThreshold {
			klog.InfoS("New node detected, creating health check", "node", node.Name, "bootID", currentBootID)
			if err := r.createCheckNodeHealth(ctx, node, currentBootID); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			klog.InfoS("Initializing bootID annotation for existing node", "node", node.Name, "bootID", currentBootID)
		}
		return ctrl.Result{}, r.updateBootIDAnnotation(ctx, node, currentBootID)
	}

	// No reboot detected.
	if lastBootID == currentBootID {
		return ctrl.Result{}, nil
	}

	// Reboot detected.
	klog.InfoS("Node reboot detected", "node", node.Name, "oldBootID", lastBootID, "newBootID", currentBootID)
	if err := r.createCheckNodeHealth(ctx, node, currentBootID); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.updateBootIDAnnotation(ctx, node, currentBootID)
}

// createCheckNodeHealth creates a CheckNodeHealth CR with a deterministic name.
// If a CR with the same name already exists (e.g., from a duplicate reconcile),
// the AlreadyExists error is safely ignored.
func (r *NodeRebootReconciler) createCheckNodeHealth(ctx context.Context, node *corev1.Node, bootID string) error {
	crName := GenerateCNHName(node.Name, bootID)
	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{
			Name: crName,
		},
		Spec: chmv1alpha1.CheckNodeHealthSpec{
			NodeRef: chmv1alpha1.NodeReference{
				Name: node.Name,
			},
		},
	}

	if err := r.Create(ctx, cnh); err != nil {
		if apierrors.IsAlreadyExists(err) {
			klog.V(1).InfoS("CheckNodeHealth already exists for this boot", "name", crName, "node", node.Name)
			return nil
		}
		return fmt.Errorf("failed to create CheckNodeHealth for node %s: %w", node.Name, err)
	}
	klog.InfoS("Created CheckNodeHealth for rebooted node", "name", crName, "node", node.Name, "bootID", bootID)
	return nil
}

// updateBootIDAnnotation patches the node's last-boot-id annotation.
func (r *NodeRebootReconciler) updateBootIDAnnotation(ctx context.Context, node *corev1.Node, bootID string) error {
	patch := client.MergeFrom(node.DeepCopy())
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	node.Annotations[AnnotationLastBootID] = bootID
	if err := r.Patch(ctx, node, patch); err != nil {
		return fmt.Errorf("failed to update bootID annotation on node %s: %w", node.Name, err)
	}
	return nil
}

// GenerateCNHName builds a deterministic CheckNodeHealth CR name from the node
// name and bootID. The bootID is hashed to keep the name short and DNS-safe.
// Format: reboot-<nodeName>-<hash8>
func GenerateCNHName(nodeName, bootID string) string {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(bootID)))[:8]
	name := fmt.Sprintf("%s%s-%s", cnhRebootPrefix, nodeName, hash)
	if len(name) > maxCNHNameLength {
		name = name[:maxCNHNameLength]
	}
	return name
}

// nodeRebootPredicate filters node events so the reconciler only processes
// events where the bootID may have changed.
func (r *NodeRebootReconciler) nodeRebootPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			// Process newly discovered nodes to initialize the annotation.
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode, okOld := e.ObjectOld.(*corev1.Node)
			newNode, okNew := e.ObjectNew.(*corev1.Node)
			if !okOld || !okNew {
				return false
			}
			// Only reconcile if bootID actually changed.
			return oldNode.Status.NodeInfo.BootID != newNode.Status.NodeInfo.BootID
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}
