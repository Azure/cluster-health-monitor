package metrics

import "github.com/prometheus/client_golang/prometheus"

const (
	HealthyStatus   = "Healthy"
	UnhealthyStatus = "Unhealthy"
	UnknownStatus   = "Unknown"

	// error_code is required although healthy and unknown checkers do not use it.
	// We set a default value for healthy and unknown result.
	HealthyCode = HealthyStatus
	UnknownCode = UnknownStatus
)

var (
	// CheckerResultCounter is a Prometheus counter that tracks the results of checker runs.
	CheckerResultCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cluster_health_monitor_checker_result_total",
			Help: "Total number of checker runs, labeled by status and code",
		},
		[]string{"checker_type", "checker_name", "status", "error_code"},
	)

	// PodHealthResultCounter is a Prometheus counter that tracks the results of CoreDNS pod checker runs.
	PodHealthResultCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cluster_health_monitor_pod_health_result_total",
			Help: "Total number of per-pod health checks, labeled by status and code",
		},
		[]string{"checker_type", "checker_name", "pod_namespace", "pod_name", "status", "error_code"},
	)
)
