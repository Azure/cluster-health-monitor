package checknodehealth

import (
	"strings"
	"testing"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// gpuNode builds a GPU SKU node (accelerator label set) advertising `gpus`
// allocatable GPUs. Pass gpus<0 to omit the allocatable resource entirely
// (simulates the device plugin not being installed yet).
func gpuNode(name string, gpus int64) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{gpuAcceleratorLabel: "nvidia"},
		},
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{}},
	}
	if gpus >= 0 {
		n.Status.Allocatable[gpuResourceName] = *resource.NewQuantity(gpus, resource.DecimalSI)
	}
	return n
}

func TestIsGPUNode(t *testing.T) {
	tests := []struct {
		name string
		node *corev1.Node
		want bool
	}{
		{"gpu sku node", gpuNode("n", 8), true},
		{"gpu sku node without device plugin", gpuNode("n", -1), true},
		{"non-gpu node", &corev1.Node{Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGPUNode(tt.node); got != tt.want {
				t.Errorf("isGPUNode()=%v want %v", got, tt.want)
			}
		})
	}
}

func TestGPUCount(t *testing.T) {
	if got := gpuCount(gpuNode("n", 8)); got != 8 {
		t.Errorf("gpuCount()=%d want 8", got)
	}
}

func TestPreflightGPU(t *testing.T) {
	t.Run("gpu node passes preflight (nil)", func(t *testing.T) {
		if res := preflightGPU(gpuNode("n", 8)); res != nil {
			t.Fatalf("expected nil preflight result, got %+v", res)
		}
	})
	t.Run("no device plugin fails hard (Unhealthy)", func(t *testing.T) {
		res := preflightGPU(gpuNode("n", -1))
		if res == nil {
			t.Fatal("expected a failing preflight result")
		}
		if res.Status != chmv1alpha1.CheckStatusUnhealthy {
			t.Errorf("status=%v want Unhealthy", res.Status)
		}
		if res.ErrorCode != GPUErrCodeDevicePluginMissing {
			t.Errorf("errorCode=%q want %q", res.ErrorCode, GPUErrCodeDevicePluginMissing)
		}
		if res.Name != GPUHealthResultName {
			t.Errorf("name=%q want %q", res.Name, GPUHealthResultName)
		}
	})
}

func TestAznhcConf(t *testing.T) {
	conf := aznhcConf(8)
	for _, want := range []string{
		"check_gpu_count 8",
		"check_nvsmi_healthmon",
		"check_gpu_xid",
		"check_gpu_ecc",
		"check_gpu_clock_throttling",
		"check_nvlink_status",
		"check_gpu_bw",
		"check_nccl_allreduce",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf missing %q\n%s", want, conf)
		}
	}
}

func TestAznhcCommandTemplatesGPUCount(t *testing.T) {
	cmd := aznhcCommand(4)
	if !strings.Contains(cmd, "check_gpu_count 4") {
		t.Errorf("command missing templated gpu count:\n%s", cmd)
	}
	if !strings.Contains(cmd, "CONFFILE=/tmp/aznhc.conf") {
		t.Errorf("command missing CONFFILE:\n%s", cmd)
	}
}

func newGPUReconciler() *CheckNodeHealthReconciler {
	scheme := runtime.NewScheme()
	_ = chmv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	return &CheckNodeHealthReconciler{
		Scheme:              scheme,
		CheckerPodNamespace: "kube-system",
	}
}

func TestBuildAznhcPod(t *testing.T) {
	r := newGPUReconciler()
	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-abc-node1"},
		Spec:       chmv1alpha1.CheckNodeHealthSpec{NodeRef: chmv1alpha1.NodeReference{Name: "node1"}},
	}
	pod, err := r.buildAznhcPod(cnh, gpuNode("node1", 8))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pod.Spec.NodeName != "node1" {
		t.Errorf("NodeName=%q want node1", pod.Spec.NodeName)
	}
	if !pod.Spec.HostPID || !pod.Spec.HostNetwork {
		t.Errorf("expected HostPID and HostNetwork true")
	}
	c := pod.Spec.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.Privileged == nil || !*c.SecurityContext.Privileged {
		t.Errorf("expected privileged container")
	}
	q := c.Resources.Limits[gpuResourceName]
	if q.Value() != 8 {
		t.Errorf("gpu limit=%d want 8", q.Value())
	}
	if c.Image != DefaultAznhcImage {
		t.Errorf("image=%q want %q", c.Image, DefaultAznhcImage)
	}
	if pod.Labels[aznhcPodKindLabel] != aznhcPodKindValue {
		t.Errorf("missing aznhc pod-kind label")
	}
	if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Name != cnh.Name {
		t.Errorf("expected owner reference to the CR")
	}
}

