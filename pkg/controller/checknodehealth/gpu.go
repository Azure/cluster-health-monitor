package checknodehealth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// GPU (DCGM diagnostic) health check — PoC.
//
// This runs NVIDIA DCGM diagnostics (`dcgmi diag`) as a separate privileged Pod
// on GPU nodes, triggered by the same CheckNodeHealth flow used for every node
// (provision / upgrade / reboot). The result is reported as an ADVISORY
// CheckResult named "GpuHealth" — it does not gate the node's overall Healthy
// verdict (see AdvisoryCheckResults).
//
// The pod uses the self-contained NVIDIA DCGM container, which bundles its own
// nv-hostengine and the CUDA diagnostic plugins. This is required because AKS
// managed GPU nodes ship DCGM for telemetry only (dcgm-exporter) and do NOT
// install the CUDA plugin package, so the host `dcgmi diag` cannot run.
//
// Level 3 (dcgmDiagLevel) runs the active stress tests (targeted stress/power,
// SM stress, memory bandwidth, EUD) on top of the hardware (memory/ECC) and
// integration (PCIe) checks. The per-test JSON results are parsed into a clear
// GpuHealth message. See notes/gpu_health_checks/plan.md.

const (
	// GPUHealthResultName is the advisory CheckResult name reported for GPU health.
	GPUHealthResultName = "GpuHealth"

	// DefaultDcgmImage is the self-contained NVIDIA DCGM container (bundles
	// nv-hostengine + CUDA diagnostic plugins). Pinned to match the DCGM version
	// AKS installs on the host. PoC: pulled directly from nvcr.io.
	// TODO(carlosalv): post-PoC, mirror into a private ACR.
	DefaultDcgmImage = "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04"

	// gpuResourceName is the extended resource advertised by the NVIDIA device plugin.
	gpuResourceName corev1.ResourceName = "nvidia.com/gpu"

	// gpuAcceleratorLabel is set by AKS on GPU node pools (e.g. value "nvidia").
	// It marks a node as a GPU SKU regardless of whether the device plugin is
	// installed yet, so we can tell "GPU node with device plugin missing" (fail)
	// from "not a GPU node" (skip).
	gpuAcceleratorLabel = "kubernetes.azure.com/accelerator"

	// dcgmPodNamePrefix is the prefix for the DCGM diagnostic pod.
	dcgmPodNamePrefix = "dcgm-diag-"

	// dcgmPodKindLabel/Value mark the DCGM pod so it can be distinguished from
	// the standard node-health-checker pod (both share the CheckNodeHealthLabel).
	dcgmPodKindLabel = "checknodehealth.clusterhealthmonitor.azure.com/pod-kind"
	dcgmPodKindValue = "dcgm-diag"

	// DcgmDiagTimeout bounds a full DCGM diag run. It is deliberately large: the
	// level-3 stress tests take several minutes and the first-time image pull of
	// the DCGM container adds more.
	DcgmDiagTimeout = 25 * time.Minute

	// dcgmDiagLevel is the diagnostic run level passed to `dcgmi diag -r`. Level 3
	// includes the active stress tests (targeted stress/power, SM stress, memory
	// bandwidth, EUD) in addition to the hardware and integration checks.
	dcgmDiagLevel = 3

	// dcgmDeploymentCategory is DCGM's software/deployment sanity category. Its
	// failures are config-level (e.g. persistence mode disabled) rather than GPU
	// hardware health, so they are reported but do NOT gate the GpuHealth verdict.
	dcgmDeploymentCategory = "Deployment"

	// dcgmOutputSentinel separates the diag JSON (stdout) from the trailing
	// rc/stderr the pod prints after it, so the controller can isolate the JSON.
	dcgmOutputSentinel = "===DCGM_DIAG_END==="

	// GPU health check error codes (reported in CheckResult.ErrorCode).
	GPUErrCodeDevicePluginMissing = "GpuDevicePluginMissing"
	GPUErrCodeRunFailed           = "GpuHealthCheckFailed"
	GPUErrCodeTimeout             = "GpuHealthCheckTimeout"
	GPUErrCodeDiagError           = "GpuDiagError"
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

// preflightGPU validates GPU prerequisites before running the DCGM diagnostic. It
// returns a non-nil (failing) CheckResult when a prerequisite is missing, or nil
// when the node is ready to run the DCGM diag health check.
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

// dcgmDiagCommand returns the container command that starts an embedded DCGM
// hostengine and runs `dcgmi diag -r <level> -j`, emitting the per-test JSON on
// stdout followed by a sentinel and the exit code. It always exits 0 so the pod
// reaches Succeeded regardless of the diag verdict: `dcgmi` returns non-zero even
// for benign warnings (e.g. persistence mode disabled), so the controller derives
// health by parsing the JSON rather than from the pod phase.
func dcgmDiagCommand(level int) string {
	return fmt.Sprintf(`set -o pipefail
nv-hostengine >/tmp/hostengine.log 2>&1 || true
sleep 3
dcgmi diag -r %d -j >/tmp/dcgm-diag.json 2>/tmp/dcgm-diag.err
rc=$?
cat /tmp/dcgm-diag.json
echo "%s"
echo "rc=$rc"
echo "=== dcgmi stderr ==="
cat /tmp/dcgm-diag.err 2>/dev/null || true
exit 0
`, level, dcgmOutputSentinel)
}

// dcgmDiagOutput models the subset of `dcgmi diag -j` output we parse.
type dcgmDiagOutput struct {
	Diagnostic struct {
		TestCategories []dcgmTestCategory `json:"test_categories"`
	} `json:"DCGM Diagnostic"`
	Metadata struct {
		Version string `json:"version"`
		Driver  string `json:"Driver Version Detected"`
	} `json:"metadata"`
}

type dcgmTestCategory struct {
	Category string     `json:"category"`
	Tests    []dcgmTest `json:"tests"`
}

type dcgmTest struct {
	Name    string       `json:"name"`
	Results []dcgmResult `json:"results"`
	Summary struct {
		Status string `json:"status"`
	} `json:"test_summary"`
}

type dcgmResult struct {
	Status   string        `json:"status"`
	Warnings []dcgmWarning `json:"warnings"`
}

type dcgmWarning struct {
	Warning string `json:"warning"`
}

// deriveTestStatus computes a test's status from its per-entity results when the
// test_summary is absent: any Fail => Fail, else any Pass => Pass, else Skip.
func deriveTestStatus(results []dcgmResult) string {
	status := "Skip"
	for _, r := range results {
		switch {
		case strings.EqualFold(r.Status, "Fail"):
			return "Fail"
		case strings.EqualFold(r.Status, "Pass"):
			status = "Pass"
		}
	}
	return status
}

// dcgmResultFromLogs isolates the diag JSON from the pod logs and derives the
// advisory GpuHealth CheckResult.
func dcgmResultFromLogs(logs string) chmv1alpha1.CheckResult {
	jsonPart := logs
	if i := strings.Index(logs, dcgmOutputSentinel); i >= 0 {
		jsonPart = logs[:i]
	}
	var out dcgmDiagOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonPart)), &out); err != nil {
		return chmv1alpha1.CheckResult{
			Name:      GPUHealthResultName,
			Status:    chmv1alpha1.CheckStatusUnknown,
			ErrorCode: GPUErrCodeDiagError,
			Message:   "Unable to parse DCGM diagnostic output",
		}
	}
	return summarizeDcgmDiag(&out)
}

