package checknodehealth

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
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

const (
	// GPUResourceName is the extended resource name for NVIDIA GPUs.
	GPUResourceName corev1.ResourceName = "nvidia.com/gpu"

	// DefaultGPUCheckTimeout bounds a GPU intrusive check when the NodeCheckSpec does not
	// set one. Intrusive diagnostics are long-running compared to the standard PodTimeout.
	DefaultGPUCheckTimeout = 15 * time.Minute

	// gpuCheckPodNamePrefix is the prefix used for GPU intrusive check pod names.
	gpuCheckPodNamePrefix = "gpu-check-"

	// NodeCheckLabel identifies which NodeCheckSpec (by name) a check pod belongs to.
	NodeCheckLabel = "clusterhealthmonitor.azure.com/nodecheck"

	// gpuShmSize sizes /dev/shm, which NCCL needs for intra-node transports.
	gpuShmSize = "8Gi"

	// nvidiaAcceleratorLabel and legacyAcceleratorLabel mark a node as having NVIDIA GPUs
	// even when the device plugin is absent (e.g. driver-only pools) and so no
	// nvidia.com/gpu is advertised as allocatable.
	nvidiaAcceleratorLabel = "kubernetes.azure.com/accelerator"
	legacyAcceleratorLabel = "accelerator"

	// instanceTypeLabel carries the node's VM SKU, used to select a bandwidth threshold.
	instanceTypeLabel = "node.kubernetes.io/instance-type"

	// ncclSubCheckName is the sub-check reported for the NCCL all-reduce test.
	ncclSubCheckName = "NcclAllReduce"

	// nvbandwidth testcase names, mirroring AzNHC's check_gpu_bw selection.
	nvbwHostToDevice   = "host_to_device_memcpy_ce"
	nvbwDeviceToHost   = "device_to_host_memcpy_ce"
	nvbwDeviceToDevice = "device_to_device_memcpy_read_ce"

	// checkerUID is the non-root user the GPU checks image runs as.
	checkerUID int64 = 65532

	// Error codes recorded on GPU check results.
	ErrorCodeTimeout          = "Timeout"
	ErrorCodeNotGPUNode       = "NotGPUNode"
	ErrorCodeGPUCheckFailed   = "GpuCheckFailed"
	ErrorCodeImageNotSet      = "GpuCheckImageNotConfigured"
	ErrorCodeNcclCorrectness  = "NcclCorrectnessError"
	ErrorCodeNcclLowBandwidth = "NcclBandwidthBelowThreshold"
	ErrorCodeGpuLowBandwidth  = "GpuBandwidthBelowThreshold"
)

// gpuThresholds are the per-SKU pass/fail bandwidth floors, mirroring the AzNHC conf values
// (check_nccl_allreduce <nccl>, check_gpu_bw <pcie> <p2p>).
type gpuThresholds struct {
	NcclBusGBps float64 // NCCL all-reduce bus bandwidth
	PCIeGBps    float64 // host<->device memcpy (applies to both directions)
	P2PGBps     float64 // device-to-device memcpy read
}

// gpuThresholdsBySKU is keyed by normalizeSKU. SKUs absent from this map are measured and
// reported without a bandwidth verdict.
var gpuThresholdsBySKU = map[string]gpuThresholds{
	// From AzNHC nd96isr_h100_v5.conf: `check_nccl_allreduce 460.0 ...` and `check_gpu_bw 48 335`.
	"nd96isr_h100_v5": {NcclBusGBps: 460.0, PCIeGBps: 48.0, P2PGBps: 335.0},
}

// normalizeSKU lowercases a VM size and strips the "standard_" prefix so both the node label
// form (Standard_ND96isr_H100_v5) and the bare form resolve to the same key.
func normalizeSKU(sku string) string {
	return strings.TrimPrefix(strings.ToLower(sku), "standard_")
}

// gpuThresholdsFor returns the configured thresholds for a SKU.
func gpuThresholdsFor(sku string) (gpuThresholds, bool) {
	v, ok := gpuThresholdsBySKU[normalizeSKU(sku)]
	return v, ok
}

