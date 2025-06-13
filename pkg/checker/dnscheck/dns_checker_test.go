package dnscheck

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/Azure/cluster-health-monitor/pkg/config"
)

func TestBuildDNSChecker(t *testing.T) {
	for _, tc := range []struct {
		name          string
		checkerConfig *config.CheckerConfig
		validateRes   func(g *WithT, checker *DNSChecker, err error)
	}{
		{
			name: "Valid config",
			checkerConfig: &config.CheckerConfig{
				Name: "test-dns-checker",
				Type: config.CheckTypeDNS,
				DNSConfig: &config.DNSConfig{
					Domain: "example.com",
				},
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
			name: "Empty Checker Name",
			checkerConfig: &config.CheckerConfig{
				Name: "",
				Type: config.CheckTypeDNS,
				DNSConfig: &config.DNSConfig{
					Domain: "example.com",
				},
			},
			validateRes: func(g *WithT, checker *DNSChecker, err error) {
				g.Expect(checker).To(BeNil())
				g.Expect(err).To(HaveOccurred())
			},
		},
		{
			name: "Missing DNSConfig",
			checkerConfig: &config.CheckerConfig{
				Name:      "test-dns-checker",
				Type:      config.CheckTypeDNS,
				DNSConfig: nil,
			},
			validateRes: func(g *WithT, checker *DNSChecker, err error) {
				g.Expect(checker).To(BeNil())
				g.Expect(err).To(HaveOccurred())
			},
		},
		{
			name: "Empty Domain",
			checkerConfig: &config.CheckerConfig{
				Name: "test-dns-checker",
				Type: config.CheckTypeDNS,
				DNSConfig: &config.DNSConfig{
					Domain: "",
				},
			},
			validateRes: func(g *WithT, checker *DNSChecker, err error) {
				g.Expect(checker).To(BeNil())
				g.Expect(err).To(HaveOccurred())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			c, err := Build(tc.checkerConfig)
			checker, ok := c.(*DNSChecker)
			g.Expect(ok).To(BeTrue(), "Expected checker to be of type *DNSChecker")
			tc.validateRes(g, checker, err)
		})
	}
}

func TestDNSCheckerRunReturnsResult(t *testing.T) {
	g := NewWithT(t)

	checker, err := Build(&config.CheckerConfig{
		Name: "test-dns-checker",
		Type: config.CheckTypeDNS,
		DNSConfig: &config.DNSConfig{
			Domain: "example.com",
		},
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(checker).NotTo(BeNil())
}
