// Package metricsserver provides a checker for the Kubernetes metrics server.
package metricsserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

// MetricsServerChecker implements the Checker interface for metrics server checks.
type MetricsServerChecker struct {
	name          string
	config        *config.MetricsServerConfig
	timeout       time.Duration
	kubeClient    kubernetes.Interface
	metricsClient metricsclientset.Interface
}

func Register() {
	checker.RegisterChecker(config.CheckTypeMetricsServer, BuildMetricsServerChecker)
}

// BuildMetricsServerChecker creates a new MetricsServerChecker instance.
func BuildMetricsServerChecker(config *config.CheckerConfig, kubeClient kubernetes.Interface) (checker.Checker, error) {
	// Get in-cluster config to access the API server
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	// Create metrics client using the official Kubernetes metrics client
	metricsClient, err := metricsclientset.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create metrics client: %w", err)
	}

	chk := &MetricsServerChecker{
		name:          config.Name,
		config:        config.MetricsServerConfig,
		timeout:       config.Timeout,
		kubeClient:    kubeClient,
		metricsClient: metricsClient,
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
// It attempts to call the metrics server API to verify it's available and responding.
func (c *MetricsServerChecker) Run(ctx context.Context) (*types.Result, error) {
	err := c.checkMetricsServerAPI(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.Unhealthy(errCodeMetricsServerTimeout, "timed out while calling metrics server API"), nil
		}
		return types.Unhealthy(errCodeMetricsServerUnavailable, fmt.Sprintf("metrics server API unavailable: %v", err)), nil
	}

	return types.Healthy(), nil
}

func (c *MetricsServerChecker) checkMetricsServerAPI(ctx context.Context) error {
	// Make a simple call to the metrics server API to check its availability
	_, err := c.metricsClient.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("metrics server API call failed: %w", err)
	}

	klog.V(2).InfoS("Metrics server API call succeeded", "checker", c.name)
	return nil
}
