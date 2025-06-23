package podstartup

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/types"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestPodStartupChecker_Run(t *testing.T) {
	timestamp := time.Now()
	checkerName := "test-checker"
	checkerNamespace := "test-namespace"
	checkerLabels := map[string]string{
		"cluster-health-monitor/checker-name": checkerName,
		"app":                                 "cluster-health-monitor-podstartup-synthetic",
	}

	tests := []struct {
		name           string
		client         *k8sfake.Clientset
		validateResult func(g *WithT, result *types.Result, err error)
	}{
		{
			name: "healthy result - no pre-existing synthetic pods",
			client: func() *k8sfake.Clientset {
				podName := "pod1"
				client := k8sfake.NewClientset(
					// pre-create a fake image pull event for the pod
					imageAlreadyPresentEvent(checkerNamespace, podName),
				)
				// create/get/delete pod calls will succeed with this pod
				fakePod := podWithLabels(podName, checkerNamespace, checkerLabels, timestamp)
				fakePod.Status = corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(timestamp.Add(3 * time.Second))},
						},
					}},
				}
				client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, fakePod, nil
				})
				client.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, fakePod, nil
				})
				client.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, fakePod, nil
				})
				return client
			}(),
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusHealthy))
			},
		},
		{
			name: "unhealthy result - pod startup took too long",
			client: func() *k8sfake.Clientset {
				podName := "pod1"
				client := k8sfake.NewClientset(
					// pre-create a fake image pull event for the pod
					imageAlreadyPresentEvent(checkerNamespace, podName),
				)
				// create/get pod calls will return this pod
				fakePod := podWithLabels(podName, checkerNamespace, checkerLabels, timestamp)
				fakePod.Status = corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(timestamp.Add(10 * time.Second))},
						},
					}},
				}
				client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, fakePod, nil
				})
				client.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, fakePod, nil
				})
				client.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, fakePod, nil
				})
				return client
			}(),
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(errCodePodStartupDurationExceeded))
			},
		},
		{
			name: "error - max synthetic pods reached",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				// preload client with the maximum number of synthetic pods
				for i := range maxSyntheticPods {
					podName := fmt.Sprintf("pod%d", i)
					client.CoreV1().Pods(checkerNamespace).Create(context.Background(), //nolint:errcheck // ignore error for test setup
						podWithLabels(podName, checkerNamespace, checkerLabels, timestamp), metav1.CreateOptions{})
				}
				// prevent pod deletion from succeeding
				client.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("error occurred")
				})
				return client
			}(),
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("maximum number of synthetic pods reached"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			podStartupChecker := &PodStartupChecker{
				name:         checkerName,
				namespace:    checkerNamespace,
				podLabels:    checkerLabels,
				k8sClientset: tt.client,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			result, err := podStartupChecker.Run(ctx)
			tt.validateResult(g, result, err)
		})
	}
}

func TestPodStartupChecker_garbageCollect(t *testing.T) {
	checkerName := "checker"
	checkerNamespace := "checker-ns"
	checkerTimeout := 5 * time.Second
	checkerPodLabels := map[string]string{
		"cluster-health-monitor/checker-name": checkerName,
		"app":                                 "cluster-health-monitor-podstartup-synthetic",
	}

	tests := []struct {
		name        string
		client      *k8sfake.Clientset
		validateRes func(g *WithT, pods *corev1.PodList, err error)
	}{
		{
			name: "only removes pods older than timeout",
			client: k8sfake.NewClientset(
				podWithLabels("old-pod", checkerNamespace, checkerPodLabels, time.Now().Add(-2*time.Hour)),
				podWithLabels("new-pod", checkerNamespace, checkerPodLabels, time.Now()),
			),
			validateRes: func(g *WithT, pods *corev1.PodList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pods.Items).To(HaveLen(1))
				g.Expect(pods.Items[0].Name).To(Equal("new-pod"))
			},
		},
		{
			name: "no pods to delete",
			client: k8sfake.NewClientset(
				podWithLabels("new-pod-1", checkerNamespace, checkerPodLabels, time.Now()),                      // pod too new
				podWithLabels("new-pod-2", checkerNamespace, map[string]string{}, time.Now().Add(-2*time.Hour)), // old pod wrong labels
			),
			validateRes: func(g *WithT, pods *corev1.PodList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pods.Items).To(HaveLen(2))
				actualNames := make([]string, len(pods.Items))
				for i, pod := range pods.Items {
					actualNames[i] = pod.Name
				}
				g.Expect(actualNames).To(ConsistOf([]string{"new-pod-1", "new-pod-2"}))
			},
		},
		{
			name: "only removes pod with checker labels",
			client: k8sfake.NewClientset(
				podWithLabels("checker-pod", checkerNamespace, checkerPodLabels, time.Now().Add(-2*time.Hour)),
				podWithLabels("non-checker-pod", checkerNamespace, map[string]string{}, time.Now().Add(-2*time.Hour)),
			),
			validateRes: func(g *WithT, pods *corev1.PodList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pods.Items).To(HaveLen(1))
				g.Expect(pods.Items[0].Name).To(Equal("non-checker-pod"))
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
					podWithLabels("old-pod-1", checkerNamespace, checkerPodLabels, time.Now().Add(-2*time.Hour)),
					podWithLabels("old-pod-2", checkerNamespace, checkerPodLabels, time.Now().Add(-2*time.Hour)),
				)
				// only fail the Delete call for old-pod-1
				client.PrependReactor("delete", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					deleteAction, ok := action.(k8stesting.DeleteAction)
					if ok && deleteAction.GetName() == "old-pod-1" {
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
				name:         checkerName,
				timeout:      checkerTimeout,
				k8sClientset: tt.client,
				namespace:    checkerNamespace,
				podLabels:    checkerPodLabels,
			}

			// Run garbage collect
			err := checker.garbageCollect(context.Background())

			// Get pods for validation
			pods, listErr := tt.client.CoreV1().Pods(checkerNamespace).List(context.Background(), metav1.ListOptions{})
			g.Expect(listErr).NotTo(HaveOccurred())

			tt.validateRes(g, pods, err)
		})
	}
}

func TestPodStartupChecker_pollPodCreationToContainerRunningDuration(t *testing.T) {
	podName := "pod1"
	checkerNamespace := "test"
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
					Namespace:         checkerNamespace,
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
			name: "error - polling timeout",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: checkerNamespace,
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
				client.CoreV1().Pods(checkerNamespace).Create(context.Background(), tt.pod, metav1.CreateOptions{}) //nolint:errcheck // ignore error for test setup
			}
			checker := &PodStartupChecker{
				k8sClientset: client,
				namespace:    checkerNamespace,
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
			checkerName: "test-checker",
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
				podLabels: map[string]string{
					"cluster-health-monitor/checker-name": tt.checkerName,
					"app":                                 "cluster-health-monitor-podstartup-synthetic",
				},
			}

			pod := checker.generateSyntheticPod()
			g.Expect(pod).ToNot(BeNil())

			// Verify pod name is k8s compliant (DNS subdomain format)
			g.Expect(validation.NameIsDNSSubdomain(pod.Name, false)).To(BeEmpty()) // this should not return any validation errors
			// Verify checker labels are applied
			g.Expect(pod.Labels).To(Equal(checker.podLabels))
		})
	}
}

// --- helpers ---
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
