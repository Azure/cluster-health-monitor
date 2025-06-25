package config

import (
	"errors"
	"fmt"
	"time"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
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
		if err := c.PodStartupConfig.validate(c.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("checker config %q PodStartupConfig validation failed: %w", c.Name, err))
		}
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

func (c *PodStartupConfig) validate(checkerConfigTimeout time.Duration) error {
	if c == nil {
		return fmt.Errorf("pod startup checker config is required")
	}

	var errs []error
	if c.SyntheticPodNamespace == "" {
		errs = append(errs, fmt.Errorf("pod startup checker config missing synthetic pod namespace"))
	}
	for _, nsErr := range apivalidation.ValidateNamespaceName(c.SyntheticPodNamespace, false) {
		errs = append(errs, fmt.Errorf("pod startup checker config synthetic pod namespace: %s", nsErr))
	}

	if c.SyntheticPodLabelKey == "" {
		errs = append(errs, fmt.Errorf("pod startup checker config missing synthetic pod label key"))
	}
	for _, labelErr := range utilvalidation.IsQualifiedName(c.SyntheticPodLabelKey) {
		errs = append(errs, fmt.Errorf("pod startup checker config synthetic pod label key: %s", labelErr))
	}

	if checkerConfigTimeout <= c.SyntheticPodStartupTimeout {
		errs = append(errs, fmt.Errorf("checker timeout must be greater than the synthetic pod startup timeout (%s), got: %s",
			checkerConfigTimeout, c.SyntheticPodStartupTimeout))
	}

	if c.MaxSyntheticPods <= 0 {
		errs = append(errs, fmt.Errorf("pod startup checker config invalid max synthetic pods: %d, must be greater than 0", c.MaxSyntheticPods))
	}

	return errors.Join(errs...)
}
