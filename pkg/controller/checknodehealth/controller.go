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
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

const (
	// CRTTL is the time-to-live for CheckNodeHealth CRs.
	// CRs that have been created for longer than this duration will be deleted.
	CRTTL = 6 * time.Hour

	// SyncPeriod is the interval at which the controller reconciles all CheckNodeHealth resources.
	// Set to 1 hour (CRTTL/6) to ensure reliable cleanup of expired CRs:
	// - Expired CRs are cleaned up within 1 hour after expiration (max 17% delay)
	// - Recovery from controller restarts within 1 hour
	// - Acceptable overhead: only 6 reconciliations per CR over the 6-hour TTL period
	SyncPeriod = 1 * time.Hour

	// PodTimeout is the maximum time the checker pod can run before being marked as completed.
	// This applies to all non-terminal phases (Pending, Running, etc.).
	PodTimeout = 30 * time.Second

	// CheckNodeHealthFinalizer is the finalizer used to ensure proper cleanup checker pods
	CheckNodeHealthFinalizer = "checknodehealth.clusterhealthmonitor.azure.com/finalizer"

	// CheckNodeHealthLabel is the label key used to identify check node health pods
	CheckNodeHealthLabel = "clusterhealthmonitor.azure.com/checknodehealth"

	// DefaultCheckerServiceAccount is the default service account name for checker pods
	DefaultCheckerServiceAccount = "checknodehealth-checker"

	// AnnotationCheckerServiceAccount is the annotation key to override the checker pod service account.
	// This is primarily for E2E testing purposes to simulate failure scenarios where the checker
	// pod cannot successfully complete its checks. By specifying a service account without proper
	// permissions (e.g., "default"), E2E tests can verify the controller's behavior when the checker
	// fails to write results to the CheckNodeHealth status, which should result in Healthy=Unknown.
	AnnotationCheckerServiceAccount = "checknodehealth.azure.com/checker-service-account"

	// ConditionTypeHealthy is the condition type used to indicate a healthy state.
	ConditionTypeHealthy = "Healthy"

	// Condition reasons for CheckNodeHealth
	ReasonCheckStarted      = "CheckStarted"
	ReasonCheckPassed       = "CheckPassed"
	ReasonCheckFailed       = "CheckFailed"
	ReasonCheckUnknown      = "CheckUnknown"
	ReasonPodStartupTimeout = "PodStartupTimeout"

	// NodeConditionNodeHealthy is the condition type set on Node objects
	// to report health status from CheckNodeHealth checks.
	NodeConditionNodeHealthy corev1.NodeConditionType = "clusterhealthmonitor.azure.com/NodeHealthy"
)

var (
	// RequiredCheckResults defines the list of health check results that must ALL be present
	// and have Healthy status for the overall Healthy condition to be True.
	// If any required check is missing, the result will be Unknown by default.
	// The "PodStartup" result is reported by the controller. All other results in this list
	// are reported by the Node Checker pod.
	// See pkg/nodecheckerrunner/runner.go for the complete list of checkers running in the Node Checker pod.
	RequiredCheckResults = []string{"PodStartup", "PodNetwork"}
)

// CheckNodeHealthReconciler reconciles a CheckNodeHealth object
type CheckNodeHealthReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	APIReader           client.Reader                // Direct API server reader (bypasses cache) for node operations
	CheckerPodLabel     string                       // Label to identify health check pods
	CheckerPodImage     string                       // Image for the health check pod
	CheckerPodNamespace string                       // Namespace to create pods in
	EnableNodeCondition bool                         // Whether to set NodeHealthy condition on the Node
	CircuitBreaker      *NodeConditionCircuitBreaker // Circuit breaker for node condition updates
}

// +kubebuilder:rbac:groups=clusterhealthmonitor.azure.com,resources=checknodehealths,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clusterhealthmonitor.azure.com,resources=checknodehealths/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete,namespace=kube-system
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get
// +kubebuilder:rbac:groups="",resources=nodes/status,verbs=patch

