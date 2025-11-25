package podnetwork

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/miekg/dns"
)

// dnsPinger is an interface for DNS ping functionality.
// It allows for easier testing/mocking.
type dnsPinger interface {
	// ping pings a DNS server by sending a query and waiting for any response
	// It doesn't care about the actual DNS response content, only that a packet is received
	ping(ctx context.Context, dnsSvcIP, domain string, queryTimeout time.Duration) error
}

// simpleDNSPinger implements the dnsPinger interface using miekg/dns.
type simpleDNSPinger struct {
}

func newDNSPinger() dnsPinger {
	return &simpleDNSPinger{}
}

// ping uses the miekg/dns library for cleaner DNS packet handling
func (p *simpleDNSPinger) ping(ctx context.Context, dnsSvcIP, domain string, queryTimeout time.Duration) error {
	// Create DNS query: type=A for the specified domain, result doesn't matter
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), dns.TypeA)

	// Create DNS client with timeout
	c := new(dns.Client)
	c.Net = "udp" // Explicitly use UDP to prevent any fallback behavior
	c.Timeout = queryTimeout

	// Ensure we use the correct address format
	dnsAddr := dnsSvcIP
	if !addressHasPort(dnsSvcIP) {
		dnsAddr = net.JoinHostPort(dnsSvcIP, "53")
	}

	// Send query
	resp, _, err := c.ExchangeContext(ctx, m, dnsAddr)
	if err != nil {
		return fmt.Errorf("no DNS response: %w", err)
	}

	if resp == nil {
		return fmt.Errorf("no DNS response received (nil response)")
	}

	// ANY DNS response is considered success — NXDOMAIN, SERVFAIL, whatever
	return nil
}

// addressHasPort checks if the address already includes a port
func addressHasPort(addr string) bool {
	_, _, err := net.SplitHostPort(addr)
	return err == nil
}
