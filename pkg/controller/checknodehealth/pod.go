package checknodehealth

import (
	"context"
	"fmt"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/cnhstatus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// maxPodNameLength is the maximum allowed length for Kubernetes pod names
	maxPodNameLength = 253
	// podNamePrefix is the prefix used for health check pod names
	// TODO: rename the prefix to "check-node-health-"
	podNamePrefix = "check-node-health-"
)

func (r *CheckNodeHealthReconciler) cleanupPod(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) error {
	// Find all pods with the specific label that matches this CR
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(r.CheckerPodNamespace),
		client.MatchingLabels{CheckNodeHealthLabel: cnh.Name},
	}

	if err := r.List(ctx, podList, listOpts...); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	// Delete all matching pods
	for _, pod := range podList.Items {
		klog.InfoS("Deleting health check pod", "pod", pod.Name, "cr", cnh.Name)
		if err := client.IgnoreNotFound(r.Delete(ctx, &pod)); err != nil {
			klog.ErrorS(err, "Failed to delete pod", "pod", pod.Name)
			return fmt.Errorf("failed to delete pod %s: %w", pod.Name, err)
		}
	}

	if len(podList.Items) > 0 {
		klog.InfoS("Cleaned up health check pods", "count", len(podList.Items), "cr", cnh.Name)
	}

	return nil
}

func (r *CheckNodeHealthReconciler) ensureHealthCheckPod(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, info gpuNodeInfo) (*corev1.Pod, error) {
	// Check if pods already exist using label selector
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(r.CheckerPodNamespace),
		client.MatchingLabels{CheckNodeHealthLabel: cnh.Name},
	}

	if err := r.List(ctx, podList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list existing pods: %w", err)
	}

	if len(podList.Items) > 0 {
		// Pod already exists, return the first one
		pod := &podList.Items[0]
		if len(podList.Items) > 1 {
			klog.InfoS("Multiple health check pods found, using first one", "count", len(podList.Items))
		}
		klog.InfoS("Health check pod already exists", "pod", pod.Name)
		return pod, nil
	}

	// Create the pod
	pod, err := r.buildHealthCheckPod(cnh, info)
	if err != nil {
		return nil, fmt.Errorf("failed to build health check pod: %w", err)
	}

	klog.InfoS("Creating health check pod", "pod", pod.Name, "node", cnh.Spec.NodeRef.Name)
	if err := r.Create(ctx, pod); err != nil {
		return nil, fmt.Errorf("failed to create pod: %w", err)
	}

	// Refetch the pod to get the CreationTimestamp set by the API server
	createdPod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{Name: pod.Name, Namespace: pod.Namespace}, createdPod); err != nil {
		return nil, fmt.Errorf("failed to get created pod: %w", err)
	}

	return createdPod, nil
}

func (r *CheckNodeHealthReconciler) buildHealthCheckPod(cnh *chmv1alpha1.CheckNodeHealth, info gpuNodeInfo) (*corev1.Pod, error) {
	podName := generateHealthCheckPodName(cnh)
	labels := map[string]string{
		CheckNodeHealthLabel: cnh.Name,
	}

	// Determine service account name from annotation or use default
	serviceAccountName := DefaultCheckerServiceAccount
	if sa, ok := cnh.Annotations[AnnotationCheckerServiceAccount]; ok && sa != "" {
		serviceAccountName = sa
	}

	container := corev1.Container{
		Name:    "node-health-checker",
		Image:   r.CheckerPodImage,
		Command: []string{"/nodechecker"},
		Args:    []string{fmt.Sprintf("--name=%s", cnh.Name)},
	}

	// NoExecute taints are enforced by the node lifecycle controller even when NodeName is
	// pre-set (bypassing the scheduler), so an explicit toleration is required to prevent
	// eviction from nodes tainted with CriticalAddonsOnly.
	tolerations := []corev1.Toleration{
		{
			Key:      "CriticalAddonsOnly",
			Operator: corev1.TolerationOpEqual,
			Value:    "true",
			Effect:   corev1.TaintEffectNoExecute,
		},
	}
	var volumes []corev1.Volume

	if info.isGPUNode {
		applyGPUCheckerShape(&container, &tolerations, &volumes, r.GPUCheckerPodImage, info)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: r.CheckerPodNamespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: serviceAccountName,
			RestartPolicy:      corev1.RestartPolicyNever,
			NodeName:           cnh.Spec.NodeRef.Name, // Schedule on specific node
			Tolerations:        tolerations,
			Volumes:            volumes,
			Containers:         []corev1.Container{container},
		},
	}

	// Set CheckNodeHealth as owner reference to establish parent-child relationship
	// This enables automatic pod cleanup when the CheckNodeHealth CR is deleted (garbage collection)
	// and allows the controller to receive pod events for reconciliation
	if err := controllerutil.SetControllerReference(cnh, pod, r.Scheme); err != nil {
		// This shouldn't fail in normal circumstances, but if it does,
		// we'll return an error rather than creating a pod without proper ownership
		return nil, err
	}

	return pod, nil
}

