package checker

import (
	"context"
	"errors"
	"fmt"

	yaml "gopkg.in/yaml.v3"

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

func BuildCheckersFromConfig(cfg []byte) ([]Checker, error) {
	var root config.Config
	if err := yaml.Unmarshal(cfg, &root); err != nil {
		return nil, fmt.Errorf("failed to unmarshal yaml: %w", err)
	}

	nameSet := make(map[string]struct{})
	var checkers []Checker
	var errs []error
	for _, cfg := range root.Checkers {
		if err := cfg.ValidateCommon(); err != nil {
			errs = append(errs, fmt.Errorf("failed to validate checker config %q: %w", cfg.Name, err))
		}
		if _, exists := nameSet[cfg.Name]; exists {
			errs = append(errs, fmt.Errorf("duplicate checker name: %q", cfg.Name))
		}
		nameSet[cfg.Name] = struct{}{}
		chk, err := buildChecker(&cfg)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to build checker %q: %w", cfg.Name, err))
			continue
		}
		checkers = append(checkers, chk)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return checkers, nil
}

// buildChecker creates a checker by registry identity (name) and passes the spec.
func buildChecker(cfg *config.CheckerConfig) (Checker, error) {
	builder, ok := checkerRegistry[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("unrecognized checker type: %q", cfg.Type)
	}
	return builder(cfg)
}