func TestBuildAznhcPodImageOverride(t *testing.T) {
	r := newGPUReconciler()
	r.AznhcImage = "myacr.azurecr.io/aznhc:test"
	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "c1"},
		Spec:       chmv1alpha1.CheckNodeHealthSpec{NodeRef: chmv1alpha1.NodeReference{Name: "node1"}},
	}
	pod, err := r.buildAznhcPod(cnh, gpuNode("node1", 1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pod.Spec.Containers[0].Image != "myacr.azurecr.io/aznhc:test" {
		t.Errorf("image override not applied: %q", pod.Spec.Containers[0].Image)
	}
}

func TestAznhcResultFromPod(t *testing.T) {
	tests := []struct {
		name     string
		phase    corev1.PodPhase
		timedOut bool
		want     chmv1alpha1.CheckStatus
		wantCode string
	}{
		{"succeeded", corev1.PodSucceeded, false, chmv1alpha1.CheckStatusHealthy, ""},
		{"failed", corev1.PodFailed, false, chmv1alpha1.CheckStatusUnhealthy, GPUErrCodeRunFailed},
		{"timeout", corev1.PodRunning, true, chmv1alpha1.CheckStatusUnhealthy, GPUErrCodeTimeout},
		{"running", corev1.PodRunning, false, chmv1alpha1.CheckStatusUnknown, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{Status: corev1.PodStatus{Phase: tt.phase}}
			res := aznhcResultFromPod(pod, tt.timedOut)
			if res.Name != GPUHealthResultName {
				t.Errorf("name=%q want %q", res.Name, GPUHealthResultName)
			}
			if res.Status != tt.want {
				t.Errorf("status=%v want %v", res.Status, tt.want)
			}
			if res.ErrorCode != tt.wantCode {
				t.Errorf("errorCode=%q want %q", res.ErrorCode, tt.wantCode)
			}
		})
	}
}

func TestSummarizeAznhcLog(t *testing.T) {
	passLog := `[1] - SUCCESS:  nhc:  Health check passed:  check_gpu_count: Expected 8 and found 8
[2] - SUCCESS:  nhc:  Health check passed:  check_nvlink_status: GPU 0 has all nvlinks active.
[3] - SUCCESS:  nhc:  Health check passed:  check_nvlink_status: GPU 1 has all nvlinks active.
[4] - SUCCESS:  nhc:  Health check passed:  check_gpu_bw: GPU Bandwidth Tests Passed
[5] - SUCCESS:  nhc:  Health check passed:  check_nccl_allreduce: NCCL all reduce bandwidth test passed, 479.624 GB/s
=== nhc exited: 0 ===`
	got := summarizeAznhcLog(passLog)
	for _, want := range []string{"check_gpu_count", "check_nvlink_status", "check_gpu_bw", "check_nccl_allreduce", "check_nccl_allreduce=479.624GB/s"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q missing %q", got, want)
		}
	}
	// nvlink appears 8x in a real log but should be de-duplicated.
	if strings.Count(got, "check_nvlink_status") != 1 {
		t.Errorf("check_nvlink_status should appear once, got: %q", got)
	}
	if strings.Contains(got, "FAILED") {
		t.Errorf("no failures expected, got: %q", got)
	}

	failLog := `SUCCESS:  nhc:  Health check passed:  check_gpu_count: Expected 8 and found 8
ERROR:  nhc:  Health check failed:  check_nvlink_status: GPU 3 has inactive nvlink`
	gotFail := summarizeAznhcLog(failLog)
	if !strings.Contains(gotFail, "FAILED: check_nvlink_status") {
		t.Errorf("expected FAILED check_nvlink_status, got: %q", gotFail)
	}
	if !strings.Contains(gotFail, "passed: check_gpu_count") {
		t.Errorf("expected passed check_gpu_count, got: %q", gotFail)
	}

	if summarizeAznhcLog("nothing useful here") != "" {
		t.Errorf("expected empty summary for unrecognized log")
	}
}

// TestAdvisoryResultExcludedFromVerdict verifies an Unhealthy advisory GpuHealth
// result does NOT flip the overall Healthy verdict (decision #3: advisory-only).
func TestAdvisoryResultExcludedFromVerdict(t *testing.T) {
	r := &CheckNodeHealthReconciler{}
	cnh := &chmv1alpha1.CheckNodeHealth{
		Status: chmv1alpha1.CheckNodeHealthStatus{
			Results: []chmv1alpha1.CheckResult{
				{Name: "PodStartup", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: GPUHealthResultName, Status: chmv1alpha1.CheckStatusUnhealthy, ErrorCode: GPUErrCodeRunFailed},
			},
		},
	}
	if r.hasUnhealthyResult(cnh) {
		t.Error("advisory GpuHealth Unhealthy should not count as an unhealthy result")
	}
	if !r.allResultsHealthy(cnh) {
		t.Error("required results are all healthy; advisory result should be ignored")
	}

	status, _, _ := r.determineHealthyCondition(cnh)
	if status != metav1.ConditionTrue {
		t.Errorf("overall verdict=%v want True (advisory excluded)", status)
	}
}
