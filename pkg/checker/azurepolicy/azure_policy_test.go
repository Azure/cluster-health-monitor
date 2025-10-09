package azurepolicy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// mockWarningCapture implements WarningCapture for testing
type mockWarningCapture struct {
	warnings []string
}

func (m *mockWarningCapture) GetWarnings() []string {
	return m.warnings
}

// mockClientFactory implements ClientFactory for testing
type mockClientFactory struct {
	client         kubernetes.Interface
	warningCapture WarningCapture
	err            error
}

func (m *mockClientFactory) CreateClientWithWarningCapture(restConfig *rest.Config) (kubernetes.Interface, WarningCapture, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return m.client, m.warningCapture, nil
}

func TestAzurePolicyChecker_check(t *testing.T) {
	checkerName := "test-azure-policy-checker"

	tests := []struct {
		name           string
		setupMocks     func() (*mockClientFactory, *k8sfake.Clientset, *mockWarningCapture)
		validateResult func(g *WithT, result *checker.Result, err error)
	}{
		{
			name: "healthy result - Azure Policy violation detected in error message",
			setupMocks: func() (*mockClientFactory, *k8sfake.Clientset, *mockWarningCapture) {
				client := k8sfake.NewClientset()
				client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					// Verify that the pod creation is a dry run
					createAction := action.(k8stesting.CreateActionImpl)
					if createAction.GetCreateOptions().DryRun[0] != metav1.DryRunAll {
						return true, nil, errors.New("Expected dry run but got actual pod creation")
					}
					return true, nil, errors.New("Error from server (Forbidden): admission webhook \"validation.gatekeeper.sh\" denied the request: [azurepolicy-k8sazurev1restrictedlabels-4a872f727137b85dcf39] Label <{\"kubernetes.azure.com\"}> is reserved for AKS use only")
				})
				warningCapture := &mockWarningCapture{warnings: []string{}}
				factory := &mockClientFactory{client: client, warningCapture: warningCapture}
				return factory, client, warningCapture
			},
			validateResult: func(g *WithT, result *checker.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusHealthy))
			},
		},
		{
			name: "healthy result - Azure Policy violation detected in warning",
			setupMocks: func() (*mockClientFactory, *k8sfake.Clientset, *mockWarningCapture) {
				client := k8sfake.NewClientset()
				client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, nil
				})
				warningCapture := &mockWarningCapture{
					warnings: []string{"Warning: [azurepolicy-k8sazurev1restrictedlabels-4a872f727137b85dcf39] Label <{\"kubernetes.azure.com\"}> is reserved for AKS use only"},
				}
				factory := &mockClientFactory{client: client, warningCapture: warningCapture}
				return factory, client, warningCapture
			},
			validateResult: func(g *WithT, result *checker.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusHealthy))
			},
		},
		{
			name: "unhealthy result - error and no Azure Policy violation detected",
			setupMocks: func() (*mockClientFactory, *k8sfake.Clientset, *mockWarningCapture) {
				client := k8sfake.NewClientset()
				client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("unrelated error")
				})
				warningCapture := &mockWarningCapture{warnings: []string{}}
				factory := &mockClientFactory{client: client, warningCapture: warningCapture}
				return factory, client, warningCapture
			},
			validateResult: func(g *WithT, result *checker.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodeAzurePolicyEnforcementMissing))
			},
		},
		{
			name: "unhealthy result - no error or warnings and no Azure Policy violation detected",
			setupMocks: func() (*mockClientFactory, *k8sfake.Clientset, *mockWarningCapture) {
				client := k8sfake.NewClientset()
				client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, nil
				})
				warningCapture := &mockWarningCapture{warnings: []string{}}
				factory := &mockClientFactory{client: client, warningCapture: warningCapture}
				return factory, client, warningCapture
			},
			validateResult: func(g *WithT, result *checker.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodeAzurePolicyEnforcementMissing))
			},
		},
		{
			name: "unhealthy result - pod creation times out",
			setupMocks: func() (*mockClientFactory, *k8sfake.Clientset, *mockWarningCapture) {
				client := k8sfake.NewClientset()
				client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, context.DeadlineExceeded
				})
				warningCapture := &mockWarningCapture{warnings: []string{}}
				factory := &mockClientFactory{client: client, warningCapture: warningCapture}
				return factory, client, warningCapture
			},
			validateResult: func(g *WithT, result *checker.Result, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("dry run request to create pod timed out"))
				g.Expect(result).To(BeNil())
			},
		},
		{
			name: "error creating client with warning capture",
			setupMocks: func() (*mockClientFactory, *k8sfake.Clientset, *mockWarningCapture) {
				factory := &mockClientFactory{err: errors.New("failed to create client")}
				return factory, nil, nil
			},
			validateResult: func(g *WithT, result *checker.Result, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to create Kubernetes client"))
				g.Expect(result).To(BeNil())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			factory, _, _ := tt.setupMocks()

			azurePolicyChecker := &AzurePolicyChecker{
				name:          checkerName,
				timeout:       1 * time.Second,
				restConfig:    &rest.Config{},
				clientFactory: factory,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			result, err := azurePolicyChecker.check(ctx)
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

	// Pod has AKS restricted label
	g.Expect(pod.ObjectMeta.Labels).To(HaveKey("kubernetes.azure.com"))
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
			message: "Warning: [azurepolicy-k8sazurev1restrictedlabels-4a872f727137b85dcf39] Label <{\"kubernetes.azure.com\"}> is reserved for AKS use only",
			validateRes: func(g *WithT, result bool) {
				g.Expect(result).To(BeTrue())
			},
		},
		{
			name:    "Azure Policy violation - realistic error",
			message: "Error from server (Forbidden): admission webhook \"validation.gatekeeper.sh\" denied the request: [azurepolicy-k8sazurev1restrictedlabels-4a872f727137b85dcf39] Label <{\"kubernetes.azure.com\"}> is reserved for AKS use only",
			validateRes: func(g *WithT, result bool) {
				g.Expect(result).To(BeTrue())
			},
		},
		{
			name:    "no violation - missing azurePolicyString",
			message: "Label <{\"kubernetes.azure.com\"}> is reserved for AKS use only",
			validateRes: func(g *WithT, result bool) {
				g.Expect(result).To(BeFalse())
			},
		},
		{
			name:    "no violation - empty message",
			message: "",
			validateRes: func(g *WithT, result bool) {
				g.Expect(result).To(BeFalse())
			},
		},
		{
			name:    "no violation - unrelated message",
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
