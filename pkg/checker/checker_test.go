package checker

import (
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	. "github.com/onsi/gomega"
)

func TestRegisterAndBuildExampleChecker(t *testing.T) {
	// Register the ExampleChecker builder
	yamlData := []byte(`
checkers:
- name: dns 
  type: dns 
  interval: 10s
  dnsConfig:
    domain: example.com
`)

	g := NewGomegaWithT(t)
	checkers, err := BuildCheckersFromConfig(yamlData)
	g.Expect(err).NotTo(HaveOccurred(), "failed to build checkers")
	g.Expect(checkers).To(HaveLen(1), "expected 1 checker")

	dc, ok := checkers[0].(*dnscheck.DNSChecker)
	g.Expect(ok).To(BeTrue(), "checker should be of type *dnscheck.DNSChecker")
	g.Expect(dc.Name()).To(Equal("dns"), "checker should have the correct name")
}

func TestValidationInBuildCheckersFromConfig(t *testing.T) {
	for _, tc := range []struct {
		name        string
		yaml        string
		validateRes func(g *WithT, checkers []Checker, err error)
	}{
		{
			name: "Valid DNS Checker",
			yaml: `
checkers:
- name: valid-checker
  type: dns
  interval: 10s
  dnsConfig:
    domain: example.com
`,
			validateRes: func(g *WithT, checkers []Checker, err error) {
				g.Expect(checkers).To(HaveLen(1))
				g.Expect(err).NotTo(HaveOccurred())
			},
		},
		{
			name: "Duplicate Name",
			yaml: `
checkers:
- name: duplicate-name
  type: dns
  interval: 10s
  dnsConfig:
    domain: example.com
- name: duplicate-name
  type: dns
  interval: 10s
  dnsConfig:
    domain: example.com
`,
			validateRes: func(g *WithT, checkers []Checker, err error) {
				g.Expect(checkers).To(BeNil())
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("duplicate checker name:"))
			},
		},
		{
			name: "Empty Name",
			yaml: `
checkers:
- name: "" 
  type: dns
  interval: 10s
`,
			validateRes: func(g *WithT, checkers []Checker, err error) {
				g.Expect(checkers).To(BeNil())
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("missing 'name'"))
			},
		},
		{
			name: "Missing Type",
			yaml: `
checkers:
- name: test-checker
  interval: 10s
`,
			validateRes: func(g *WithT, checkers []Checker, err error) {
				g.Expect(checkers).To(BeNil())
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("missing 'type'"))
			},
		},
		{
			name: "Invalid Interval",
			yaml: `
checkers:
- name: test-checker
  type: dns
  interval: -10s
`,
			validateRes: func(g *WithT, checkers []Checker, err error) {
				g.Expect(checkers).To(BeNil())
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("invalid 'interval'"))
			},
		},
		{
			name: "Invalid Timeout",
			yaml: `
checkers:
- name: test-checker
  type: dns
  interval: 10s
  timeout: -5s
`,
			validateRes: func(g *WithT, checkers []Checker, err error) {
				g.Expect(checkers).To(BeNil())
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("invalid 'timeout'"))
			},
		},
		{
			name: "Unknown Type",
			yaml: `
checkers:
- name: unknown-checker
  type: unknown
`,
			validateRes: func(g *WithT, checkers []Checker, err error) {
				g.Expect(checkers).To(BeNil())
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("unrecognized checker type:"))
			},
		},
		{
			name: "Multiple Errors",
			yaml: `
checkers:
- name: invalid-test-checker
  type: dns
  interval: -10s
  timeout: -5s
  dnsConfig:
    domain:
- name: unknown-checker
  type: unknown
- name: duplicate-test-checker
  type: dns
  interval: 10s
  timeout: 5s
  dnsConfig:
    domain: example.com
- name: duplicate-test-checker
  type: dns
  interval: 10s
  timeout: 5s
  dnsConfig:
    domain: example.com
`, validateRes: func(g *WithT, checkers []Checker, err error) {
				g.Expect(checkers).To(BeNil())
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("\"invalid-test-checker\""))
				g.Expect(err.Error()).To(ContainSubstring("invalid 'interval'"))
				g.Expect(err.Error()).To(ContainSubstring("invalid 'timeout'"))
				g.Expect(err.Error()).To(ContainSubstring("domain is required for DNSChecker"))
				g.Expect(err.Error()).To(ContainSubstring("\"unknown-checker\""))
				g.Expect(err.Error()).To(ContainSubstring("unrecognized checker type:"))
				g.Expect(err.Error()).To(ContainSubstring("duplicate checker name: \"duplicate-test-checker\""))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			checkers, err := BuildCheckersFromConfig([]byte(tc.yaml))
			tc.validateRes(g, checkers, err)
		})
	}
}
