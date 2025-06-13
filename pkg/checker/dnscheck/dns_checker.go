// Package dnscheck provides a checker for DNS.
package dnscheck

import (
	"context"
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
)

// DNSChecker implements the Checker interface for DNS checks.
type DNSChecker struct {
	name   string
	config *config.DNSConfig
}

func init() {
	checker.RegisterChecker(config.CheckTypeDNS, func(cfg *config.CheckerConfig) (checker.Checker, error) {
		return Build(cfg)
	})
}

// Build creates a new DNSChecker instance.
func Build(config *config.CheckerConfig) (checker.Checker, error) {
	if config.Name == "" {
		return nil, fmt.Errorf("checker name cannot be empty")
	}
	if err := config.DNSConfig.ValidateDNSConfig(); err != nil {
		return nil, err
	}

	return &DNSChecker{
		name:   config.Name,
		config: config.DNSConfig,
	}, nil
}

func (c *DNSChecker) Name() string {
	return c.name
}

func (c *DNSChecker) Run(ctx context.Context) (checker.Result, error) {
	// TODO: Get the CoreDNS service IP and pod IPs.

	// TODO: Get LocalDNS IP.

	// TODO: Implement the DNS checking logic here
	return checker.Result{
		Status: checker.StatusUnhealthy,
		ErrorDetail: &checker.ErrorDetail{
			Code:    "NOT_IMPLEMENTED",
			Message: "DNSChecker not implemented yet",
		},
	}, nil
}
