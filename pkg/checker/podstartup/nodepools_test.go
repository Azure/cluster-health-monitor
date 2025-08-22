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
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

const _testSyntheticLabelKey = "test-synthetic-label-key"

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
				client.PrependReactor("create", "nodepools", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
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
					SyntheticPodLabelKey:  _testSyntheticLabelKey,
				},
			}
			err := checker.createKarpenterNodePool(ctx, checker.karpenterNodePool(nodePoolName, timestampStr))
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
				client.PrependReactor("delete", "nodepools", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
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
				client.PrependReactor("delete", "nodepools", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
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

func TestDeleteAllKarpenterNodePools(t *testing.T) {
	g := NewWithT(t)

	tests := []struct {
		name            string
		mutateClient    func(client *dynamicfake.FakeDynamicClient)
		validateResults func(g *WithT, client *dynamicfake.FakeDynamicClient, err error)
	}{
		{
			name: "no node pools to delete",
			validateResults: func(g *WithT, client *dynamicfake.FakeDynamicClient, err error) {
				g.Expect(err).To(BeNil())
				g.Expect(client.Actions()).To(HaveLen(1))
			},
		},
		{
			name: "deletion success",
			mutateClient: func(client *dynamicfake.FakeDynamicClient) {
				client.PrependReactor("list", "nodepools", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, &unstructured.UnstructuredList{
						Items: []unstructured.Unstructured{
							{
								Object: map[string]interface{}{
									"apiVersion": "karpenter.sh/v1",
									"kind":       "NodePool",
									"metadata": map[string]interface{}{
										"name": "test-checker-nodepool-1",
										"labels": map[string]interface{}{
											_testSyntheticLabelKey: "123456",
										},
									},
								},
							},
						},
					}, nil
				})
				client.PrependReactor("delete", "nodepools", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, &unstructured.Unstructured{}, nil
				})
			},
			validateResults: func(g *WithT, client *dynamicfake.FakeDynamicClient, err error) {
				g.Expect(err).To(BeNil())
				g.Expect(client.Actions()).To(HaveLen(2))
			},
		},
		{
			name: "error of Karpenter NodePool with no name field in metadata",
			mutateClient: func(client *dynamicfake.FakeDynamicClient) {
				client.PrependReactor("list", "nodepools", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, &unstructured.UnstructuredList{
						Items: []unstructured.Unstructured{
							{
								Object: map[string]interface{}{
									"apiVersion": "karpenter.sh/v1",
									"kind":       "NodePool",
									"metadata": map[string]interface{}{
										"labels": map[string]interface{}{
											_testSyntheticLabelKey: "123456",
										},
									},
								},
							},
						},
					}, nil
				})
				client.PrependReactor("delete", "nodepools", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, &unstructured.Unstructured{}, nil
				})
			},
			validateResults: func(g *WithT, client *dynamicfake.FakeDynamicClient, err error) {
				g.Expect(client.Actions()).To(HaveLen(1))
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("got Karpenter NodePool with no name field in metadata"))
			},
		},
		{
			name: "deletion failure",
			mutateClient: func(client *dynamicfake.FakeDynamicClient) {
				client.PrependReactor("list", "nodepools", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, &unstructured.UnstructuredList{
						Items: []unstructured.Unstructured{
							{
								Object: map[string]interface{}{
									"apiVersion": "karpenter.sh/v1",
									"kind":       "NodePool",
									"metadata": map[string]interface{}{
										"name": "test-checker-nodepool-1",
										"labels": map[string]interface{}{
											_testSyntheticLabelKey: "123456",
										},
									},
								},
							},
						},
					}, nil
				})
				client.PrependReactor("delete", "nodepools", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, &unstructured.Unstructured{}, errors.New("unexpected error occurred while deleting node pool")
				})
			},
			validateResults: func(g *WithT, client *dynamicfake.FakeDynamicClient, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("unexpected error occurred while deleting node pool"))
				g.Expect(client.Actions()).To(HaveLen(2))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			fakeDynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				NodePoolGVR: "NodePoolList",
			})
			if tt.mutateClient != nil {
				tt.mutateClient(fakeDynamicClient)
			}

			checker := &PodStartupChecker{
				name:          "test-checker",
				dynamicClient: fakeDynamicClient,
				config: &config.PodStartupConfig{
					SyntheticPodNamespace: "test",
					SyntheticPodLabelKey:  _testSyntheticLabelKey,
				},
			}
			err := checker.deleteAllKarpenterNodePools(ctx)
			tt.validateResults(g, fakeDynamicClient, err)
		})
	}
}