// buildGPUCheckPod builds an unprivileged GPU intrusive check pod for a single NodeCheckSpec,
// pinned to the target node. It runs the nccl-tests all-reduce benchmark. gpuCount is the
// number of nvidia.com/gpu the pod requests so it holds the whole node's GPUs for the duration
// of the check (isolation); GPU devices and driver libraries are injected by the NVIDIA device
// plugin, so no elevated privileges are needed. The requester's Image overrides the
// controller-configured default, and Args override the default all-reduce sweep.
func (r *CheckNodeHealthReconciler) buildGPUCheckPod(cnh *chmv1alpha1.CheckNodeHealth, check *chmv1alpha1.NodeCheckSpec, gpuCount int64) (*corev1.Pod, error) {
	image := check.Image
	if image == "" {
		image = r.GPUCheckImage
	}

	labels := map[string]string{
		CheckNodeHealthLabel: cnh.Name,
		NodeCheckLabel:       check.Name,
	}

	// When the device plugin advertises GPUs we pin the count; otherwise the entrypoint
	// detects it with nvidia-smi, which is the only reliable source on driver-only pools.
	var env []corev1.EnvVar
	if gpuCount > 0 {
		env = append(env, corev1.EnvVar{Name: "NGPUS", Value: strconv.FormatInt(gpuCount, 10)})
	}
	// Only override the image defaults when the requester supplied args.
	if len(check.Args) > 0 {
		env = append(env, corev1.EnvVar{Name: "NCCL_ARGS", Value: strings.Join(check.Args, " ")})
	}

	allowPrivilegeEscalation := false
	runAsNonRoot := true
	uid := checkerUID
	container := corev1.Container{
		Name:  "gpu-health-checker",
		Image: image,
		Env:   env,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			RunAsNonRoot:             &runAsNonRoot,
			RunAsUser:                &uid,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		// NCCL uses shared memory for intra-node transports; the default 64Mi is too small.
		VolumeMounts: []corev1.VolumeMount{
			{Name: "dshm", MountPath: "/dev/shm"},
		},
	}

	// Request (and limit) all of the node's GPUs so no customer GPU workload can schedule
	// alongside the check. Extended resources require request == limit.
	if gpuCount > 0 {
		quantity := resource.NewQuantity(gpuCount, resource.DecimalSI)
		container.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{GPUResourceName: *quantity},
			Limits:   corev1.ResourceList{GPUResourceName: *quantity},
		}
	} else {
		// No device plugin advertises GPUs on this node; ask the NVIDIA container runtime
		// to expose them so the check can still attempt to run.
		container.Env = append(container.Env,
			corev1.EnvVar{Name: "NVIDIA_VISIBLE_DEVICES", Value: "all"},
			corev1.EnvVar{Name: "NVIDIA_DRIVER_CAPABILITIES", Value: "compute,utility"},
		)
	}

	shmSize := resource.MustParse(gpuShmSize)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generateGPUCheckPodName(cnh, check),
			Namespace: r.CheckerPodNamespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeName:      cnh.Spec.NodeRef.Name, // Pin to the target node, bypassing the scheduler.
			// Tolerate all taints: the pod is NodeName-pinned, and GPU nodes commonly carry
			// scheduling and NoExecute taints (e.g. nvidia.com/gpu, CriticalAddonsOnly) that
			// would otherwise keep it off the node or evict it.
			Tolerations: []corev1.Toleration{
				{Operator: corev1.TolerationOpExists},
			},
			Volumes: []corev1.Volume{
				{
					Name: "dshm",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium:    corev1.StorageMediumMemory,
							SizeLimit: &shmSize,
						},
					},
				},
			},
			Containers: []corev1.Container{container},
		},
	}

	if err := controllerutil.SetControllerReference(cnh, pod, r.Scheme); err != nil {
		return nil, err
	}

	return pod, nil
}

// generateGPUCheckPodName returns a unique, length-bounded pod name for a GPU check.
func generateGPUCheckPodName(cnh *chmv1alpha1.CheckNodeHealth, check *chmv1alpha1.NodeCheckSpec) string {
	name := fmt.Sprintf("%s%s-%s", gpuCheckPodNamePrefix, cnh.Name, strings.ToLower(check.Name))
	if len(name) > maxPodNameLength {
		name = name[:maxPodNameLength]
	}
	return name
}

// gpuCheckTimeout returns the effective timeout for a GPU check, using the request's Timeout
// when set and the profile default otherwise.
func gpuCheckTimeout(check *chmv1alpha1.NodeCheckSpec) time.Duration {
	if check.Timeout != nil {
		return check.Timeout.Duration
	}
	return DefaultGPUCheckTimeout
}

