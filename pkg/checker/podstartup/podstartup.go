package podstartup

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
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

func (c *PodStartupChecker) Run(ctx context.Context) (*types.Result, error) {
	// podLabels are shared by all synthetic pods created by this checker.
	podLabels := map[string]string{
		"cluster-health-monitor/checker-name": c.name,
		"app":                                 "cluster-health-monitor-podstartup-synthetic",
	}

	// Garbage collect any synthetic pods previously created by this checker.
	pods, err := c.k8sClientset.CoreV1().Pods(c.config.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set(podLabels)).String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods in namespace %s: %w", c.config.Namespace, err)
	}
	if err := c.garbageCollect(ctx, pods.Items); err != nil {
		// Logging instead of returning an error here to avoid failing the checker run.
		fmt.Printf("garbageCollect failed: %s\n", err.Error())
	}

	// Do not run the checker if the maximum number of synthetic pods has been reached.
	if len(pods.Items) >= c.config.MaxSyntheticPods {
		return nil, fmt.Errorf("maximum number of synthetic pods reached in namespace %s, current: %d, max allowed: %d, delete some pods before running the checker again",
			c.config.Namespace, len(pods.Items), c.config.MaxSyntheticPods)
	}

	// Create a synthetic pod to measure the startup time.
	synthPod, err := c.k8sClientset.CoreV1().Pods(c.config.Namespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("%s-synthetic-%d", c.name, time.Now().UnixNano()),
			Labels: podLabels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					// TODO? Maybe use a different image
					Name:  "pause",
					Image: "k8s.gcr.io/pause:3.2",
				},
			},
			// TODO: Add pod cpu/memory requests and/or limits.
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create synthetic pod in namespace %s: %w", c.config.Namespace, err)
	}

	podCreationToContainerReadyDuration, err := c.pollPodCreationToContainerReadyDuration(ctx, synthPod.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to poll pod creation and container ready time for pod %s in namespace %s: %w", synthPod.Name, c.config.Namespace, err)
	}
	// This does not poll because if the container is ready, the event for pulling the image should already exist.
	imagePullDuration, err := c.getImagePullDuration(ctx, synthPod.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to poll image pull duration for pod %s in namespace %s: %w", synthPod.Name, c.config.Namespace, err)
	}

	// Rounding to the seconds place because that is our least accurate measurement unit.
	podStartupDuration := (podCreationToContainerReadyDuration - imagePullDuration).Round(time.Second)
	if podStartupDuration > 5*time.Second {
		return types.Unhealthy(
			"POD_STARTUP_DURATION_TOO_LONG",
			fmt.Sprintf("pod startup duration for pod %s in namespace %s is too long: %s", synthPod.Name, c.config.Namespace, podStartupDuration.String()),
		), nil
	}

	err = c.k8sClientset.CoreV1().Pods(c.config.Namespace).Delete(ctx, synthPod.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		// Logging instead of returning an error here to avoid failing the checker run.
		fmt.Printf("failed to delete synthetic pod %s in namespace %s: %s\n", synthPod.Name, c.config.Namespace, err.Error())
	}

	return types.Healthy(), nil
}

func (c *PodStartupChecker) garbageCollect(ctx context.Context, pods []corev1.Pod) error {
	var errs []error
	for _, pod := range pods {
		// TODO? Maybe take into account pod age and only try delete pods older than timeout in config
		if err := c.k8sClientset.CoreV1().Pods(c.config.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("failed to delete synthetic pod %s: %s", pod.Name, err.Error()))
		}
	}
	return errors.Join(errs...)
}

// Polls the pod until the container is ready or the context is no longer valid. It returns the duration between the
// pod creation and the container being ready.
func (c *PodStartupChecker) pollPodCreationToContainerReadyDuration(ctx context.Context, podName string) (time.Duration, error) {
	var podCreationToContainerReadyDuration time.Duration
	err := wait.PollUntilContextCancel(ctx, 5*time.Second, false, func(ctx context.Context) (bool, error) {
		pod, err := c.k8sClientset.CoreV1().Pods(c.config.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			fmt.Printf("failed to get pod %s in namespace %s: %s", podName, c.config.Namespace, err.Error())
			return false, nil
		}
		if len(pod.Status.ContainerStatuses) == 0 {
			fmt.Printf("pod %s in namespace %s has no container statuses\n", podName, c.config.Namespace)
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
		fmt.Printf("no running containers for pod %s in namespace %s\n", podName, c.config.Namespace)
		return false, nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to poll pod creation to container ready duration for pod %s in namespace %s: %w", podName, c.config.Namespace, err)
	}
	return podCreationToContainerReadyDuration, nil
}

// Returns the image pull duration including waiting time.
func (c *PodStartupChecker) getImagePullDuration(ctx context.Context, podName string) (time.Duration, error) {
	events, err := c.k8sClientset.CoreV1().Events(c.config.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s,reason=Pulled", podName),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to list events for pod %s in namespace %s: %w", podName, c.config.Namespace, err)
	}

	// events with reason=Pulled have messages expected to be in one of two formats:
	// 1. "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (426ms including waiting). Image size: 299513 bytes."
	// 2. "Container image \"k8s.gcr.io/pause:3.2\" already present on machine"
	for _, event := range events.Items {
		if strings.Contains(event.Message, "Successfully pulled image") {
			return c.extractImagePullDuration(event.Message)
		} else if strings.Contains(event.Message, "already present on machine") {
			return 0, nil
		} else {
			// Logging instead of returning an error to avoid failing the checker run.
			fmt.Printf("Unexpected event message format for pod %s in namespace %s: %s\n", podName, c.config.Namespace, event.Message)
		}
	}
	return 0, fmt.Errorf("no image pull events found for pod %s in namespace %s", podName, c.config.Namespace)
}

// Extracts the image pull duration in ms from the event message. Message is expected to be in the format:
// "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (426ms including waiting). Image size: 299513 bytes."
func (c *PodStartupChecker) extractImagePullDuration(message string) (time.Duration, error) {
	re := regexp.MustCompile(`\((\d+)ms including waiting\)`)
	matches := re.FindStringSubmatch(message)
	if len(matches) != 2 {
		return 0, fmt.Errorf("failed to extract image pull duration from event message: message in unexpected format: %s", message)
	}
	imagePullDuration, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("failed to extract image pull duration from event message: could not parse duration: %s", matches[1])

	}
	return time.Duration(imagePullDuration) * time.Millisecond, nil
}
