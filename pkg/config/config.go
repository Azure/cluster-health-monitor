package config

import (
	"time"

	"github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

// Type aliases for backward compatibility
type CheckerType = v1alpha1.CheckerType

const (
	CheckTypeDNS           = v1alpha1.CheckerTypeDNS
	CheckTypePodStartup    = v1alpha1.CheckerTypePodStartup
	CheckTypeAPIServer     = v1alpha1.CheckerTypeAPIServer
	CheckTypeMetricsServer = v1alpha1.CheckerTypeMetricsServer
	CheckTypeAzurePolicy   = v1alpha1.CheckerTypeAzurePolicy
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
	// It must be greater than 0.
	Interval time.Duration `yaml:"interval"`

	// Required.
	// The timeout for the checker, used to determine how long to wait for a response before considering the check failed.
	// The string format see https://pkg.go.dev/time#ParseDuration
	// It must be greater than 0.
	Timeout time.Duration `yaml:"timeout"`

	// Optional.
	// The configuration for the DNS checker, this field is required if Type is CheckTypeDNS.
	DNSConfig *DNSConfig `yaml:"dnsConfig,omitempty"`

	// Optional.
	// The configuration for the pod startup checker, this field is required if Type is CheckTypePodStartup.
	PodStartupConfig *PodStartupConfig `yaml:"podStartupConfig,omitempty"`

	// Optional.
	// The configuration for the API server checker, this field is required if Type is CheckTypeAPIServer.
	APIServerConfig *APIServerConfig `yaml:"apiServerConfig,omitempty"`
}

type DNSConfig struct {
	// Required.
	// The domain to check, used to determine the DNS records to query.
	Domain string `yaml:"domain"`
	// Required.
	// The timeout for DNS queries. The string format see https://pkg.go.dev/time#ParseDuration
	// It must be greater than 0.
	QueryTimeout time.Duration `yaml:"queryTimeout"`
	// Required.
	// DNS check mode: core DNS, per-pod core DNS, or local DNS.
	Target DNSCheckTarget `yaml:"target,omitempty"`
}
type DNSCheckTarget string

const (
	DNSCheckTargetCoreDNS       DNSCheckTarget = "CoreDNS"
	DNSCheckTargetCoreDNSPerPod DNSCheckTarget = "CoreDNSPerPod"
	DNSCheckTargetLocalDNS      DNSCheckTarget = "LocalDNS"
)

type PodStartupConfig struct {
	// Required.
	// The namespace in which synthetic pods are created.
	SyntheticPodNamespace string `yaml:"syntheticPodNamespace"`
	// Required.
	// The Kubernetes label key used to identify synthetic pods created by the checker.
	SyntheticPodLabelKey string `yaml:"syntheticPodLabelKey"`
	// Required.
	// The maximum synthetic pod startup duration for which the checker will return healthy status. Exceeding this duration will cause the
	// checker to return unhealthy status. The pod startup duration is defined as the time between the pod's creation timestamp and the time
	// its container starts running, minus the image pull duration (including waiting).
	SyntheticPodStartupTimeout time.Duration `yaml:"syntheticPodStartupTimeout"`
	// Required.
	// The maximum number of synthetic pods created by the checker that can exist at any one time. If the limit has been reached, the checker
	// will not create any more synthetic pods until some of the existing ones are deleted. Instead, it will fail the run with an error.
	// Reaching this limit effectively disables the checker.
	MaxSyntheticPods int `yaml:"maxSyntheticPods,omitempty"`
	// Required.
	// The maximum duration for which the checker will wait for a TCP connection to be established with a synthetic pod. Exceeding this
	// duration will cause the checker to return unhealthy status.
	TCPTimeout time.Duration `yaml:"tcpTimeout,omitempty"`

	// Optional.
	// This field is meant to be enabled only on AKS Automatic clusters. If set to true, the PodStartupChecker will trigger node provisioning and deploy synthetic pods to the new node.
	EnableNodeProvisioningTest bool `yaml:"enableNodeProvisioningTest,omitempty"`

	// Optional.
	// The PodStartupChecker will attach specified CSI storages to the synthetic pods.
	EnabledCSIs []CSIType `yaml:"enabledCSIs,omitempty"`
}

type CSIType string

const (
	CSITypeAzureFile CSIType = "azureFile"
	CSITypeAzureDisk CSIType = "azureDisk"
	CSITypeAzureBlob CSIType = "azureBlob"
)

type APIServerConfig struct {
	// Required.
	// The namespace in which the object is created for checking API server operations.
	Namespace string `yaml:"namespace"`
	// Required.
	// The Kubernetes label key used to identify the objects created by the checker.
	LabelKey string `yaml:"labelKey"`
	// Required.
	// The maximum duration for which the checker will wait return healthy status for mutating API calls.
	MutateTimeout time.Duration `yaml:"mutateTimeout"`
	// Required.
	// The maximum duration for which the checker will wait return healthy status for read-only API calls.
	ReadTimeout time.Duration `yaml:"readTimeout"`
	// Required.
	// The maximum number of objects created by the checker that can exist at any one time. If the limit has been reached, the checker
	// will not create any more objects until some of the existing ones are deleted. Instead, it will fail the run with an error.
	// Reaching this limit effectively disables the checker.
	MaxObjects int `yaml:"maxObjects,omitempty"`
}
