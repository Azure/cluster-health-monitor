package podstartup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	retry "github.com/avast/retry-go/v4"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// =============================================================================
// Test Helpers and Mocks
// =============================================================================

// Pod and Event creation helpers
func imageSuccessfullyPulledEvent(namespace, podName string, pullDuration time.Duration) *corev1.Event {
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "event1"},
		Message: fmt.Sprintf("Successfully pulled image \"k8s.gcr.io/pause:3.2\" in %s (%s including waiting). Image size: 299513 bytes.",
			pullDuration, pullDuration),
		Reason:         "Pulled",
		InvolvedObject: corev1.ObjectReference{Name: podName},
	}
}

func imageAlreadyPresentEvent(namespace, podName string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: namespace, Name: "event1"},
		Message:        "Container image \"k8s.gcr.io/pause:3.2\" already present on machine",
		Reason:         "Pulled",
		InvolvedObject: corev1.ObjectReference{Name: podName},
	}
}

// mockDialer is a mock implementation of the Dialer interface for testing
type mockDialer struct {
	dialFunc func(ctx context.Context, network, address string) (net.Conn, error)
}

func (m *mockDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return m.dialFunc(ctx, network, address)
}

// Dialer creation helpers
func successfulDialer() Dialer {
	return &mockDialer{
		dialFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, _ := net.Pipe()
			return conn, nil
		},
	}
}

func failingDialer(errMsg string) Dialer {
	return &mockDialer{
		dialFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
			return nil, fmt.Errorf(errMsg, address)
		},
	}
}

// =============================================================================
// Test Functions
// =============================================================================

