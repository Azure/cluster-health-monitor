package config

import (
	"fmt"
	"os"

	yaml "gopkg.in/yaml.v3"
)

func ParseFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %q: %w", path, err)
	}
	return ParseFromYAML(data)
}

func ParseFromYAML(cfgData []byte) (*Config, error) {

	var cfg Config
	if err := yaml.Unmarshal(cfgData, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal yaml: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}
	return &cfg, nil
}
