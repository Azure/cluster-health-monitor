package checknodehealth

import (
	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/metrics"
)

var (
	// checkCounter tracks completed CheckNodeHealth checks. It is incremented exactly once per
	// CheckNodeHealth, when the check reaches a terminal state.
	checkCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cluster_health_monitor_node_check_total",
			Help: "Total number of completed node health checks, labeled by overall result and reason",
		},
		[]string{"result", "reason"},
	)

	// checkResultCounter tracks the individual check results reported within a CheckNodeHealth.
	checkResultCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cluster_health_monitor_node_check_result_total",
			Help: "Total number of individual node check results, labeled by check name, status and code",
		},
		[]string{"checker_name", "status", "error_code"},
	)
)

// These collectors are registered into the controller-runtime registry rather than a registry of
// our own so that they are served by the manager's existing metrics endpoint.
func init() {
	ctrlmetrics.Registry.MustRegister(
		checkCounter,
		checkResultCounter,
	)
}

// resultLabel maps the Healthy condition status onto the result label of the outcome counter.
func resultLabel(status metav1.ConditionStatus) string {
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

// recordNodeCheckMetrics emits the outcome counter once and the per-check counter once per
// reported result.
//
// Call only after the status update that sets FinishedAt on the CR has succeeded. That is what makes
// subsequent reconciles short circuit through isCompleted, so emitting before it would cause double
// counting when the reconcile is retried.
func recordNodeCheckMetrics(cnh *chmv1alpha1.CheckNodeHealth, status metav1.ConditionStatus, reason string) {
	checkCounter.WithLabelValues(resultLabel(status), reason).Inc()

	for _, result := range cnh.Status.Results {
		checkResultCounter.WithLabelValues(
			result.Name,
			string(result.Status),
			errorCodeLabel(result),
		).Inc()
	}
}
