package dnscheck

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

type fakeResolver struct {
	lookupHostFunc func(ctx context.Context, ip, domain string, queryTimeout time.Duration) ([]string, error)
}

func (f *fakeResolver) lookupHost(ctx context.Context, ip, domain string, queryTimeout time.Duration) ([]string, error) {
	return f.lookupHostFunc(ctx, ip, domain, queryTimeout)
}

func TestDNSChecker_Run(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name          string
		client        *k8sfake.Clientset
		mockResolver  resolver
		checkLocalDNS bool
		validateRes   func(g *WithT, res *types.Result, err error)
	}{
		{
			name: "CoreDNS Healthy",
			client: k8sfake.NewClientset(
				makeCoreDNSService("10.0.0.10"),
				makeCoreDNSEndpointSlice([]string{"10.0.0.11", "10.0.0.12"}),
			),
			mockResolver: &fakeResolver{
				lookupHostFunc: func(ctx context.Context, ip, domain string, queryTimeout time.Duration) ([]string, error) {
					return []string{"1.2.3.4"}, nil
				},
			},
			validateRes: func(g *WithT, res *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(res.Status).To(Equal(types.StatusHealthy))
			},
		},
		{
			name:   "CoreDNS Service Not Ready",
			client: k8sfake.NewClientset(), // No service.
			mockResolver: &fakeResolver{
				lookupHostFunc: func(ctx context.Context, ip, domain string, queryTimeout time.Duration) ([]string, error) {
					return []string{"1.2.3.4"}, nil
				},
			},
			validateRes: func(g *WithT, res *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(res.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(res.Detail.Code).To(Equal(errCodeServiceNotReady))
			},
		},
		{
			name: "CoreDNS Pods Not Ready",
			client: k8sfake.NewClientset(
				makeCoreDNSService("10.0.0.10"),
			),
			mockResolver: &fakeResolver{
				lookupHostFunc: func(ctx context.Context, ip, domain string, queryTimeout time.Duration) ([]string, error) {
					return []string{"1.2.3.4"}, nil
				},
			},
			validateRes: func(g *WithT, res *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(res.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(res.Detail.Code).To(Equal(errCodePodsNotReady))
			},
		},
		{
			name: "CoreDNS Service Timeout",
			client: k8sfake.NewClientset(
				makeCoreDNSService("10.0.0.10"),
				makeCoreDNSEndpointSlice([]string{"10.0.0.11"}),
			),
			mockResolver: &fakeResolver{
				lookupHostFunc: func(ctx context.Context, ip, domain string, queryTimeout time.Duration) ([]string, error) {
					return nil, context.DeadlineExceeded
				},
			},
			validateRes: func(g *WithT, res *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(res.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(res.Detail.Code).To(Equal(errCodeServiceTimeout))
			},
		},
		{
			name:   "LocalDNS Healthy",
			client: k8sfake.NewClientset(),
			mockResolver: &fakeResolver{
				lookupHostFunc: func(ctx context.Context, ip, domain string, queryTimeout time.Duration) ([]string, error) {
					if ip != "169.254.10.11" {
						return nil, fmt.Errorf("unexpected IP: %s", ip)
					}
					return []string{"1.2.3.4"}, nil
				},
			},
			checkLocalDNS: true,
			validateRes: func(g *WithT, res *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(res.Status).To(Equal(types.StatusHealthy))
			},
		},
		{
			name:   "LocalDNS Error",
			client: k8sfake.NewClientset(),
			mockResolver: &fakeResolver{
				lookupHostFunc: func(ctx context.Context, ip, domain string, queryTimeout time.Duration) ([]string, error) {
					if ip != "169.254.10.11" {
						return []string{"1.2.3.4"}, nil
					}
					return nil, fmt.Errorf("local dns error")
				},
			},
			checkLocalDNS: true,
			validateRes: func(g *WithT, res *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(res.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(res.Detail.Code).To(Equal(errCodeLocalDNSError))
			},
		},
		{
			name:   "LocalDNS Timeout",
			client: k8sfake.NewClientset(),
			mockResolver: &fakeResolver{
				lookupHostFunc: func(ctx context.Context, ip, domain string, queryTimeout time.Duration) ([]string, error) {
					if ip != "169.254.10.11" {
						return []string{"1.2.3.4"}, nil
					}
					return nil, context.DeadlineExceeded
				},
			},
			checkLocalDNS: true,
			validateRes: func(g *WithT, res *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(res.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(res.Detail.Code).To(Equal(errCodeLocalDNSTimeout))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			chk := &DNSChecker{
				name: "dns-test",
				config: &config.DNSConfig{
					Domain:        "example.com",
					CheckLocalDNS: tc.checkLocalDNS,
					QueryTimeout:  2 * time.Second,
				},
				kubeClient:   tc.client,
				resolver:     tc.mockResolver,
				queryTimeout: 2 * time.Second,
			}

			res, err := chk.Run(context.Background())
			tc.validateRes(g, res, err)
		})
	}
}

func TestDNSChecker_QueryTimeout(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	var capturedTimeout time.Duration
	mockResolver := &fakeResolver{
		lookupHostFunc: func(ctx context.Context, ip, domain string, queryTimeout time.Duration) ([]string, error) {
			capturedTimeout = queryTimeout
			return []string{"1.2.3.4"}, nil
		},
	}

	chk := &DNSChecker{
		name: "dns-test",
		config: &config.DNSConfig{
			Domain:       "example.com",
			QueryTimeout: 5 * time.Second,
		},
		kubeClient:   k8sfake.NewClientset(makeCoreDNSService("10.0.0.10"), makeCoreDNSEndpointSlice([]string{"10.0.0.11"})),
		resolver:     mockResolver,
		queryTimeout: 5 * time.Second,
	}

	_, err := chk.Run(context.Background())
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(capturedTimeout).To(Equal(5 * time.Second))
}

// --- helpers ---

func makeCoreDNSService(ip string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: coreDNSNamespace,
			Name:      coreDNSServiceName,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: ip,
		},
	}
}

func makeCoreDNSEndpointSlice(ips []string) *discoveryv1.EndpointSlice {
	endpoints := []discoveryv1.Endpoint{}
	for _, ip := range ips {
		ready := true
		endpoints = append(endpoints, discoveryv1.Endpoint{
			Addresses: []string{ip},
			Conditions: discoveryv1.EndpointConditions{
				Ready: &ready,
			},
		})
	}
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: coreDNSNamespace,
			Labels: map[string]string{
				discoveryv1.LabelServiceName: coreDNSServiceName,
			},
		},
		Endpoints: endpoints,
	}
}
