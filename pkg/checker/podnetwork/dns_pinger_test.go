package podnetwork

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestSimpleDNSPinger_Ping(t *testing.T) {
	pinger := newDNSPinger()

	tests := []struct {
		name           string
		dnsIP          string
		domain         string
		queryTimeout   time.Duration
		expectError    bool
		setupMockDNS   bool
		mockDNSHandler func(w dns.ResponseWriter, r *dns.Msg)
	}{
		{
			name:         "successful ping to mock DNS server",
			dnsIP:        "127.0.0.1:0", // Will be replaced with actual mock server port
			domain:       "example.com",
			queryTimeout: time.Second * 2,
			expectError:  false,
			setupMockDNS: true,
			mockDNSHandler: func(w dns.ResponseWriter, r *dns.Msg) {
				// Return a successful DNS response
				m := new(dns.Msg)
				m.SetReply(r)
				m.Answer = append(m.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
					A:   net.ParseIP("1.2.3.4"),
				})
				w.WriteMsg(m)
			},
		},
		{
			name:         "successful ping with NXDOMAIN response",
			dnsIP:        "127.0.0.1:0", // Will be replaced with actual mock server port
			domain:       "nonexistent.example",
			queryTimeout: time.Second * 2,
			expectError:  false, // NXDOMAIN is still a valid response
			setupMockDNS: true,
			mockDNSHandler: func(w dns.ResponseWriter, r *dns.Msg) {
				// Return NXDOMAIN response
				m := new(dns.Msg)
				m.SetReply(r)
				m.Rcode = dns.RcodeNameError
				w.WriteMsg(m)
			},
		},
		{
			name:         "successful ping with SERVFAIL response",
			dnsIP:        "127.0.0.1:0", // Will be replaced with actual mock server port
			domain:       "example.com",
			queryTimeout: time.Second * 2,
			expectError:  false, // SERVFAIL is still a valid response
			setupMockDNS: true,
			mockDNSHandler: func(w dns.ResponseWriter, r *dns.Msg) {
				// Return SERVFAIL response
				m := new(dns.Msg)
				m.SetReply(r)
				m.Rcode = dns.RcodeServerFailure
				w.WriteMsg(m)
			},
		},
		{
			name:         "successful ping to mock DNS server",
			dnsIP:        "127.0.0.1:0", // Will be replaced with actual mock server port
			domain:       "example.com",
			queryTimeout: time.Second * 2,
			expectError:  false,
			setupMockDNS: true,
			mockDNSHandler: func(w dns.ResponseWriter, r *dns.Msg) {
				// Return a successful DNS response
				m := new(dns.Msg)
				m.SetReply(r)
				m.Answer = append(m.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
					A:   net.ParseIP("1.2.3.4"),
				})
				w.WriteMsg(m)
			},
		},
		{
			name:         "invalid DNS server address",
			dnsIP:        "invalid-address",
			domain:       "example.com",
			queryTimeout: time.Second * 2,
			expectError:  true,
			setupMockDNS: false,
		},
		{
			name:         "successful ping with explicit port",
			dnsIP:        "127.0.0.1:0", // Will be replaced with actual mock server port
			domain:       "example.com",
			queryTimeout: time.Second * 2,
			expectError:  false,
			setupMockDNS: true,
			mockDNSHandler: func(w dns.ResponseWriter, r *dns.Msg) {
				m := new(dns.Msg)
				m.SetReply(r)
				w.WriteMsg(m)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
			defer cancel()

			dnsIP := tt.dnsIP

			// Setup mock DNS server if needed
			if tt.setupMockDNS {
				// Create a single shared handler for all tests in this run
				mux := dns.NewServeMux()
				mux.HandleFunc(".", tt.mockDNSHandler)

				server := &dns.Server{
					Addr:    "127.0.0.1:0",
					Net:     "udp",
					Handler: mux,
				}

				// Start server
				go func() {
					if err := server.ListenAndServe(); err != nil {
						t.Logf("DNS server error: %v", err)
					}
				}()
				defer server.Shutdown()

				// Wait for server to start
				time.Sleep(time.Millisecond * 100)

				// Get the actual listening address
				if server.PacketConn != nil {
					addr := server.PacketConn.LocalAddr().String()
					dnsIP = addr
				} else {
					t.Skip("Unable to start mock DNS server")
				}
			}

			// Execute the ping
			err := pinger.ping(ctx, dnsIP, tt.domain, tt.queryTimeout)

			// Check result
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}
		})
	}
}
