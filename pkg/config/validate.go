package config

import (
	"errors"
	"fmt"
)

// ValidateCommon validates the common fields of a CheckerConfig.
func (c *CheckerConfig) ValidateCommon() error {
	var errs []error
	if c.Name == "" {
		errs = append(errs, fmt.Errorf("checker config missing 'name'"))
	}
	if c.Type == "" {
		errs = append(errs, fmt.Errorf("checker config missing 'type'"))
	}
	if c.Interval < 0 {
		errs = append(errs, fmt.Errorf("checker config invalid 'interval'"))
	}
	if c.Timeout < 0 {
		errs = append(errs, fmt.Errorf("checker config invalid 'timeout'"))
	}
	return errors.Join(errs...)
}

// ValidateDNSConfig validates the DNSConfig.
func (c *DNSConfig) ValidateDNSConfig() error {
	if c == nil {
		return fmt.Errorf("dnsConfig is required for DNSChecker")
	}
	if c.Domain == "" {
		return fmt.Errorf("domain is required for DNSChecker")
	}
	return nil
}

func (c *PodStartupConfig) ValidatePodStartupConfig() error {
	if c == nil {
		return fmt.Errorf("podStartupConfig is required for PodStartupChecker")
	}
	if c.Namespace == "" {
		return fmt.Errorf("namespace is required for PodStartupChecker")
	}
	if c.MaxSyntheticPods <= 0 {
		return fmt.Errorf("maxSyntheticPods must be greater than 0 for PodStartupChecker")
	}
	return nil
}
