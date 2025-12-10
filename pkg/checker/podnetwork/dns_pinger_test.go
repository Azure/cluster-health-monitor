package podnetwork

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestSimpleDNSPinger_Ping(t *testing.T) {
	// Start local DNS server for testing
	dnsAddr, cleanup := startTestDNSServer(t)
	defer cleanup()

	pinger := newDNSPinger()

	tests := []struct {
		name         string
		dnsIP        string
		domain       string
		queryTimeout time.Duration
		expectError  bool
	}{
		{
			name:         "successful ping with existing domain",
			dnsIP:        dnsAddr,
			domain:       "example.com",
			queryTimeout: time.Second * 2,
			expectError:  false,
		},
		{
			name:         "successful ping with non-existent domain",
			dnsIP:        dnsAddr,
			domain:       "notexist.example",
			queryTimeout: time.Second * 2,
			expectError:  false,
		},
		{
			name:         "invalid DNS server address",
			dnsIP:        "127.0.0.1:9999", // Assumption is that nothing is listening here. Unit test can fail if something does.
			domain:       "example.com",
			queryTimeout: time.Millisecond * 100,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := pinger.ping(ctx, tt.dnsIP, tt.domain, tt.queryTimeout)

			if tt.expectError && err == nil {
				t.Errorf("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}
		})
	}
}

// startTestDNSServer starts a local DNS server for testing on a random port.
// Returns the server address (e.g., "127.0.0.1:12345") and a cleanup function.
func startTestDNSServer(t *testing.T) (string, func()) {
	t.Helper()

	// Listen on random available port
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start DNS server: %v", err)
	}

	// Simple DNS handler that responds to all queries
	dns.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		// Always returns the server IP and fixed TTL for any dns query.
		if len(req.Question) > 0 {
			rr, _ := dns.NewRR(fmt.Sprintf("%s 300 IN A 127.0.0.1", req.Question[0].Name))
			if rr != nil {
				resp.Answer = append(resp.Answer, rr)
			}
		}
		w.WriteMsg(resp)
	})

	server := &dns.Server{PacketConn: pc}

	// Start server in background
	go server.ActivateAndServe()

	// Give server time to start
	time.Sleep(50 * time.Millisecond)

	cleanup := func() {
		server.Shutdown()
	}

	return pc.LocalAddr().String(), cleanup
}
