package podstartup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func (c *PodStartupChecker) syntheticPodLabels() map[string]string {
	return map[string]string{
		// c.name is supposed to be a unique identifier for each checker. Using this as the label value to ensure that synthetic pods
		// created by different checkers do not conflict with each other.
		c.config.SyntheticPodLabelKey: c.name,
	}
}

func (c *PodStartupChecker) syntheticPodNamePrefix() string {
	// The synthetic pod name prefix is used as an additional safety measure to ensure that the checker only operates on its own synthetic pods.
	// c.name is supposed to be a unique identifier for each checker, so this prefix should be unique across all checkers.
	return strings.ToLower(fmt.Sprintf("%s-synthetic-", c.name))
}

func (c *PodStartupChecker) generateSyntheticPod(timestampStr string) *corev1.Pod {
	podName := fmt.Sprintf("%s%s", c.syntheticPodNamePrefix(), timestampStr)

	volumes := []corev1.Volume{}
	volumeMounts := []corev1.VolumeMount{}

	for _, csiType := range c.config.EnabledCSIs {
		switch csiType {
		case config.CSITypeAzureFile:
			volumes = append(volumes, corev1.Volume{
				Name: "azurefile-volume",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: c.azureFilePVC(timestampStr).Name,
					},
				},
			})
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      "azurefile-volume",
				MountPath: "/mnt/azurefile",
			})
		case config.CSITypeAzureDisk:
			volumes = append(volumes, corev1.Volume{
				Name: "azuredisk-volume",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: c.azureDiskPVC(timestampStr).Name,
					},
				},
			})
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      "azuredisk-volume",
				MountPath: "/mnt/azuredisk",
			})
		case config.CSITypeAzureBlob:
			volumes = append(volumes, corev1.Volume{
				Name: "azureblob-volume",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: c.azureBlobPVC(timestampStr).Name,
					},
				},
			})
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      "azureblob-volume",
				MountPath: "/mnt/azureblob",
			})
		}
	}

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:  "synthetic",
				Image: syntheticPodImage,
				Ports: []corev1.ContainerPort{
					{
						ContainerPort: syntheticPodPort,
						Protocol:      corev1.ProtocolTCP,
					},
				},
				VolumeMounts: volumeMounts,
			},
		},
		Tolerations: []corev1.Toleration{
			{
				Key:    "node-role.kubernetes.io/master",
				Effect: corev1.TaintEffectNoSchedule,
			},
			{
				Key:      "CriticalAddonsOnly",
				Operator: corev1.TolerationOpExists,
			},
		},
		Affinity: &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "kubernetes.azure.com/cluster",
									Operator: corev1.NodeSelectorOpExists,
								},
								{
									Key:      "type",
									Operator: corev1.NodeSelectorOpNotIn,
									Values:   []string{"virtual-kubelet"},
								},
								{
									Key:      "kubernetes.io/os",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"linux"},
								},
								{
									Key:      "kubernetes.azure.com/mode",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"system"},
								},
							},
						},
					},
				},
			},
		},
		Volumes: volumes,
		// TODOcarlosalv: Add pod cpu/memory requests and/or limits.
	}

	if c.config.EnableNodeProvisioningTest {
		// If node provisioning test is enabled, we will add a node selector to ensure the synthetic pod is scheduled on a node from the NodePool created by the checker.
		podSpec.NodeSelector = map[string]string{
			c.config.SyntheticPodLabelKey: timestampStr,
		}

		if podSpec.Tolerations == nil {
			podSpec.Tolerations = []corev1.Toleration{}
		}
		podSpec.Tolerations = append(podSpec.Tolerations, corev1.Toleration{
			Key:      c.config.SyntheticPodLabelKey,
			Operator: corev1.TolerationOpEqual,
			Effect:   corev1.TaintEffectNoSchedule,
		})
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   podName,
			Labels: c.syntheticPodLabels(),
		},
		Spec: podSpec,
	}
}

// getSyntheticPodIP gets the IP address assigned to the synthetic pod with the specified name
func (c *PodStartupChecker) getSyntheticPodIP(ctx context.Context, podName string) (string, error) {
	pod, err := c.k8sClientset.CoreV1().Pods(c.config.SyntheticPodNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("error getting pod %s: %w", podName, err)
	}
	if pod.Status.PodIP == "" {
		return "", fmt.Errorf("pod IP is empty")
	}
	return pod.Status.PodIP, nil
}

func (c *PodStartupChecker) syntheticPodGarbageCollection(ctx context.Context) error {
	podList, err := c.k8sClientset.CoreV1().Pods(c.config.SyntheticPodNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set(c.syntheticPodLabels())).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to list pods for garbage collection: %w", err)
	}
	var errs []error
	for _, pod := range podList.Items {
		if time.Since(pod.CreationTimestamp.Time) > c.timeout {
			err := c.k8sClientset.CoreV1().Pods(c.config.SyntheticPodNamespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("failed to delete old synthetic pod %s: %w", pod.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}
