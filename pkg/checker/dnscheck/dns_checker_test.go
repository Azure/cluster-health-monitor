package dnscheck

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestBuildDNSChecker(t *testing.T) {
	tests := []struct {
		name           string
		checkerName    string
		spec           map[string]any
		expectErr      bool
		errContains    string
		expectName     string
		expectDomain   string
		expectInterval time.Duration
		expectTimeout  time.Duration
	}{
		{
			name:        "valid spec",
			checkerName: "test-checker",
			spec: map[string]any{
				"domain":   "kubernetes.default.svc",
				"timeout":  2 * time.Second,
				"interval": 5 * time.Second,
			},
			expectErr:      false,
			expectName:     "test-checker",
			expectDomain:   "kubernetes.default.svc",
			expectInterval: 5 * time.Second,
			expectTimeout:  2 * time.Second,
		},
		{
			name:        "empty name",
			checkerName: "",
			spec: map[string]any{
				"domain": "kubernetes.default.svc",
			},
			expectErr:   true,
			errContains: "name is required",
		},
		{
			name:        "empty domain",
			checkerName: "test-checker",
			spec: map[string]any{
				"domain": "",
			},
			expectErr:   true,
			errContains: "domain is required",
		},
		{
			name:        "default values",
			checkerName: "test-checker",
			spec: map[string]any{
				"domain": "kubernetes.default.svc",
			},
			expectErr:      false,
			expectName:     "test-checker",
			expectDomain:   "kubernetes.default.svc",
			expectInterval: DefaultInterval,
			expectTimeout:  DefaultTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildDNSChecker(tt.checkerName, tt.spec)

			if tt.expectErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error containing %q, got %q", tt.errContains, err.Error())
				}
				if got != nil {
					t.Errorf("Expected nil checker, got %v", got)
				}
				return
			}

			if got == nil {
				t.Error("Expected non-nil checker, got nil")
				return
			}

			checkFields := []struct {
				name     string
				got      interface{}
				expected interface{}
			}{
				{"name", got.name, tt.expectName},
				{"domain", got.Domain, tt.expectDomain},
				{"interval", got.Interval, tt.expectInterval},
				{"timeout", got.Timeout, tt.expectTimeout},
			}

			for _, cf := range checkFields {
				if cf.got != cf.expected {
					t.Errorf("Expected %s %v, got %v", cf.name, cf.expected, cf.got)
				}
			}
		})
	}
}

func TestDNSChecker_GetTargets(t *testing.T) {
	tests := []struct {
		name             string
		service          *corev1.Service
		endpointIPs      []string
		expectServiceIP  string
		expectServiceErr bool
		expectPodIPs     map[string]struct{}
		expectPodErr     bool
	}{
		{
			name:             "Both service and endpoints exist",
			service:          createCoreDNSService("10.0.0.10"),
			endpointIPs:      []string{"10.0.0.11", "10.0.0.12"},
			expectServiceIP:  "10.0.0.10",
			expectServiceErr: false,
			expectPodIPs:     map[string]struct{}{"10.0.0.11": {}, "10.0.0.12": {}},
			expectPodErr:     false,
		},
		{
			name:             "Service exists but no endpoints",
			service:          createCoreDNSService("10.0.0.10"),
			endpointIPs:      nil,
			expectServiceIP:  "10.0.0.10",
			expectServiceErr: false,
			expectPodIPs:     nil,
			expectPodErr:     true,
		},
		{
			name:             "No service, but endpoints exist",
			service:          nil,
			endpointIPs:      []string{"10.0.0.11", "10.0.0.12"},
			expectServiceIP:  "",
			expectServiceErr: true,
			expectPodIPs:     map[string]struct{}{"10.0.0.11": {}, "10.0.0.12": {}},
			expectPodErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []runtime.Object
			if tt.service != nil {
				objects = append(objects, tt.service)
			}
			if tt.endpointIPs != nil && len(tt.endpointIPs) > 0 {
				objects = append(objects, createCoreDNSEndpointSlice(tt.endpointIPs))
			}
			clientset := fake.NewSimpleClientset(objects...)

			checker, err := BuildDNSCheckerWithClient(
				"test-dns",
				map[string]any{
					"domain": "kubernetes.default.svc",
				},
				clientset,
			)
			if err != nil {
				t.Fatalf("Failed to build checker: %v", err)
			}

			serviceIP, err := checker.getCoreDNSServiceIP()
			assertServiceResult(t, serviceIP, err, tt.expectServiceIP, tt.expectServiceErr)

			podIPs, err := checker.getCoreDNSPodIPs()
			assertPodResults(t, podIPs, err, tt.expectPodIPs, tt.expectPodErr)
		})
	}
}

