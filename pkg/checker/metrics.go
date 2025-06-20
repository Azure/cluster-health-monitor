package checker

import (
	"github.com/Azure/cluster-health-monitor/pkg/metrics"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

// RecordCheckerResult increments the result counter for a specific checker run.
// If err is not nil, it records a run error (unknown status).
// If result is not nil, it records the status from the result.
func RecordCheckerResult(checkerType, checkerName string, result *types.Result, err error) {
	// If there's an error, record as unknown.
	if err != nil {
		metrics.CheckerResultCounter.WithLabelValues(checkerType, checkerName, metrics.UnknownStatus, metrics.UnknownCode).Inc()
		return
	}

	// Record based on result status.
	var status string
	var errorCode string
	switch result.Status {
	case types.StatusHealthy:
		status = metrics.HealthyStatus
		errorCode = metrics.HealthyCode
	case types.StatusUnhealthy:
		status = metrics.UnhealthyStatus
		errorCode = result.Detail.Code
	}

	metrics.CheckerResultCounter.WithLabelValues(checkerType, checkerName, status, errorCode).Inc()
}
