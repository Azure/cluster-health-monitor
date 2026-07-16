package checknodehealth

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// GPU (AzNHC) health check — PoC.
//
// This runs the upstream Azure HPC node health check container
// (github.com/Azure/azurehpc-health-checks, image mcr.microsoft.com/aznhc/aznhc-nv)
// as a separate privileged Pod on GPU nodes, triggered by the same CheckNodeHealth
// flow used for every node (provision / upgrade / reboot). The result is reported
// as an ADVISORY CheckResult named "GpuHealth" — it does not gate the node's overall
// Healthy verdict (see AdvisoryCheckResults).
//
// Scope (PoC): telemetry-only checks (no active benchmarks), single aggregate
// GpuHealth result derived from the AzNHC pod's terminal phase. See
// notes/gpu_health_checks/plan.md.

const (
	// GPUHealthResultName is the advisory CheckResult name reported for AzNHC GPU health.
	GPUHealthResultName = "GpuHealth"

	// DefaultAznhcImage is the upstream Azure HPC node health check container.
	// PoC: pulled directly from MCR.
	// TODO(carlosalv): post-PoC, consider forking/rebuilding into a private ACR.
	DefaultAznhcImage = "mcr.microsoft.com/aznhc/aznhc-nv:latest"

	// gpuResourceName is the extended resource advertised by the NVIDIA device plugin.
	gpuResourceName corev1.ResourceName = "nvidia.com/gpu"

	// gpuAcceleratorLabel is set by AKS on GPU node pools (e.g. value "nvidia").
	// It marks a node as a GPU SKU regardless of whether the device plugin is
	// installed yet, so we can tell "GPU node with device plugin missing" (fail)
	// from "not a GPU node" (skip).
	gpuAcceleratorLabel = "kubernetes.azure.com/accelerator"

	// aznhcPodNamePrefix is the prefix for the AzNHC health check pod.
	aznhcPodNamePrefix = "aznhc-"

	// aznhcPodKindLabel/Value mark the AzNHC pod so it can be distinguished from
	// the standard node-health-checker pod (both share the CheckNodeHealthLabel).
	aznhcPodKindLabel = "checknodehealth.clusterhealthmonitor.azure.com/pod-kind"
	aznhcPodKindValue = "aznhc"

	// AznhcTimeout bounds a full AzNHC run. It is deliberately much larger than
	// PodTimeout (30s), which is far too short for the ~7GB image pull (~6 min on
	// first pull) plus the telemetry run.
	AznhcTimeout = 15 * time.Minute

	// aznhcNhcTimeout is the per-check timeout passed to nhc itself (seconds).
	// The active benchmarks (nvbandwidth + 8-GPU NCCL all-reduce) can take 1-2 min.
	aznhcNhcTimeout = 900

	// GPU health check error codes (reported in CheckResult.ErrorCode).
	GPUErrCodeDevicePluginMissing = "GpuDevicePluginMissing"
	GPUErrCodeRunFailed           = "GpuHealthCheckFailed"
	GPUErrCodeTimeout             = "GpuHealthCheckTimeout"
)

// AdvisoryCheckResults are CheckResult names that are reported on the CR but
// deliberately excluded from the node's overall Healthy verdict. During the GPU
// PoC, GpuHealth is advisory only.
var AdvisoryCheckResults = map[string]bool{
	GPUHealthResultName: true,
}

// isAdvisoryResult reports whether a CheckResult name is advisory (excluded from
// the overall Healthy verdict).
func isAdvisoryResult(name string) bool {
	return AdvisoryCheckResults[name]
}

// isGPUNode reports whether the node is a GPU SKU node (via the AKS accelerator
// label). This is true even if the device plugin has not advertised GPUs yet,
// which lets preflight report a missing device plugin as a hard failure rather
// than silently skipping.
func isGPUNode(node *corev1.Node) bool {
	return node.Labels[gpuAcceleratorLabel] != ""
}

