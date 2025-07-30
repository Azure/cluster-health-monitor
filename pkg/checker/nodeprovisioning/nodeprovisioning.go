package nodeprovisioning

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	"k8s.io/klog/v2"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

// Dialer is an interface for making network connections
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

const (
	// syntheticPodImage is the hardcoded container image used for synthetic pods in pod-to-pod communication testing.
	syntheticPodImage = "mcr.microsoft.com/azurelinux/base/nginx:1.25.4-4-azl3.0.20250702"

	// syntheticPodPort is the hardcoded TCP port that synthetic pods listen on for connectivity testing.
	syntheticPodPort = 80
)

type NodeProvisioningChecker struct {
	name         string
	config       *config.NodeProvisioningConfig
	timeout      time.Duration
	k8sClientset kubernetes.Interface
	dialer       Dialer
}

// How often to poll the pod status to check if the container is running.
var pollingInterval = 1 * time.Second // used for unit tests

// The regular expression used to parse the image pull duration from a k8s event message for successfully pulling an image.
var imagePullDurationRegex = regexp.MustCompile(`\(([a-zA-Z0-9\.]+) including waiting\)`)

func Register() {
	checker.RegisterChecker(config.CheckTypeNodeProvisioning, BuildNodeProvisioningChecker)
}

// BuildNodeProvisioningChecker creates a new NodeProvisioningChecker instance.
func BuildNodeProvisioningChecker(config *config.CheckerConfig, kubeClient kubernetes.Interface) (checker.Checker, error) {
	chk := &NodeProvisioningChecker{
		name:         config.Name,
		config:       config.NodeProvisioningConfig,
		timeout:      config.Timeout,
		k8sClientset: kubeClient,
		dialer: &net.Dialer{
			Timeout: config.NodeProvisioningConfig.TCPTimeout,
		},
	}
	klog.InfoS("Built NodeProvisioningChecker",
		"name", chk.name,
		"config", chk.config,
		"timeout", chk.timeout.String(),
	)
	return chk, nil
}

func (c *NodeProvisioningChecker) Name() string {
	return c.name
}

func (c *NodeProvisioningChecker) Type() config.CheckerType {
	return config.CheckTypeNodeProvisioning
}

// Run executes the node provisioning checker logic. It creates synthetic pods to measure the startup time. The startup time is defined as the
// duration between the pod creation and the container running, minus the image pull duration (including waiting). If it is within the
// allowed limit, the checker is considered healthy. Otherwise, it is considered unhealthy. Before each run, the checker also attempts to
// garbage collect any leftover synthetic pods from previous runs that may not have been previously deleted due to errors or other issues.
func (c *NodeProvisioningChecker) Run(ctx context.Context) (*types.Result, error) {
	// Garbage collect any leftover synthetic pods previously created by this checker.
	if err := c.garbageCollect(ctx); err != nil {
		// Logging instead of returning an error here to avoid failing the checker run.
		klog.ErrorS(err, "Failed to garbage collect old synthetic pods")
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
			klog.ErrorS(err, "Failed to delete synthetic pod", "name", synthPod.Name)
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

	// perform pod communication check - get pod IP and create TCP connection
	podIP, err := c.getSyntheticPodIP(ctx, synthPod.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get synthetic pod IP: %w", err)
	}

	err = c.createTCPConnection(ctx, podIP)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.Unhealthy(errCodeRequestTimeout, "TCP request to synthetic pod timed out"), nil
		}
		return types.Unhealthy(errCodeRequestFailed, fmt.Sprintf("TCP request to synthetic pod failed: %s", err)), nil
	}

	return types.Healthy(), nil
}

// garbageCollect deletes all pods created by the checker that are older than the checker's timeout.
func (c *NodeProvisioningChecker) garbageCollect(ctx context.Context) error {
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

// Returns the duration between the pod creation and the container running. This is precise to the second.
func (c *NodeProvisioningChecker) pollPodCreationToContainerRunningDuration(ctx context.Context, podName string) (time.Duration, error) {
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
func (c *NodeProvisioningChecker) getImagePullDuration(ctx context.Context, podName string) (time.Duration, error) {
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
		klog.InfoS("Unexpected event message format for pod", "name", podName, "message", event.Message)
	}
	return 0, fmt.Errorf("no image pull events found for pod %s", podName)
}

// Parses the image pull duration (including waiting) from a kubernetes event message expected to be in a format like:
// "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (426ms including waiting). Image size: 299513 bytes." or
// "Successfully pulled image \"k8s.gcr.io/pause:3.2\" in 426ms (1s34ms including waiting). Image size: 299513 bytes."
func (c *NodeProvisioningChecker) parseImagePullDuration(message string) (time.Duration, error) {
	matches := imagePullDurationRegex.FindStringSubmatch(message)
	if len(matches) != 2 {
		return 0, fmt.Errorf("failed to extract image pull duration from event message: message in unexpected format: %s", message)
	}
	return time.ParseDuration(matches[1])
}

func (c *NodeProvisioningChecker) syntheticPodLabels() map[string]string {
	return map[string]string{
		// c.name is supposed to be a unique identifier for each checker. Using this as the label value to ensure that synthetic pods
		// created by different checkers do not conflict with each other.
		c.config.SyntheticPodLabelKey: c.name,
	}
}

func (c *NodeProvisioningChecker) syntheticPodNamePrefix() string {
	// The synthetic pod name prefix is used as an additional safety measure to ensure that the checker only operates on its own synthetic pods.
	// c.name is supposed to be a unique identifier for each checker, so this prefix should be unique across all checkers.
	return strings.ToLower(fmt.Sprintf("%s-synthetic-", c.name))
}

func (c *NodeProvisioningChecker) generateSyntheticPod() *corev1.Pod {
	podName := fmt.Sprintf("%s%d", c.syntheticPodNamePrefix(), time.Now().UnixNano())

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
		// TODOcarlosalv: Add pod cpu/memory requests and/or limits.
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
func (c *NodeProvisioningChecker) getSyntheticPodIP(ctx context.Context, podName string) (string, error) {
	pod, err := c.k8sClientset.CoreV1().Pods(c.config.SyntheticPodNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("error getting pod %s: %w", podName, err)
	}
	if pod.Status.PodIP == "" {
		return "", fmt.Errorf("pod IP is empty")
	}
	return pod.Status.PodIP, nil
}

// createTCPConnection makes a simple TCP connection to the pod IP
func (c *NodeProvisioningChecker) createTCPConnection(ctx context.Context, podIP string) error {
	address := fmt.Sprintf("%s:%s", podIP, strconv.Itoa(syntheticPodPort))

	conn, err := c.dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("TCP connection failed: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			klog.ErrorS(err, "Failed to close TCP connection", "podIP", podIP)
		}
	}()

	return nil
}
