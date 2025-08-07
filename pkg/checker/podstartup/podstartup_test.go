package podstartup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	karpenter "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// =============================================================================
// Test Helpers and Mocks
// =============================================================================

// Pod and Event creation helpers
func podWithLabels(name string, namespace string, labels map[string]string, creationTime time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			Labels:            labels,
			CreationTimestamp: metav1.NewTime(creationTime),
		},
	}
}

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

func TestPodStartupChecker_Run(t *testing.T) {
	// Defines adjustable parameters for the test scenarios
	type testScenario struct {
		podName         string
		namespace       string
		labels          map[string]string
		podIP           string
		startupDelay    time.Duration
		preExistingPods []string
		hasDeleteError  bool
		dialer          Dialer
	}

	// Mutator function type
	type scenarioMutator func(*testScenario)

	checkerName := "test"
	syntheticPodNamespace := "test-namespace"
	syntheticPodLabelKey := "cluster-health-monitor/checker-name"
	maxSyntheticPods := 3

	tests := []struct {
		name           string
		mutators       []scenarioMutator
		validateResult func(g *WithT, result *types.Result, err error)
	}{
		{
			name:     "healthy result - no pre-existing synthetic pods",
			mutators: nil, // Use default scenario
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusHealthy))
			},
		},
		{
			name: "unhealthy result - pod startup took too long",
			mutators: []scenarioMutator{
				func(s *testScenario) { s.startupDelay = 10 * time.Second },
			},
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(errCodePodStartupDurationExceeded))
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
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("maximum number of synthetic pods reached"))
			},
		},
		{
			name: "error - fail to get pod IP",
			mutators: []scenarioMutator{
				func(s *testScenario) { s.podIP = "" },
			},
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to get synthetic pod IP"))
			},
		},
		{
			name: "unhealthy result - TCP dialer error",
			mutators: []scenarioMutator{
				func(s *testScenario) { s.dialer = failingDialer("error") },
			},
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(errCodeRequestFailed))
				g.Expect(result.Detail.Message).To(ContainSubstring("TCP request to synthetic pod failed"))
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

			nodepool := &unstructured.Unstructured{}

			nodepool.SetUnstructuredContent(map[string]interface{}{
				"kind":       "NodePool",
				"apiVersion": "karpenter.sh/v1",
				"metadata": map[string]interface{}{
					"name": "test-nodepool",
				},
			})

			dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			dynamicClient.PrependReactor("create", "nodepools", func(action k8stesting.Action) (bool, runtime.Object, error) {
				return true, &karpenterv1.NodePool{}, nil
			})
			dynamicClient.PrependReactor("delete", "nodepools", func(action k8stesting.Action) (bool, runtime.Object, error) {
				return true, &karpenterv1.NodePool{}, nil
			})

			podStartupChecker := &PodStartupChecker{
				name: checkerName,
				config: &config.PodStartupConfig{
					SyntheticPodNamespace:      syntheticPodNamespace,
					SyntheticPodLabelKey:       syntheticPodLabelKey,
					SyntheticPodStartupTimeout: 5 * time.Second,
					MaxSyntheticPods:           maxSyntheticPods,
					EnableNodeProvisioningTest: true,
				},
				timeout:       5 * time.Second,
				k8sClientset:  client,
				dialer:        scenario.dialer,
				dynamicClient: dynamicClient,
			}

			ctx, cancel := context.WithTimeout(context.Background(), podStartupChecker.timeout)
			defer cancel()

			result, err := podStartupChecker.Run(ctx)
			tt.validateResult(g, result, err)
		})
	}
}