func TestDNSChecker_RunWithContext(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		createCoreDNSService("10.0.0.10"),
		createCoreDNSEndpointSlice([]string{"10.0.0.11", "10.0.0.12"}),
	)

	tests := []struct {
		name        string
		dnsResponse map[string]struct {
			duration time.Duration
			err      error
		}
	}{
		{
			name: "all successful DNS checks",
			dnsResponse: map[string]struct {
				duration time.Duration
				err      error
			}{
				"10.0.0.10": {duration: 10 * time.Millisecond, err: nil},
				"10.0.0.11": {duration: 5 * time.Millisecond, err: nil},
				"10.0.0.12": {duration: 15 * time.Millisecond, err: nil},
			},
		},
		{
			name: "some DNS checks timeout",
			dnsResponse: map[string]struct {
				duration time.Duration
				err      error
			}{
				"10.0.0.10": {duration: 200 * time.Millisecond, err: context.DeadlineExceeded},
				"10.0.0.11": {duration: 5 * time.Millisecond, err: nil},
				"10.0.0.12": {duration: 15 * time.Millisecond, err: nil},
			},
		},
		{
			name: "some DNS checks error",
			dnsResponse: map[string]struct {
				duration time.Duration
				err      error
			}{
				"10.0.0.10": {duration: 10 * time.Millisecond, err: nil},
				"10.0.0.11": {duration: 0, err: errors.New("network error")},
				"10.0.0.12": {duration: 15 * time.Millisecond, err: nil},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			checker, err := BuildDNSCheckerWithClient(
				"test-dns",
				map[string]any{
					"domain":   "kubernetes.default.svc",
					"timeout":  DefaultTimeout,
					"interval": 10 * time.Millisecond,
				},
				clientset,
			)
			if err != nil {
				t.Fatalf("Failed to build DNSChecker: %v", err)
			}

			var mu sync.Mutex
			checkedIPs := make(map[string]struct{})
			mockResolver := func(ip string) (time.Duration, error) {
				mu.Lock()
				checkedIPs[ip] = struct{}{}
				mu.Unlock()

				resp, found := tt.dnsResponse[ip]
				if !found {
					return 0, errors.New("no mock response for IP: " + ip)
				}
				return resp.duration, resp.err
			}
			checker.dnsResolver = mockResolver

			// Run the checker - it should run until context timeout.
			err = checker.RunWithContext(ctx)

			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Expected context.DeadlineExceeded, got: %v", err)
			}

			expectedIPs := map[string]struct{}{"10.0.0.10": {}, "10.0.0.11": {}, "10.0.0.12": {}}
			if !maps.Equal(checkedIPs, expectedIPs) {
				t.Errorf("Checked IPs do not match expected: got %v, want %v", checkedIPs, expectedIPs)
			}
		})
	}
}

// createCoreDNSService creates a CoreDNS service with the specified IP.
func createCoreDNSService(ip string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      CoreDNSServiceName,
			Namespace: CoreDNSNamespace,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: ip,
		},
	}
}

// createCoreDNSEndpointSlice creates a CoreDNS EndpointSlice with the specified IPs.
func createCoreDNSEndpointSlice(ips []string) *v1.EndpointSlice {
	return &v1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      CoreDNSServiceName,
			Namespace: CoreDNSNamespace,
			Labels: map[string]string{
				v1.LabelServiceName: CoreDNSServiceName,
			},
		},
		AddressType: v1.AddressTypeIPv4,
		Endpoints: []v1.Endpoint{
			{
				Addresses: ips,
			},
		},
	}
}

// assertServiceResult checks the result of getting the CoreDNS service IP.
func assertServiceResult(t *testing.T, serviceIP string, err error, expectedIP string, expectError bool) {
	t.Helper()
	if expectError {
		if err == nil {
			t.Errorf("Expected error when getting CoreDNS service IP, got none")
		}
		if serviceIP != "" {
			t.Errorf("Expected empty service IP, got '%s'", serviceIP)
		}
		return
	}

	if err != nil {
		t.Errorf("Unexpected error when getting CoreDNS service IP: %v", err)
	}
	if serviceIP != expectedIP {
		t.Errorf("Expected service IP '%s', got '%s'", expectedIP, serviceIP)
	}
}

// assertPodResults checks the results of getting CoreDNS pod IPs.
func assertPodResults(t *testing.T, podIPs map[string]struct{}, err error, expectedIPs map[string]struct{}, expectError bool) {
	t.Helper()
	if expectError {
		if err == nil {
			t.Errorf("Expected error when getting CoreDNS pod IPs, got none")
		}
		if len(podIPs) != 0 {
			t.Errorf("Expected empty pod IPs, got %v", podIPs)
		}
		return
	}

	if err != nil {
		t.Errorf("Unexpected error when getting CoreDNS pod IPs: %v", err)
	}
	if len(podIPs) != len(expectedIPs) {
		t.Errorf("Expected %d pod IPs, got %d", len(expectedIPs), len(podIPs))
		return
	}

	if !maps.Equal(podIPs, expectedIPs) {
		t.Errorf("Pod IPs do not match expected: got %v, want %v", podIPs, expectedIPs)
		return
	}
}