// hasAllocatableGPU reports whether the node advertises allocatable GPUs, i.e.
// the NVIDIA device plugin is installed and GPUs are schedulable.
func hasAllocatableGPU(node *corev1.Node) bool {
	q, ok := node.Status.Allocatable[gpuResourceName]
	return ok && !q.IsZero()
}

// gpuCount returns the number of allocatable GPUs advertised by the node.
func gpuCount(node *corev1.Node) int64 {
	q := node.Status.Allocatable[gpuResourceName]
	return q.Value()
}

// preflightGPU validates GPU prerequisites before running AzNHC. It returns a
// non-nil (failing) CheckResult when a prerequisite is missing, or nil when the
// node is ready to run the AzNHC health check.
//
// TODO(carlosalv): revisit whether some prerequisites (e.g. device plugin still
// installing on a freshly provisioned node) should be Unknown/skip instead of a
// hard fail to avoid false negatives during node bring-up.
func preflightGPU(node *corev1.Node) *chmv1alpha1.CheckResult {
	if !hasAllocatableGPU(node) {
		return &chmv1alpha1.CheckResult{
			Name:      GPUHealthResultName,
			Status:    chmv1alpha1.CheckStatusUnhealthy,
			ErrorCode: GPUErrCodeDevicePluginMissing,
			Message: "Node advertises 0 allocatable nvidia.com/gpu; the NVIDIA device plugin is not " +
				"installed. Use an AKS managed GPU node pool (--enable-managed-gpu=true) or install the device plugin.",
		}
	}
	return nil
}

// aznhcConf builds the NHC config for the given GPU count: passive telemetry
// checks plus the active GPU benchmarks (nvbandwidth + 8-GPU NCCL all-reduce).
// Thresholds are the H100 values from the upstream nd96isr_h100_v5 conf.
// TODO(carlosalv): source per-SKU thresholds / confs instead of this generic set.
// https://github.com/Azure/azurehpc-health-checks/tree/main/conf
func aznhcConf(gpus int64) string {
	return fmt.Sprintf(`* || export MARK_OFFLINE=1 NHC_CHECK_ALL=0
 * || check_gpu_count %d
 * || check_nvsmi_healthmon
 * || check_gpu_xid
 * || check_gpu_ecc 20000000 10000
 * || check_gpu_clock_throttling
 * || check_nvlink_status
 * || check_gpu_bw 48 335
 * || check_nccl_allreduce 460.0 1 16G /azure-nhc/topofiles/ndv5-topo.xml
`, gpus)
}

// aznhcCommand returns the container command that writes the conf, runs nhc,
// prints the log, and exits with nhc's return code so the pod phase
// (Succeeded/Failed) reflects health.
func aznhcCommand(gpus int64) string {
	conf := aznhcConf(gpus)
	return fmt.Sprintf(`set -o pipefail
cp /azure-nhc/customTests-cm/*.nhc /etc/nhc/scripts/ 2>/dev/null || true
cat >/tmp/aznhc.conf <<'AZNHC_EOF'
%sAZNHC_EOF
LOG=/tmp/aznhc.log
nhc -v -d -a CONFFILE=/tmp/aznhc.conf LOGFILE=$LOG TIMEOUT=%d
RC=$?
echo "=== nhc exited: $RC ==="
cat $LOG 2>/dev/null || true
echo "=== end log ==="
exit $RC
`, conf, aznhcNhcTimeout)
}

// generateAznhcPodName builds the AzNHC pod name for a CheckNodeHealth CR.
func generateAznhcPodName(cnh *chmv1alpha1.CheckNodeHealth) string {
	name := fmt.Sprintf("%s%s", aznhcPodNamePrefix, cnh.Name)
	if len(name) > maxPodNameLength {
		name = name[:maxPodNameLength]
	}
	return name
}

