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
				Timeout:   5 * time.Second,
				DNSConfig: &DNSConfig{Domain: "example.com", QueryTimeout: 2 * time.Second},
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
			{Name: "foo", Type: CheckTypeDNS, Interval: 1, Timeout: 2 * time.Second, DNSConfig: &DNSConfig{Domain: "a", QueryTimeout: 1 * time.Second}},
			{Name: "foo", Type: CheckTypeDNS, Interval: 1, Timeout: 2 * time.Second, DNSConfig: &DNSConfig{Domain: "b", QueryTimeout: 1 * time.Second}},
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
				Name:    "test",
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
			name: "invalid namespace",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.APIServerConfig.Namespace = ""
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("invalid namespace"))
			},
		},
		{
			name: "invalid label key",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.APIServerConfig.LabelKey = ""
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("invalid label key"))
			},
		},
		{
			name: "timeout less than mutate timeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 3 * time.Second
				cfg.APIServerConfig.MutateTimeout = 5 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than mutate timeout"))
			},
		},
		{
			name: "timeout equal to mutate timeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 5 * time.Second
				cfg.APIServerConfig.MutateTimeout = 5 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than mutate timeout"))
			},
		},
		{
			name: "timeout less than read timeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 3 * time.Second
				cfg.APIServerConfig.ReadTimeout = 5 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than read timeout"))
			},
		},
		{
			name: "timeout equal to read timeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 5 * time.Second
				cfg.APIServerConfig.ReadTimeout = 5 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than read timeout"))
			},
		},
		{
			name: "max objects is zero",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.APIServerConfig.MaxObjects = 0
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("invalid max objects"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			chkCfg := &CheckerConfig{
				Name:     "test",
				Type:     CheckTypeAPIServer,
				Timeout:  10 * time.Second,
				Interval: 30 * time.Second,
				APIServerConfig: &APIServerConfig{
					Namespace:     "config-map-namespace",
					LabelKey:      "cluster-health-monitor/checker-name",
					MutateTimeout: 5 * time.Second,
					ReadTimeout:   1 * time.Second,
					MaxObjects:    3,
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

func TestDNSConfig_Validate(t *testing.T) {
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
			name: "nil dns config",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.DNSConfig = nil
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("dnsConfig is required for DNSChecker"))
			},
		},
		{
			name: "missing domain",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.DNSConfig.Domain = ""
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("domain is required for DNSChecker"))
			},
		},
		{
			name: "zero queryTimeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.DNSConfig.QueryTimeout = 0
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("queryTimeout must be greater than 0"))
			},
		},
		{
			name: "negative queryTimeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.DNSConfig.QueryTimeout = -1 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("queryTimeout must be greater than 0"))
			},
		},
		{
			name: "checker timeout less than query timeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 3 * time.Second
				cfg.DNSConfig.QueryTimeout = 5 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than DNS query timeout"))
			},
		},
		{
			name: "checker timeout equal to query timeout",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 5 * time.Second
				cfg.DNSConfig.QueryTimeout = 5 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than DNS query timeout"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			chkCfg := &CheckerConfig{
				Name:     "test-checker",
				Type:     CheckTypeDNS,
				Timeout:  10 * time.Second,
				Interval: 30 * time.Second,
				DNSConfig: &DNSConfig{
					Domain:       "example.com",
					QueryTimeout: 2 * time.Second,
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

func TestMetricsServerConfig_Validate(t *testing.T) {
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
			name: "nil metrics server config",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.MetricsServerConfig = nil
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("metrics server checker config is required"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			chkCfg := &CheckerConfig{
				Name:                "test",
				Type:                CheckTypeMetricsServer,
				Timeout:             10 * time.Second,
				Interval:            30 * time.Second,
				MetricsServerConfig: &MetricsServerConfig{},
			}

			if tt.mutateConfig != nil {
				chkCfg = tt.mutateConfig(chkCfg)
			}

			err := chkCfg.validate()
			tt.validateRes(g, err)
		})
	}
}
