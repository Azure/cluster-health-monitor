package checker

import (
	"context"
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/config"
)

type Checker interface {
	Name() string
	Run(ctx context.Context) (Result, error)
}

type Builder func(cfg *config.CheckerConfig) (Checker, error)

var checkerRegistry = make(map[config.CheckerType]Builder)

func RegisterChecker(t config.CheckerType, builder Builder) {
	checkerRegistry[t] = builder
}

// Build creates a new Checker instance based on the provided configuration.
func Build(cfg *config.CheckerConfig) (Checker, error) {
	builder, ok := checkerRegistry[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("unrecognized checker type: %q", cfg.Type)
	}
	return builder(cfg)
}
