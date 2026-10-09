package checknodehealth

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sretry "k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/gpu"
	"github.com/Azure/cluster-health-monitor/pkg/utils"
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
	PodTimeout = 2 * time.Minute

	// CheckNodeHealthFinalizer is the finalizer used to ensure proper cleanup checker pods
	CheckNodeHealthFinalizer = "checknodehealth.clusterhealthmonitor.azure.com/finalizer"

	// CheckNodeHealthLabel is the label key used to identify check node health pods
	CheckNodeHealthLabel = "clusterhealthmonitor.azure.com/checknodehealth"

	// annotationNodeKind records on a checker pod whether its node was a GPU node when the pod was
	// created, as NodeKindGPU or NodeKindStandard.
	annotationNodeKind = "clusterhealthmonitor.azure.com/node-kind"

	// annotationGPUChecksSKU records on a checker pod the SKU its GPU checks run for. It is only set
	// when the pod runs the GPU checks.
	annotationGPUChecksSKU = "clusterhealthmonitor.azure.com/gpu-checks-sku"

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
	ReasonCheckStarted = "CheckStarted"
	ReasonCheckPassed  = "CheckPassed"
	ReasonCheckFailed  = "CheckFailed"
	ReasonCheckUnknown = "CheckUnknown"
	// ReasonPodStartupTimeout is set when the checker pod never started in time.
	ReasonPodStartupTimeout = "PodStartupTimeout"
	ReasonCheckUnsupported  = "CheckUnsupported"

	// ErrorCodeCheckNotReported marks a check whose pod ended before reporting it.
	ErrorCodeCheckNotReported = "CheckNotReported"

	// nodeConditionPrefix is the domain prefix on every Node condition this controller writes.
	nodeConditionPrefix = "kubernetes.azure.com/"

	// NodeConditionNodeHealthy is the condition type set on non-GPU Nodes to report ConditionTypeHealthy
	// from CheckNodeHealth checks.
	//
	// GPU nodes report granularNodeConditionTypes instead, so that existing remediation keyed on NodeHealthy
	// is never triggered by a GPU node.
	NodeConditionNodeHealthy corev1.NodeConditionType = nodeConditionPrefix + "NodeHealthy"

	// NodeConditionPodStartupHealthy reports the PodStartup check on a GPU node.
	NodeConditionPodStartupHealthy corev1.NodeConditionType = nodeConditionPrefix + "PodStartupHealthy"
	// NodeConditionPodNetworkHealthy reports the PodNetwork check on a GPU node.
	NodeConditionPodNetworkHealthy corev1.NodeConditionType = nodeConditionPrefix + "PodNetworkHealthy"
)

const (
	CheckerPodStartup = "PodStartup"
	CheckerPodNetwork = "PodNetwork"
)

// baseCheckConditions maps each base check to the Node condition its result is published as on a
// GPU node.
var baseCheckConditions = map[string]corev1.NodeConditionType{
	CheckerPodStartup: NodeConditionPodStartupHealthy,
	CheckerPodNetwork: NodeConditionPodNetworkHealthy,
}

// granularNodeConditionTypes are the conditions that each report a single aspect of node health, as
// opposed to the aggregate NodeHealthy. Only GPU nodes publish them.
var granularNodeConditionTypes = func() []corev1.NodeConditionType {
	types := make([]corev1.NodeConditionType, 0, len(baseCheckerNames)+len(gpuConditions))
	for _, name := range baseCheckerNames {
		types = append(types, baseCheckConditions[name])
	}
	for _, c := range gpuConditions {
		types = append(types, c.conditionType)
	}
	return types
}()

// managedNodeConditionTypes are every Node condition this controller owns.
var managedNodeConditionTypes = append([]corev1.NodeConditionType{NodeConditionNodeHealthy}, granularNodeConditionTypes...)

// ManagedNodeConditionTypes returns every Node condition type this controller owns.
func ManagedNodeConditionTypes() []corev1.NodeConditionType {
	return slices.Clone(managedNodeConditionTypes)
}

