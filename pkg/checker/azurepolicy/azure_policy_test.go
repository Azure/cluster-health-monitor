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
				name:       checkerName,
				timeout:    5 * time.Second,
				kubeClient: tt.client,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			result, err := azurePolicyChecker.Run(ctx)
			tt.validateResult(g, result, err)
		})
	}
}
