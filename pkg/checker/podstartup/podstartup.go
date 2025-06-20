package podstartup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

type PodStartupChecker struct {
	name         string
	timeout      time.Duration
	k8sClientset kubernetes.Interface
	// READONLY: The namespace in which the the checker will create synthetic pods.
	namespace string
	// READONLY: Labels applied to all synthetic pods created by the checker.
	podLabels map[string]string
}

// The maximum number of synthetic pods created by the checker that can exist at any one time. If the limit has been reached, the checker
// will not create any more synthetic pods until some of the existing ones are deleted. Instead, it will fail the run with an error.
// Reaching this limit effectively disables the checker.
const maxSyntheticPods = 10

// The maximum pod startup duration for which the checker will return healthy status. The pod startup duration is defined as the time
// between  pod creation and the container being ready, minus the image pull duration (including waiting).
const maxHealthyPodStartupDuration = 5 * time.Second

var sleepTime = maxHealthyPodStartupDuration // used for unit tests

func Register() {
	checker.RegisterChecker(config.CheckTypePodStartup, BuildPodStartupChecker)
}

// BuildPodStartupChecker creates a new PodStartupChecker instance.
func BuildPodStartupChecker(config *config.CheckerConfig) (checker.Checker, error) {
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}
	k8sClientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	// Pods and service accounts must share the same namespace. Read the namespace the checker is running in.
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return nil, fmt.Errorf("failed to read namespace from service account: %s", err)
	}
	namespace := strings.TrimSpace(string(data))
	if namespace == "" {
		return nil, fmt.Errorf("read empty namespace from service account")
	}

	podLabels := map[string]string{
		"cluster-health-monitor/checker-name": config.Name,
		"app":                                 "cluster-health-monitor-podstartup-synthetic",
	}

	if config.Timeout <= maxHealthyPodStartupDuration {
		return nil, fmt.Errorf("timeout must be greater than the maximum healthy pod startup duration (%s), got: %s", config.Timeout, maxHealthyPodStartupDuration)
	}

	return &PodStartupChecker{
		name:         config.Name,
		timeout:      config.Timeout,
		k8sClientset: k8sClientset,
		namespace:    namespace,
		podLabels:    podLabels,
	}, nil
}

func (c *PodStartupChecker) Name() string {
	return c.name
}

func (c *PodStartupChecker) Type() config.CheckerType {
	return config.CheckTypePodStartup
}

// Run executes the pod startup checker logic. It creates synthetic pods to measure the startup time. The startup time is defined as the
// duration between the pod creation and the container being ready, minus the image pull duration (including waiting). If it is within the
// allowed limit, the checker is considered healthy. Otherwise, it is considered unhealthy. Before each run, the checker also attempts to
// garbage collect any leftover synthetic pods from previous runs that may not have been previously deleted due to errors or other issues.
func (c *PodStartupChecker) Run(ctx context.Context) (*types.Result, error) {
	// Garbage collect any leftover synthetic pods previously created by this checker.
	if err := c.garbageCollect(ctx); err != nil {
		// Logging instead of returning an error here to avoid failing the checker run.
		klog.Warningf("failed to garbage collect old synthetic pods: %s", err.Error())
	}

	// List pods to check the current number of synthetic pods. Do not run the checker if the maximum number of synthetic pods has been reached.
	pods, err := c.k8sClientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set(c.podLabels)).String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %s", err.Error())
	}
	if len(pods.Items) >= maxSyntheticPods {
		return nil, fmt.Errorf("maximum number of synthetic pods reached, current: %d, max allowed: %d, delete some pods before running the checker again",
			len(pods.Items), maxSyntheticPods)
	}

	// Create a synthetic pod to measure the startup time.
	synthPod, err := c.k8sClientset.CoreV1().Pods(c.namespace).Create(ctx, c.generateSyntheticPod(), metav1.CreateOptions{})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.Unhealthy(errCodePodCreationTimeout, "timed out creating synthetic pod"), nil
		} else {
			return types.Unhealthy(errCodePodCreationError, fmt.Sprintf("error creating synthetic pod: %s", err)), nil
		}
	}
	defer func() {
		err := c.k8sClientset.CoreV1().Pods(c.namespace).Delete(ctx, synthPod.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			// Logging instead of returning an error here to avoid failing the checker run.
			klog.Warningf("failed to delete synthetic pod %s: %s", synthPod.Name, err.Error())
		}
	}()

	// Sleep to allow the pod to start and events to be generated.
	time.Sleep(sleepTime)

	podCreationToContainerReadyDuration, err := c.getPodCreationToContainerReadyDuration(ctx, synthPod.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod creation to container ready duration for pod %s: %s", synthPod.Name, err.Error())
	}
	imagePullDuration, err := c.getImagePullDuration(ctx, synthPod.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get image pull duration for pod %s: %s", synthPod.Name, err.Error())
	}

	// Calculate the pod startup duration. Round to the seconds place because that is the unit of the least precise measurement.
	podStartupDuration := (podCreationToContainerReadyDuration - imagePullDuration).Round(time.Second)
	if podStartupDuration > maxHealthyPodStartupDuration {
		return types.Unhealthy(
			errCodeHealthyPodStartupDurationExceeded, fmt.Sprintf("pod %s exceeded the maximum healthy startup duration", synthPod.Name),
		), nil
	}

	return types.Healthy(), nil
}

