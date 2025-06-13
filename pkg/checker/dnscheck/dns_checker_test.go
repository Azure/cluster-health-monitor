package dnscheck

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Azure/cluster-health-monitor/pkg/config"
)

func TestBuildDNSChecker(t *testing.T) {
	for _, tc := range []struct {
		name        string
		checkerName string
		dnsConfig   *config.DNSConfig
		validateRes func(g *WithT, checker *DNSChecker, err error)
	}{
		{
			name:        "Valid config",
			checkerName: "test-dns-checker",
			dnsConfig: &config.DNSConfig{
				Domain: "example.com",
			},
			validateRes: func(g *WithT, checker *DNSChecker, err error) {
				g.Expect(checker).To(Equal(
					&DNSChecker{
						name: "test-dns-checker",
						config: &config.DNSConfig{
							Domain: "example.com",
						},
					}))
				g.Expect(err).NotTo(HaveOccurred())
			},
		},
		{
			name:        "Empty Checker Name",
			checkerName: "",
			dnsConfig: &config.DNSConfig{
				Domain: "example.com",
			},
			validateRes: func(g *WithT, checker *DNSChecker, err error) {
				g.Expect(checker).To(BeNil())
				g.Expect(err).To(HaveOccurred())
			},
		},
		{
			name:        "Missing DNSConfig",
			checkerName: "test-dns-checker",
			dnsConfig:   nil,
			validateRes: func(g *WithT, checker *DNSChecker, err error) {
				g.Expect(checker).To(BeNil())
				g.Expect(err).To(HaveOccurred())
			},
		},
		{
			name:        "Empty Domain",
			checkerName: "test-dns-checker",
			dnsConfig: &config.DNSConfig{
				Domain: "",
			},
			validateRes: func(g *WithT, checker *DNSChecker, err error) {
				g.Expect(checker).To(BeNil())
				g.Expect(err).To(HaveOccurred())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			checker, err := BuildDNSChecker(tc.checkerName, tc.dnsConfig)
			tc.validateRes(g, checker, err)
		})
	}
}

func TestGetCoreDNSServiceIP(t *testing.T) {
	for _, tc := range []struct {
		name           string
		setupClientset func() *fake.Clientset
		validateTarget func(*WithT, DNSTarget, error)
	}{
		{
			name: "Success",
			setupClientset: func() *fake.Clientset {
				return fake.NewSimpleClientset(&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kube-dns",
						Namespace: "kube-system",
					},
					Spec: corev1.ServiceSpec{
						ClusterIP: "10.96.0.10",
					},
				})
			},
			validateTarget: func(g *WithT, target DNSTarget, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(target).To(Equal(DNSTarget{IP: "10.96.0.10", Type: CoreDNSService}))
			},
		},
		{
			name: "Error when service does not exist",
			setupClientset: func() *fake.Clientset {
				return fake.NewSimpleClientset()
			},
			validateTarget: func(g *WithT, target DNSTarget, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(target).To(Equal(DNSTarget{}))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			target, err := getCoreDNSServiceIP(context.Background(), tc.setupClientset())
			tc.validateTarget(g, target, err)
		})
	}
}

func TestGetCoreDNSPodIPs(t *testing.T) {
	for _, tc := range []struct {
		name            string
		setupClientset  func() *fake.Clientset
		validateTargets func(*WithT, []DNSTarget, error)
	}{
		{
			name: "Success with ready endpoints",
			setupClientset: func() *fake.Clientset {
				ready := true
				return fake.NewSimpleClientset(
					&discoveryv1.EndpointSlice{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "kube-dns-12345",
							Namespace: "kube-system",
							Labels: map[string]string{
								discoveryv1.LabelServiceName: "kube-dns",
							},
						},
						Endpoints: []discoveryv1.Endpoint{
							{
								Addresses:  []string{"10.244.0.2", "10.244.0.3"},
								Conditions: discoveryv1.EndpointConditions{Ready: &ready},
							},
						},
					},
				)
			},
			validateTargets: func(g *WithT, targets []DNSTarget, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(targets).To(ConsistOf(
					DNSTarget{IP: "10.244.0.2", Type: CoreDNSPod},
					DNSTarget{IP: "10.244.0.3", Type: CoreDNSPod},
				))
			},
		},
		{
			name: "Success with nil ready condition endpoints",
			setupClientset: func() *fake.Clientset {
				return fake.NewSimpleClientset(
					&discoveryv1.EndpointSlice{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "kube-dns-12345",
							Namespace: "kube-system",
							Labels: map[string]string{
								discoveryv1.LabelServiceName: "kube-dns",
							},
						},
						Endpoints: []discoveryv1.Endpoint{
							{
								Addresses:  []string{"10.244.0.2", "10.244.0.3"},
								Conditions: discoveryv1.EndpointConditions{Ready: nil},
							},
						},
					},
				)
			},
			validateTargets: func(g *WithT, targets []DNSTarget, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(targets).To(ConsistOf(
					DNSTarget{IP: "10.244.0.2", Type: CoreDNSPod},
					DNSTarget{IP: "10.244.0.3", Type: CoreDNSPod},
				))
			},
		},
		{
			name: "Error when endpoints are not ready",
			setupClientset: func() *fake.Clientset {
				ready := false
				return fake.NewSimpleClientset(
					&discoveryv1.EndpointSlice{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "kube-dns-12345",
							Namespace: "kube-system",
							Labels: map[string]string{
								discoveryv1.LabelServiceName: "kube-dns",
							},
						},
						Endpoints: []discoveryv1.Endpoint{
							{
								Addresses:  []string{"10.244.0.2", "10.244.0.3"},
								Conditions: discoveryv1.EndpointConditions{Ready: &ready},
							},
						},
					},
				)
			},
			validateTargets: func(g *WithT, targets []DNSTarget, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(targets).To(BeNil())
			},
		},
		{
			name: "Error when endpoints do not exist",
			setupClientset: func() *fake.Clientset {
				return fake.NewSimpleClientset()
			},
			validateTargets: func(g *WithT, targets []DNSTarget, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(targets).To(BeNil())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			targets, err := getCoreDNSPodIPs(context.Background(), tc.setupClientset())
			tc.validateTargets(g, targets, err)
		})
	}
}
