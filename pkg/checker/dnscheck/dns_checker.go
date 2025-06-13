// Package dnscheck provides a checker for DNS.
package dnscheck

import (
	"context"
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

// DNSChecker implements the Checker interface for DNS checks.
type DNSChecker struct {
	name   string
	config *config.DNSConfig
}

// BuildDNSChecker creates a new DNSChecker instance.
func BuildDNSChecker(name string, config *config.DNSConfig) (*DNSChecker, error) {
	if name == "" {
		return nil, fmt.Errorf("checker name cannot be empty")
	}
	if err := config.ValidateDNSConfig(); err != nil {
		return nil, err
	}

	return &DNSChecker{
		name:   name,
		config: config,
	}, nil
}

func (c DNSChecker) Name() string {
	return c.name
}

func (c DNSChecker) Run(ctx context.Context) types.Result {
	// TODO: Get the CoreDNS service IP and pod IPs.

	// TODO: Get LocalDNS IP.

	// TODO: Implement the DNS checking logic here
	return types.Result{
		Status: types.StatusUnknown,
		ErrorDetail: &types.ErrorDetail{
			Code:    "NOT_IMPLEMENTED",
			Message: "DNSChecker not implemented yet",
		},
	}
}
