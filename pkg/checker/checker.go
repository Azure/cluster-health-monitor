package checker

import (
	"context"
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

type Checker interface {
	// Name returns the name of the checker.
	Name() string

	// Type returns the type of the checker.
	Type() config.CheckerType

	// Run executes the health check logic for the checker.
	Run(ctx context.Context) (*types.Result, error)
}

type Builder func(cfg *config.CheckerConfig) (Checker, error)

var checkerRegistry = make(map[config.CheckerType]Builder)

func RegisterChecker(t config.CheckerType, builder Builder) {
	checkerRegistry[t] = builder
}

// Build creates checkers from a list of checker configs
func Build(cfg *config.CheckerConfig) (Checker, error) {
	builder, ok := checkerRegistry[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("unrecognized checker type: %q", cfg.Type)
	}
	return builder(cfg)
}
