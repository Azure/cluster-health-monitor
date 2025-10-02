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
		if err := c.DNSConfig.validate(c.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("checker config %q DNSConfig validation failed: %w", c.Name, err))
		}
	case CheckTypePodStartup:
		if err := c.PodStartupConfig.validate(c.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("checker config %q PodStartupConfig validation failed: %w", c.Name, err))
		}
	case CheckTypeAPIServer:
		if err := c.APIServerConfig.validate(c.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("checker config %q APIServerConfig validation failed: %w", c.Name, err))
		}
	case CheckTypeAzurePolicy:
		// There is no specific validation for AzurePolicyConfig as it does not have additional fields.
	case CheckTypeMetricsServer:
		// There is no specific validation for MetricsServerConfig as it does not have additional fields.
	default:
		errs = append(errs, fmt.Errorf("checker config %q has unsupported type: %s", c.Name, c.Type))
	}
	return errors.Join(errs...)
}

// validate validates the DNSConfig.
func (c *DNSConfig) validate(checkerConfigTimeout time.Duration) error {
	if c == nil {
		return fmt.Errorf("dnsConfig is required for DNSChecker")
	}

	var errs []error
	if c.Domain == "" {
		errs = append(errs, fmt.Errorf("domain is required for DNSChecker"))
	}
	if c.QueryTimeout <= 0 {
		errs = append(errs, fmt.Errorf("queryTimeout must be greater than 0"))
	}
	switch c.Target {
	case DNSCheckTargetCoreDNS, DNSCheckTargetLocalDNS, DNSCheckTargetCoreDNSPerPod:
		// Valid check types for DNSChecker.
	case "":
		errs = append(errs, fmt.Errorf("target is required for DNSChecker"))
	default:
		errs = append(errs, fmt.Errorf("target %s is not valid for DNSChecker", c.Target))
	}

	if checkerConfigTimeout <= c.QueryTimeout {
		errs = append(errs, fmt.Errorf("checker timeout must be greater than DNS query timeout: checker timeout='%s', DNS query timeout='%s'",
			checkerConfigTimeout, c.QueryTimeout))
	}

	return errors.Join(errs...)
}

func (c *PodStartupConfig) validate(checkerConfigTimeout time.Duration) error {
	if c == nil {
		return fmt.Errorf("pod startup checker config is required")
	}

	var errs []error
	for _, nsErr := range apivalidation.ValidateNamespaceName(c.SyntheticPodNamespace, false) {
		errs = append(errs, fmt.Errorf("invalid synthetic pod namespace: value='%s', error='%s'", c.SyntheticPodNamespace, nsErr))
	}
	for _, labelErr := range utilvalidation.IsQualifiedName(c.SyntheticPodLabelKey) {
		errs = append(errs, fmt.Errorf("invalid synthetic pod label key: value='%s', error='%s'", c.SyntheticPodLabelKey, labelErr))
	}

	if checkerConfigTimeout <= c.SyntheticPodStartupTimeout+c.TCPTimeout {
		errs = append(errs, fmt.Errorf(
			"checker timeout must be greater than the combined synthetic pod startup timeout and TCP timeout: checker timeout='%s', synthetic pod startup timeout='%s', TCP timeout='%s'",
			checkerConfigTimeout, c.SyntheticPodStartupTimeout, c.TCPTimeout,
		))
	}

	if c.SyntheticPodStartupTimeout <= 0 {
		errs = append(errs, fmt.Errorf("synthetic pod startup timeout must be greater than 0: value='%s'", c.SyntheticPodStartupTimeout))
	}

	if c.TCPTimeout <= 0 {
		errs = append(errs, fmt.Errorf("TCP timeout must be greater than 0: value='%s'", c.TCPTimeout))
	}

	if c.MaxSyntheticPods <= 0 {
		errs = append(errs, fmt.Errorf("invalid max synthetic pods: value=%d, must be greater than 0", c.MaxSyntheticPods))
	}

	return errors.Join(errs...)
}

func (c *APIServerConfig) validate(checkerConfigTimeout time.Duration) error {
	if c == nil {
		return fmt.Errorf("API server checker config is required")
	}

	var errs []error
	for _, nsErr := range apivalidation.ValidateNamespaceName(c.Namespace, false) {
		errs = append(errs, fmt.Errorf("invalid namespace: value='%s', error='%s'", c.Namespace, nsErr))
	}
	for _, labelErr := range utilvalidation.IsQualifiedName(c.LabelKey) {
		errs = append(errs, fmt.Errorf("invalid label key: value='%s', error='%s'", c.LabelKey, labelErr))
	}

	if checkerConfigTimeout <= c.MutateTimeout {
		errs = append(errs, fmt.Errorf("checker timeout must be greater than mutate timeout: checker timeout='%s', mutate timeout='%s'",
			checkerConfigTimeout, c.MutateTimeout))
	}

	if checkerConfigTimeout <= c.ReadTimeout {
		errs = append(errs, fmt.Errorf("checker timeout must be greater than read timeout: checker timeout='%s', read timeout='%s'",
			checkerConfigTimeout, c.ReadTimeout))
	}

	if c.MaxObjects <= 0 {
		errs = append(errs, fmt.Errorf("invalid max objects: value=%d, must be greater than 0", c.MaxObjects))
	}

	return errors.Join(errs...)
}