// applyGPUCheckerShape switches the checker pod to the GPU image and gives it what the intrusive
// benchmarks need. GPU devices and driver libraries are injected by the device plugin and the
// NVIDIA container runtime, so no elevated privileges are required.
func applyGPUCheckerShape(container *corev1.Container, tolerations *[]corev1.Toleration, volumes *[]corev1.Volume, image string, info gpuNodeInfo) {
	container.Image = image
	container.Args = append(container.Args,
		"--enable-gpu-checks",
		fmt.Sprintf("--sku=%s", info.sku),
		fmt.Sprintf("--gpu-count=%d", info.gpuCount),
	)

	allowPrivilegeEscalation := false
	runAsNonRoot := true
	uid := checkerUID
	container.SecurityContext = &corev1.SecurityContext{
		AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		RunAsNonRoot:             &runAsNonRoot,
		RunAsUser:                &uid,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "dshm", MountPath: "/dev/shm"})

	if info.gpuCount > 0 {
		// Requesting every GPU on the node keeps customer GPU workloads from scheduling
		// alongside the benchmarks. Extended resources require request == limit.
		quantity := resource.NewQuantity(info.gpuCount, resource.DecimalSI)
		container.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{GPUResourceName: *quantity},
			Limits:   corev1.ResourceList{GPUResourceName: *quantity},
		}
	} else {
		// Driver-only pool: no device plugin advertises GPUs, so ask the NVIDIA container
		// runtime to expose them directly.
		container.Env = append(container.Env,
			corev1.EnvVar{Name: "NVIDIA_VISIBLE_DEVICES", Value: "all"},
			corev1.EnvVar{Name: "NVIDIA_DRIVER_CAPABILITIES", Value: "compute,utility"},
		)
	}

	// GPU nodes commonly carry scheduling and NoExecute taints that would otherwise evict the
	// NodeName-pinned pod.
	*tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}

	shmSize := resource.MustParse(gpuShmSize)
	*volumes = append(*volumes, corev1.Volume{
		Name: "dshm",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				Medium:    corev1.StorageMediumMemory,
				SizeLimit: &shmSize,
			},
		},
	})
}

func (r *CheckNodeHealthReconciler) updatePodstartCheckerResult(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, pod *corev1.Pod, timeout time.Duration) error {
	// PodStartup checker evaluates whether containers can successfully start on the node.

	// Case 1: All containers have started successfully
	// - Containers are running OR have terminated after starting (e.g., CrashLoopBackOff)
	// - Even if containers crash after starting, PodStartup is Healthy because the container
	//   runtime successfully started them. The crash is an application(Checker) issue, not a node issue.
	if r.areAllContainersStarted(pod) {
		return r.markPodStartupResult(ctx, cnh, chmv1alpha1.CheckStatusHealthy, "All containers started successfully")
	}

	// Case 2: Pod timeout
	// - We cannot rely on container status to detect startup failures because the container runtime
	//   will automatically retry failed containers (e.g., image pull failures, resource constraints)
	// - The only reliable way to detect pod startup failure is by waiting for a timeout
	// - If the pod remains in Pending state beyond the timeout, it indicates a persistent node-level
	//   issue preventing container startup
	if pod.Status.Phase == corev1.PodPending && isPodTimeout(pod, timeout) {
		return r.markPodStartupResult(ctx, cnh, chmv1alpha1.CheckStatusUnhealthy, "Pod stuck in Pending state - timeout exceeded")
	}

	// Case 3: Still initializing or container runtime is retrying
	// - Containers are being pulled, created, or retried by the runtime
	// - No action needed yet, wait for containers to start or timeout
	return nil
}

// areAllContainersStarted checks if all containers have started successfully
func (r *CheckNodeHealthReconciler) areAllContainersStarted(pod *corev1.Pod) bool {
	// If no container statuses available yet, containers haven't started
	if len(pod.Status.ContainerStatuses) == 0 {
		return false
	}

	// Check each container status
	for _, containerStatus := range pod.Status.ContainerStatuses {
		// Container has started if it's currently running OR if it terminated after starting
		hasStarted := containerStatus.State.Running != nil ||
			(containerStatus.State.Terminated != nil && !containerStatus.State.Terminated.StartedAt.IsZero())

		if !hasStarted {
			return false
		}
	}
	return true
}

// markPodStartupResult marks the CheckNodeHealth with a PodStartup check result
func (r *CheckNodeHealthReconciler) markPodStartupResult(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, status chmv1alpha1.CheckStatus, message string) error {
	result := chmv1alpha1.CheckResult{
		Name:    "PodStartup",
		Status:  status,
		Message: message,
	}

	// Checker pods write into the same results list, so this has to be an upsert under
	// optimistic concurrency rather than an update of the object read at reconcile start.
	if err := cnhstatus.UpsertResults(ctx, r.Client, cnh.Name, result); err != nil {
		return fmt.Errorf("failed to update CheckNodeHealth status: %w", err)
	}
	cnhstatus.UpsertResult(&cnh.Status, result)

	klog.InfoS("PodStartup check result recorded", "cr", cnh.Name, "status", status, "message", message)
	return nil
}

// isPodTimeout checks if the pod has been running for too long without completing
func isPodTimeout(pod *corev1.Pod, timeout time.Duration) bool {
	return time.Since(pod.CreationTimestamp.Time) > timeout
}

func generateHealthCheckPodName(cnh *chmv1alpha1.CheckNodeHealth) string {
	desiredName := fmt.Sprintf("%s%s", podNamePrefix, cnh.Name)

	// If the name is too long, truncate it
	if len(desiredName) > maxPodNameLength {
		desiredName = desiredName[:maxPodNameLength]
	}

	return desiredName
}