// SetupWithManager sets up the controller with the Manager
func (r *CheckNodeHealthReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Only watch pods in the same namespace where we create them
	podPredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetNamespace() == r.CheckerPodNamespace
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&chmv1alpha1.CheckNodeHealth{}).
		Owns(&corev1.Pod{}, builder.WithPredicates(podPredicate)).
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

	// Check if the CR has expired
	if isExpired(cnh) {
		klog.InfoS("CheckNodeHealth has expired, deleting", "name", cnh.Name, "CreationTimestamp", cnh.CreationTimestamp)
		if err := client.IgnoreNotFound(r.Delete(ctx, cnh)); err != nil {
			klog.ErrorS(err, "Failed to delete expired CheckNodeHealth")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present
	if !controllerutil.ContainsFinalizer(cnh, CheckNodeHealthFinalizer) {
		controllerutil.AddFinalizer(cnh, CheckNodeHealthFinalizer)
		if err := r.Update(ctx, cnh); err != nil {
			klog.ErrorS(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		klog.InfoS("Added finalizer, continuing with reconcile")
	}

	// Check if already completed - if so, cleanup pod and skip
	// This case happens when pod deletion failed in determineCheckResult
	if isCompleted(cnh) {
		klog.InfoS("CheckNodeHealth already completed", "name", cnh.Name)
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
		klog.ErrorS(err, "Failed to mark as started", "name", cnh.Name)
		return ctrl.Result{}, err
	}

	if err := r.updatePodstartCheckerResult(ctx, cnh, pod); err != nil {
		klog.ErrorS(err, "Failed to update PodStartup check result")
		return ctrl.Result{}, err
	}

	// Determine the overall result based on pod status
	return r.determineCheckResult(ctx, cnh, pod)
}

func (r *CheckNodeHealthReconciler) determineCheckResult(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, pod *corev1.Pod) (ctrl.Result, error) {
	// Check if pod succeeded or failed (completed), or if it's timed out
	isPodCompleted := pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed

	if isPodCompleted || r.isPodTimeout(pod) {
		if isPodCompleted {
			klog.InfoS("Health check pod completed, marking as completed", "phase", pod.Status.Phase)
		} else {
			klog.InfoS("Health check pod timeout, marking as completed", "timeout", PodTimeout, "phase", pod.Status.Phase)
		}

		// Step 1: Mark as completed (determines health based on Results)
		healthyStatus, err := r.markCompleted(ctx, cnh)
		if err != nil {
			klog.ErrorS(err, "Failed to mark as completed")
			return ctrl.Result{}, err
		}

		// Step 2: Update node condition based on health status
		if r.EnableNodeCondition {
			if err := r.updateNodeCondition(ctx, cnh); err != nil {
				klog.ErrorS(err, "Failed to update node condition, continuing with cleanup", "node", cnh.Spec.NodeRef.Name)
			}

			// Track consecutive unhealthy/healthy results for circuit breaker
			if healthyStatus == metav1.ConditionFalse {
				r.CircuitBreaker.RecordUnhealthyNode()
			} else {
				r.CircuitBreaker.RecordHealthyNode()
			}
		}

		// Step 3: Delete the pod
		if err := r.cleanupPod(ctx, cnh); err != nil {
			klog.ErrorS(err, "Failed to cleanup pod, will retry")
			return ctrl.Result{}, nil
		}

		klog.InfoS("Successfully marked as completed and deleted pod")
		return ctrl.Result{}, nil
	}

	// Other pod phases (Unknown, etc.)
	klog.InfoS("Health check pod in unexpected phase", "phase", pod.Status.Phase)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
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
			Type:               ConditionTypeHealthy,
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

func (r *CheckNodeHealthReconciler) markCompleted(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) (metav1.ConditionStatus, error) {
	now := metav1.Now()
	cnh.Status.FinishedAt = &now
	healthyStatus, reason, message := r.determineHealthyCondition(cnh)
	cnh.Status.Conditions = []metav1.Condition{
		{
			Type:               ConditionTypeHealthy,
			Status:             healthyStatus,
			LastTransitionTime: now,
			Reason:             reason,
			Message:            message,
		},
	}

	klog.InfoS("CheckNodeHealth Result", "name", cnh.Name, "nodeName", cnh.Spec.NodeRef.Name, "status", healthyStatus, "reason", reason, "message", message)
	if err := r.Status().Update(ctx, cnh); err != nil {
		return healthyStatus, fmt.Errorf("failed to update status: %w", err)
	}

	return healthyStatus, nil
}

// updateNodeCondition sets the clusterhealthmonitor.azure.com/NodeHealthy condition on the Node
// when the CheckNodeHealth's Healthy condition is False.
func (r *CheckNodeHealthReconciler) updateNodeCondition(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) error {
	// Find the Healthy condition from the CheckNodeHealth status
	var chhHealthyCondition *metav1.Condition
	for i := range cnh.Status.Conditions {
		if cnh.Status.Conditions[i].Type == ConditionTypeHealthy {
			chhHealthyCondition = &cnh.Status.Conditions[i]
			break
		}
	}

	// Only emit node condition when Healthy=False
	if chhHealthyCondition == nil || chhHealthyCondition.Status != metav1.ConditionFalse {
		return nil
	}

	// Check circuit breaker before setting the node condition
	if !r.CircuitBreaker.Allow() {
		klog.InfoS("Circuit breaker is open, skipping node condition update",
			"node", cnh.Spec.NodeRef.Name,
			"checkNodeHealth", cnh.Name,
		)
		return nil
	}

	nodeName := cnh.Spec.NodeRef.Name
	node := &corev1.Node{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	patch := client.MergeFrom(node.DeepCopy())

	now := metav1.Now()
	found := false
	for i, c := range node.Status.Conditions {
		if c.Type == NodeConditionNodeHealthy {
			if node.Status.Conditions[i].Status != corev1.ConditionFalse {
				node.Status.Conditions[i].LastTransitionTime = now
			}
			node.Status.Conditions[i].Status = corev1.ConditionFalse
			node.Status.Conditions[i].LastHeartbeatTime = now
			node.Status.Conditions[i].Message = chhHealthyCondition.Message
			node.Status.Conditions[i].Reason = chhHealthyCondition.Reason
			found = true
			break
		}
	}

	if !found {
		node.Status.Conditions = append(node.Status.Conditions, corev1.NodeCondition{
			Type:               NodeConditionNodeHealthy,
			Status:             corev1.ConditionFalse,
			LastTransitionTime: now,
			LastHeartbeatTime:  now,
			Message:            chhHealthyCondition.Message,
			Reason:             chhHealthyCondition.Reason,
		})
	}

	if err := r.Status().Patch(ctx, node, patch); err != nil {
		return fmt.Errorf("failed to update node %s condition: %w", nodeName, err)
	}

	klog.InfoS("Updated node condition", "node", nodeName, "type", NodeConditionNodeHealthy, "status", corev1.ConditionFalse)
	return nil
}

// determineHealthyCondition determines the Healthy condition status based on check results
func (r *CheckNodeHealthReconciler) determineHealthyCondition(cnh *chmv1alpha1.CheckNodeHealth) (metav1.ConditionStatus, string, string) {
	// Rule 1: Check if any Result.Status == "Unhealthy"
	if r.hasUnhealthyResult(cnh) {
		return metav1.ConditionFalse, ReasonCheckFailed, "At least one health check result is Unhealthy"
	}

	// Rule 2: Check if any Result.Status == "Unknown". This must be checked after Unhealthy
	if r.hasUnknownResult(cnh) {
		return metav1.ConditionUnknown, ReasonCheckUnknown, "At least one health check result has Unknown status or is missing"
	}

	// Rule 3: Check if any required results are missing
	missingResults := r.findMissingResult(cnh)
	if len(missingResults) > 0 {
		return metav1.ConditionUnknown, ReasonCheckUnknown, fmt.Sprintf("Missing required health check results: %v", missingResults)
	}

	// Rule 4: Check if no results
	if len(cnh.Status.Results) == 0 {
		return metav1.ConditionUnknown, ReasonCheckUnknown, "No health check results available"
	}

	// Rule 5: All Results.Status == "Healthy" (or yet)
	if r.allResultsHealthy(cnh) {
		return metav1.ConditionTrue, ReasonCheckPassed, "All health checks completed successfully"
	}

	// Default case - should not happen if logic is correct
	return metav1.ConditionUnknown, ReasonCheckUnknown, "Unable to determine health status"
}

// hasunknownresult checks whether any result reported by a checker has an Unknown status.
// If the required results are missing, it also returns true because the default result is Unknown.
func (r *CheckNodeHealthReconciler) hasUnknownResult(cnh *chmv1alpha1.CheckNodeHealth) bool {
	for _, result := range cnh.Status.Results {
		if result.Status == chmv1alpha1.CheckStatusUnknown {
			return true
		}
	}
	return false
}

func (r *CheckNodeHealthReconciler) findMissingResult(cnh *chmv1alpha1.CheckNodeHealth) []string {
	missed := []string{}
	for _, requiredCheckName := range RequiredCheckResults {
		if found, _ := r.findResult(cnh, requiredCheckName); !found {
			klog.Warningf("required checker result %q is missing", requiredCheckName)
			missed = append(missed, requiredCheckName)
		}
	}
	return missed
}

// hasUnhealthyResult checks whether any result reported by a checker has an Unhealthy status.
func (r *CheckNodeHealthReconciler) hasUnhealthyResult(cnh *chmv1alpha1.CheckNodeHealth) bool {
	for _, result := range cnh.Status.Results {
		if result.Status == chmv1alpha1.CheckStatusUnhealthy {
			return true
		}
	}
	return false
}

// allResultsHealthy verifies that all result reported by checker has Healthy status.
func (r *CheckNodeHealthReconciler) allResultsHealthy(cnh *chmv1alpha1.CheckNodeHealth) bool {
	for _, result := range cnh.Status.Results {
		if result.Status != chmv1alpha1.CheckStatusHealthy {
			return false
		}
	}
	return true
}

// findResult searches for a result by name in the CheckNodeHealth status
func (r *CheckNodeHealthReconciler) findResult(cnh *chmv1alpha1.CheckNodeHealth, name string) (bool, chmv1alpha1.CheckResult) {
	for _, result := range cnh.Status.Results {
		if result.Name == name {
			return true, result
		}
	}
	return false, chmv1alpha1.CheckResult{}
}

func isCompleted(cnh *chmv1alpha1.CheckNodeHealth) bool {
	return cnh.Status.FinishedAt != nil
}

// isExpired checks if the CR has been created for longer than CRTTL
func isExpired(cnh *chmv1alpha1.CheckNodeHealth) bool {
	return time.Since(cnh.CreationTimestamp.Time) > CRTTL
}

// handleCompletion handles completed checks by cleaning up any remaining pods
func (r *CheckNodeHealthReconciler) handleCompletion(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) (ctrl.Result, error) {
	if err := r.cleanupPod(ctx, cnh); err != nil {
		klog.ErrorS(err, "Failed to cleanup remaining pods")
		return ctrl.Result{}, err
	}

	klog.InfoS("CheckNodeHealth completion cleanup finished")
	return ctrl.Result{}, nil
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