func TestPodStartupChecker_check(t *testing.T) {
	// Defines adjustable parameters for the test scenarios
	type testScenario struct {
		podName                   string
		namespace                 string
		labels                    map[string]string
		podIP                     string
		startupDelay              time.Duration
		preExistingPods           []string
		preExistingPVCs           []string
		preExistingStorageClasses []string
		hasDeleteError            bool
		dialer                    Dialer
		enableNodeProvisioning    bool
		enabledCSITests           []config.CSIType
		hasCSICreateError         bool
		hasStorageClassGetError   bool
		fakeDynamicClient         *dynamicfake.FakeDynamicClient
	}

	// Mutator function type
	type scenarioMutator func(*testScenario)

	checkerName := "test"
	syntheticPodNamespace := "test-namespace"
	syntheticPodLabelKey := "cluster-health-monitor/checker-name"
	maxSyntheticPods := 3

	crdGVR := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

	tests := []struct {
		name           string
		mutators       []scenarioMutator
		validateResult func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient)
	}{
		{
			name:     "healthy result - no pre-existing synthetic pods",
			mutators: nil, // Use default scenario
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusHealthy))
				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(0)) // No dynamic client actions should be taken
			},
		},
		{
			name: "unhealthy result - pod startup took too long",
			mutators: []scenarioMutator{
				func(s *testScenario) { s.startupDelay = 10 * time.Second },
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodePodStartupDurationExceeded))
				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(0)) // No dynamic client actions should be taken
			},
		},
		{
			name: "error - max synthetic pods reached",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					for i := 0; i < maxSyntheticPods; i++ {
						s.preExistingPods = append(s.preExistingPods, fmt.Sprintf("pod%d", i))
					}
					s.hasDeleteError = true
				},
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("maximum number of synthetic pods reached"))
				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(0)) // No dynamic client actions should be taken
			},
		},
		{
			name: "error - max persistent volume claims reached",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					s.enabledCSITests = []config.CSIType{config.CSITypeAzureFile}
					s.preExistingStorageClasses = []string{azureFileStorageClassName}
					for i := 0; i < maxSyntheticPods; i++ {
						s.preExistingPVCs = append(s.preExistingPVCs, fmt.Sprintf("pvc%d", i))
					}
				},
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("CSI resource limit check failed"))
				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(0)) // No dynamic client actions should be taken
			},
		},
		{
			name: "error - fail to get pod IP",
			mutators: []scenarioMutator{
				func(s *testScenario) { s.podIP = "" },
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to get synthetic pod IP"))
				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(0)) // No dynamic client actions should be taken
			},
		},
		{
			name: "unhealthy result - TCP dialer error",
			mutators: []scenarioMutator{
				func(s *testScenario) { s.dialer = failingDialer("error") },
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodeRequestFailed))
				g.Expect(result.Detail.Message).To(ContainSubstring("TCP request to synthetic pod failed"))
				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(0)) // No dynamic client actions should be taken
			},
		},
		{
			name: "healthy result - transient TCP failures recovered by retry",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					calls := 0
					s.dialer = &mockDialer{dialFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
						calls++
						if calls < 2 {
							return nil, errors.New("temporary TCP error")
						}
						conn, _ := net.Pipe()
						return conn, nil
					}}
				},
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusHealthy))
				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(0))
			},
		},
		{
			name: "unhealthy result - TCP timeout only when all attempts timed out",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					s.dialer = &mockDialer{dialFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
						return nil, context.DeadlineExceeded
					}}
				},
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodeRequestTimeout))
				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(0))
			},
		},
		{
			name: "unhealthy result - mixed TCP errors reported as request failed",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					calls := 0
					s.dialer = &mockDialer{dialFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
						calls++
						if calls == 1 {
							return nil, context.DeadlineExceeded
						}
						return nil, errors.New("connection refused")
					}}
				},
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodeRequestFailed))
				g.Expect(result.Detail.Message).To(ContainSubstring("TCP request to synthetic pod failed"))
				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(0))
			},
		},
		{
			name: "healthy result - default scenario with node provisioning test",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					s.enableNodeProvisioning = true
					s.fakeDynamicClient.PrependReactor("get", "customresourcedefinitions", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
						return true, &unstructured.UnstructuredList{
							Items: []unstructured.Unstructured{
								{
									Object: map[string]interface{}{
										"apiVersion": "apiextensions.k8s.io/v1",
										"kind":       "CustomResourceDefinition",
										"metadata": map[string]interface{}{
											"name": "nodepools.karpenter.sh",
										},
									},
								},
							},
						}, nil
					})
				},
			},

			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusHealthy))

				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(4))
				g.Expect(fakeDynamicClient.Actions()[0].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[0].GetVerb()).To(Equal("list"))
				g.Expect(fakeDynamicClient.Actions()[1].GetResource()).To(Equal(crdGVR))
				g.Expect(fakeDynamicClient.Actions()[1].GetVerb()).To(Equal("get"))
				g.Expect(fakeDynamicClient.Actions()[2].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[2].GetVerb()).To(Equal("create"))
				g.Expect(fakeDynamicClient.Actions()[3].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[3].GetVerb()).To(Equal("delete"))
			},
		},
		{
			name: "healthy result - default scenario with node provisioning test",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					s.enableNodeProvisioning = true
					s.fakeDynamicClient.PrependReactor("get", "customresourcedefinitions", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
						return true, &unstructured.UnstructuredList{
							Items: []unstructured.Unstructured{
								{
									Object: map[string]interface{}{
										"apiVersion": "apiextensions.k8s.io/v1",
										"kind":       "CustomResourceDefinition",
										"metadata": map[string]interface{}{
											"name": "nodepools.karpenter.sh",
										},
									},
								},
							},
						}, nil
					})
				},
			},

			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusHealthy))

				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(4))
				g.Expect(fakeDynamicClient.Actions()[0].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[0].GetVerb()).To(Equal("list"))
				g.Expect(fakeDynamicClient.Actions()[1].GetResource()).To(Equal(crdGVR))
				g.Expect(fakeDynamicClient.Actions()[1].GetVerb()).To(Equal("get"))
				g.Expect(fakeDynamicClient.Actions()[2].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[2].GetVerb()).To(Equal("create"))
				g.Expect(fakeDynamicClient.Actions()[3].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[3].GetVerb()).To(Equal("delete"))
			},
		},
		{
			name: "healthy result - default scenario with node provisioning test and garbage collection",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					s.enableNodeProvisioning = true
					s.fakeDynamicClient.PrependReactor("get", "customresourcedefinitions", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
						return true, &unstructured.UnstructuredList{
							Items: []unstructured.Unstructured{
								{
									Object: map[string]interface{}{
										"apiVersion": "apiextensions.k8s.io/v1",
										"kind":       "CustomResourceDefinition",
										"metadata": map[string]interface{}{
											"name": "nodepools.karpenter.sh",
										},
									},
								},
							},
						}, nil
					})
					s.fakeDynamicClient.PrependReactor("list", "nodepools", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
						return true, &unstructured.UnstructuredList{
							Items: []unstructured.Unstructured{
								{
									Object: map[string]interface{}{
										"apiVersion": "karpenter.sh/v1",
										"kind":       "NodePool",
										"metadata": map[string]interface{}{
											"name": "test-node-pool",
											"labels": map[string]interface{}{
												syntheticPodLabelKey: "123456",
											},
										},
									},
								},
							},
						}, nil
					})
				},
			},

			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusHealthy))

				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(5))
				g.Expect(fakeDynamicClient.Actions()[0].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[0].GetVerb()).To(Equal("list"))
				g.Expect(fakeDynamicClient.Actions()[1].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[1].GetVerb()).To(Equal("delete"))
				g.Expect(fakeDynamicClient.Actions()[2].GetResource()).To(Equal(crdGVR))
				g.Expect(fakeDynamicClient.Actions()[2].GetVerb()).To(Equal("get"))
				g.Expect(fakeDynamicClient.Actions()[3].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[3].GetVerb()).To(Equal("create"))
				g.Expect(fakeDynamicClient.Actions()[4].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[4].GetVerb()).To(Equal("delete"))
			},
		},
		{
			name: "skipped result - node pool CRD not found",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					s.enableNodeProvisioning = true
					s.fakeDynamicClient.PrependReactor("get", "customresourcedefinitions", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
						return true, nil, apierrors.NewNotFound(
							schema.GroupResource{
								Group:    "apiextensions.k8s.io",
								Resource: "customresourcedefinitions",
							}, "nodepools.karpenter.sh")
					})
				},
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusSkipped))

				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(2))
				g.Expect(fakeDynamicClient.Actions()[0].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[0].GetVerb()).To(Equal("list"))
				g.Expect(fakeDynamicClient.Actions()[1].GetResource()).To(Equal(crdGVR))
				g.Expect(fakeDynamicClient.Actions()[1].GetVerb()).To(Equal("get"))
			},
		},
		{
			name: "error - failed to create node pool",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					s.enableNodeProvisioning = true
					s.fakeDynamicClient.PrependReactor("get", "customresourcedefinitions", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
						return true, &unstructured.UnstructuredList{
							Items: []unstructured.Unstructured{
								{
									Object: map[string]interface{}{
										"apiVersion": "apiextensions.k8s.io/v1",
										"kind":       "CustomResourceDefinition",
										"metadata": map[string]interface{}{
											"name": "nodepools.karpenter.sh",
										},
									},
								},
							},
						}, nil
					})
					s.fakeDynamicClient.PrependReactor("create", "nodepools", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
						return true, &unstructured.Unstructured{}, errors.New("unexpected error occurred while creating node pool")
					})
				},
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("unexpected error occurred while creating node pool"))

				g.Expect(fakeDynamicClient.Actions()).To(HaveLen(3))
				g.Expect(fakeDynamicClient.Actions()[0].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[0].GetVerb()).To(Equal("list"))
				g.Expect(fakeDynamicClient.Actions()[1].GetResource()).To(Equal(crdGVR))
				g.Expect(fakeDynamicClient.Actions()[1].GetVerb()).To(Equal("get"))
				g.Expect(fakeDynamicClient.Actions()[2].GetResource()).To(Equal(NodePoolGVR))
				g.Expect(fakeDynamicClient.Actions()[2].GetVerb()).To(Equal("create"))
			},
		},
		{
			name: "error - failed to create CSI resources",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					s.enabledCSITests = []config.CSIType{config.CSITypeAzureBlob}
					s.preExistingStorageClasses = []string{azureBlobStorageClassName}
					s.hasCSICreateError = true
				},
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to create CSI test resources"))
			},
		},
		{
			name: "unhealthy result - storage class not found",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					s.enabledCSITests = []config.CSIType{config.CSITypeAzureDisk}
					// No preExistingStorageClasses set, so the storage class won't exist
				},
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(checker.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodeStorageClassNotFound))
			},
		},
		{
			name: "error - non 404 error getting storage class",
			mutators: []scenarioMutator{
				func(s *testScenario) {
					s.enabledCSITests = []config.CSIType{config.CSITypeAzureDisk}
					s.hasStorageClassGetError = true
				},
			},
			validateResult: func(g *WithT, result *checker.Result, err error, fakeDynamicClient *dynamicfake.FakeDynamicClient) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to get StorageClass"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Initialize default healthy scenario
			scenario := &testScenario{
				podName:        "pod1",
				namespace:      syntheticPodNamespace,
				labels:         map[string]string{syntheticPodLabelKey: checkerName},
				podIP:          "10.0.0.0",
				startupDelay:   3 * time.Second,
				hasDeleteError: false,
				dialer:         successfulDialer(),
				fakeDynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
					NodePoolGVR: "NodePoolList",
				}),
			}

			// Apply mutators to modify the scenario
			for _, mutator := range tt.mutators {
				mutator(scenario)
			}

			// Build the test client and setup
			events := []runtime.Object{imageAlreadyPresentEvent(scenario.namespace, scenario.podName)}
			client := k8sfake.NewClientset(events...)

			// Add pre-existing pods if any
			podCreationTimestamp := time.Now()
			for _, podName := range scenario.preExistingPods {
				pod := podWithLabels(podName, scenario.namespace, scenario.labels, podCreationTimestamp)
				client.CoreV1().Pods(scenario.namespace).Create(context.Background(), pod, metav1.CreateOptions{}) //nolint:errcheck
			}

			for _, pvcName := range scenario.preExistingPVCs {
				pvc := pvcWithLabels(pvcName, scenario.namespace, scenario.labels, podCreationTimestamp)
				client.CoreV1().PersistentVolumeClaims(scenario.namespace).Create(context.Background(), pvc, metav1.CreateOptions{}) //nolint:errcheck
			}

			// Create the main test pod
			fakePod := podWithLabels(scenario.podName, scenario.namespace, scenario.labels, podCreationTimestamp)
			fakePod.Status = corev1.PodStatus{
				PodIP: scenario.podIP,
				ContainerStatuses: []corev1.ContainerStatus{{
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(podCreationTimestamp.Add(scenario.startupDelay))},
					},
				}},
			}

			// Add reactors
			client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				return true, fakePod, nil
			})
			client.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				return true, fakePod, nil
			})
			client.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if scenario.hasDeleteError {
					return true, nil, errors.New("error occurred")
				}
				return true, fakePod, nil
			})

			if scenario.hasCSICreateError {
				// Simulate error when creating any CSI resources
				client.PrependReactor("create", "persistentvolumeclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("error creating persistentvolumeclaims")
				})
			}

			for _, scName := range scenario.preExistingStorageClasses {
				sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: scName}}
				client.StorageV1().StorageClasses().Create(context.Background(), sc, metav1.CreateOptions{}) //nolint:errcheck
			}

			if scenario.hasStorageClassGetError {
				client.PrependReactor("get", "storageclasses", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("internal server error")
				})
			}

			podStartupChecker := &PodStartupChecker{
				name: checkerName,
				config: &config.PodStartupConfig{
					SyntheticPodNamespace:      syntheticPodNamespace,
					SyntheticPodLabelKey:       syntheticPodLabelKey,
					SyntheticPodStartupTimeout: 5 * time.Second,
					MaxSyntheticPods:           maxSyntheticPods,
					TCPTimeout:                 1 * time.Second,
					TCPMaxRetries:              3,
					TCPRetryInterval:           1 * time.Millisecond,
					EnableNodeProvisioningTest: scenario.enableNodeProvisioning,
					EnabledCSIs:                scenario.enabledCSITests,
				},
				timeout:       5 * time.Second,
				k8sClientset:  client,
				dialer:        scenario.dialer,
				dynamicClient: scenario.fakeDynamicClient,
			}

			ctx, cancel := context.WithTimeout(context.Background(), podStartupChecker.timeout)
			defer cancel()

			result, err := podStartupChecker.check(ctx)
			tt.validateResult(g, result, err, scenario.fakeDynamicClient)
		})
	}
}

