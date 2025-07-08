package checker

import (
	"context"
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

type Checker interface {
	// Name returns the name of the checker.
	Name() string

	// Type returns the type of the checker.
	Type() config.CheckerType

	// Run executes the health check logic for the checker.
	Run(ctx context.Context) (*types.Result, error)
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