// buildAznhcPod builds the privileged AzNHC telemetry pod for a GPU node. The pod
// is pinned to the target node via NodeName, requests all allocatable GPUs, and
// mounts host /dev (+ syslog for the Xid check). It is owner-referenced to the CR
// so it is garbage-collected with the CR.
func (r *CheckNodeHealthReconciler) buildAznhcPod(cnh *chmv1alpha1.CheckNodeHealth, node *corev1.Node) (*corev1.Pod, error) {
	image := r.AznhcImage
	if image == "" {
		image = DefaultAznhcImage
	}
	gpus := gpuCount(node)
	privileged := true
	hostPathFile := corev1.HostPathFile
	hostPathDir := corev1.HostPathDirectory

	gpuQty := *resource.NewQuantity(gpus, resource.DecimalSI)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generateAznhcPodName(cnh),
			Namespace: r.CheckerPodNamespace,
			Labels: map[string]string{
				CheckNodeHealthLabel: cnh.Name,
				aznhcPodKindLabel:    aznhcPodKindValue,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeName:      cnh.Spec.NodeRef.Name,
			HostPID:       true,
			HostNetwork:   true,
			// NodeName pins the pod (bypassing the scheduler), but tolerate GPU
			// taints defensively so the kubelet admits it on tainted GPU pools.
			Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			Containers: []corev1.Container{
				{
					Name:    "aznhc",
					Image:   image,
					Command: []string{"/bin/bash", "-c", aznhcCommand(gpus)},
					SecurityContext: &corev1.SecurityContext{
						Privileged: &privileged,
					},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							gpuResourceName: gpuQty,
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "dev", MountPath: "/dev"},
						{Name: "shm", MountPath: "/dev/shm"},
						{Name: "syslog", MountPath: "/azure-nhc/syslog", ReadOnly: true},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "dev",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{Path: "/dev", Type: &hostPathDir},
					},
				},
				{
					// NCCL all-reduce overruns the default 64MB /dev/shm; back it with a
					// 16Gi tmpfs so check_nccl_allreduce can run.
					Name: "shm",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium:    corev1.StorageMediumMemory,
							SizeLimit: resource.NewQuantity(16*1024*1024*1024, resource.BinarySI),
						},
					},
				},
				{
					// Mounts the node syslog so check_gpu_xid can scan for Xid errors.
					// TODO(carlosalv): if the node has no /var/log/syslog the pod fails to
					// mount; make this optional / preflight it.
					Name: "syslog",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{Path: "/var/log/syslog", Type: &hostPathFile},
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(cnh, pod, r.Scheme); err != nil {
		return nil, err
	}
	return pod, nil
}

// aznhcResultFromPod derives the advisory GpuHealth CheckResult from the AzNHC
// pod's terminal state. Succeeded => Healthy; Failed => Unhealthy; a timeout =>
// Unhealthy with a timeout code.
// TODO(carlosalv): parse the AzNHC pod logs to split this into per-check results
// (GpuCount, GpuEcc, GpuXid, NvLink, ...) with the actual failure detail.
func aznhcResultFromPod(pod *corev1.Pod, timedOut bool) chmv1alpha1.CheckResult {
	switch {
	case timedOut:
		return chmv1alpha1.CheckResult{
			Name:      GPUHealthResultName,
			Status:    chmv1alpha1.CheckStatusUnhealthy,
			ErrorCode: GPUErrCodeTimeout,
			Message:   fmt.Sprintf("AzNHC did not complete within %s", AznhcTimeout),
		}
	case pod.Status.Phase == corev1.PodSucceeded:
		return chmv1alpha1.CheckResult{
			Name:    GPUHealthResultName,
			Status:  chmv1alpha1.CheckStatusHealthy,
			Message: "AzNHC checks passed",
		}
	case pod.Status.Phase == corev1.PodFailed:
		return chmv1alpha1.CheckResult{
			Name:      GPUHealthResultName,
			Status:    chmv1alpha1.CheckStatusUnhealthy,
			ErrorCode: GPUErrCodeRunFailed,
			Message:   "AzNHC reported a failing GPU health check",
		}
	default:
		return chmv1alpha1.CheckResult{
			Name:    GPUHealthResultName,
			Status:  chmv1alpha1.CheckStatusUnknown,
			Message: fmt.Sprintf("AzNHC pod in phase %q", pod.Status.Phase),
		}
	}
}