func TestPodStartupChecker_garbageCollect(t *testing.T) {
	checkerName := "chk"
	syntheticPodNamespace := "checker-ns"
	checkerTimeout := 5 * time.Second
	syntheticPodLabelKey := "cluster-health-monitor/checker-name"

	tests := []struct {
		name                       string
		enableNodeProvisioningTest bool
		client                     *k8sfake.Clientset
		dynamicClient              *dynamicfake.FakeDynamicClient
		validateRes                func(g *WithT, err error)
	}{
		{
			name:   "success",
			client: k8sfake.NewClientset(),
			validateRes: func(g *WithT, err error) {
				g.Expect(err).NotTo(HaveOccurred())
			},
		},
		{
			name: "error garbage collecting pods",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("list", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("error bad things")
				})
				return client
			}(),
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to garbage collect outdated synthetic pods"))
			},
		},
		{
			name:                       "clean up node pools success",
			enableNodeProvisioningTest: true,
			client:                     k8sfake.NewClientset(),
			dynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				NodePoolGVR: "NodePoolList",
			}),
			validateRes: func(g *WithT, err error) {
				g.Expect(err).NotTo(HaveOccurred())
			},
		},
		{
			name:                       "clean up node pools failure",
			enableNodeProvisioningTest: true,
			client:                     k8sfake.NewClientset(),
			dynamicClient: func() *dynamicfake.FakeDynamicClient {
				client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
					NodePoolGVR: "NodePoolList",
				})
				client.PrependReactor("list", "nodepools", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("error listing node pools")
				})
				return client
			}(),
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("error listing node pools"))
			},
		},
		{
			name: "error persistent volume claim garbage collection",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("list", "persistentvolumeclaims", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("error bad things")
				})
				return client
			}(),
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to garbage collect outdated persistent volume claims"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			checker := &PodStartupChecker{
				name: checkerName,
				config: &config.PodStartupConfig{
					SyntheticPodNamespace:      syntheticPodNamespace,
					SyntheticPodLabelKey:       syntheticPodLabelKey,
					SyntheticPodStartupTimeout: 3 * time.Second,
					MaxSyntheticPods:           5,
					EnableNodeProvisioningTest: tt.enableNodeProvisioningTest,
				},
				timeout:       checkerTimeout,
				k8sClientset:  tt.client,
				dynamicClient: tt.dynamicClient,
			}

			// Run garbage collect
			err := checker.garbageCollect(context.Background())

			tt.validateRes(g, err)
		})
	}
}

