package podnetwork

import (
	"context"
	"testing"
	"time"
)

func TestSimpleDNSPinger_Ping(t *testing.T) {
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
			dnsIP:        "8.8.8.8", // Will be replaced with actual mock server port
			domain:       "example.com",
			queryTimeout: time.Second * 2,
			expectError:  false,
		},
		{
			name:         "successful ping with no existing domain",
			dnsIP:        "8.8.8.8", // Will be replaced with actual mock server port
			domain:       "notexist.example",
			queryTimeout: time.Second * 2,
			expectError:  false,
		},
		{
			name:         "invalid DNS server address",
			dnsIP:        "8.8.8.8:54", // wrong port
			domain:       "example.com",
			queryTimeout: time.Second * 2,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
			defer cancel()

			dnsIP := tt.dnsIP
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
