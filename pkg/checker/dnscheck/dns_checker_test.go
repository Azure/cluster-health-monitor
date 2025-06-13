package dnscheck

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

func TestBuildDNSChecker(t *testing.T) {
	for _, tc := range []struct {
		name        string
		checkerName string
		dnsConfig   *config.DNSConfig
		validateRes func(g *WithT, checker *DNSChecker, err error)
	}{
		{
			name:        "Valid config",
			checkerName: "test-dns-checker",
			dnsConfig: &config.DNSConfig{
				Domain: "example.com",
			},
			validateRes: func(g *WithT, checker *DNSChecker, err error) {
				g.Expect(checker).To(Equal(
					&DNSChecker{
						name: "test-dns-checker",
						config: &config.DNSConfig{
							Domain: "example.com",
						},
					}))
				g.Expect(err).NotTo(HaveOccurred())
			},
		},
		{
			name:        "Empty Checker Name",
			checkerName: "",
			dnsConfig: &config.DNSConfig{
				Domain: "example.com",
			},
			validateRes: func(g *WithT, checker *DNSChecker, err error) {
				g.Expect(checker).To(BeNil())
				g.Expect(err).To(HaveOccurred())
			},
		},
		{
			name:        "Missing DNSConfig",
			checkerName: "test-dns-checker",
			dnsConfig:   nil,
			validateRes: func(g *WithT, checker *DNSChecker, err error) {
				g.Expect(checker).To(BeNil())
				g.Expect(err).To(HaveOccurred())
			},
		},
		{
			name:        "Empty Domain",
			checkerName: "test-dns-checker",
			dnsConfig: &config.DNSConfig{
				Domain: "",
			},
			validateRes: func(g *WithT, checker *DNSChecker, err error) {
				g.Expect(checker).To(BeNil())
				g.Expect(err).To(HaveOccurred())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			checker, err := BuildDNSChecker(tc.checkerName, tc.dnsConfig)
			tc.validateRes(g, checker, err)
		})
	}
}

func TestDNSCheckerRunReturnsResult(t *testing.T) {
	g := NewWithT(t)
	
	checker, err := BuildDNSChecker("test-dns-checker", &config.DNSConfig{
		Domain: "example.com",
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(checker).NotTo(BeNil())
	
	ctx := context.Background()
	result := checker.Run(ctx)
	
	// Since DNSChecker is not implemented, it should return unknown status
	g.Expect(result.Status).To(Equal(types.StatusUnknown))
	g.Expect(result.ErrorDetail).NotTo(BeNil())
	g.Expect(result.ErrorDetail.Code).To(Equal("NOT_IMPLEMENTED"))
	g.Expect(result.ErrorDetail.Message).To(Equal("DNSChecker not implemented yet"))
}
