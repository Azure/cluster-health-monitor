package config

import (
	"errors"
	"fmt"
)

// validate validates the entire Config structure.
func (c *Config) validate() error {
	if c == nil {
		return fmt.Errorf("config is required")
	}
	if len(c.Checkers) == 0 {
		return fmt.Errorf("at least one checker is required")
	}

	var errs []error
	nameSet := make(map[string]struct{})
	for _, chk := range c.Checkers {
		if err := chk.validate(); err != nil {
			errs = append(errs, fmt.Errorf("checker %q: %w", chk.Name, err))
		}
		if _, exists := nameSet[chk.Name]; exists {
			errs = append(errs, fmt.Errorf("duplicate checker name: %q", chk.Name))
		}
		nameSet[chk.Name] = struct{}{}
	}

	return errors.Join(errs...)
}

// validate validates the common fields of a CheckerConfig.
func (c *CheckerConfig) validate() error {
	var errs []error
	if c.Name == "" {
		errs = append(errs, fmt.Errorf("checker config missing 'name'"))
	}
	if c.Namespace == "" {
		errs = append(errs, fmt.Errorf("checker config missing 'namespace'"))
	}
	if c.Type == "" {
		errs = append(errs, fmt.Errorf("checker config missing 'type'"))
	}
	if c.Interval <= 0 {
		errs = append(errs, fmt.Errorf("checker config invalid 'interval': %s", c.Interval))
	}
	if c.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("checker config invalid 'timeout': %s", c.Timeout))
	}

	switch c.Type {
	case CheckTypeDNS:
		if err := c.DNSConfig.validate(); err != nil {
			errs = append(errs, fmt.Errorf("checker config %q DNSConfig validation failed: %w", c.Name, err))
		}
	case CheckTypePodStartup:
		// No specific config for pod startup checker.
	default:
		errs = append(errs, fmt.Errorf("checker config %q has unsupported type: %s", c.Name, c.Type))
	}
	return errors.Join(errs...)
}

// validate validates the DNSConfig.
func (c *DNSConfig) validate() error {
	if c == nil {
		return fmt.Errorf("dnsConfig is required for DNSChecker")
	}
	if c.Domain == "" {
		return fmt.Errorf("domain is required for DNSChecker")
	}
	return nil
}