func TestPodStartupChecker_pollPodCreationToContainerRunningDuration(t *testing.T) {
	podName := "pod1"
	syntheticPodNamespace := "test"
	timestamp := time.Now()
	tests := []struct {
		name        string
		pod         *corev1.Pod
		validateRes func(g *WithT, duration time.Duration, err error)
	}{
		{
			name: "container running",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              podName,
					Namespace:         syntheticPodNamespace,
					CreationTimestamp: metav1.NewTime(timestamp.Add(-10 * time.Second)),
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(timestamp)},
						},
					}},
				},
			},
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(duration).To(Equal(10 * time.Second))
			},
		},
		{
			name: "error - polling timeout occurred",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: syntheticPodNamespace,
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{},
						},
					}},
				},
			},
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err).To(Equal(context.DeadlineExceeded))
				g.Expect(duration).To(Equal(0 * time.Second))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			client := k8sfake.NewClientset()
			if tt.pod != nil {
				client.CoreV1().Pods(syntheticPodNamespace).Create(context.Background(), tt.pod, metav1.CreateOptions{}) //nolint:errcheck // ignore error for test setup
			}
			checker := &PodStartupChecker{
				k8sClientset: client,
				config: &config.PodStartupConfig{
					SyntheticPodNamespace:      syntheticPodNamespace,
					SyntheticPodLabelKey:       "cluster-health-monitor/checker-name",
					SyntheticPodStartupTimeout: 5 * time.Second,
					MaxSyntheticPods:           3,
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			dur, err := checker.pollPodCreationToContainerRunningDuration(ctx, podName)
			tt.validateRes(g, dur, err)
		})
	}
}

