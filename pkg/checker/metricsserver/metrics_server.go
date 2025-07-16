// Package metricsserver provides a checker for the Kubernetes metrics server.
package metricsserver

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

const (
	metricsServerNamespace      = "kube-system"
	metricsServerDeploymentName = "metrics-server"
)

// MetricsServerChecker implements the Checker interface for metrics server checks.
type MetricsServerChecker struct {
	name       string
	config     *config.MetricsServerConfig
	timeout    time.Duration
	kubeClient kubernetes.Interface
}

func Register() {
	checker.RegisterChecker(config.CheckTypeMetricsServer, BuildMetricsServerChecker)
}

// BuildMetricsServerChecker creates a new MetricsServerChecker instance.
func BuildMetricsServerChecker(config *config.CheckerConfig, kubeClient kubernetes.Interface) (checker.Checker, error) {
	chk := &MetricsServerChecker{
		name:       config.Name,
		config:     config.MetricsServerConfig,
		timeout:    config.Timeout,
		kubeClient: kubeClient,
	}
	klog.InfoS("Built MetricsServerChecker",
		"name", chk.name,
		"config", chk.config,
		"timeout", chk.timeout.String(),
	)
	return chk, nil
}

func (c *MetricsServerChecker) Name() string {
	return c.name
}

func (c *MetricsServerChecker) Type() config.CheckerType {
	return config.CheckTypeMetricsServer
}

// Run executes the metrics server check.
func (c *MetricsServerChecker) Run(ctx context.Context) (*types.Result, error) {
	// TODO: Implement the metrics server availability check

	return nil, fmt.Errorf("metrics server check not implemented yet")
}