// summarizeDcgmDiag turns parsed DCGM diag output into a clear GpuHealth result.
// Hardware/integration/stress test failures gate the verdict (Unhealthy);
// Deployment (software/config) failures are reported as notes but do not gate.
func summarizeDcgmDiag(out *dcgmDiagOutput) chmv1alpha1.CheckResult {
	var failed, passed, skipped, notes []string
	total := 0
	gated := false

	for _, cat := range out.Diagnostic.TestCategories {
		for _, t := range cat.Tests {
			total++
			status := t.Summary.Status
			if status == "" {
				status = deriveTestStatus(t.Results)
			}

			// Collect de-duplicated warning messages for this test.
			var warns []string
			seen := map[string]bool{}
			for _, res := range t.Results {
				for _, w := range res.Warnings {
					msg := strings.TrimSpace(w.Warning)
					if msg != "" && !seen[msg] {
						seen[msg] = true
						warns = append(warns, msg)
					}
				}
			}

			switch {
			case strings.EqualFold(status, "Fail"):
				label := t.Name
				if len(warns) > 0 {
					label += " (" + strings.Join(warns, "; ") + ")"
				}
				if cat.Category == dcgmDeploymentCategory {
					notes = append(notes, label)
				} else {
					gated = true
					failed = append(failed, label)
				}
			case strings.EqualFold(status, "Skip"):
				skipped = append(skipped, t.Name)
			default:
				passed = append(passed, t.Name)
			}
		}
	}

	if total == 0 {
		return chmv1alpha1.CheckResult{
			Name:      GPUHealthResultName,
			Status:    chmv1alpha1.CheckStatusUnknown,
			ErrorCode: GPUErrCodeDiagError,
			Message:   "DCGM diagnostic returned no test results",
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "DCGM diag r%d", dcgmDiagLevel)
	if out.Metadata.Version != "" {
		fmt.Fprintf(&b, " (DCGM %s", out.Metadata.Version)
		if out.Metadata.Driver != "" {
			fmt.Fprintf(&b, ", driver %s", out.Metadata.Driver)
		}
		b.WriteString(")")
	}
	if gated {
		fmt.Fprintf(&b, ": UNHEALTHY. FAILED: %s.", strings.Join(failed, ", "))
	} else {
		b.WriteString(": healthy.")
	}
	if len(passed) > 0 {
		fmt.Fprintf(&b, " passed: %s.", strings.Join(passed, ", "))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&b, " skipped: %s.", strings.Join(skipped, ", "))
	}
	if len(notes) > 0 {
		fmt.Fprintf(&b, " [deployment note: %s]", strings.Join(notes, "; "))
	}

	res := chmv1alpha1.CheckResult{Name: GPUHealthResultName, Message: b.String()}
	if gated {
		res.Status = chmv1alpha1.CheckStatusUnhealthy
		res.ErrorCode = GPUErrCodeRunFailed
	} else {
		res.Status = chmv1alpha1.CheckStatusHealthy
	}
	return res
}

