package podstartup

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestPodStartupChecker_GarbageCollect(t *testing.T) {
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
				makePodWithLabels("old-pod", checkerNamespace, checkerPodLabels, time.Now().Add(-2*time.Hour)),
				makePodWithLabels("new-pod", checkerNamespace, checkerPodLabels, time.Now()),
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
				makePodWithLabels("new-pod-1", checkerNamespace, checkerPodLabels, time.Now()),                      // pod too new
				makePodWithLabels("new-pod-2", checkerNamespace, map[string]string{}, time.Now().Add(-2*time.Hour)), // old pod wrong labels
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
				makePodWithLabels("checker-pod", checkerNamespace, checkerPodLabels, time.Now().Add(-2*time.Hour)),
				makePodWithLabels("non-checker-pod", checkerNamespace, map[string]string{}, time.Now().Add(-2*time.Hour)),
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
					makePodWithLabels("old-pod-1", checkerNamespace, checkerPodLabels, time.Now().Add(-2*time.Hour)),
					makePodWithLabels("old-pod-2", checkerNamespace, checkerPodLabels, time.Now().Add(-2*time.Hour)),
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

// --- helpers ---
func makePodWithLabels(name string, namespace string, labels map[string]string, creationTime time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			Labels:            labels,
			CreationTimestamp: metav1.NewTime(creationTime),
		},
	}
}
