package podstartup

import (
	"context"
	"errors"
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type PodStartupChecker struct {
	name         string
	k8sClientset kubernetes.Interface
}

func Register() {
	checker.RegisterChecker(config.CheckTypePodStartup, BuildPodStartupChecker)
}

// BuildPodStartupChecker creates a new PodStartupChecker instance.
func BuildPodStartupChecker(config *config.CheckerConfig) (checker.Checker, error) {
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}
	k8sClientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	return &PodStartupChecker{
		name:         config.Name,
		k8sClientset: k8sClientset,
	}, nil
}

func (c *PodStartupChecker) Name() string {
	return c.name
}

func (c *PodStartupChecker) Type() config.CheckerType {
	return config.CheckTypePodStartup
}

func (c *PodStartupChecker) Run(ctx context.Context) (*types.Result, error) {
	return nil, errors.New("PodStartupChecker not implemented yet")
}
