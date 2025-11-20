package dns

import (
	"context"
	"net"
	"time"
)

// resolver is an interface for DNS resolution.
type Resolver interface {
	LookupHost(ctx context.Context, dnsIP, domain string, queryTimeout time.Duration) ([]string, error)
}

// DefaultResolver implements the Resolver interface using net.Resolver.
type DefaultResolver struct {
}

func NewResolver() Resolver {
	return &DefaultResolver{}
}

func (r *DefaultResolver) LookupHost(ctx context.Context, dnsIP, domain string, queryTimeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, network, net.JoinHostPort(dnsIP, "53"))
		},
	}
	return resolver.LookupHost(ctx, domain)
}
