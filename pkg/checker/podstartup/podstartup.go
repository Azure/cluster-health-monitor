package podstartup

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

type PodStartupChecker struct {
	name         string
	config       *config.PodStartupConfig
	timeout      time.Duration
	k8sClientset kubernetes.Interface
}

// How often to poll the pod status to check if the container is running.
var pollingInterval = 1 * time.Second // used for unit tests

// The regular expression used to parse the image pull duration from a k8s event message for successfully pulling an image.
var imagePullDurationRegex = regexp.MustCompile(`\(([a-zA-Z0-9]+) including waiting\)`)

func Register() {
	checker.RegisterChecker(config.CheckTypePodStartup, BuildPodStartupChecker)
}

func validateConfig(config *config.CheckerConfig) error {
	if config == nil {
		return fmt.Errorf("checker config is required") // should never happen
	}

	if config.PodStartupConfig == nil {
		return fmt.Errorf("pod startup checker config is required")
	}

	var errs []error
	if config.PodStartupConfig.SyntheticPodNamespace == "" {
		errs = append(errs, fmt.Errorf("pod startup checker config missing synthetic pod namespace"))
	}
	if len(apivalidation.ValidateNamespaceName(config.PodStartupConfig.SyntheticPodNamespace, false)) > 0 {
		errs = append(errs, fmt.Errorf("pod startup checker config synthetic pod namespace must be a valid k8s namespace name"))
	}

	if config.PodStartupConfig.SyntheticPodLabelKey == "" {
		errs = append(errs, fmt.Errorf("pod startup checker config missing synthetic pod label key"))
	}
	if len(utilvalidation.IsQualifiedName(config.PodStartupConfig.SyntheticPodLabelKey)) > 0 {
		errs = append(errs, fmt.Errorf("pod startup checker config synthetic pod label key must be a valid k8s label key"))
	}

	if config.Timeout <= config.PodStartupConfig.SyntheticPodStartupTimeout {
		errs = append(errs, fmt.Errorf("timeout must be greater than the maximum healthy pod startup duration (%s), got: %s",
			config.Timeout, config.PodStartupConfig.SyntheticPodStartupTimeout))
	}

	if config.PodStartupConfig.MaxSyntheticPods <= 0 {
		errs = append(errs, fmt.Errorf("pod startup checker config invalid max synthetic pods: %d, must be greater than 0", config.PodStartupConfig.MaxSyntheticPods))
	}

	return errors.Join(errs...)
}

// BuildPodStartupChecker creates a new PodStartupChecker instance.
func BuildPodStartupChecker(config *config.CheckerConfig) (checker.Checker, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid pod startup checker config: %w", err)
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
		timeout:      config.Timeout,
		k8sClientset: k8sClientset,
	}, nil
}

func (c *PodStartupChecker) Name() string {
	return c.name
}

func (c *PodStartupChecker) Type() config.CheckerType {
	return config.CheckTypePodStartup
}

// Run executes the pod startup checker logic. It creates synthetic pods to measure the startup time. The startup time is defined as the
// duration between the pod creation and the container running, minus the image pull duration (including waiting). If it is within the
// allowed limit, the checker is considered healthy. Otherwise, it is considered unhealthy. Before each run, the checker also attempts to
// garbage collect any leftover synthetic pods from previous runs that may not have been previously deleted due to errors or other issues.
func (c *PodStartupChecker) Run(ctx context.Context) (*types.Result, error) {
	// Garbage collect any leftover synthetic pods previously created by this checker.
	if err := c.garbageCollect(ctx); err != nil {
		// Logging instead of returning an error here to avoid failing the checker run.
		klog.Warningf("failed to garbage collect old synthetic pods: %s", err.Error())
	}

	// List pods to check the current number of synthetic pods. Do not run the checker if the maximum number of synthetic pods has been reached.
	pods, err := c.k8sClientset.CoreV1().Pods(c.config.SyntheticPodNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set(c.syntheticPodLabels())).String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}
	if len(pods.Items) >= c.config.MaxSyntheticPods {
		return nil, fmt.Errorf("maximum number of synthetic pods reached, current: %d, max allowed: %d, delete some pods before running the checker again",
			len(pods.Items), c.config.MaxSyntheticPods)
	}

	// Create a synthetic pod to measure the startup time.
	synthPod, err := c.k8sClientset.CoreV1().Pods(c.config.SyntheticPodNamespace).Create(ctx, c.generateSyntheticPod(), metav1.CreateOptions{})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.Unhealthy(errCodePodCreationTimeout, "timed out creating synthetic pod"), nil
		}
		return types.Unhealthy(errCodePodCreationError, fmt.Sprintf("error creating synthetic pod: %s", err)), nil
	}
	defer func() {
		err := c.k8sClientset.CoreV1().Pods(c.config.SyntheticPodNamespace).Delete(ctx, synthPod.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			// Logging instead of returning an error here to avoid failing the checker run.
			klog.Warningf("failed to delete synthetic pod %s: %s", synthPod.Name, err.Error())
		}
	}()

	podCreationToContainerRunningDuration, err := c.pollPodCreationToContainerRunningDuration(ctx, synthPod.Name)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.Unhealthy(errCodePodStartupDurationExceeded, "pod has no running container"), nil
		}
		return nil, fmt.Errorf("pod has no running container: %w", err)
	}
	imagePullDuration, err := c.getImagePullDuration(ctx, synthPod.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get image pull duration: %w", err)
	}

	// Calculate the pod startup duration. Round to the seconds place because that is the unit of the least precise measurement.
	podStartupDuration := (podCreationToContainerRunningDuration - imagePullDuration).Round(time.Second)
	if podStartupDuration >= c.config.SyntheticPodStartupTimeout {
		return types.Unhealthy(errCodePodStartupDurationExceeded, "pod exceeded the maximum healthy startup duration"), nil
	}

	return types.Healthy(), nil
}