func TestPodStartupChecker_parseImagePullDuration(t *testing.T) {
	checker := &PodStartupChecker{}
	tests := []struct {
		name        string
		msg         string
		validateRes func(g *WithT, duration time.Duration, err error)
	}{
		{
			name: "valid message - only ms",
			msg:  "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (800ms including waiting). Image size: 299513 bytes.",
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(duration).To(Equal(800 * time.Millisecond))
			},
		},
		{
			name: "valid message - sec and ms",
			msg:  "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (1s34ms including waiting). Image size: 299513 bytes.",
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(duration).To(Equal(1*time.Second + 34*time.Millisecond))
			},
		},
		{
			name: "valid message - seconds with decimal",
			msg:  "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 2.149s (2.149s including waiting). Image size: 299513 bytes.",
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(duration).To(Equal(2*time.Second + 149*time.Millisecond))
			},
		},
		{
			name: "invalid format",
			msg:  "Successfully pulled image in foo (bar including waiting).",
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(duration).To(Equal(0 * time.Millisecond))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			dur, err := checker.parseImagePullDuration(tt.msg)
			tt.validateRes(g, dur, err)
		})
	}
}

func TestPodStartupChecker_getImagePullDuration(t *testing.T) {
	podName := "pod1"
	namespace := "testns"

	tests := []struct {
		name        string
		client      *k8sfake.Clientset
		validateRes func(g *WithT, duration time.Duration, err error)
	}{
		{
			name: "valid image pulled event",
			client: k8sfake.NewClientset(
				imageSuccessfullyPulledEvent(namespace, podName, 800*time.Millisecond),
			),
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(duration).To(Equal(800 * time.Millisecond))
			},
		},
		{
			name: "valid image already present event",
			client: k8sfake.NewClientset(
				imageAlreadyPresentEvent(namespace, podName),
			),
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(duration).To(Equal(0 * time.Millisecond))
			},
		},
		{
			name:   "no events",
			client: k8sfake.NewClientset(),
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("no image pull events found"))
			},
		},
		{
			name: "error listing events",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor(
					"list", "events", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
						return true, nil, apierrors.NewInternalError(errors.New("error occurred"))
					})
				return client
			}(),
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to list events"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			checker := &PodStartupChecker{
				config: &config.PodStartupConfig{
					SyntheticPodNamespace: namespace,
				},
				k8sClientset: tt.client,
			}
			dur, err := checker.getImagePullDuration(context.Background(), "test-pod")
			tt.validateRes(g, dur, err)
		})
	}
}

