package podstartup

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

func TestExtractImagePullDuration(t *testing.T) {
	checker := &PodStartupChecker{}
	tests := []struct {
		name        string
		msg         string
		validateRes func(g *WithT, duration time.Duration, err error)
	}{
		{
			name: "valid message",
			msg:  "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (800ms including waiting). Image size: 299513 bytes.",
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(duration).To(Equal(800 * time.Millisecond))
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
			dur, err := checker.extractImagePullDuration(tt.msg)
			tt.validateRes(g, dur, err)
		})
	}
}

func TestGetImagePullDuration(t *testing.T) {
	tests := []struct {
		name               string
		events             []corev1.Event
		prependReactorFunc func(action k8stesting.Action) (handled bool, ret runtime.Object, err error)
		validateRes        func(g *WithT, duration time.Duration, err error)
	}{
		{
			name: "valid image pulled event",
			events: []corev1.Event{{
				ObjectMeta:     metav1.ObjectMeta{Name: "event1"},
				Message:        "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (800ms including waiting). Image size: 299513 bytes.",
				Reason:         "Pulled",
				InvolvedObject: corev1.ObjectReference{Name: "test-pod"},
			}},
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(duration).To(Equal(800 * time.Millisecond))
			},
		},
		{
			name: "valid image already present event",
			events: []corev1.Event{{
				ObjectMeta:     metav1.ObjectMeta{Name: "event2"},
				Message:        "Container image \"k8s.gcr.io/pause:3.2\" already present on machine",
				Reason:         "Pulled",
				InvolvedObject: corev1.ObjectReference{Name: "test-pod"},
			}},
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(duration).To(Equal(0 * time.Millisecond))
			},
		},
		{
			name:   "no events",
			events: []corev1.Event{},
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("no image pull events found"))
			},
		},
		{
			name: "error listing events",
			prependReactorFunc: func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
				return true, nil, apierrors.NewInternalError(fmt.Errorf("simulated internal server error"))
			},
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to list events"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			client := fake.NewSimpleClientset()

			if tt.prependReactorFunc != nil {
				client.PrependReactor("list", "events", tt.prependReactorFunc)
			}
			// Add events to the fake client
			for _, ev := range tt.events {
				client.CoreV1().Events("default").Create(context.Background(), &ev, metav1.CreateOptions{})
			}

			checker := &PodStartupChecker{
				config:       &config.PodStartupConfig{Namespace: "default"},
				k8sClientset: client,
			}
			dur, err := checker.getImagePullDuration(context.Background(), "test-pod")
			tt.validateRes(g, dur, err)
		})
	}
}

func TestPollPodCreationToContainerReadyDuration(t *testing.T) {
	tests := []struct {
		name        string
		pod         *corev1.Pod
		validateRes func(g *WithT, duration time.Duration, err error)
	}{
		{
			name: "container running",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "pod1",
					Namespace:         "default",
					CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Second)),
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{{
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Now())},
						},
					}},
				},
			},
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(duration).To(BeNumerically(">=", 10*time.Second-1*time.Second)) // allow some clock drift and prevent flakes
			},
		},
		{
			name: "polling timeout",
			validateRes: func(g *WithT, duration time.Duration, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to poll pod creation to container ready duration"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			client := fake.NewSimpleClientset()
			if tt.pod != nil {
				client.CoreV1().Pods("default").Create(context.Background(), tt.pod, metav1.CreateOptions{})
			}

			checker := &PodStartupChecker{
				config:       &config.PodStartupConfig{Namespace: "default"},
				k8sClientset: client,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			dur, err := checker.pollPodCreationToContainerReadyDuration(ctx, "pod1")
			tt.validateRes(g, dur, err)
		})
	}
}

func TestPodStartupChecker_Run(t *testing.T) {
	timestamp := time.Now()
	checkerName := "test-checker"
	tests := []struct {
		name           string
		checkerConfig  config.PodStartupConfig
		prepareClient  func(client *fake.Clientset)
		validateResult func(g *WithT, result *types.Result, err error)
	}{
		{
			name: "healthy result - no pre-existing synthetic pods",
			checkerConfig: config.PodStartupConfig{
				Namespace:        "default",
				MaxSyntheticPods: 5,
			},
			prepareClient: func(client *fake.Clientset) {
				podName := "pod1"
				// pre-create a fake image pull event for the pod
				fakeEvent := &corev1.Event{
					ObjectMeta:     metav1.ObjectMeta{Name: "event1"},
					Message:        "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (500ms including waiting). Image size: 299513 bytes.",
					Reason:         "Pulled",
					InvolvedObject: corev1.ObjectReference{Name: podName},
				}
				client.CoreV1().Events("default").Create(context.Background(), fakeEvent, metav1.CreateOptions{})
				// create/get pod calls will return this pod
				fakePod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              podName,
						Namespace:         "default",
						CreationTimestamp: metav1.NewTime(timestamp),
						Labels: map[string]string{
							"cluster-health-monitor/checker-name": checkerName,
							"app":                                 "cluster-health-monitor-podstartup-synthetic",
						},
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{{
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(timestamp.Add(3 * time.Second))},
							},
						}},
					},
				}
				client.Fake.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, fakePod, nil
				})
				client.Fake.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, fakePod, nil
				})
				// delete pod call will succeed
				client.Fake.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, nil
				})
			},
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusHealthy))
			},
		},
		{
			name: "unhealthy result - pod startup took too long",
			checkerConfig: config.PodStartupConfig{
				Namespace:        "default",
				MaxSyntheticPods: 5,
			},
			prepareClient: func(client *fake.Clientset) {
				podName := "pod1"
				// pre-create a fake image pull event for the pod
				fakeEvent := &corev1.Event{
					ObjectMeta:     metav1.ObjectMeta{Name: "event1"},
					Message:        "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (500ms including waiting). Image size: 299513 bytes.",
					Reason:         "Pulled",
					InvolvedObject: corev1.ObjectReference{Name: podName},
				}
				client.CoreV1().Events("default").Create(context.Background(), fakeEvent, metav1.CreateOptions{})
				// create/get pod calls will return this pod
				fakePod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              podName,
						Namespace:         "default",
						CreationTimestamp: metav1.NewTime(timestamp),
						Labels: map[string]string{
							"cluster-health-monitor/checker-name": checkerName,
							"app":                                 "cluster-health-monitor-podstartup-synthetic",
						},
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{{
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(timestamp.Add(10 * time.Second))},
							},
						}},
					},
				}
				client.Fake.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, fakePod, nil
				})
				client.Fake.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, fakePod, nil
				})
				// delete pod call will succeed
				client.Fake.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, nil
				})
			},
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusUnhealthy))
			},
		},
		{
			name: "error - max synthetic pods reached",
			checkerConfig: config.PodStartupConfig{
				Namespace:        "default",
				MaxSyntheticPods: 0, // Realistically, this is blocked by validation when building the checker, but testing the error handling here
			},
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("maximum number of synthetic pods reached"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			client := fake.NewSimpleClientset()
			if tt.prepareClient != nil {
				tt.prepareClient(client)
			}
			podStartupChecker := &PodStartupChecker{
				name:         checkerName,
				config:       &tt.checkerConfig,
				k8sClientset: client,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := podStartupChecker.Run(ctx)
			tt.validateResult(g, result, err)
		})
	}
}
