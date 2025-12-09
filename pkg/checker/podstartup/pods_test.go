package podstartup

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestGenerateSyntheticPod(t *testing.T) {
	tests := []struct {
		name                       string
		checkerName                string
		enableNodeProvisioningTest bool
		csiTests                   []config.CSIType
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
		{
			name:        "successfully enables all CSI tests",
			checkerName: "test",
			csiTests:    []config.CSIType{config.CSITypeAzureFile, config.CSITypeAzureDisk, config.CSITypeAzureBlob},
		},
		{
			name:        "successfully enables one CSI tests",
			checkerName: "test",
			csiTests:    []config.CSIType{config.CSITypeAzureFile},
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
					EnabledCSIs:                tt.csiTests,
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
				// ensure a toleration is added so the pod can be scheduled to synthetic nodes
				g.Expect(pod.Spec.Tolerations).To(ContainElement(corev1.Toleration{
					Key:      _testSyntheticLabelKey,
					Operator: corev1.TolerationOpEqual,
					Effect:   corev1.TaintEffectNoSchedule,
				}))
			} else {
				g.Expect(pod.Spec.NodeSelector).ToNot(HaveKey(_testSyntheticLabelKey))
			}

			g.Expect(len(pod.Spec.Volumes)).To(Equal(len(tt.csiTests)))
			g.Expect(len(pod.Spec.Containers)).To(Equal(1))
			g.Expect(len(pod.Spec.Containers[0].VolumeMounts)).To(Equal(len(tt.csiTests)))

			for _, csiTest := range tt.csiTests {
				switch csiTest {
				case config.CSITypeAzureFile:
					g.Expect(pod.Spec.Volumes).To(ContainElement(corev1.Volume{
						Name: "azurefile-volume",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: checker.azureFilePVC(timestampStr).Name,
							},
						},
					}))
					g.Expect(pod.Spec.Containers[0].VolumeMounts).To(ContainElement(corev1.VolumeMount{
						Name:      "azurefile-volume",
						MountPath: "/mnt/azurefile",
					}))
				case config.CSITypeAzureDisk:
					g.Expect(pod.Spec.Volumes).To(ContainElement(corev1.Volume{
						Name: "azuredisk-volume",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: checker.azureDiskPVC(timestampStr).Name,
							},
						},
					}))
					g.Expect(pod.Spec.Containers[0].VolumeMounts).To(ContainElement(corev1.VolumeMount{
						Name:      "azuredisk-volume",
						MountPath: "/mnt/azuredisk",
					}))
				case config.CSITypeAzureBlob:
					g.Expect(pod.Spec.Volumes).To(ContainElement(corev1.Volume{
						Name: "azureblob-volume",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: checker.azureBlobPVC(timestampStr).Name,
							},
						},
					}))
					g.Expect(pod.Spec.Containers[0].VolumeMounts).To(ContainElement(corev1.VolumeMount{
						Name:      "azureblob-volume",
						MountPath: "/mnt/azureblob",
					}))
				}
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

func TestPodStartupChecker_syntheticPodgarbageCollection(t *testing.T) {
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
				},
				timeout:      checkerTimeout,
				k8sClientset: tt.client,
			}

			// Run garbage collect
			err := checker.syntheticPodGarbageCollection(context.Background())

			// Get pods for validation
			pods, listErr := tt.client.CoreV1().Pods(syntheticPodNamespace).List(context.Background(), metav1.ListOptions{})
			g.Expect(listErr).NotTo(HaveOccurred())

			tt.validateRes(g, pods, err)
		})
	}
}

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