// gpuChecks returns the GPUIntrusive checks requested on the CR.
func gpuChecks(cnh *chmv1alpha1.CheckNodeHealth) []chmv1alpha1.NodeCheckSpec {
	var out []chmv1alpha1.NodeCheckSpec
	for _, c := range cnh.Spec.Checks {
		if c.Profile == chmv1alpha1.NodeCheckProfileGPUIntrusive {
			out = append(out, c)
		}
	}
	return out
}

// reconcileGPUChecks ensures a pod per requested GPUIntrusive check, records terminal results
// onto the CR status, and reports whether every requested GPU check has completed.
func (r *CheckNodeHealthReconciler) reconcileGPUChecks(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth) (bool, error) {
	checks := gpuChecks(cnh)
	if len(checks) == 0 {
		return true, nil
	}

	gpuCount, isGPUNode, sku, err := r.nodeGPUInfo(ctx, cnh.Spec.NodeRef.Name)
	if err != nil {
		return false, err
	}

	statusChanged := false
	var podsToDelete []string

	for i := range checks {
		check := &checks[i]
		if found, _ := r.findResult(cnh, check.Name); found {
			continue // Result already recorded; do not recreate the pod.
		}

		// Only skip genuinely non-GPU nodes. A GPU node without allocatable GPUs (e.g. a
		// driver-only pool with no device plugin) still attempts the run; any GPU access
		// failure then surfaces in the pod logs.
		if !isGPUNode {
			r.updateCheckResult(cnh, chmv1alpha1.CheckResult{
				Name:      check.Name,
				Status:    chmv1alpha1.CheckStatusUnknown,
				ErrorCode: ErrorCodeNotGPUNode,
				Message:   "node is not a GPU node (no allocatable nvidia.com/gpu and no nvidia accelerator label)",
			})
			statusChanged = true
			continue
		}

		// Without a configured image there is nothing to run; record it terminally.
		if check.Image == "" && r.GPUCheckImage == "" {
			r.updateCheckResult(cnh, chmv1alpha1.CheckResult{
				Name:      check.Name,
				Status:    chmv1alpha1.CheckStatusUnknown,
				ErrorCode: ErrorCodeImageNotSet,
				Message:   "no GPU check image configured (set GPU_CHECK_IMAGE or spec.checks[].image)",
			})
			statusChanged = true
			continue
		}
		podName := generateGPUCheckPodName(cnh, check)
		pod := &corev1.Pod{}
		getErr := r.Get(ctx, client.ObjectKey{Namespace: r.CheckerPodNamespace, Name: podName}, pod)
		if apierrors.IsNotFound(getErr) {
			newPod, buildErr := r.buildGPUCheckPod(cnh, check, gpuCount)
			if buildErr != nil {
				return false, buildErr
			}
			klog.InfoS("Creating GPU check pod", "pod", podName, "node", cnh.Spec.NodeRef.Name, "check", check.Name)
			if createErr := r.Create(ctx, newPod); createErr != nil && !apierrors.IsAlreadyExists(createErr) {
				return false, fmt.Errorf("failed to create GPU check pod %s: %w", podName, createErr)
			}
			continue
		}
		if getErr != nil {
			return false, fmt.Errorf("failed to get GPU check pod %s: %w", podName, getErr)
		}

		switch {
		case pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed:
			logs, logErr := r.podLogs(ctx, pod)
			if logErr != nil {
				klog.ErrorS(logErr, "Failed to read GPU check pod logs, using phase-only result", "pod", podName)
			}
			r.updateCheckResult(cnh, parseGPUChecksResult(check.Name, pod.Status.Phase, logs, sku))
			statusChanged = true
			podsToDelete = append(podsToDelete, podName)
		case gpuPodTimedOut(pod, gpuCheckTimeout(check)):
			r.updateCheckResult(cnh, chmv1alpha1.CheckResult{
				Name:      check.Name,
				Status:    chmv1alpha1.CheckStatusUnknown,
				ErrorCode: ErrorCodeTimeout,
				Message:   fmt.Sprintf("GPU check exceeded timeout %s", gpuCheckTimeout(check)),
			})
			statusChanged = true
			podsToDelete = append(podsToDelete, podName)
		}
	}

	if statusChanged {
		if err := r.Status().Update(ctx, cnh); err != nil {
			return false, fmt.Errorf("failed to update status with GPU check results: %w", err)
		}
	}
	for _, name := range podsToDelete {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: r.CheckerPodNamespace}}
		if err := client.IgnoreNotFound(r.Delete(ctx, pod)); err != nil {
			klog.ErrorS(err, "Failed to delete finished GPU check pod", "pod", name)
		}
	}

	for i := range checks {
		if found, _ := r.findResult(cnh, checks[i].Name); !found {
			return false, nil
		}
	}
	return true, nil
}

