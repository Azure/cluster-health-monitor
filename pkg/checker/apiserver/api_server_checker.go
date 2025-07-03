// Package apiserver provides a checker for the Kubernetes API server.
package apiserver

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

// APIServerChecker implements the Checker interface for API server checks.
type APIServerChecker struct {
	name       string
	config     *config.APIServerConfig
	kubeClient kubernetes.Interface
}

func Register() {
	checker.RegisterChecker(config.CheckTypeAPIServer, buildAPIServerChecker)
}

// buildAPIServerChecker creates a new APIServerChecker instance.
func buildAPIServerChecker(config *config.CheckerConfig) (checker.Checker, error) {
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	chk := &APIServerChecker{
		name:       config.Name,
		config:     config.APIServerConfig,
		kubeClient: client,
	}
	klog.InfoS("Built APIServerChecker",
		"name", chk.name,
		"config", chk.config,
	)
	return chk, nil
}

func (c APIServerChecker) Name() string {
	return c.name
}

func (c APIServerChecker) Type() config.CheckerType {
	return config.CheckTypeAPIServer
}

// Run executes the API server check.
// It creates an empty ConfigMap, reads it, and then deletes it.
// If all operations succeed, the check is considered healthy.
func (c APIServerChecker) Run(ctx context.Context) (*types.Result, error) {
	// TODO: Implement the API server check logic.

	return nil, fmt.Errorf("APIServerChecker is not implemented yet")
}
