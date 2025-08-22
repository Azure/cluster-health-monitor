package podstartup

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestGenerateSyntheticPod(t *testing.T) {
	tests := []struct {
		name                       string
		checkerName                string
		enableNodeProvisioningTest bool
	}{
		{
			name:        "generates valid synthetic pod",
			checkerName: "test",
		},
		{
			name:        "successfully handles uppercase checker name",
			checkerName: "UPPERCASE",
		},
		{
			name:                       "successfully enables node provisioning test",
			checkerName:                "test",
			enableNodeProvisioningTest: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			checker := &PodStartupChecker{
				name: tt.checkerName,
				config: &config.PodStartupConfig{
					SyntheticPodLabelKey:       _testSyntheticLabelKey,
					EnableNodeProvisioningTest: tt.enableNodeProvisioningTest,
				},
			}

			timestampStr := fmt.Sprintf("%d", time.Now().UnixNano())
			pod := checker.generateSyntheticPod(timestampStr)
			g.Expect(pod).ToNot(BeNil())

			// Verify pod name is k8s compliant (DNS subdomain format)
			g.Expect(validation.NameIsDNSSubdomain(pod.Name, false)).To(BeEmpty()) // this should not return any validation errors
			g.Expect(pod.Name).To(HavePrefix(checker.syntheticPodNamePrefix()))
			g.Expect(pod.Labels).To(Equal(checker.syntheticPodLabels()))

			if tt.enableNodeProvisioningTest {
				g.Expect(pod.Spec.NodeSelector).To(HaveKeyWithValue(_testSyntheticLabelKey, timestampStr))
			} else {
				g.Expect(pod.Spec.NodeSelector).ToNot(HaveKey(_testSyntheticLabelKey))
			}
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
