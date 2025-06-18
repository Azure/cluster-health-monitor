package config

import "time"

type CheckerType string

const (
	CheckTypeDNS        CheckerType = "dns"
	CheckTypePodStartup CheckerType = "podStartup"
)

// Config represents the configuration for the health checkers.
type Config struct {
	// Required.
	// The min number is 1, the max number is 20.
	Checkers []CheckerConfig `yaml:"checkers"`
}

// CheckerConfig represents the configuration for a specific health checker.
type CheckerConfig struct {
	// Required.
	// The unique name of the checker configuration, used to identify the checker in the system. The name is case-sensitive.
	// Name follow the DNS label standard rfc1123.
	Name string `yaml:"name"`

	// Required.
	// The type of the checker, used to determine which checker implementation to use.
	// Each checker type must be accompanied by its specific configuration if it requires additional parameters.
	Type CheckerType `yaml:"type"`

	// Required.
	// The interval at which the checker should run. The string format see https://pkg.go.dev/time#ParseDuration
	// Default is 0, which means the checker will run only once.
	Interval time.Duration `yaml:"interval"`

	// Required.
	// The timeout for the checker, used to determine how long to wait for a response before considering the check failed.
	// The string format see https://pkg.go.dev/time#ParseDuration
	// Default is 0, which means the checker will wait indefinitely for a response.
	Timeout time.Duration `yaml:"timeout"`

	// Optional.
	// The configuration for the DNS checker, this field is required if Type is CheckTypeDNS.
	DNSConfig *DNSConfig `yaml:"dnsConfig,omitempty"`
}

type DNSConfig struct {
	// Required.
	// The domain to check, used to determine the DNS records to query.
	Domain string `yaml:"domain"`
}