// garbageCollect deletes all pods created by the checker that are older than the checker's timeout.
func (c *PodStartupChecker) garbageCollect(ctx context.Context) error {
	podList, err := c.k8sClientset.CoreV1().Pods(c.config.SyntheticPodNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set(c.syntheticPodLabels())).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to list pods for garbage collection: %w", err)
	}
	var errs []error
	for _, pod := range podList.Items {
		if !strings.HasPrefix(pod.Name, c.syntheticPodNamePrefix()) {
			// This pod is not a synthetic pod created by this checker, skip it.
			continue
		}
		if time.Since(pod.CreationTimestamp.Time) > c.timeout {
			err := c.k8sClientset.CoreV1().Pods(c.config.SyntheticPodNamespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("failed to delete old synthetic pod %s: %w", pod.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Returns the duration between the pod creation and the container running. This is precise to the second.
func (c *PodStartupChecker) pollPodCreationToContainerRunningDuration(ctx context.Context, podName string) (time.Duration, error) {
	var podCreationToContainerRunningDuration time.Duration
	err := wait.PollUntilContextCancel(ctx, pollingInterval, true, func(ctx context.Context) (bool, error) {
		pod, err := c.k8sClientset.CoreV1().Pods(c.config.SyntheticPodNamespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if len(pod.Status.ContainerStatuses) == 0 {
			return false, nil
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.State.Running != nil {
				containerRunningTime := status.State.Running.StartedAt.Time
				podCreationToContainerRunningDuration = containerRunningTime.Sub(pod.CreationTimestamp.Time)
				return true, nil
			}
		}
		return false, nil
	})
	return podCreationToContainerRunningDuration, err
}

// Returns the image pull duration including waiting time. This is precise to the millisecond.
func (c *PodStartupChecker) getImagePullDuration(ctx context.Context, podName string) (time.Duration, error) {
	events, err := c.k8sClientset.CoreV1().Events(c.config.SyntheticPodNamespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s,reason=Pulled", podName),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to list events for pod %s: %w", podName, err)
	}

	// events with reason=Pulled have messages expected to be in one of two formats:
	// 1. "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (426ms including waiting). Image size: 299513 bytes."
	// 2. "Container image \"k8s.gcr.io/pause:3.2\" already present on machine"
	for _, event := range events.Items {
		if strings.Contains(event.Message, "Successfully pulled image") {
			return c.parseImagePullDuration(event.Message)
		}
		if strings.Contains(event.Message, "already present on machine") {
			return 0, nil
		}
		// Logging instead of returning an error to avoid failing the checker run.
		klog.Warningf("Unexpected event message format for pod %s: %s\n", podName, event.Message)
	}
	return 0, fmt.Errorf("no image pull events found for pod %s", podName)
}

// Parses the image pull duration (including waiting) from a kubernetes event message expected to be in a format like:
// "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (426ms including waiting). Image size: 299513 bytes." or
// "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (1s34ms including waiting). Image size: 299513 bytes."
func (c *PodStartupChecker) parseImagePullDuration(message string) (time.Duration, error) {
	matches := imagePullDurationRegex.FindStringSubmatch(message)
	if len(matches) != 2 {
		return 0, fmt.Errorf("failed to extract image pull duration from event message: message in unexpected format: %s", message)
	}
	return time.ParseDuration(matches[1])
}

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

func (c *PodStartupChecker) generateSyntheticPod() *corev1.Pod {
	podName := fmt.Sprintf("%s%d", c.syntheticPodNamePrefix(), time.Now().UnixNano())
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   podName,
			Labels: c.syntheticPodLabels(),
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