// reconcileGPUHealth drives the advisory AzNHC GPU health check for a GPU node.
// It is idempotent and returns done=true once a GpuHealth result has been (or
// already was) recorded, or when the node is not a GPU node. It returns
// done=false while the AzNHC pod is still running so the caller requeues.
//
// GPU node detection uses r.APIReader intentionally: the node controller's cache
// transformer strips node.Status (including Allocatable), so a cached read would
// hide the GPU resource. APIReader bypasses the cache.
func (r *CheckNodeHealthReconciler) reconcileGPUHealth(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) (bool, error) {
	// Idempotent: a result was already recorded (e.g. preflight fail or a prior pass).
	if found, _ := r.findResult(cnh, GPUHealthResultName); found {
		return true, nil
	}

	node := &corev1.Node{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: cnh.Spec.NodeRef.Name}, node); err != nil {
		if apierrors.IsNotFound(err) {
			// Node is gone; nothing to check.
			return true, nil
		}
		return false, fmt.Errorf("failed to get node %s for GPU health check: %w", cnh.Spec.NodeRef.Name, err)
	}

	// Only GPU SKU nodes get an AzNHC check; everything else is skipped silently.
	if !isGPUNode(node) {
		return true, nil
	}

	// Preflight — a missing prerequisite is a hard FAIL.
	if res := preflightGPU(node); res != nil {
		klog.InfoS("GPU preflight failed", "cr", cnh.Name, "node", node.Name, "errorCode", res.ErrorCode)
		return true, r.markGPUResult(ctx, cnh, *res)
	}

	// Ensure the AzNHC pod exists (this starts the run / image pull).
	pod, err := r.ensureAznhcPod(ctx, cnh, node)
	if err != nil {
		return false, err
	}

	// Terminal phase => record the result and finish.
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		res := aznhcResultFromPod(pod, false)
		// Embed a per-check summary parsed from the AzNHC log so the detail lives on
		// the CR itself (the pod is deleted right after, so its logs are ephemeral).
		if logs, err := r.readAznhcLogs(ctx, pod.Namespace, pod.Name); err != nil {
			klog.ErrorS(err, "Failed to read AzNHC pod logs; recording result without detail", "pod", pod.Name)
		} else if summary := summarizeAznhcLog(logs); summary != "" {
			res.Message = summary
		}
		return true, r.markGPUResult(ctx, cnh, res)
	}

	// Timeout (own budget, NOT the 30s PodTimeout).
	if time.Since(pod.CreationTimestamp.Time) > AznhcTimeout {
		klog.InfoS("AzNHC pod timed out", "cr", cnh.Name, "pod", pod.Name, "timeout", AznhcTimeout)
		return true, r.markGPUResult(ctx, cnh, aznhcResultFromPod(pod, true))
	}

	klog.InfoS("AzNHC still running, deferring completion", "cr", cnh.Name, "pod", pod.Name, "phase", pod.Status.Phase)
	return false, nil
}

// ensureAznhcPod returns the AzNHC pod for the CR, creating it if it does not exist.
func (r *CheckNodeHealthReconciler) ensureAznhcPod(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, node *corev1.Node) (*corev1.Pod, error) {
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(r.CheckerPodNamespace),
		client.MatchingLabels{CheckNodeHealthLabel: cnh.Name, aznhcPodKindLabel: aznhcPodKindValue},
	}
	if err := r.List(ctx, podList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list AzNHC pods: %w", err)
	}
	if len(podList.Items) > 0 {
		return &podList.Items[0], nil
	}

	pod, err := r.buildAznhcPod(cnh, node)
	if err != nil {
		return nil, fmt.Errorf("failed to build AzNHC pod: %w", err)
	}
	klog.InfoS("Creating AzNHC pod", "pod", pod.Name, "node", cnh.Spec.NodeRef.Name, "image", pod.Spec.Containers[0].Image)
	if err := r.Create(ctx, pod); err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing := &corev1.Pod{}
			if gerr := r.Get(ctx, client.ObjectKey{Name: pod.Name, Namespace: pod.Namespace}, existing); gerr != nil {
				return nil, gerr
			}
			return existing, nil
		}
		return nil, fmt.Errorf("failed to create AzNHC pod: %w", err)
	}

	created := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{Name: pod.Name, Namespace: pod.Namespace}, created); err != nil {
		return nil, fmt.Errorf("failed to get created AzNHC pod: %w", err)
	}
	return created, nil
}

