package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ParseFromFile reads the configuration from a file and parses it.
func ParseFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %q: %w", path, err)
	}
	return parseFromYAML(data)
}

// parseFromYAML parses the configuration from a YAML.
func parseFromYAML(cfgData []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(cfgData, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal yaml: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}
	return &cfg, nil
}
