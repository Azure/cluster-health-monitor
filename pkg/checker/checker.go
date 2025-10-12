package checker

import (
	"context"
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/metrics"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

type Checker interface {
	// Name returns the name of the checker.
	Name() string

	// Type returns the type of the checker.
	Type() config.CheckerType

	// Run executes the health check logic for the checker.
	Run(ctx context.Context)
}

type Builder func(cfg *config.CheckerConfig, kubeClient kubernetes.Interface) (Checker, error)

var checkerRegistry = make(map[config.CheckerType]Builder)

func RegisterChecker(t config.CheckerType, builder Builder) {
	checkerRegistry[t] = builder
	klog.InfoS("Registered checker", "type", t)
}

// Build creates checkers from a list of checker configs
func Build(cfg *config.CheckerConfig, kubeClient kubernetes.Interface) (Checker, error) {
	if kubeClient == nil {
		return nil, fmt.Errorf("kubernetes client cannot be nil")
	}

	builder, ok := checkerRegistry[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("unrecognized checker type: %q", cfg.Type)
	}
	return builder(cfg, kubeClient)
}

// RecordResult increments the result counter for a specific checker run.
// If err is not nil, it records a run error (unknown status).
// If result is not nil, it records the status from the result.
func RecordResult(checker Checker, result *Result, err error) {
	checkerType := string(checker.Type())
	checkerName := checker.Name()
	// If there's an error, record as unknown.
	if err != nil {
		metrics.CheckerResultCounter.WithLabelValues(checkerType, checkerName, metrics.UnknownStatus, err.Error()).Inc()
		klog.V(3).InfoS("Recorded checker result", "name", checkerName, "type", checkerType, "status", metrics.UnknownStatus)
		klog.ErrorS(err, "Failed checker run", "name", checkerName, "type", checkerType)
		return
	}

	// Record based on result status.
	var status string
	var errorCode string
	switch result.Status {
	case StatusHealthy:
		status = metrics.HealthyStatus
		errorCode = metrics.HealthyCode
	case StatusUnhealthy:
		status = metrics.UnhealthyStatus
		errorCode = result.Detail.Code
	}

	metrics.CheckerResultCounter.WithLabelValues(checkerType, checkerName, status, errorCode).Inc()
	klog.V(3).InfoS("Recorded checker result", "name", checkerName, "type", checkerType, "status", status, "errorCode", errorCode, "message", result.Detail.Message)
}

// RecordCoreDNSPodResult increments the result counter for a specific core DNS pod check.
// If err is not nil, it records a run error (unknown status).
// If result is not nil, it records the status from the result.
func RecordCoreDNSPodResult(checker Checker, result *Result, err error) {
	checkerType := string(checker.Type())
	checkerName := checker.Name()

	// If there's an error, record as unknown.
	if err != nil {
		metrics.CoreDNSPodResultCounter.WithLabelValues(checkerType, checkerName, metrics.UnknownStatus, metrics.UnknownStatus, metrics.UnknownCode).Inc()
		klog.V(3).InfoS("Recorded checker result", "name", checkerName, "type", checkerType, "status", metrics.UnknownStatus)
		klog.ErrorS(err, "Failed checker run", "name", checkerName, "type", checkerType)
		return
	}

	// Record based on result status.
	var status string
	var errorCode string
	switch result.Status {
	case StatusHealthy:
		status = metrics.HealthyStatus
		errorCode = metrics.HealthyCode
	case StatusUnhealthy:
		status = metrics.UnhealthyStatus
		errorCode = result.Detail.Code
	}

	metrics.CoreDNSPodResultCounter.WithLabelValues(checkerType, checkerName, result.Detail.Pod, status, errorCode).Inc()
	klog.V(3).InfoS("Recorded checker result", "name", checkerName, "type", checkerType, "podName", result.Detail.Pod, "status", status, "errorCode", errorCode, "message", result.Detail.Message)
}