// InapplicableNodeConditionTypes returns the managed conditions that do not apply to this kind of
// node: NodeHealthy on GPU nodes, and the granular conditions on all others.
func InapplicableNodeConditionTypes(node *corev1.Node) []corev1.NodeConditionType {
	if gpuNodeInfoFrom(node).isGPUNode {
		return []corev1.NodeConditionType{NodeConditionNodeHealthy}
	}
	return slices.Clone(granularNodeConditionTypes)
}

// baseCheckerNames are the checks every node reports. PodStartup comes from the
// controller itself; the rest come from the checker pod, listed in pkg/nodecheckerrunner/runner.go.
var baseCheckerNames = []string{CheckerPodStartup, CheckerPodNetwork}

// checkerSpec is the checker run a CheckNodeHealth gets. The reconciler selects it in one place,
// checkerSpecFor, so the EnableGPUChecks gate and every GPU decision are read there. Once the
// checker pod exists, the spec is read back from its annotations instead; see recordedRun.
type checkerSpec struct {
	image string
	// gpuNode decides which Node conditions, metrics and circuit breaker the result goes to.
	gpuNode bool
	// timeout bounds the checker pod.
	timeout time.Duration
	// checkerNames are the checks this node owes a result for. Anything unreported becomes Unknown.
	checkerNames []string
	// gpu is set when the checker runs the GPU checks, and nil otherwise.
	gpu *gpuChecks
	// wait, when set, means the GPU checks cannot be decided yet: select again after this long, and
	// create no checker pod meanwhile.
	wait time.Duration
}

// checkerSpecFor picks the checker run for a node. A GPU node without claimable GPUs waits up to
// GPUWait after the CheckNodeHealth is created for them to appear before reporting it has none.
func (r *CheckNodeHealthReconciler) checkerSpecFor(info gpuNodeInfo, age time.Duration) checkerSpec {
	spec := checkerSpec{image: r.CheckerPodImage, gpuNode: info.isGPUNode}
	if !info.isGPUNode || !r.EnableGPUChecks {
		spec.checkerNames, spec.timeout = expectedRun(false, "")
		return spec
	}

	spec.image = r.GPUCheckerPodImage
	spec.checkerNames, spec.timeout = expectedRun(true, info.sku)
	switch {
	case info.claimableGPUs > 0:
		spec.gpu = &gpuChecks{sku: info.sku, gpus: info.claimableGPUs}
	case age < r.GPUWait:
		spec.wait = min(r.GPUWait-age, gpuPollInterval)
	default:
		spec.gpu = &gpuChecks{sku: info.sku, skipReason: gpu.ErrorCodeGPUsNotClaimable}
	}
	return spec
}

// expectedRun returns the checks a checker run owes a result for, and its pod timeout.
func expectedRun(gpuChecks bool, sku string) ([]string, time.Duration) {
	if !gpuChecks {
		return baseCheckerNames, PodTimeout
	}
	return append(slices.Clone(baseCheckerNames), gpu.CheckerNames(sku)...), GPUPodTimeout
}

// recordMissingResults marks every check the pod never reported as Unknown. Refetched so a
// result written after this reconcile began is left alone.
func (r *CheckNodeHealthReconciler) recordMissingResults(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, spec checkerSpec) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		latest := &chmv1alpha1.CheckNodeHealth{}
		if err := r.APIReader.Get(ctx, client.ObjectKey{Name: cnh.Name}, latest); err != nil {
			return err
		}

		added := false
		for _, name := range spec.checkerNames {
			if found, _ := r.findResult(latest, name); found {
				continue
			}
			latest.Status.Results = append(latest.Status.Results, chmv1alpha1.CheckResult{
				Name:      name,
				Status:    chmv1alpha1.CheckStatusUnknown,
				ErrorCode: ErrorCodeCheckNotReported,
				Message:   "the checker pod ended without reporting this check",
			})
			added = true
		}
		if !added {
			return nil
		}

		if err := r.Status().Update(ctx, latest); err != nil {
			return err
		}
		latest.DeepCopyInto(cnh)
		return nil
	})
}

