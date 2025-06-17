package podstartup

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
)

type PodStartupChecker struct {
	name         string
	config       *config.PodStartupConfig
	k8sClientset kubernetes.Interface
}

func Register() {
	checker.RegisterChecker(config.CheckTypePodStartup, BuildPodStartupChecker)
}

// BuildPodStartupChecker creates a new PodStartupChecker instance.
func BuildPodStartupChecker(config *config.CheckerConfig) (checker.Checker, error) {
	if config.Name == "" {
		return nil, fmt.Errorf("checker name cannot be empty")
	}
	if err := config.PodStartupConfig.ValidatePodStartupConfig(); err != nil {
		return nil, err
	}

	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}
	k8sClientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	return &PodStartupChecker{
		name:         config.Name,
		config:       config.PodStartupConfig,
		k8sClientset: k8sClientset,
	}, nil
}

func (c *PodStartupChecker) Name() string {
	return c.name
}

func (c *PodStartupChecker) Run(ctx context.Context) error {
	// labels shared by all synthetic pods created by this checker
	podLabels := map[string]string{
		"app":                                 "cluster-health-monitor-podstartup-synthetic",
		"cluster-health-monitor/checker-name": c.name,
	}

	// List pods and check count of exisiting. If too high attempt garbage collect and return with error
	pods, err := c.k8sClientset.CoreV1().Pods(c.config.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set(podLabels)).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to list pods in namespace %s: %w", c.config.Namespace, err)
	}
	if len(pods.Items) >= c.config.MaxSyntheticPods {
		c.garbageCollect(ctx, pods.Items)
		return fmt.Errorf("maximum number of synthetic pods reached in namespace %s, current: %d, max allowed: %d, delete some pods before running the checker again",
			c.config.Namespace, len(pods.Items), c.config.MaxSyntheticPods)
	}

	// Create synthetic pod in namespace
	synthPod, err := c.k8sClientset.CoreV1().Pods(c.config.Namespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("%s-synthetic-%d", c.name, time.Now().UnixNano()),
			Labels: podLabels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "pause",
					Image: "k8s.gcr.io/pause:3.2",
				},
			},
			// TODO: Add pod cpu/memory requests and/or limits.
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create synthetic pod in namespace %s: %w", c.config.Namespace, err)
	}

	// Poll the durations to calculate the startup duration metric
	imagePullDuration, err := c.pollImagePullDuration(ctx, synthPod.Name)
	if err != nil {
		return fmt.Errorf("failed to poll image pull duration for pod %s in namespace %s: %w", synthPod.Name, c.config.Namespace, err)
	}
	podCreationToContainerReadyDuration, err := c.pollPodCreationToContainerReadyDuration(ctx, synthPod.Name)
	if err != nil {
		return fmt.Errorf("failed to poll pod creation and container ready time for pod %s in namespace %s: %w", synthPod.Name, c.config.Namespace, err)
	}
	podStartupTime := podCreationToContainerReadyDuration - imagePullDuration

	// TODO: Record startup duration as Prometheus metric
	fmt.Println(podStartupTime)

	err = c.k8sClientset.CoreV1().Pods(c.config.Namespace).Delete(ctx, synthPod.Name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		klog.Warningf("failed to delete synthetic pod %s in namespace %s: %s", synthPod.Name, c.config.Namespace, err.Error())
	}

	return nil
}

func (c *PodStartupChecker) garbageCollect(ctx context.Context, pods []corev1.Pod) {
	for _, pod := range pods {
		// TODO? Maybe take into account pod age and only try delete pods older than timeout in config
		if err := c.k8sClientset.CoreV1().Pods(c.config.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			klog.Errorf("failed to delete synthetic pod %s: %s", pod.Name, err.Error())
		}
	}
}

// Polls the pod until the container is ready or the context is no longer valid. It returns the duration between the
// pod creation and the container being ready.
func (c *PodStartupChecker) pollPodCreationToContainerReadyDuration(ctx context.Context, podName string) (time.Duration, error) {
	var podCreationToContainerReadyDuration time.Duration
	err := wait.PollUntilContextCancel(ctx, 5*time.Second, false, func(ctx context.Context) (bool, error) {
		pod, err := c.k8sClientset.CoreV1().Pods(c.config.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			klog.Warningf("failed to get pod %s in namespace %s: %s", podName, c.config.Namespace, err.Error())
			return false, nil
		}
		if len(pod.Status.ContainerStatuses) == 0 {
			klog.Warningf("pod %s in namespace %s has no container statuses", podName, c.config.Namespace)
			return false, nil
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.State.Running != nil {
				podCreationTime := pod.CreationTimestamp.Time
				containerReadyTime := status.State.Running.StartedAt.Time
				podCreationToContainerReadyDuration = containerReadyTime.Sub(podCreationTime)
				return true, nil
			}
		}
		klog.Warningf("no running containers for pod %s in namespace %s", podName, c.config.Namespace)
		return false, nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to poll pod creation to container ready duration for pod %s in namespace %s: %w", podName, c.config.Namespace, err)
	}
	return podCreationToContainerReadyDuration, nil
}

// Polls pod events until an event with reason "Pulled" is found or the context is no longer valid. Returns the image pull duration
// including waiting time.
func (c *PodStartupChecker) pollImagePullDuration(ctx context.Context, podName string) (time.Duration, error) {
	var imagePullDuration time.Duration
	err := wait.PollUntilContextCancel(ctx, 5*time.Second, false, func(ctx context.Context) (bool, error) {
		events, err := c.k8sClientset.CoreV1().Events(c.config.Namespace).List(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.name=%s,reason=Pulled", podName),
		})
		if err != nil {
			klog.Errorf("failed to list events for pod %s in namespace %s: %s", podName, c.config.Namespace, err.Error())
			return false, nil
		}

		// events with reason=Pulled have messages expected to be in one of two formats:
		// 1. "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (426ms including waiting). Image size: 299513 bytes."
		// 2. "Container image \"k8s.gcr.io/pause:3.2\" already present on machine"
		for _, event := range events.Items {
			if strings.Contains(event.Message, "Successfully pulled image") {
				imagePullDuration, err = c.extractImagePullDuration(event.Message)
				if err != nil {
					return false, fmt.Errorf("failed to extract image pull duration from event message: %w", err)
				}
				return true, nil
			} else if strings.Contains(event.Message, "already present on machine") {
				imagePullDuration = 0
				return true, nil
			} else {
				klog.Warningf("Unexpected event message format for pod %s in namespace %s: %s", podName, c.config.Namespace, event.Message)
				return false, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return 0, err
	}
	return imagePullDuration, nil
}

// Extracts the image pull duration in ms from the event message. Message is expected to be in the format:
// "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (426ms including waiting). Image size: 299513 bytes."
func (c *PodStartupChecker) extractImagePullDuration(message string) (time.Duration, error) {
	re := regexp.MustCompile(`\((\d+)ms including waiting\)`)
	matches := re.FindStringSubmatch(message)
	if len(matches) != 2 {
		return 0, fmt.Errorf("message in unexpected format: %s", message)
	}
	imagePullDuration, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("could not parse duration: %s", matches[1])

	}
	return time.Duration(imagePullDuration) * time.Millisecond, nil
}
