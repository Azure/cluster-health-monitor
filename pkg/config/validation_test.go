package config

import (
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

func TestConfigValidate_Valid(t *testing.T) {
	g := NewWithT(t)
	cfg := &Config{
		Checkers: []CheckerConfig{
			{
				Name:      "dns1",
				Type:      CheckTypeDNS,
				Interval:  10 * time.Second,
				Timeout:   2 * time.Second,
				DNSConfig: &DNSConfig{Domain: "example.com"},
			},
		},
	}
	err := cfg.validate()
	g.Expect(err).ToNot(HaveOccurred())
}

func TestConfigValidate_NoCheckers(t *testing.T) {
	g := NewWithT(t)
	cfg := &Config{}
	err := cfg.validate()
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(Equal("at least one checker is required"))
}

func TestConfigValidate_DuplicateNames(t *testing.T) {
	g := NewWithT(t)
	cfg := &Config{
		Checkers: []CheckerConfig{
			{Name: "foo", Type: CheckTypeDNS, Interval: 1, Timeout: 1, DNSConfig: &DNSConfig{Domain: "a"}},
			{Name: "foo", Type: CheckTypeDNS, Interval: 1, Timeout: 1, DNSConfig: &DNSConfig{Domain: "b"}},
		},
	}
	err := cfg.validate()
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("duplicate checker name"))
}

func TestCheckerConfigValidate_MissingFields(t *testing.T) {
	g := NewWithT(t)
	chk := CheckerConfig{}
	err := chk.validate()
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("missing 'name'"))
	g.Expect(err.Error()).To(ContainSubstring("missing 'type'"))
	g.Expect(err.Error()).To(ContainSubstring("invalid 'interval'"))
	g.Expect(err.Error()).To(ContainSubstring("invalid 'timeout'"))
}

func TestCheckerConfigValidate_UnsupportedType(t *testing.T) {
	g := NewWithT(t)
	chk := CheckerConfig{Name: "foo", Type: "badtype", Interval: 1, Timeout: 1}
	err := chk.validate()
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("unsupported type"))
}