// nodeGPUInfo returns the node's allocatable nvidia.com/gpu count, whether it is a GPU node,
// and its VM SKU. A node is treated as a GPU node when it advertises allocatable GPUs or
// carries an NVIDIA accelerator label (the latter covers driver-only pools with no device
// plugin).
func (r *CheckNodeHealthReconciler) nodeGPUInfo(ctx context.Context, nodeName string) (int64, bool, string, error) {
	node := &corev1.Node{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return 0, false, "", fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}
	var allocatable int64
	if q, ok := node.Status.Allocatable[GPUResourceName]; ok {
		allocatable = q.Value()
	}
	isGPUNode := allocatable > 0 ||
		node.Labels[nvidiaAcceleratorLabel] == "nvidia" ||
		node.Labels[legacyAcceleratorLabel] == "nvidia"
	return allocatable, isGPUNode, node.Labels[instanceTypeLabel], nil
}

// podLogs returns the (bounded) stdout/stderr of the pod's check container.
func (r *CheckNodeHealthReconciler) podLogs(ctx context.Context, pod *corev1.Pod) (string, error) {
	raw, err := r.Clientset.CoreV1().
		Pods(pod.Namespace).
		GetLogs(pod.Name, &corev1.PodLogOptions{}).
		DoRaw(ctx)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// gpuPodTimedOut reports whether the pod has been running longer than the check timeout.
func gpuPodTimedOut(pod *corev1.Pod, timeout time.Duration) bool {
	return time.Since(pod.CreationTimestamp.Time) > timeout
}

var (
	// avgBusBandwidthRe matches nccl-tests' summary line, e.g.
	//   # Avg bus bandwidth    : 480.18
	avgBusBandwidthRe = regexp.MustCompile(`Avg bus bandwidth\s*:\s*([0-9]+(?:\.[0-9]+)?)`)
	// outOfBoundsRe matches the correctness summary, e.g.
	//   # Out of bounds values : 0 OK
	outOfBoundsRe = regexp.MustCompile(`Out of bounds values\s*:\s*([0-9]+)`)
	// checkSectionRe matches the delimiters emitted by run-gpu-checks.sh.
	checkSectionRe    = regexp.MustCompile(`^=== CHM CHECK: (\S+) ===$`)
	checkSectionEndRe = regexp.MustCompile(`^=== CHM CHECK END: (\S+) rc=(-?\d+) ===$`)
	// nvbwRunningRe matches nvbandwidth's per-testcase banner, e.g. "Running host_to_device_memcpy_ce."
	nvbwRunningRe = regexp.MustCompile(`^Running\s+(\S+?)\.?$`)
)

// gpuCheckSection is one tool's output slice from the check pod logs.
type gpuCheckSection struct {
	body     string
	exitCode int
	found    bool
}

// splitCheckSections carves the pod log into the per-tool sections emitted by run-gpu-checks.sh.
func splitCheckSections(logs string) map[string]gpuCheckSection {
	sections := map[string]gpuCheckSection{}
	current := ""
	var body []string

	for _, line := range strings.Split(logs, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := checkSectionRe.FindStringSubmatch(trimmed); m != nil {
			current = m[1]
			body = nil
			sections[current] = gpuCheckSection{found: true, exitCode: -1}
			continue
		}
		if m := checkSectionEndRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			rc, _ := strconv.Atoi(m[2])
			sections[name] = gpuCheckSection{body: strings.Join(body, "\n"), exitCode: rc, found: true}
			current = ""
			body = nil
			continue
		}
		if current != "" {
			body = append(body, line)
		}
	}
	return sections
}