func TestPodStartupChecker_makeTCPRequest(t *testing.T) {
	tests := []struct {
		name        string
		podIP       string
		dialer      Dialer
		validateRes func(g *WithT, err error)
	}{
		{
			name:   "successful TCP connection",
			podIP:  "10.0.0.0",
			dialer: successfulDialer(),
			validateRes: func(g *WithT, err error) {
				g.Expect(err).ToNot(HaveOccurred())
			},
		},
		{
			name:   "failed TCP connection",
			podIP:  "10.0.0.0",
			dialer: failingDialer("error occurred"),
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("TCP connection failed"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			checker := &PodStartupChecker{
				dialer: tt.dialer,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			err := checker.createTCPConnection(ctx, tt.podIP)
			tt.validateRes(g, err)
		})
	}
}

func TestPodStartupChecker_createTCPConnectionWithRetry(t *testing.T) {
	tests := []struct {
		name        string
		dialer      func(*int) Dialer
		ctx         func() (context.Context, context.CancelFunc)
		validateRes func(g *WithT, err error, calls int)
	}{
		{
			name: "successful first try",
			dialer: func(calls *int) Dialer {
				return &mockDialer{dialFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
					(*calls)++
					conn, _ := net.Pipe()
					return conn, nil
				}}
			},
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 1*time.Second)
			},
			validateRes: func(g *WithT, err error, calls int) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(calls).To(Equal(1))
			},
		},
		{
			name: "successful after retries",
			dialer: func(calls *int) Dialer {
				return &mockDialer{dialFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
					(*calls)++
					if *calls < 2 {
						return nil, errors.New("temporary TCP error")
					}
					conn, _ := net.Pipe()
					return conn, nil
				}}
			},
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 1*time.Second)
			},
			validateRes: func(g *WithT, err error, calls int) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(calls).To(Equal(2))
			},
		},
		{
			name: "fails after max retries",
			dialer: func(calls *int) Dialer {
				return &mockDialer{dialFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
					(*calls)++
					return nil, errors.New("temporary TCP error")
				}}
			},
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 1*time.Second)
			},
			validateRes: func(g *WithT, err error, calls int) {
				g.Expect(err).To(HaveOccurred())
				var retryErr retry.Error
				g.Expect(errors.As(err, &retryErr)).To(BeTrue())
				g.Expect(retryErr).To(HaveLen(4))
				g.Expect(retryErr[0].Error()).To(ContainSubstring("attempt 1:"))
				g.Expect(retryErr[1].Error()).To(ContainSubstring("attempt 2:"))
				g.Expect(retryErr[2].Error()).To(ContainSubstring("attempt 3:"))
				g.Expect(retryErr[3].Error()).To(ContainSubstring("attempt 4:"))
				// Although calls seems redundant given we check the length of the retry error, this is used in other test cases that don't
				// return an error to make sure that there actually were retries attempted.
				g.Expect(calls).To(Equal(4))
			},
		},
		{
			name: "stops on context timeout",
			dialer: func(calls *int) Dialer {
				return &mockDialer{dialFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
					(*calls)++
					return nil, errors.New("temporary TCP error")
				}}
			},
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 1*time.Millisecond)
			},
			validateRes: func(g *WithT, err error, calls int) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
				g.Expect(calls).To(BeNumerically(">=", 1))
				g.Expect(calls).To(BeNumerically("<=", 4))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			calls := 0
			ctx, cancel := tt.ctx()
			defer cancel()

			checker := &PodStartupChecker{
				dialer: tt.dialer(&calls),
				config: &config.PodStartupConfig{
					TCPMaxRetries:    3,
					TCPRetryInterval: 1 * time.Millisecond,
				},
			}

			err := checker.createTCPConnectionWithRetry(ctx, "10.0.0.0")
			tt.validateRes(g, err, calls)
		})
	}
}

func TestAllErrorsAreDeadlineExceeded(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "all deadline exceeded errors",
			err: retry.Error{
				fmt.Errorf("attempt 1: %w", context.DeadlineExceeded),
				fmt.Errorf("attempt 2: %w", context.DeadlineExceeded),
			},
			want: true,
		},
		{
			name: "mixed errors",
			err: retry.Error{
				fmt.Errorf("attempt 1: %w", context.DeadlineExceeded),
				errors.New("connection refused"),
			},
			want: false,
		},
		{
			name: "single deadline exceeded error",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "single non-timeout error",
			err:  errors.New("connection refused"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(allErrorsAreDeadlineExceeded(tt.err)).To(Equal(tt.want))
		})
	}
}
