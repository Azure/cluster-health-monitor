package checknodehealth

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/metrics"
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/gpu"
)

var (
	// healthCheckCounter tracks completed CheckNodeHealth checks. It is incremented exactly once per
	// CheckNodeHealth, when the check reaches a terminal state.
	healthCheckCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cluster_health_monitor_node_health_check_total",
			Help: "Total number of completed node health checks, labeled by node kind, GPU SKU, overall status and reason",
		},
		[]string{"node_kind", "sku", "status", "reason"},
	)

	// checkerResultCounter tracks the individual check results reported within a CheckNodeHealth.
	checkerResultCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cluster_health_monitor_node_checker_result_total",
			Help: "Total number of individual node check results, labeled by node kind, GPU SKU, check name, status and code",
		},
		[]string{"node_kind", "sku", "checker_name", "status", "error_code"},
	)

	// healthCheckDuration tracks how long completed CheckNodeHealth checks took, from the CR being
	// created to its result being written. That is the time to a verdict that the CR's consumers see.
	// It does not include updating node conditions or deleting the checker pod, which come after.
	healthCheckDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cluster_health_monitor_node_health_check_duration_seconds",
			Help:    "Duration in seconds from a node health check being requested to its result being written, labeled by node kind, GPU SKU and overall status",
			Buckets: durationBuckets,
		},
		[]string{"node_kind", "sku", "status"},
	)
)

// durationBuckets spans from the quickest health checks up to 30 minutes, which leaves headroom
// above GPUPodTimeout for slower checks to be added.
// TODO adjust buckets based on observed durations.
var durationBuckets = []float64{1, 2.5, 5, 10, 15, 30, 45, 60, 90, 120, 180, 300, 450, 600, 900, 1200, 1800}

const (
	// NodeKindGPU labels results from GPU nodes.
	NodeKindGPU = "gpu"

	// NodeKindStandard labels results from non-GPU nodes.
	NodeKindStandard = "standard"
)

// nodeKindLabel splits the counters between GPU and non-GPU nodes, the same line that decides whether
// a node gets NodeHealthy, so that GPU node health can be alerted on without the rest of the fleet
// diluting it, and vice versa.
func nodeKindLabel(info gpuNodeInfo) string {
	if info.isGPUNode {
		return NodeKindGPU
	}
	return NodeKindStandard
}

// SKUOther labels results from GPU nodes whose SKU the GPU checks have no profile for.
const SKUOther = "other"

// skuLabel splits GPU node results by SKU, as the GPU checks' thresholds, and so their results and
// durations, differ per SKU.
//
// The label is kept to a bounded cardinality, since every value it takes adds series to each metric
// here Only SKUs the GPU checks have a profile for are used as values, and any other GPU SKU is
// SKUOther, so the label only grows when a profile is added. Non-GPU nodes are left unlabeled, as
// any VM size can be one, which would leave the cardinality unbounded, and their checks do not depend
// on it.
func skuLabel(info gpuNodeInfo) string {
	if !info.isGPUNode {
		return ""
	}
	if sku, ok := gpu.ProfiledSKU(info.sku); ok {
		return sku
	}
	return SKUOther
}

// These collectors are registered into the controller-runtime registry rather than a registry of
// our own so that they are served by the manager's existing metrics endpoint.
func init() {
	ctrlmetrics.Registry.MustRegister(
		healthCheckCounter,
		checkerResultCounter,
		healthCheckDuration,
	)
}

// statusLabel maps the Healthy condition's status onto the status label of the overall metrics. It
// takes the same values as the per-check status label, which is the CheckResult status.
func statusLabel(status metav1.ConditionStatus) string {
	switch status {
	case metav1.ConditionTrue:
		return metrics.HealthyStatus
	case metav1.ConditionFalse:
		return metrics.UnhealthyStatus
	case metav1.ConditionUnknown:
		return metrics.UnknownStatus
	default:
		return metrics.UnknownStatus
	}
}

// errorCodeLabel supplies an error code for results that do not carry one. Healthy results never
// set a code, so they are labeled Healthy, and anything else without a code falls back to Unknown.
func errorCodeLabel(result chmv1alpha1.CheckResult) string {
	if result.ErrorCode != "" {
		return result.ErrorCode
	}
	if result.Status == chmv1alpha1.CheckStatusHealthy {
		return metrics.HealthyCode
	}
	return metrics.UnknownCode
}

// durationSinceCreated returns how long after the CR was created resultWrittenAt is, which is when
// consumers waiting on the CR get their verdict. It is nil when the creation time is unknown, and
// clamped at zero as the controller's clock can differ slightly from the API server's.
func durationSinceCreated(cnh *chmv1alpha1.CheckNodeHealth, resultWrittenAt metav1.Time) *time.Duration {
	if cnh.CreationTimestamp.IsZero() {
		return nil
	}
	d := max(resultWrittenAt.Sub(cnh.CreationTimestamp.Time), 0)
	return &d
}

// recordNodeCheckMetrics emits the outcome counter and duration once, and the per-check counter
// once per reported result. A nil duration is not observed.
//
// Call only after the status update that sets FinishedAt on the CR has succeeded. That is what makes
// subsequent reconciles short circuit through isCompleted, so emitting before it would cause double
// counting when the reconcile is retried.
func recordNodeCheckMetrics(cnh *chmv1alpha1.CheckNodeHealth, info gpuNodeInfo, status metav1.ConditionStatus, reason string, duration *time.Duration) {
	nodeKind := nodeKindLabel(info)
	sku := skuLabel(info)
	overall := statusLabel(status)
	healthCheckCounter.WithLabelValues(nodeKind, sku, overall, reason).Inc()

	if duration != nil {
		healthCheckDuration.WithLabelValues(nodeKind, sku, overall).Observe(duration.Seconds())
	}

	for _, result := range cnh.Status.Results {
		checkerResultCounter.WithLabelValues(
			nodeKind,
			sku,
			result.Name,
			string(result.Status),
			errorCodeLabel(result),
		).Inc()
	}
}

// logNodeCheckDuration logs how long a completed CheckNodeHealth took alongside the node it ran on.
// The histogram cannot single out a run, so this is what finds the slow ones. The duration is in
// seconds, and is omitted when unknown, so it can be queried as a number.
func logNodeCheckDuration(cnh *chmv1alpha1.CheckNodeHealth, info gpuNodeInfo, status metav1.ConditionStatus, reason string, duration *time.Duration) {
	keysAndValues := []any{
		"name", cnh.Name,
		"node", cnh.Spec.NodeRef.Name,
		"nodeKind", nodeKindLabel(info),
		// The node's SKU as is, unlike the metrics' sku label, so an unprofiled one can be identified.
		"sku", info.sku,
		"status", statusLabel(status),
		"reason", reason,
	}
	if duration != nil {
		keysAndValues = append(keysAndValues, "durationSeconds", duration.Seconds())
	}

	klog.InfoS("CheckNodeHealth completed", keysAndValues...)
}