// parseGPUChecksResult maps a finished GPU check pod's logs to a CheckResult with one
// SubCheckResult per benchmark. Per-check pass/fail comes from the parsed measurements rather
// than the pod exit code, so one failing tool never masks the other's result.
func parseGPUChecksResult(name string, phase corev1.PodPhase, logs, sku string) chmv1alpha1.CheckResult {
	thresholds, haveThresholds := gpuThresholdsFor(sku)
	sections := splitCheckSections(logs)

	var subs []chmv1alpha1.SubCheckResult

	// If the dispatcher never ran, fall back to a single failure carrying the log tail.
	if len(sections) == 0 {
		status := chmv1alpha1.CheckStatusUnhealthy
		msg := truncateMessage(lastLines(logs, 20))
		if phase == corev1.PodSucceeded {
			msg = "GPU checks produced no output sections\n" + msg
		}
		return chmv1alpha1.CheckResult{
			Name:      name,
			Status:    status,
			ErrorCode: ErrorCodeGPUCheckFailed,
			Message:   msg,
			SubResults: []chmv1alpha1.SubCheckResult{{
				Name: ncclSubCheckName, Status: status, ErrorCode: ErrorCodeGPUCheckFailed, Message: msg,
			}},
		}
	}

	if s, ok := sections["nccl_all_reduce"]; ok {
		subs = append(subs, parseNCCLSection(s, sku, thresholds, haveThresholds))
	}
	if s, ok := sections["nvbandwidth"]; ok {
		subs = append(subs, parseNvbandwidthSection(s, thresholds, haveThresholds)...)
	}

	status := chmv1alpha1.CheckStatusHealthy
	errorCode := ""
	for _, s := range subs {
		if s.Status != chmv1alpha1.CheckStatusHealthy {
			status = s.Status
			errorCode = s.ErrorCode
			break
		}
	}

	return chmv1alpha1.CheckResult{
		Name:       name,
		Status:     status,
		ErrorCode:  errorCode,
		Message:    summarizeSubResults(subs),
		SubResults: subs,
	}
}

// parseNCCLSection turns the nccl-tests output into its sub-result.
func parseNCCLSection(s gpuCheckSection, sku string, t gpuThresholds, haveThresholds bool) chmv1alpha1.SubCheckResult {
	sub := chmv1alpha1.SubCheckResult{Name: ncclSubCheckName, Status: chmv1alpha1.CheckStatusHealthy}
	observations := map[string]string{}
	if sku != "" {
		observations["sku"] = sku
	}

	var busbw float64
	haveBusbw := false
	if m := avgBusBandwidthRe.FindStringSubmatch(s.body); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			busbw, haveBusbw = v, true
			observations["busBandwidthGBps"] = m[1]
		}
	}

	outOfBounds, haveCorrectness := 0, false
	if m := outOfBoundsRe.FindStringSubmatch(s.body); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			outOfBounds, haveCorrectness = v, true
			observations["outOfBoundsValues"] = m[1]
		}
	}

	useThreshold := haveThresholds && t.NcclBusGBps > 0
	if useThreshold {
		observations["thresholdGBps"] = strconv.FormatFloat(t.NcclBusGBps, 'f', -1, 64)
	}

	switch {
	case haveCorrectness && outOfBounds > 0:
		sub.Status = chmv1alpha1.CheckStatusUnhealthy
		sub.ErrorCode = ErrorCodeNcclCorrectness
		sub.Message = fmt.Sprintf("NCCL all-reduce reported %d out-of-bounds values", outOfBounds)
	case !haveBusbw:
		sub.Status = chmv1alpha1.CheckStatusUnhealthy
		sub.ErrorCode = ErrorCodeGPUCheckFailed
		sub.Message = truncateMessage("nccl-tests produced no bandwidth summary\n" + lastLines(s.body, 15))
	case useThreshold && busbw < t.NcclBusGBps:
		sub.Status = chmv1alpha1.CheckStatusUnhealthy
		sub.ErrorCode = ErrorCodeNcclLowBandwidth
		sub.Message = fmt.Sprintf("bus bandwidth %.3f GB/s below %.3f GB/s threshold", busbw, t.NcclBusGBps)
	case useThreshold:
		sub.Message = fmt.Sprintf("bus bandwidth %.3f GB/s (>= %.3f GB/s threshold)", busbw, t.NcclBusGBps)
	default:
		sub.Message = fmt.Sprintf("bus bandwidth %.3f GB/s (no threshold configured for %q)", busbw, sku)
	}

	sub.Observations = observations
	return sub
}

