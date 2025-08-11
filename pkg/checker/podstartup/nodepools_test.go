package podstartup

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCreateKarpenterNodePool(t *testing.T) {
	g := NewWithT(t)

	tests := []struct {
		name                string
		getClient           func() *dynamicfake.FakeDynamicClient
		expectedErrorString string
	}{
		{
			name: "successful creation",
		},
		{
			name: "creation failure",
			getClient: func() *dynamicfake.FakeDynamicClient {
				client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
				client.PrependReactor("create", "nodepool", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, &unstructured.Unstructured{}, errors.New("unexpected error occurred while creating node pool")
				})
				return client
			},
			expectedErrorString: "unexpected error occurred while creating node pool",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			nodePoolName := "test-nodepool"
			timestampStr := "123456"

			fakeDynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			if tt.getClient != nil {
				fakeDynamicClient = tt.getClient()
			}

			checker := &PodStartupChecker{
				dynamicClient: fakeDynamicClient,
				config: &config.PodStartupConfig{
					SyntheticPodNamespace: "test",
				},
			}
			err := checker.createKarpenterNodePool(ctx, karpenterNodePool(nodePoolName, timestampStr))
			if tt.expectedErrorString != "" {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring(tt.expectedErrorString))
			} else {
				g.Expect(err).To(BeNil())
			}
		})
	}
}

func TestDeleteKarpenterNodePool(t *testing.T) {
	g := NewWithT(t)

	tests := []struct {
		name                string
		getClient           func() *dynamicfake.FakeDynamicClient
		expectedErrorString string
	}{
		{
			name: "successful deletion",
		},
		{
			name: "not found - skip without error",
			getClient: func() *dynamicfake.FakeDynamicClient {
				client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
				client.PrependReactor("delete", "nodepool", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, &unstructured.Unstructured{}, apierrors.NewNotFound(
						NodePoolGVR.GroupResource(),
						"test-nodepool",
					)
				})
				return client
			},
		},
		{
			name: "deletion failure",
			getClient: func() *dynamicfake.FakeDynamicClient {
				client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
				client.PrependReactor("delete", "nodepool", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, &unstructured.Unstructured{}, errors.New("unexpected error occurred while deleting node pool")
				})
				return client
			},
			expectedErrorString: "unexpected error occurred while deleting node pool",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			nodePoolName := "test-nodepool"

			fakeDynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			if tt.getClient != nil {
				fakeDynamicClient = tt.getClient()
			}

			checker := &PodStartupChecker{
				dynamicClient: fakeDynamicClient,
				config: &config.PodStartupConfig{
					SyntheticPodNamespace: "test",
				},
			}
			err := checker.deleteKarpenterNodePool(ctx, nodePoolName)
			if tt.expectedErrorString != "" {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring(tt.expectedErrorString))
			} else {
				g.Expect(err).To(BeNil())
			}
		})
	}
}
