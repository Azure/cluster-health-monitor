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

	if c.SyntheticPodStartupTimeout <= 0 {
		errs = append(errs, fmt.Errorf("synthetic pod startup timeout must be greater than 0: value='%s'", c.SyntheticPodStartupTimeout))
	}

	if c.TCPTimeout <= 0 {
		errs = append(errs, fmt.Errorf("TCP timeout must be greater than 0: value='%s'", c.TCPTimeout))
	}

	if c.TCPMaxRetries < 0 {
		errs = append(errs, fmt.Errorf("TCP retry attempts must be 0 or greater: value='%d'", c.TCPMaxRetries))
	}

	if c.TCPRetryInterval < 0 {
		errs = append(errs, fmt.Errorf("TCP retry interval must be 0 or greater: value='%s'", c.TCPRetryInterval))
	}

	if c.TCPMaxRetries == 0 && c.TCPRetryInterval != 0 {
		errs = append(errs, fmt.Errorf("TCP retry interval must be 0 when TCP max retries is 0: value='%s'", c.TCPRetryInterval))
	}

	if c.TCPMaxRetries > 0 && c.TCPRetryInterval <= 0 {
		errs = append(errs, fmt.Errorf("TCP retry interval must be greater than 0 when TCP max retries is greater than 0: value='%s'", c.TCPRetryInterval))
	}

	// tcpConnectivityBudget is the maximum possible time spent on TCP connection attempts including retries. This is calculated as
	// (TCP timeout * max number of attempts) + (TCP retry interval * max number of retries). Number of attempts is max retries + 1
	// because the first attempt is not a retry.
	tcpConnectivityBudget := time.Duration(c.TCPMaxRetries+1)*c.TCPTimeout + time.Duration(c.TCPMaxRetries)*c.TCPRetryInterval
	if checkerConfigTimeout <= c.SyntheticPodStartupTimeout+tcpConnectivityBudget {
		errs = append(errs, fmt.Errorf(
			"checker timeout must be greater than the combined synthetic pod startup timeout and TCP connectivity budget (max time spent on TCP connection attempts and retries): checker timeout='%s', synthetic pod startup timeout='%s', tcp connectivity budget='%s'",
			checkerConfigTimeout, c.SyntheticPodStartupTimeout, tcpConnectivityBudget,
		))
	}

	if c.MaxSyntheticPods <= 0 {
		errs = append(errs, fmt.Errorf("invalid max synthetic pods: value=%d, must be greater than 0", c.MaxSyntheticPods))
	}

	if c.CSI != nil && len(c.CSI) == 0 {
		errs = append(errs, fmt.Errorf("csi must not be empty when present"))
	}

	seenCSITypes := make(map[CSIType]struct{})
	for i, csi := range c.CSI {
		switch csi.Type {
		case CSITypeAzureFile, CSITypeAzureDisk, CSITypeAzureBlob:
			// valid CSI type
		default:
			errs = append(errs, fmt.Errorf("invalid csi type at index %d: value='%s'", i, csi.Type))
		}

		if _, exists := seenCSITypes[csi.Type]; exists {
			errs = append(errs, fmt.Errorf("duplicate csi type: %s", csi.Type))
		} else {
			seenCSITypes[csi.Type] = struct{}{}
		}

		for _, scErr := range utilvalidation.IsDNS1123Subdomain(csi.StorageClass) {
			errs = append(errs, fmt.Errorf("invalid csi storage class at index %d: value='%s', error='%s'", i, csi.StorageClass, scErr))
		}
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
