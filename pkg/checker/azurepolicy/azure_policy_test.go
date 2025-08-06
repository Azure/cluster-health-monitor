package azurepolicy

import (
	"context"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/types"
	. "github.com/onsi/gomega"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestAzurePolicyChecker_Run(t *testing.T) {
	checkerName := "test-azure-policy-checker"

	tests := []struct {
		name           string
		client         *k8sfake.Clientset
		validateResult func(g *WithT, result *types.Result, err error)
	}{
		{
			name: "TODO: implement test cases for Azure Policy checker",
			client: func() *k8sfake.Clientset {
				return k8sfake.NewSimpleClientset()
			}(),
			validateResult: func(g *WithT, result *types.Result, err error) {
				// TODO: Implement proper test validation
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusUnhealthy))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			azurePolicyChecker := &AzurePolicyChecker{
				name:    checkerName,
				timeout: 5 * time.Second,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			result, err := azurePolicyChecker.Run(ctx)
			tt.validateResult(g, result, err)
		})
	}
}

func TestAzurePolicyChecker_createTestPod(t *testing.T) {
	g := NewWithT(t)

	checker := &AzurePolicyChecker{
		name:    "azure-policy",
		timeout: 5 * time.Second,
	}

	pod := checker.createTestPod()
	g.Expect(pod).ToNot(BeNil())

	// Has expected prefix
	g.Expect(pod.ObjectMeta.Name).To(HavePrefix("azure-policy-test-pod-"))

	// Namespace should be default
	g.Expect(pod.ObjectMeta.Namespace).To(Equal("default"))

	// Pod does not have readiness or liveness probes so it can trigger policy violations
	g.Expect(pod.Spec.Containers).To(HaveLen(1))
	g.Expect(pod.Spec.Containers[0].ReadinessProbe).To(BeNil())
	g.Expect(pod.Spec.Containers[0].LivenessProbe).To(BeNil())

	// Image should be sourced from MCR
	g.Expect(pod.Spec.Containers[0].Image).To(HavePrefix("mcr.microsoft.com/"))
}

func TestAzurePolicyChecker_hasAzurePolicyViolation(t *testing.T) {
	checker := &AzurePolicyChecker{}

	tests := []struct {
		name        string
		message     string
		validateRes func(g *WithT, result bool)
	}{
		{
			name:    "Azure Policy violation - realistic warning",
			message: "Warning: [azurepolicy-k8sazurev2containerenforceprob-74321cbd58a88a12c510] Container <synthetic> in your Pod <test-pod> has no <livenessProbe>. Required probes: [\"readinessProbe\", \"livenessProbe\"]",
			validateRes: func(g *WithT, result bool) {
				g.Expect(result).To(BeTrue())
			},
		},
		{
			name:    "Azure Policy violation - realistic error",
			message: "Error from server (Forbidden): admission webhook \"validation.gatekeeper.sh\" denied the request: [azurepolicy-k8sazurev2containerenforceprob-39c2336da6b53f16b908] Container <synthetic> in your Pod <test-pod> has no <livenessProbe>",
			validateRes: func(g *WithT, result bool) {
				g.Expect(result).To(BeTrue())
			},
		},
		{
			name:    "empty message",
			message: "",
			validateRes: func(g *WithT, result bool) {
				g.Expect(result).To(BeFalse())
			},
		},
		{
			name:    "unrelated message",
			message: "some unrelated message",
			validateRes: func(g *WithT, result bool) {
				g.Expect(result).To(BeFalse())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			result := checker.hasAzurePolicyViolation(tt.message)
			tt.validateRes(g, result)
		})
	}
}
