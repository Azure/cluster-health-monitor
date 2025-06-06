package checker

import (
	"fmt"

	yaml "gopkg.in/yaml.v3"

	dnscheck "github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	"github.com/Azure/cluster-health-monitor/pkg/checker/example"
)

type Checker interface {
	Name() string
	Run() error
}

type CheckerConfig struct {
	Name string         `yaml:"name"`
	Type string         `yaml:"type"`
	Spec map[string]any `yaml:"spec"`
}

type Config struct {
	Checkers []CheckerConfig `yaml:"checkers"`
}

type checkerBuilder func(name string, spec map[string]any) (Checker, error)

var registry = make(map[string]checkerBuilder)

func registerChecker(chkType string, builder checkerBuilder) {
	registry[chkType] = builder
}

func init() {
	// this is a example to show how to register a checker
	registerChecker("example", func(name string, spec map[string]any) (Checker, error) {
		return example.BuildExampleChecker(name, spec)
	})

	// Register dns checker
	registerChecker("dns", func(name string, spec map[string]any) (Checker, error) {
		return dnscheck.BuildDNSChecker(name, spec)
	})
}

func BuildCheckersFromConfig(config []byte) ([]Checker, error) {
	var root Config
	if err := yaml.Unmarshal(config, &root); err != nil {
		return nil, fmt.Errorf("failed to unmarshal yaml: %w", err)
	}

	nameSet := make(map[string]struct{})
	var checkers []Checker
	for _, cfg := range root.Checkers {
		if cfg.Name == "" || cfg.Spec == nil {
			return nil, fmt.Errorf("checker entry missing 'name' or 'spec'")
		}
		if _, exists := nameSet[cfg.Name]; exists {
			return nil, fmt.Errorf("duplicate checker name: %q", cfg.Name)
		}
		nameSet[cfg.Name] = struct{}{}
		chk, err := buildChecker(cfg.Name, cfg.Type, cfg.Spec)
		if err != nil {
			return nil, fmt.Errorf("failed to build checker %q: %w", cfg.Name, err)
		}
		checkers = append(checkers, chk)
	}
	return checkers, nil
}

// buildChecker creates a checker by registry identity (name) and passes the spec.
func buildChecker(name, chkType string, spec map[string]any) (Checker, error) {
	builder, exists := registry[chkType]
	if !exists {
		return nil, fmt.Errorf("checker type %q not registered", chkType)
	}
	return builder(name, spec)
}
