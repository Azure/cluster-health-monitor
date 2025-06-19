package checker

import (
	"context"
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

type Checker interface {
	Name() string

	// Run executes the health check logic for the checker.
	Run(ctx context.Context) (*types.Result, error)
}

type Builder func(cfg *config.CheckerConfig) (Checker, error)

var checkerRegistry = make(map[config.CheckerType]Builder)

func RegisterChecker(t config.CheckerType, builder Builder) {
	checkerRegistry[t] = builder
}

func Build(cfg *config.CheckerConfig) (Checker, error) {
	builder, ok := checkerRegistry[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("unrecognized checker type: %q", cfg.Type)
	}
	return builder(cfg)
}