// markGPUResult records/updates the advisory GpuHealth result on the CR status.
func (r *CheckNodeHealthReconciler) markGPUResult(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, result chmv1alpha1.CheckResult) error {
	r.updateCheckResult(cnh, result)
	if err := r.Status().Update(ctx, cnh); err != nil {
		return fmt.Errorf("failed to update GpuHealth result: %w", err)
	}
	klog.InfoS("GpuHealth result recorded", "cr", cnh.Name, "status", result.Status, "errorCode", result.ErrorCode, "message", result.Message)
	return nil
}

// readAznhcLogs returns the AzNHC pod's logs. Returns "" (no error) when no
// clientset is configured.
func (r *CheckNodeHealthReconciler) readAznhcLogs(ctx context.Context, namespace, name string) (string, error) {
	if r.ClientSet == nil {
		return "", nil
	}
	stream, err := r.ClientSet.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{}).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to stream AzNHC pod logs: %w", err)
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("failed to read AzNHC pod logs: %w", err)
	}
	return string(data), nil
}

var aznhcGBpsRe = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?\s*GB/s`)

// summarizeAznhcLog parses an nhc run log into a compact, self-contained summary
// of which checks passed/failed (plus any measured GB/s), suitable for the
// GpuHealth result message. Returns "" if nothing recognizable was found.
//
// nhc log lines look like:
//
//	SUCCESS:  nhc:  Health check passed:  check_gpu_count: Expected 8 and found 8
//	ERROR:  nhc:  Health check failed:  check_X: <reason>
func summarizeAznhcLog(logs string) string {
	var passedOrder, failedOrder []string
	passed := map[string]bool{}
	failed := map[string]bool{}
	metrics := map[string]string{}

	for _, line := range strings.Split(logs, "\n") {
		if name, ok := aznhcCheckName(line, "Health check passed:"); ok {
			if !passed[name] {
				passed[name] = true
				passedOrder = append(passedOrder, name)
			}
			if m := aznhcGBpsRe.FindString(line); m != "" {
				metrics[name] = strings.ReplaceAll(m, " ", "")
			}
			continue
		}
		if name, ok := aznhcCheckName(line, "Health check failed:"); ok {
			if !failed[name] {
				failed[name] = true
				failedOrder = append(failedOrder, name)
			}
		}
	}

	var b strings.Builder
	if len(failedOrder) > 0 {
		fmt.Fprintf(&b, "FAILED: %s. ", strings.Join(failedOrder, ", "))
	}
	if len(passedOrder) > 0 {
		fmt.Fprintf(&b, "passed: %s", strings.Join(passedOrder, ", "))
	}
	if len(metrics) > 0 {
		keys := make([]string, 0, len(metrics))
		for k := range metrics {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+metrics[k])
		}
		fmt.Fprintf(&b, " [%s]", strings.Join(parts, ", "))
	}
	return strings.TrimSpace(b.String())
}

// aznhcCheckName extracts the check_* name that follows the given marker in an
// nhc log line, e.g. marker "Health check passed:" -> "check_gpu_count".
func aznhcCheckName(line, marker string) (string, bool) {
	idx := strings.Index(line, marker)
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimSpace(line[idx+len(marker):])
	if c := strings.IndexByte(rest, ':'); c >= 0 {
		rest = rest[:c]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	return rest, true
}