func TestPodStartupChecker_garbageCollect(t *testing.T) {
	checkerName := "chk"
	syntheticPodNamespace := "checker-ns"
	checkerTimeout := 5 * time.Second
	syntheticPodLabelKey := "cluster-health-monitor/checker-name"

	tests := []struct {
		name        string
		client      *k8sfake.Clientset
		validateRes func(g *WithT, pods *corev1.PodList, err error)
	}{
		{
			name: "only removes pods older than timeout",
			client: k8sfake.NewClientset(
				podWithLabels("chk-synthetic-old", syntheticPodNamespace, map[string]string{syntheticPodLabelKey: checkerName}, time.Now().Add(-2*time.Hour)),
				podWithLabels("chk-synthetic-new", syntheticPodNamespace, map[string]string{syntheticPodLabelKey: checkerName}, time.Now()),
			),
			validateRes: func(g *WithT, pods *corev1.PodList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pods.Items).To(HaveLen(1))
				g.Expect(pods.Items[0].Name).To(Equal("chk-synthetic-new"))
			},
		},
		{
			name: "no pods to delete",
			client: k8sfake.NewClientset(
				podWithLabels("chk-synthetic-too-new", syntheticPodNamespace, map[string]string{syntheticPodLabelKey: checkerName}, time.Now()), // pod too new
				podWithLabels("chk-synthetic-no-labels", syntheticPodNamespace, map[string]string{}, time.Now().Add(-2*time.Hour)),              // old pod wrong labels
				podWithLabels("no-name-prefix", syntheticPodNamespace, map[string]string{}, time.Now().Add(-2*time.Hour)),                       // pod missing name prefix
			),
			validateRes: func(g *WithT, pods *corev1.PodList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pods.Items).To(HaveLen(3))
				actualNames := make([]string, len(pods.Items))
				for i, pod := range pods.Items {
					actualNames[i] = pod.Name
				}
				g.Expect(actualNames).To(ConsistOf([]string{"chk-synthetic-too-new", "chk-synthetic-no-labels", "no-name-prefix"}))
			},
		},
		{
			name: "only removes pod with checker labels",
			client: k8sfake.NewClientset(
				podWithLabels("chk-synthetic-pod", syntheticPodNamespace, map[string]string{syntheticPodLabelKey: checkerName}, time.Now().Add(-2*time.Hour)),
				podWithLabels("chk-synthetic-no-label-pod", syntheticPodNamespace, map[string]string{}, time.Now().Add(-2*time.Hour)),
			),
			validateRes: func(g *WithT, pods *corev1.PodList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pods.Items).To(HaveLen(1))
				g.Expect(pods.Items[0].Name).To(Equal("chk-synthetic-no-label-pod"))
			},
		},
		{
			name: "error listing pods",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("list", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					// fail the List call in garbageCollect because it uses a label selector. This prevents breaking the test which also
					// lists pods but does not use a selector.
					listAction, ok := action.(k8stesting.ListAction)
					if ok && listAction.GetListRestrictions().Labels.String() != "" {
						return true, nil, errors.New("error bad things")
					}
					return false, nil, nil
				})
				return client
			}(),
			validateRes: func(g *WithT, pods *corev1.PodList, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to list pods for garbage collection"))
			},
		},
		{
			name: "error deleting pod",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset(
					podWithLabels("chk-synthetic-pod-1", syntheticPodNamespace, map[string]string{syntheticPodLabelKey: checkerName}, time.Now().Add(-2*time.Hour)),
					podWithLabels("chk-synthetic-pod-2", syntheticPodNamespace, map[string]string{syntheticPodLabelKey: checkerName}, time.Now().Add(-2*time.Hour)),
				)
				// only fail the Delete call for old-pod-1
				client.PrependReactor("delete", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					deleteAction, ok := action.(k8stesting.DeleteAction)
					if ok && deleteAction.GetName() == "chk-synthetic-pod-1" {
						return true, nil, errors.New("error bad things")
					}
					return false, nil, nil
				})
				return client
			}(),
			validateRes: func(g *WithT, pods *corev1.PodList, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to delete old synthetic pod"))
				g.Expect(pods.Items).To(HaveLen(1)) // one pod should be deleted
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
					EnableNodeProvisioningTest: false,
				},
				timeout:       checkerTimeout,
				k8sClientset:  tt.client,
				dynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
			}

			// Run garbage collect
			err := checker.garbageCollect(context.Background())

			// Get pods for validation
			pods, listErr := tt.client.CoreV1().Pods(syntheticPodNamespace).List(context.Background(), metav1.ListOptions{})
			g.Expect(listErr).NotTo(HaveOccurred())

			tt.validateRes(g, pods, err)
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
				dynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), &karpenter.NodePool{}),
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

func TestGenerateSyntheticPod(t *testing.T) {
	tests := []struct {
		name        string
		checkerName string
	}{
		{
			name:        "generates valid synthetic pod",
			checkerName: "test",
		},
		{
			name:        "succesfully handles uppercase checker name",
			checkerName: "UPPERCASE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			checker := &PodStartupChecker{
				name: tt.checkerName,
				config: &config.PodStartupConfig{
					SyntheticPodLabelKey: "cluster-health-monitor/checker-name",
				},
			}

			pod := checker.generateSyntheticPod(fmt.Sprintf("%d", time.Now().UnixNano()))
			g.Expect(pod).ToNot(BeNil())

			// Verify pod name is k8s compliant (DNS subdomain format)
			g.Expect(validation.NameIsDNSSubdomain(pod.Name, false)).To(BeEmpty()) // this should not return any validation errors
			g.Expect(pod.Name).To(HavePrefix(checker.syntheticPodNamePrefix()))
			g.Expect(pod.Labels).To(Equal(checker.syntheticPodLabels()))
		})
	}
}

func TestPodStartupChecker_getSyntheticPodIP(t *testing.T) {
	podName := "test-pod"
	namespace := "test-namespace"

	tests := []struct {
		name        string
		scenario    func() *k8sfake.Clientset
		validateRes func(g *WithT, podIP string, err error)
	}{
		{
			name: "successfully gets pod IP",
			scenario: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      podName,
						Namespace: namespace,
					},
					Status: corev1.PodStatus{
						PodIP: "10.0.0.0",
					},
				}
				client.CoreV1().Pods(namespace).Create(context.Background(), pod, metav1.CreateOptions{}) //nolint:errcheck
				return client
			},
			validateRes: func(g *WithT, podIP string, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(podIP).To(Equal("10.0.0.0"))
			},
		},
		{
			name: "error - pod not found",
			scenario: func() *k8sfake.Clientset {
				return k8sfake.NewClientset()
			},
			validateRes: func(g *WithT, podIP string, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("error getting pod"))
				g.Expect(podIP).To(BeEmpty())
			},
		},
		{
			name: "error - pod IP is empty",
			scenario: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      podName,
						Namespace: namespace,
					},
					Status: corev1.PodStatus{
						PodIP: "",
					},
				}
				client.CoreV1().Pods(namespace).Create(context.Background(), pod, metav1.CreateOptions{}) //nolint:errcheck
				return client
			},
			validateRes: func(g *WithT, podIP string, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("pod IP is empty"))
				g.Expect(podIP).To(BeEmpty())
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
				k8sClientset: tt.scenario(),
			}

			podIP, err := checker.getSyntheticPodIP(context.Background(), podName)
			tt.validateRes(g, podIP, err)
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