// generateDcgmPodName builds the DCGM diagnostic pod name for a CheckNodeHealth CR.
func generateDcgmPodName(cnh *chmv1alpha1.CheckNodeHealth) string {
	name := fmt.Sprintf("%s%s", dcgmPodNamePrefix, cnh.Name)
	if len(name) > maxPodNameLength {
		name = name[:maxPodNameLength]
	}
	return name
}

// buildDcgmPod builds the privileged DCGM diagnostic pod for a GPU node. The pod
// is pinned to the target node via NodeName and requests all allocatable GPUs so
// the device plugin injects them; the self-contained DCGM image runs its own
// hostengine + CUDA plugins (no host mounts needed). It is owner-referenced to
// the CR so it is garbage-collected with the CR.
func (r *CheckNodeHealthReconciler) buildDcgmPod(cnh *chmv1alpha1.CheckNodeHealth, node *corev1.Node) (*corev1.Pod, error) {
	image := r.DcgmImage
	if image == "" {
		image = DefaultDcgmImage
	}
	gpus := gpuCount(node)
	privileged := true

	gpuQty := *resource.NewQuantity(gpus, resource.DecimalSI)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generateDcgmPodName(cnh),
			Namespace: r.CheckerPodNamespace,
			Labels: map[string]string{
				CheckNodeHealthLabel: cnh.Name,
				dcgmPodKindLabel:     dcgmPodKindValue,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeName:      cnh.Spec.NodeRef.Name,
			// NodeName pins the pod (bypassing the scheduler), but tolerate GPU
			// taints defensively so the kubelet admits it on tainted GPU pools.
			Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			Containers: []corev1.Container{
				{
					Name:    "dcgm-diag",
					Image:   image,
					Command: []string{"/bin/bash", "-c", dcgmDiagCommand(dcgmDiagLevel)},
					SecurityContext: &corev1.SecurityContext{
						Privileged: &privileged,
					},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							gpuResourceName: gpuQty,
						},
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

// reconcileGPUHealth drives the advisory DCGM diagnostic GPU health check for a GPU node.
// It is idempotent and returns done=true once a GpuHealth result has been (or
// already was) recorded, or when the node is not a GPU node. It returns
// done=false while the DCGM diag pod is still running so the caller requeues.
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

	// Only GPU SKU nodes get a DCGM diag check; everything else is skipped silently.
	if !isGPUNode(node) {
		return true, nil
	}

	// Preflight — a missing prerequisite is a hard FAIL.
	if res := preflightGPU(node); res != nil {
		klog.InfoS("GPU preflight failed", "cr", cnh.Name, "node", node.Name, "errorCode", res.ErrorCode)
		return true, r.markGPUResult(ctx, cnh, *res)
	}

	// Ensure the DCGM diag pod exists (this starts the run / image pull).
	pod, err := r.ensureDcgmPod(ctx, cnh, node)
	if err != nil {
		return false, err
	}

	// Terminal phase => parse the diag JSON from the pod logs and record the result.
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		logs, err := r.readDcgmLogs(ctx, pod.Namespace, pod.Name)
		if err != nil {
			klog.ErrorS(err, "Failed to read DCGM diag pod logs", "pod", pod.Name)
			return true, r.markGPUResult(ctx, cnh, chmv1alpha1.CheckResult{
				Name:      GPUHealthResultName,
				Status:    chmv1alpha1.CheckStatusUnknown,
				ErrorCode: GPUErrCodeDiagError,
				Message:   "DCGM diagnostic completed but its output could not be read",
			})
		}
		return true, r.markGPUResult(ctx, cnh, dcgmResultFromLogs(logs))
	}

	// Timeout (own budget, NOT the 30s PodTimeout).
	if time.Since(pod.CreationTimestamp.Time) > DcgmDiagTimeout {
		klog.InfoS("DCGM diag pod timed out", "cr", cnh.Name, "pod", pod.Name, "timeout", DcgmDiagTimeout)
		return true, r.markGPUResult(ctx, cnh, chmv1alpha1.CheckResult{
			Name:      GPUHealthResultName,
			Status:    chmv1alpha1.CheckStatusUnhealthy,
			ErrorCode: GPUErrCodeTimeout,
			Message:   fmt.Sprintf("DCGM diagnostic did not complete within %s", DcgmDiagTimeout),
		})
	}

	klog.InfoS("DCGM diag still running, deferring completion", "cr", cnh.Name, "pod", pod.Name, "phase", pod.Status.Phase)
	return false, nil
}

// ensureDcgmPod returns the DCGM diag pod for the CR, creating it if it does not exist.
func (r *CheckNodeHealthReconciler) ensureDcgmPod(ctx context.Context, cnh *chmv1alpha1.CheckNodeHealth, node *corev1.Node) (*corev1.Pod, error) {
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(r.CheckerPodNamespace),
		client.MatchingLabels{CheckNodeHealthLabel: cnh.Name, dcgmPodKindLabel: dcgmPodKindValue},
	}
	if err := r.List(ctx, podList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list DCGM diag pods: %w", err)
	}
	if len(podList.Items) > 0 {
		return &podList.Items[0], nil
	}

	pod, err := r.buildDcgmPod(cnh, node)
	if err != nil {
		return nil, fmt.Errorf("failed to build DCGM diag pod: %w", err)
	}
	klog.InfoS("Creating DCGM diag pod", "pod", pod.Name, "node", cnh.Spec.NodeRef.Name, "image", pod.Spec.Containers[0].Image)
	if err := r.Create(ctx, pod); err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing := &corev1.Pod{}
			if gerr := r.Get(ctx, client.ObjectKey{Name: pod.Name, Namespace: pod.Namespace}, existing); gerr != nil {
				return nil, gerr
			}
			return existing, nil
		}
		return nil, fmt.Errorf("failed to create DCGM diag pod: %w", err)
	}

	created := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{Name: pod.Name, Namespace: pod.Namespace}, created); err != nil {
		return nil, fmt.Errorf("failed to get created DCGM diag pod: %w", err)
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

// readDcgmLogs returns the DCGM diag pod's logs. Returns "" (no error) when no
// clientset is configured.
func (r *CheckNodeHealthReconciler) readDcgmLogs(ctx context.Context, namespace, name string) (string, error) {
	if r.ClientSet == nil {
		return "", nil
	}
	stream, err := r.ClientSet.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{}).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to stream DCGM diag pod logs: %w", err)
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("failed to read DCGM diag pod logs: %w", err)
	}
	return string(data), nil
}
