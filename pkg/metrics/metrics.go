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

	// CoreDNSPodResultCounter is a Prometheus counter that tracks the results of CoreDNS pod checker runs.
	CoreDNSPodResultCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cluster_health_monitor_coredns_pod_result_total",
			Help: "Total number of CoreDNS pod checker runs, labeled by status and code",
		},
		[]string{"checker_type", "checker_name", "pod", "status", "error_code"},
	)
)