// CheckNodeHealthReconciler reconciles a CheckNodeHealth object
type CheckNodeHealthReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	APIReader           client.Reader                 // Direct API server reader (bypasses cache) for node operations
	CheckerPodLabel     string                        // Label to identify health check pods
	CheckerPodImage     string                        // Image for the health check pod
	GPUCheckerPodImage  string                        // Image for the health check pod on GPU nodes
	CheckerPodNamespace string                        // Namespace to create pods in
	EnableNodeCondition bool                          // Whether to set NodeHealthy condition on the Node
	CircuitBreakers     *NodeConditionCircuitBreakers // Circuit breakers for node condition updates. Required when EnableNodeCondition is set.
	EnableGPUChecks     bool                          // Whether to run GPU checks on supported GPU nodes
	// GPUWait is how long a GPU node without claimable GPUs is given to get some before its GPU
	// checks start. Zero starts them immediately. See DefaultGPUWait.
	GPUWait time.Duration
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

	// The target node drives both whether we check it at all and which condition the result is
	// published as, so it is read once here.
	node, err := r.readTargetNode(ctx, cnh.Spec.NodeRef.Name)
	if err != nil {
		klog.ErrorS(err, "Failed to read target node", "node", cnh.Spec.NodeRef.Name)
		return ctrl.Result{}, err
	}
	info := gpuNodeInfoFrom(node)

	if node != nil {
		if supported, reason := utils.IsSupported(node); !supported {
			klog.InfoS("Target node is not supported, skipping health check",
				"name", cnh.Name, "node", cnh.Spec.NodeRef.Name, "reason", reason)
			return r.markUnsupported(ctx, cnh, info, reason)
		}
	}

	spec := r.checkerSpecFor(info, time.Since(cnh.CreationTimestamp.Time))
	if info.isGPUNode {
		klog.InfoS("Detected GPU node", "name", cnh.Name, "node", cnh.Spec.NodeRef.Name,
			"claimableGPUs", info.claimableGPUs, "sku", info.sku)
	}

	pod, err := r.findHealthCheckPod(ctx, cnh)
	if err != nil {
		klog.ErrorS(err, "Failed to look up health check pod")
		return ctrl.Result{}, err
	}
	if pod == nil {
		// The wait only gates creating the pod. Once it exists the check is underway, whatever the
		// node reports since.
		if spec.wait > 0 {
			klog.InfoS("Waiting for claimable GPUs before starting the GPU checks",
				"name", cnh.Name, "node", cnh.Spec.NodeRef.Name, "requeueAfter", spec.wait)
			return ctrl.Result{RequeueAfter: spec.wait}, nil
		}
		if spec.gpu != nil {
			// How long after the CheckNodeHealth the GPU checks started, for tuning GPUWait.
			klog.InfoS("Starting GPU checks", "name", cnh.Name, "node", cnh.Spec.NodeRef.Name,
				"sinceCreated", time.Since(cnh.CreationTimestamp.Time).Round(time.Millisecond),
				"gpus", spec.gpu.gpus, "skipReason", spec.gpu.skipReason)
		}
		if pod, err = r.createHealthCheckPod(ctx, cnh, spec); err != nil {
			klog.ErrorS(err, "Failed to create health check pod")
			return ctrl.Result{}, err
		}
	}
	// Handle the results according to the pod's annotations rather than the live node, whose GPU state
	// can change mid-run. Otherwise a GPU node that stops looking like one could get NodeHealthy.
	info, spec = recordedRun(pod, info)

	// Mark the CheckNodeHealth as started
	if err := r.markStarted(ctx, cnh); err != nil {
		klog.ErrorS(err, "Failed to mark as started", "name", cnh.Name)
		return ctrl.Result{}, err
	}

	if err := r.updatePodstartCheckerResult(ctx, cnh, pod, spec.timeout); err != nil {
		klog.ErrorS(err, "Failed to update PodStartup check result")
		return ctrl.Result{}, err
	}

	// Determine the overall result based on pod status
	return r.determineCheckResult(ctx, cnh, pod, info, spec)
}