// parseNvbandwidthSection turns each nvbandwidth testcase matrix into its own sub-result,
// thresholding on the minimum measured value (AzNHC fails if any pair is below expectation).
func parseNvbandwidthSection(s gpuCheckSection, t gpuThresholds, haveThresholds bool) []chmv1alpha1.SubCheckResult {
	mins, order := parseNvbandwidthMatrices(s.body)
	if len(order) == 0 {
		return []chmv1alpha1.SubCheckResult{{
			Name:      "Nvbandwidth",
			Status:    chmv1alpha1.CheckStatusUnhealthy,
			ErrorCode: ErrorCodeGPUCheckFailed,
			Message:   truncateMessage("nvbandwidth produced no results\n" + lastLines(s.body, 15)),
		}}
	}

	subs := make([]chmv1alpha1.SubCheckResult, 0, len(order))
	for _, testcase := range order {
		min := mins[testcase]
		sub := chmv1alpha1.SubCheckResult{
			Name:         testcase,
			Status:       chmv1alpha1.CheckStatusHealthy,
			Observations: map[string]string{"minBandwidthGBps": strconv.FormatFloat(min, 'f', 3, 64)},
		}

		threshold := 0.0
		if haveThresholds {
			switch testcase {
			case nvbwHostToDevice, nvbwDeviceToHost:
				threshold = t.PCIeGBps
			case nvbwDeviceToDevice:
				threshold = t.P2PGBps
			}
		}

		switch {
		case threshold > 0 && min < threshold:
			sub.Status = chmv1alpha1.CheckStatusUnhealthy
			sub.ErrorCode = ErrorCodeGpuLowBandwidth
			sub.Message = fmt.Sprintf("min bandwidth %.3f GB/s below %.3f GB/s threshold", min, threshold)
			sub.Observations["thresholdGBps"] = strconv.FormatFloat(threshold, 'f', -1, 64)
		case threshold > 0:
			sub.Message = fmt.Sprintf("min bandwidth %.3f GB/s (>= %.3f GB/s threshold)", min, threshold)
			sub.Observations["thresholdGBps"] = strconv.FormatFloat(threshold, 'f', -1, 64)
		default:
			sub.Message = fmt.Sprintf("min bandwidth %.3f GB/s (no threshold configured)", min)
		}
		subs = append(subs, sub)
	}
	return subs
}

// parseNvbandwidthMatrices returns the minimum measured value per testcase, preserving the
// order the testcases were reported in. nvbandwidth prints a banner line then a bandwidth
// matrix whose first column is the row index; "N/A" cells are skipped.
func parseNvbandwidthMatrices(body string) (map[string]float64, []string) {
	mins := map[string]float64{}
	var order []string
	current := ""
	skipColumnHeader := false

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if m := nvbwRunningRe.FindStringSubmatch(trimmed); m != nil {
			current = m[1]
			skipColumnHeader = true
			continue
		}
		if current == "" || strings.Contains(trimmed, "memcpy") || strings.HasPrefix(trimmed, "SUM") ||
			strings.HasPrefix(trimmed, "COEFFICIENT_OF_VARIATION") {
			continue
		}
		// The line after the matrix banner lists column indices, not measurements.
		if skipColumnHeader {
			skipColumnHeader = false
			continue
		}

		// Matrix rows start with the row index; remaining cells are values or "N/A".
		fields := strings.Fields(trimmed)
		for _, f := range fields[1:] {
			v, err := strconv.ParseFloat(f, 64)
			if err != nil || v <= 0 {
				continue
			}
			if cur, ok := mins[current]; !ok || v < cur {
				if !ok {
					order = append(order, current)
				}
				mins[current] = v
			}
		}
	}
	return mins, order
}

// summarizeSubResults renders a short per-check summary for the top-level result message.
func summarizeSubResults(subs []chmv1alpha1.SubCheckResult) string {
	if len(subs) == 0 {
		return "GPU checks produced no results"
	}
	lines := make([]string, 0, len(subs))
	for _, s := range subs {
		line := fmt.Sprintf("%s: %s", s.Name, s.Status)
		if s.Message != "" {
			line += fmt.Sprintf(" (%s)", s.Message)
		}
		lines = append(lines, line)
	}
	return truncateMessage(strings.Join(lines, "\n"))
}

// lastLines returns the trailing n non-empty lines of s, for surfacing failure context.
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// maxResultMessageLen bounds messages well under the CRD's 32768 limit.
const maxResultMessageLen = 4000

// truncateMessage bounds a message to maxResultMessageLen, keeping the tail.
func truncateMessage(msg string) string {
	if len(msg) > maxResultMessageLen {
		return msg[len(msg)-maxResultMessageLen:]
	}
	return msg
}
