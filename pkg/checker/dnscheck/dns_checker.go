// Package dnscheck provides a checker for DNS.
package dnscheck

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/mitchellh/mapstructure"
	v1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	DefaultTimeout     = 100 * time.Millisecond
	DefaultInterval    = 5 * time.Second
	CoreDNSNamespace   = "kube-system"
	CoreDNSServiceName = "kube-dns"
)

// DNSChecker implements the Checker interface for DNS checks.
type DNSChecker struct {
	name     string
	Domain   string        `mapstructure:"domain"`
	Timeout  time.Duration `mapstructure:"timeout"`
	Interval time.Duration `mapstructure:"interval"`

	kubeClient kubernetes.Interface
	// dnsResolver handles DNS resolution with the default implementation being checkDNS.
	// This can be overridden in tests to mock DNS resolution behavior.
	dnsResolver func(string) (time.Duration, error)
}

// DNSTargetType defines the type of DNS target.
type DNSTargetType string

const (
	CoreDNSService DNSTargetType = "service"
	CoreDNSPod     DNSTargetType = "pod"
)

// DNSTarget represents a DNS target with its IP and type.
type DNSTarget struct {
	IP   string
	Type DNSTargetType
}

// BuildDNSChecker creates a new DNSChecker instance.
func BuildDNSChecker(name string, spec map[string]any) (*DNSChecker, error) {
	return BuildDNSCheckerWithClient(name, spec, nil)
}

// BuildDNSCheckerWithClient creates a new DNSChecker instance with a custom Kubernetes client.
func BuildDNSCheckerWithClient(name string, spec map[string]any, client kubernetes.Interface) (*DNSChecker, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required to build DNSChecker")
	}

	checker := &DNSChecker{
		name:       name,
		Timeout:    DefaultTimeout,
		Interval:   DefaultInterval,
		kubeClient: client,
	}

	if err := mapstructure.Decode(spec, checker); err != nil {
		return nil, fmt.Errorf("failed to decode DNSChecker spec: %w", err)
	}

	if checker.Domain == "" {
		return nil, fmt.Errorf("domain is required for DNSChecker")
	}

	checker.dnsResolver = checker.checkDNS

	return checker, nil
}

// Name returns the name of the checker.
func (c *DNSChecker) Name() string {
	return c.name
}

// Run executes the CoreDNS check.
func (c *DNSChecker) Run() error {
	return c.RunWithContext(context.Background())
}

// RunWithContext executes the CoreDNS check with a provided context for cancellation.
func (c *DNSChecker) RunWithContext(ctx context.Context) error {
	if c.kubeClient == nil {
		config, err := rest.InClusterConfig()
		if err != nil {
			return fmt.Errorf("failed to create in-cluster config: %w", err)
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			return fmt.Errorf("failed to create Kubernetes client: %w", err)
		}
		c.kubeClient = clientset
	}

	targets := make(map[DNSTarget]struct{})

	serviceIP, err := c.getCoreDNSServiceIP()
	if err != nil {
		log.Println("Warning: failed to get CoreDNS service IP:", err)
	} else {
		targets[DNSTarget{IP: serviceIP, Type: CoreDNSService}] = struct{}{}
	}

	podIPs, err := c.getCoreDNSPodIPs()
	if err != nil {
		log.Println("Warning: failed to get CoreDNS pod IPs:", err)
	} else {
		for ip := range podIPs {
			targets[DNSTarget{IP: ip, Type: CoreDNSPod}] = struct{}{}
		}
	}

	// TODO: Get LocalDNS IP.

	if len(targets) == 0 {
		return fmt.Errorf("no DNS targets found to query")
	}

	log.Println("Querying DNS targets:", slices.Collect(maps.Keys(targets)))

	checkTicker := time.NewTicker(c.Interval)
	defer checkTicker.Stop()

	for {
		select {
		case <-checkTicker.C:
			var wg sync.WaitGroup
			for target := range targets {
				wg.Add(1)
				go func(t DNSTarget) {
					defer wg.Done()
					rtt, err := c.dnsResolver(t.IP)

					if err != nil || rtt > c.Timeout {
						if errors.Is(err, context.DeadlineExceeded) {
							// TODO: Observe timeout as metric.
							return
						}

						// TODO: Observe error as metric.
						return
					}
					// TODO: Observe success as metric.
				}(target)
			}
			wg.Wait()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// getCoreDNSServiceIP retrieves the IP address of the CoreDNS service.
func (c *DNSChecker) getCoreDNSServiceIP() (string, error) {
	if c.kubeClient == nil {
		return "", fmt.Errorf("Kubernetes client not initialized")
	}

	service, err := c.kubeClient.CoreV1().Services(CoreDNSNamespace).Get(
		context.Background(),
		CoreDNSServiceName,
		metav1.GetOptions{},
	)
	if err != nil {
		return "", fmt.Errorf("failed to get CoreDNS service: %w", err)
	}

	if service.Spec.ClusterIP == "" {
		return "", fmt.Errorf("CoreDNS service has no cluster IP")
	}

	return service.Spec.ClusterIP, nil
}

// getCoreDNSPodIPs retrieves the IP addresses of all CoreDNS pods.
func (c *DNSChecker) getCoreDNSPodIPs() (map[string]struct{}, error) {
	if c.kubeClient == nil {
		return nil, fmt.Errorf("Kubernetes client not initialized")
	}

	endpointSlices, err := c.kubeClient.DiscoveryV1().EndpointSlices(CoreDNSNamespace).
		List(context.Background(), metav1.ListOptions{LabelSelector: v1.LabelServiceName + "=" + CoreDNSServiceName})
	if err != nil {
		return nil, fmt.Errorf("failed to list CoreDNS EndpointSlices: %w", err)
	}

	ips := make(map[string]struct{})
	for _, es := range endpointSlices.Items {
		for _, ep := range es.Endpoints {
			for _, addr := range ep.Addresses {
				ips[addr] = struct{}{}
			}
		}
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no running CoreDNS pods found")
	}

	return ips, nil
}

// checkDNS performs the DNS query.
func (c *DNSChecker) checkDNS(ip string) (time.Duration, error) {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: c.Timeout}
			return d.DialContext(ctx, network, net.JoinHostPort(ip, "53"))
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	start := time.Now()
	_, err := resolver.LookupHost(ctx, c.Domain)
	return time.Since(start), err
}
