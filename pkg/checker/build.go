package checker

import (
	"context"
	"errors"
	"fmt"

	yaml "gopkg.in/yaml.v3"

	"github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	"github.com/Azure/cluster-health-monitor/pkg/checker/podstartup"
	"github.com/Azure/cluster-health-monitor/pkg/config"
)

type Checker interface {
	Name() string
	Run(ctx context.Context) error
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
		chk, err := buildChecker(cfg)
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
func buildChecker(cfg config.CheckerConfig) (Checker, error) {
	switch cfg.Type {
	case config.CheckTypeDNS:
		return dnscheck.BuildDNSChecker(cfg.Name, cfg.DNSConfig)
	case config.CheckTypePodStartup:
		return podstartup.BuildPodStartupChecker(cfg.Name, cfg.PodStartupConfig)
	default:
		return nil, fmt.Errorf("unrecognized checker type: %q", cfg.Type)
	}
}
