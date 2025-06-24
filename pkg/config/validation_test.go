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
			{
				Name:     "podStartup1",
				Type:     CheckTypePodStartup,
				Interval: 1 * time.Minute,
				Timeout:  30 * time.Second,
				PodStartupConfig: &PodStartupConfig{
					SyntheticPodNamespace:      "default",
					SyntheticPodLabelKey:       "cluster-health-monitor/checker-name",
					SyntheticPodStartupTimeout: 5 * time.Second,
					MaxSyntheticPods:           10,
				},
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

func TestPodStartupConfig_Validate(t *testing.T) {
	tests := []struct {
		name         string
		mutateConfig func(cfg *CheckerConfig) *CheckerConfig
		validateRes  func(g *WithT, err error)
	}{
		{
			name: "valid config",
			validateRes: func(g *WithT, err error) {
				g.Expect(err).ToNot(HaveOccurred())
			},
		},
		{
			name: "nil podStartup config",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig = nil
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("pod startup checker config is required"))
			},
		},
		{
			name: "missing synthetic pod namespace",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.SyntheticPodNamespace = ""
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("pod startup checker config missing synthetic pod namespace"))
			},
		},
		{
			name: "invalid synthetic pod namespace",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.SyntheticPodNamespace = "!@%#^(^#&!@^#*&!)"
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("pod startup checker config synthetic pod namespace must be a valid k8s namespace name"))
			},
		},
		{
			name: "missing synthetic pod label key",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.SyntheticPodLabelKey = ""
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("pod startup checker config missing synthetic pod label key"))
			},
		},
		{
			name: "invalid synthetic pod label key",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.SyntheticPodLabelKey = "!@%#^(^#&!@^#*&!)"
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("pod startup checker config synthetic pod label key must be a valid k8s label key"))
			},
		},
		{
			name: "timeout less than or equal to pod startup timeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 3 * time.Second
				cfg.PodStartupConfig.SyntheticPodStartupTimeout = 5 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than the synthetic pod startup timeout"))
			},
		},
		{
			name: "max synthetic pods is zero",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.MaxSyntheticPods = 0
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("pod startup checker config invalid max synthetic pods: 0, must be greater than 0"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Valida chkCfg
			chkCfg := &CheckerConfig{
				Name:    "test-checker",
				Type:    CheckTypePodStartup,
				Timeout: 10 * time.Second,
				PodStartupConfig: &PodStartupConfig{
					SyntheticPodNamespace:      "synthetic-pod-namespace",
					SyntheticPodLabelKey:       "cluster-health-monitor/checker-name",
					SyntheticPodStartupTimeout: 5 * time.Second,
					MaxSyntheticPods:           3,
				},
			}

			// Mutate func changes this in various ways to invalidate it
			if tt.mutateConfig != nil {
				chkCfg = tt.mutateConfig(chkCfg)
			}

			err := chkCfg.PodStartupConfig.validate(chkCfg.Timeout)
			tt.validateRes(g, err)
		})
	}
}
