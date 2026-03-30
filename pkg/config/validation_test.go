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
				DNSConfig: &DNSConfig{Domain: "example.com", QueryTimeout: 2 * time.Second, Target: DNSCheckTargetCoreDNS},
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
					TCPTimeout:                 2 * time.Second,
					TCPMaxRetries:              3,
					TCPRetryInterval:           500 * time.Millisecond,
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
			name: "timeout less than or equal to combined pod startup timeout and tcp connectivity budget",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 3 * time.Second
				cfg.PodStartupConfig.SyntheticPodStartupTimeout = 2 * time.Second
				cfg.PodStartupConfig.TCPTimeout = 2 * time.Second
				cfg.PodStartupConfig.TCPMaxRetries = 1
				cfg.PodStartupConfig.TCPRetryInterval = 1 * time.Second
				// tcpConnectivityBudget = (2 * 2s) + (1 * 1s) = 5s
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than the combined synthetic pod startup timeout and TCP connectivity budget"))
			},
		},
		{
			name: "timeout less than or equal to combined pod startup timeout and tcp connectivity budget",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.Timeout = 8 * time.Second
				cfg.PodStartupConfig.SyntheticPodStartupTimeout = 3 * time.Second
				cfg.PodStartupConfig.TCPTimeout = 2 * time.Second
				cfg.PodStartupConfig.TCPMaxRetries = 2
				cfg.PodStartupConfig.TCPRetryInterval = 1 * time.Second
				// tcpConnectivityBudget = (2 * 2s) + (1 * 1s) = 5s, podStartup + tcpConnectivityBudget = 8s, so timeout must fail when equal.
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("checker timeout must be greater than the combined synthetic pod startup timeout and TCP connectivity budget"))
			},
		},
		{
			name: "pod startup timeout is zero",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.SyntheticPodStartupTimeout = 0
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("synthetic pod startup timeout must be greater than 0"))
			},
		},
		{
			name: "tcp timeout is zero",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.TCPTimeout = 0
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("TCP timeout must be greater than 0"))
			},
		},
		{
			name: "tcp timeout is negative",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.TCPTimeout = -1 * time.Second
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("TCP timeout must be greater than 0"))
			},
		},
		{
			name: "tcp retry attempts is zero and retry interval is zero",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.TCPMaxRetries = 0
				cfg.PodStartupConfig.TCPRetryInterval = 0
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).ToNot(HaveOccurred())
			},
		},
		{
			name: "tcp retry attempts is negative",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.TCPMaxRetries = -1
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("TCP retry attempts must be 0 or greater"))
			},
		},
		{
			name: "tcp retry attempts is zero and retry interval is non-zero",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.TCPMaxRetries = 0
				cfg.PodStartupConfig.TCPRetryInterval = 1 * time.Millisecond
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("TCP retry interval must be 0 when TCP max retries is 0"))
			},
		},
		{
			name: "tcp retry attempts is greater than 0 and retry interval is zero",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.TCPMaxRetries = 1
				cfg.PodStartupConfig.TCPRetryInterval = 0
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("TCP retry interval must be greater than 0 when TCP max retries is greater than 0"))
			},
		},
		{
			name: "tcp retry interval is negative",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.TCPRetryInterval = -1 * time.Millisecond
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("TCP retry interval must be 0 or greater"))
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
		{
			name: "valid CSI config",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.EnabledCSIs = []CSIConfig{
					{Type: CSITypeAzureDisk, StorageClass: "managed-csi"},
					{Type: CSITypeAzureFile, StorageClass: "azurefile-csi"},
					{Type: CSITypeAzureBlob, StorageClass: "clusterhealthmonitor-azureblob-sc"},
				}
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).ToNot(HaveOccurred())
			},
		},
		{
			name: "csi present but empty",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.EnabledCSIs = []CSIConfig{}
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("csi must not be empty when present"))
			},
		},
		{
			name: "duplicate csi type",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.EnabledCSIs = []CSIConfig{
					{Type: CSITypeAzureFile, StorageClass: "azurefile-csi"},
					{Type: CSITypeAzureFile, StorageClass: "azurefile-csi"},
				}
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("duplicate csi type"))
			},
		},
		{
			name: "unknown csi type",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.EnabledCSIs = []CSIConfig{
					{Type: CSIType("unknown"), StorageClass: "some-class"},
				}
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("invalid csi type"))
			},
		},
		{
			name: "invalid csi storage class name",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.PodStartupConfig.EnabledCSIs = []CSIConfig{
					{Type: CSITypeAzureBlob, StorageClass: "Invalid_Class_Name"},
				}
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("invalid csi storage class name"))
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
					TCPTimeout:                 1 * time.Second,
					TCPMaxRetries:              1,
					TCPRetryInterval:           1 * time.Second,
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
		{
			name: "DNS config target is missing",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.DNSConfig.Target = ""
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("target is required for DNSChecker"))
			},
		},
		{
			name: "DNS config target is invalid",
			mutateConfig: func(cfg *CheckerConfig) *CheckerConfig {
				cfg.DNSConfig.Target = "invalidTarget"
				return cfg
			},
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("target invalidTarget is not valid for DNSChecker"))
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
					Target:       DNSCheckTargetCoreDNS,
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			chkCfg := &CheckerConfig{
				Name:     "test",
				Type:     CheckTypeMetricsServer,
				Timeout:  10 * time.Second,
				Interval: 30 * time.Second,
			}

			if tt.mutateConfig != nil {
				chkCfg = tt.mutateConfig(chkCfg)
			}

			err := chkCfg.validate()
			tt.validateRes(g, err)
		})
	}
}
