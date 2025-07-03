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
			name: "invalid synthetic pod namespace",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.SyntheticPodNamespace = ""
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("invalid synthetic pod namespace"))
			},
		},
		{
			name: "invalid synthetic pod label key",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.SyntheticPodLabelKey = ""
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("invalid synthetic pod label key"))
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
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than synthetic pod startup timeout"))
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
				g.Expect(err.Error()).To(ContainSubstring("invalid max synthetic pods"))
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

func TestAPIServerConfig_Validate(t *testing.T) {
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
			name: "nil apiServer config",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.APIServerConfig = nil
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("API server checker config is required"))
			},
		},
		{
			name: "invalid config map namespace",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.APIServerConfig.ConfigMapNamespace = ""
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("invalid config map namespace"))
			},
		},
		{
			name: "invalid config map label key",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.APIServerConfig.ConfigMapLabelKey = ""
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("invalid config map label key"))
			},
		},
		{
			name: "timeout less than config map mutate timeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 3 * time.Second
				cfg.APIServerConfig.ConfigMapMutateTimeout = 5 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than config map mutate timeout"))
			},
		},
		{
			name: "timeout equal to config map mutate timeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 5 * time.Second
				cfg.APIServerConfig.ConfigMapMutateTimeout = 5 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than config map mutate timeout"))
			},
		},
		{
			name: "timeout less than config map read timeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 3 * time.Second
				cfg.APIServerConfig.ConfigMapReadTimeout = 5 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than config map read timeout"))
			},
		},
		{
			name: "timeout equal to config map read timeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 5 * time.Second
				cfg.APIServerConfig.ConfigMapReadTimeout = 5 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than config map read timeout"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			chkCfg := &CheckerConfig{
				Name:     "test-checker",
				Type:     CheckTypeAPIServer,
				Timeout:  10 * time.Second,
				Interval: 30 * time.Second,
				APIServerConfig: &APIServerConfig{
					ConfigMapNamespace:     "config-map-namespace",
					ConfigMapLabelKey:      "cluster-health-monitor/checker-name",
					ConfigMapMutateTimeout: 5 * time.Second,
					ConfigMapReadTimeout:   1 * time.Second,
				},
			}

			if tt.mutateConfig != nil {
				chkCfg = tt.mutateConfig(chkCfg)
			}

			err := chkCfg.validate()
			tt.validateRes(g, err)
		})
	}
}