func (r *CheckNodeHealthReconciler) determineCheckResult(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, pod *corev1.Pod, info gpuNodeInfo, spec checkerSpec) (ctrl.Result, error) {
	// Check if pod succeeded or failed (completed), or if it's timed out
	isPodCompleted := pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
	timeout := spec.timeout

	if isPodCompleted || isPodTimeout(pod, timeout) {
		if isPodCompleted {
			klog.InfoS("Health check pod completed, marking as completed", "phase", pod.Status.Phase)
		} else {
			klog.InfoS("Health check pod timeout, marking as completed", "timeout", timeout, "phase", pod.Status.Phase)
		}

		// Step 1: Mark as completed (determines health based on Results)
		healthyStatus, err := r.markCompleted(ctx, cnh, info, spec)
		if err != nil {
			klog.ErrorS(err, "Failed to mark as completed")
			return ctrl.Result{}, err
		}

		// Step 2: Update node condition based on health status. GPU nodes report granular
		// conditions rather than NodeHealthy, so a GPU check failure never reaches the signal
		// existing AKS remediation acts on.
		if r.EnableNodeCondition {
			if err := r.updateNodeCondition(ctx, cnh, info); err != nil {
				klog.ErrorS(err, "Failed to update node condition, continuing with cleanup", "node", cnh.Spec.NodeRef.Name)
			}

			// Track consecutive unhealthy/healthy results for the circuit breaker guarding the
			// condition this node reports to.
			circuitBreaker := r.CircuitBreakers.For(info)
			if healthyStatus == metav1.ConditionFalse {
				circuitBreaker.RecordUnhealthyNode()
			} else {
				circuitBreaker.RecordHealthyNode()
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

// readTargetNode fetches the CheckNodeHealth's target node. A node that no longer exists yields a
// nil node rather than an error, so a check whose target was deleted still reaches a terminal state
// instead of requeueing until the CR expires.
func (r *CheckNodeHealthReconciler) readTargetNode(ctx context.Context, nodeName string) (*corev1.Node, error) {
	node := &corev1.Node{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}
	return node, nil
}

// markUnsupported marks the CheckNodeHealth as completed with Healthy=Unknown and a
// Unsupported reason. It intentionally does NOT set the NodeHealthy condition on the node,
// so unsupported nodes are never flagged as unhealthy.
func (r *CheckNodeHealthReconciler) markUnsupported(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, info gpuNodeInfo, message string) (ctrl.Result, error) {
	now := metav1.Now()
	if cnh.Status.StartedAt == nil {
		cnh.Status.StartedAt = &now
	}
	cnh.Status.FinishedAt = &now
	cnh.Status.Conditions = []metav1.Condition{
		{
			Type:               ConditionTypeHealthy,
			Status:             metav1.ConditionUnknown,
			LastTransitionTime: now,
			Reason:             ReasonCheckUnsupported,
			Message:            message,
		},
	}
	if err := r.Status().Update(ctx, cnh); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	// Unsupported nodes are a terminal outcome that never reaches markCompleted, so count them
	// here to keep the outcome counter a complete record of every CheckNodeHealth.
	recordNodeCheckMetrics(cnh, info, metav1.ConditionUnknown, ReasonCheckUnsupported)

	// No pod is created for unsupported nodes, but clean up defensively in case one exists.
	if err := r.cleanupPod(ctx, cnh); err != nil {
		klog.ErrorS(err, "Failed to cleanup pod for unsupported node", "name", cnh.Name)
		return ctrl.Result{}, err
	}
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

func (r *CheckNodeHealthReconciler) markCompleted(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, info gpuNodeInfo, spec checkerSpec) (metav1.ConditionStatus, error) {
	// A check that reported nothing becomes an Unknown result here so that we only have to take into account existing checks when
	// determining the healthy condition.
	if err := r.recordMissingResults(ctx, cnh, spec); err != nil {
		return metav1.ConditionUnknown, fmt.Errorf("failed to record unreported results: %w", err)
	}

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

	recordNodeCheckMetrics(cnh, info, healthyStatus, reason)

	return healthyStatus, nil
}

// updateNodeCondition publishes the check outcome onto the Node. Non-GPU nodes get the single
// aggregate NodeHealthy condition. GPU nodes instead get granualar conditions.
func (r *CheckNodeHealthReconciler) updateNodeCondition(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, info gpuNodeInfo) error {
	desired := desiredNodeConditions(cnh, info)
	if len(desired) == 0 {
		return nil
	}

	publish := r.CircuitBreakers.For(info).Allow()
	if !publish {
		klog.InfoS("Circuit breaker is open, only removing node conditions that no longer apply",
			"node", cnh.Spec.NodeRef.Name,
			"checkNodeHealth", cnh.Name,
		)
	}

	nodeName := cnh.Spec.NodeRef.Name
	node := &corev1.Node{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	patch := client.MergeFrom(node.DeepCopy())

	now := metav1.Now()
	keep := make(map[corev1.NodeConditionType]bool, len(desired))
	for _, condition := range desired {
		keep[condition.Type] = true
		if publish {
			setNodeCondition(node, condition, now)
		}
	}
	removed := removeUnwantedNodeConditions(node, keep)
	if !publish && len(removed) == 0 {
		return nil
	}

	if err := r.Status().Patch(ctx, node, patch); err != nil {
		return fmt.Errorf("failed to update node %s condition: %w", nodeName, err)
	}

	if publish {
		for _, condition := range desired {
			klog.InfoS("Updated node condition", "node", nodeName, "type", condition.Type,
				"status", condition.Status, "reason", condition.Reason)
		}
	}
	if len(removed) > 0 {
		klog.InfoS("Removed node conditions that no longer apply", "node", nodeName, "types", removed)
	}
	return nil
}

// desiredNodeConditions builds the Node conditions carrying this check's outcome. It returns
// nothing until the check has completed and the CNH condition is set.
func desiredNodeConditions(cnh *chmv1alpha1.CheckNodeHealth, info gpuNodeInfo) []corev1.NodeCondition {
	var healthy *metav1.Condition
	for i := range cnh.Status.Conditions {
		if cnh.Status.Conditions[i].Type == ConditionTypeHealthy {
			healthy = &cnh.Status.Conditions[i]
			break
		}
	}
	if healthy == nil {
		return nil
	}

	if !info.isGPUNode {
		return []corev1.NodeCondition{{
			Type:    NodeConditionNodeHealthy,
			Status:  corev1.ConditionStatus(healthy.Status),
			Reason:  healthy.Reason,
			Message: healthy.Message,
		}}
	}

	conditions := make([]corev1.NodeCondition, 0, len(cnh.Status.Results))
	for _, result := range cnh.Status.Results {
		conditionType, ok := baseCheckConditions[result.Name]
		if !ok {
			continue
		}
		conditions = append(conditions, corev1.NodeCondition{
			Type:    conditionType,
			Status:  nodeConditionStatusFor(result.Status),
			Reason:  nodeConditionReasonFor(result),
			Message: result.Message,
		})
	}
	return append(conditions, gpuNodeConditions(cnh.Status.Results, gpu.CheckerNames(info.sku))...)
}

// nodeConditionStatusFor maps a single check's status onto the Node condition status.
func nodeConditionStatusFor(status chmv1alpha1.CheckStatus) corev1.ConditionStatus {
	switch status {
	case chmv1alpha1.CheckStatusHealthy:
		return corev1.ConditionTrue
	case chmv1alpha1.CheckStatusUnhealthy:
		return corev1.ConditionFalse
	default:
		return corev1.ConditionUnknown
	}
}

// nodeConditionReasonFor names the specific failure mode.
func nodeConditionReasonFor(result chmv1alpha1.CheckResult) string {
	if result.ErrorCode != "" {
		return result.ErrorCode
	}
	switch result.Status {
	case chmv1alpha1.CheckStatusHealthy:
		return ReasonCheckPassed
	case chmv1alpha1.CheckStatusUnhealthy:
		return ReasonCheckFailed
	default:
		return ReasonCheckUnknown
	}
}

// removeUnwantedNodeConditions drops every condition this controller manages that is not in keep,
// returning the types it removed.
func removeUnwantedNodeConditions(node *corev1.Node, keep map[corev1.NodeConditionType]bool) []corev1.NodeConditionType {
	kept := make([]corev1.NodeCondition, 0, len(node.Status.Conditions))
	var removed []corev1.NodeConditionType
	for _, c := range node.Status.Conditions {
		if !keep[c.Type] && slices.Contains(managedNodeConditionTypes, c.Type) {
			removed = append(removed, c.Type)
			continue
		}
		kept = append(kept, c)
	}
	node.Status.Conditions = kept
	return removed
}

// setNodeCondition upserts a Node condition.
func setNodeCondition(node *corev1.Node, desired corev1.NodeCondition, now metav1.Time) {
	for i, c := range node.Status.Conditions {
		if c.Type != desired.Type {
			continue
		}
		if node.Status.Conditions[i].Status != desired.Status {
			node.Status.Conditions[i].LastTransitionTime = now
		}
		node.Status.Conditions[i].Status = desired.Status
		node.Status.Conditions[i].LastHeartbeatTime = now
		node.Status.Conditions[i].Message = desired.Message
		node.Status.Conditions[i].Reason = desired.Reason
		return
	}

	desired.LastTransitionTime = now
	desired.LastHeartbeatTime = now
	node.Status.Conditions = append(node.Status.Conditions, desired)
}

// determineHealthyCondition determines the Healthy condition status from every reported result,
// GPU checks included. Checks that never reported must be filled in as Unknown before this is
// called, so an absent result here means the node was never expected to run it.
func (r *CheckNodeHealthReconciler) determineHealthyCondition(cnh *chmv1alpha1.CheckNodeHealth) (metav1.ConditionStatus, string, string) {
	return conditionFromResults(cnh.Status.Results)
}

// conditionFromResults reduces a set of check results to a condition status, reason and message.
func conditionFromResults(results []chmv1alpha1.CheckResult) (metav1.ConditionStatus, string, string) {
	message := formatResultsMessage(results)

	// Rule 1: Check if any Result.Status == "Unhealthy"
	if hasResultWithStatus(results, chmv1alpha1.CheckStatusUnhealthy) {
		return metav1.ConditionFalse, ReasonCheckFailed, message
	}

	// Rule 2: Check if any Result.Status == "Unknown". Checked after Unhealthy so that Unhealthy takes precedence.
	if hasResultWithStatus(results, chmv1alpha1.CheckStatusUnknown) {
		return metav1.ConditionUnknown, ReasonCheckUnknown, message
	}

	// Rule 3: All Results.Status == "Healthy"
	if allResultsHealthy(results) {
		return metav1.ConditionTrue, ReasonCheckPassed, message
	}

	// Default case - should not happen if logic is correct
	return metav1.ConditionUnknown, ReasonCheckUnknown, message
}

// formatResultsMessage returns a per-line summary of each reported check result, e.g.:
//
//	PodStartup: Healthy
//	PodNetwork: Unhealthy
func formatResultsMessage(results []chmv1alpha1.CheckResult) string {
	lines := make([]string, 0, len(results))
	for _, result := range results {
		lines = append(lines, fmt.Sprintf("%s: %s", result.Name, result.Status))
	}
	return strings.Join(lines, "\n")
}

// hasResultWithStatus checks whether any of the results has the given status.
func hasResultWithStatus(results []chmv1alpha1.CheckResult, status chmv1alpha1.CheckStatus) bool {
	for _, result := range results {
		if result.Status == status {
			return true
		}
	}
	return false
}

// allResultsHealthy verifies that all results have a Healthy status. An empty set of results has
// not passed anything, so it is not healthy.
func allResultsHealthy(results []chmv1alpha1.CheckResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, result := range results {
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
