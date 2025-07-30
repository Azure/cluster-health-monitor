// Package azurepolicy provides a checker for Azure Policy webhook validations.
package azurepolicy

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

// AzurePolicyChecker implements the Checker interface for Azure Policy checks.
type AzurePolicyChecker struct {
	name       string
	timeout    time.Duration
	kubeClient kubernetes.Interface
}

func Register() {
	checker.RegisterChecker(config.CheckTypeAzurePolicy, buildAzurePolicyChecker)
}

// buildAzurePolicyChecker creates a new AzurePolicyChecker instance.
func buildAzurePolicyChecker(config *config.CheckerConfig, kubeClient kubernetes.Interface) (checker.Checker, error) {

	return &AzurePolicyChecker{
		name:       config.Name,
		timeout:    config.Timeout,
		kubeClient: kubeClient,
	}, nil
}

func (c AzurePolicyChecker) Name() string {
	return c.name
}

func (c AzurePolicyChecker) Type() config.CheckerType {
	return config.CheckTypeAzurePolicy
}

// Run executes the Azure Policy check.
func (c AzurePolicyChecker) Run(ctx context.Context) (*types.Result, error) {
	// TODO
	return nil, fmt.Errorf("Azure Policy checker is not implemented yet")
}

// containsAzurePolicyWarning checks if the error contains Azure Policy warnings.
func (c AzurePolicyChecker) containsAzurePolicyWarning(_ error) bool {
	// TODO
	return false
}