// garbageCollect deletes all pods created by the checker that are older than the checker's timeout.
func (c *PodStartupChecker) garbageCollect(ctx context.Context) error {
	podList, err := c.k8sClientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set(c.podLabels)).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to list pods for garbage collection: %w", err)
	}
	var errs []error
	for _, pod := range podList.Items {
		if time.Since(pod.CreationTimestamp.Time) > c.timeout {
			err := c.k8sClientset.CoreV1().Pods(c.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("failed to delete old synthetic pod %s: %s", pod.Name, err.Error()))
			}
		}
	}
	return errors.Join(errs...)
}

// Returns the duration between the pod creation and the container being ready. This is precise to the second.
func (c *PodStartupChecker) getPodCreationToContainerReadyDuration(ctx context.Context, podName string) (time.Duration, error) {
	pod, err := c.k8sClientset.CoreV1().Pods(c.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to get pod %s: %s", podName, err.Error())
	}
	if len(pod.Status.ContainerStatuses) == 0 {
		return 0, fmt.Errorf("no container statuses found for pod %s", podName)
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Running != nil {
			podCreationTime := pod.CreationTimestamp.Time
			containerReadyTime := status.State.Running.StartedAt.Time
			return containerReadyTime.Sub(podCreationTime), nil
		}
	}
	return 0, fmt.Errorf("no running containers for pod %s", podName)
}

// Returns the image pull duration including waiting time. This is precise to the millisecond.
func (c *PodStartupChecker) getImagePullDuration(ctx context.Context, podName string) (time.Duration, error) {
	events, err := c.k8sClientset.CoreV1().Events(c.namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s,reason=Pulled", podName),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to list events for pod %s: %s", podName, err.Error())
	}

	// events with reason=Pulled have messages expected to be in one of two formats:
	// 1. "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (426ms including waiting). Image size: 299513 bytes."
	// 2. "Container image \"k8s.gcr.io/pause:3.2\" already present on machine"
	for _, event := range events.Items {
		if strings.Contains(event.Message, "Successfully pulled image") {
			return c.parseImagePullDuration(event.Message)
		} else if strings.Contains(event.Message, "already present on machine") {
			return 0, nil
		} else {
			// Logging instead of returning an error to avoid failing the checker run.
			klog.Warningf("Unexpected event message format for pod %s: %s\n", podName, event.Message)
		}
	}
	return 0, fmt.Errorf("no image pull events found for pod %s", podName)
}

// Parses the image pull duration (including waiting) from a kubernetes event message expected to be in a format like:
// "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (426ms including waiting). Image size: 299513 bytes." or
// "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (1s34ms including waiting). Image size: 299513 bytes."
func (c *PodStartupChecker) parseImagePullDuration(message string) (time.Duration, error) {
	re := regexp.MustCompile(`\(([a-zA-Z0-9]+) including waiting\)`)
	matches := re.FindStringSubmatch(message)
	if len(matches) != 2 {
		return 0, fmt.Errorf("failed to extract image pull duration from event message: message in unexpected format: %s", message)
	}
	return time.ParseDuration(matches[1])
}

func (c *PodStartupChecker) generateSyntheticPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("%s-synthetic-%d", c.name, time.Now().UnixNano()),
			Labels: c.podLabels,
		},
		// TODO? maybe allow configuring the pod spec in the config
		Spec: corev1.PodSpec{
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
										Operator: corev1.NodeSelectorOpExists},
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
			Containers: []corev1.Container{
				{
					// TODO? Maybe use a different image
					Name:  "pause",
					Image: "k8s.gcr.io/pause:3.2",
				},
			},
			// TODO: Add pod cpu/memory requests and/or limits.
		},
	}
}
