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

func TestDcgmDiagCommand(t *testing.T) {
	cmd := dcgmDiagCommand(3)
	for _, want := range []string{
		"nv-hostengine",
		"dcgmi diag -r 3 -j",
		dcgmOutputSentinel,
		"exit 0",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q:\n%s", want, cmd)
		}
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

func TestBuildDcgmPod(t *testing.T) {
	r := newGPUReconciler()
	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-abc-node1"},
		Spec:       chmv1alpha1.CheckNodeHealthSpec{NodeRef: chmv1alpha1.NodeReference{Name: "node1"}},
	}
	pod, err := r.buildDcgmPod(cnh, gpuNode("node1", 8))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pod.Spec.NodeName != "node1" {
		t.Errorf("NodeName=%q want node1", pod.Spec.NodeName)
	}
	c := pod.Spec.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.Privileged == nil || !*c.SecurityContext.Privileged {
		t.Errorf("expected privileged container")
	}
	q := c.Resources.Limits[gpuResourceName]
	if q.Value() != 8 {
		t.Errorf("gpu limit=%d want 8", q.Value())
	}
	if c.Image != DefaultDcgmImage {
		t.Errorf("image=%q want %q", c.Image, DefaultDcgmImage)
	}
	if pod.Labels[dcgmPodKindLabel] != dcgmPodKindValue {
		t.Errorf("missing dcgm pod-kind label")
	}
	if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Name != cnh.Name {
		t.Errorf("expected owner reference to the CR")
	}
}

func TestBuildDcgmPodImageOverride(t *testing.T) {
	r := newGPUReconciler()
	r.DcgmImage = "myacr.azurecr.io/dcgm:test"
	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "c1"},
		Spec:       chmv1alpha1.CheckNodeHealthSpec{NodeRef: chmv1alpha1.NodeReference{Name: "node1"}},
	}
	pod, err := r.buildDcgmPod(cnh, gpuNode("node1", 1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pod.Spec.Containers[0].Image != "myacr.azurecr.io/dcgm:test" {
		t.Errorf("image override not applied: %q", pod.Spec.Containers[0].Image)
	}
}

// dcgmHealthyJSON: hardware (memory) and integration (pcie) pass; the Deployment
// software test "fails" only on the benign persistence-mode warning.
const dcgmHealthyJSON = `{
  "DCGM Diagnostic": {
    "test_categories": [
      { "category": "Deployment", "tests": [
        { "name": "software", "results": [
          { "status": "Fail", "warnings": [ { "warning": "Persistence Mode: Persistence mode for GPU 0 is disabled." } ] }
        ], "test_summary": { "status": "Fail" } }
      ] },
      { "category": "Hardware", "tests": [
        { "name": "memory", "results": [ { "status": "Pass" } ], "test_summary": { "status": "Pass" } }
      ] },
      { "category": "Integration", "tests": [
        { "name": "pcie", "results": [ { "status": "Pass" } ], "test_summary": { "status": "Pass" } }
      ] }
    ]
  },
  "metadata": { "version": "4.5.3", "Driver Version Detected": "580.126.09" }
}`

// dcgmUnhealthyJSON: a hardware (memory) test fails => gates the verdict.
const dcgmUnhealthyJSON = `{
  "DCGM Diagnostic": {
    "test_categories": [
      { "category": "Hardware", "tests": [
        { "name": "memory", "results": [
          { "status": "Fail", "warnings": [ { "warning": "GPU 3 ECC error" } ] }
        ], "test_summary": { "status": "Fail" } }
      ] }
    ]
  },
  "metadata": { "version": "4.5.3" }
}`

func TestDcgmResultFromLogsHealthy(t *testing.T) {
	// Deployment-only failure is advisory and must not gate the verdict.
	logs := dcgmHealthyJSON + "\n" + dcgmOutputSentinel + "\nrc=226\n=== dcgmi stderr ===\n"
	res := dcgmResultFromLogs(logs)
	if res.Status != chmv1alpha1.CheckStatusHealthy {
		t.Fatalf("status=%v want Healthy: %s", res.Status, res.Message)
	}
	if res.Name != GPUHealthResultName {
		t.Errorf("name=%q want %q", res.Name, GPUHealthResultName)
	}
	for _, want := range []string{"memory", "pcie", "deployment", "Persistence"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("message missing %q: %s", want, res.Message)
		}
	}
	if strings.Contains(res.Message, "UNHEALTHY") {
		t.Errorf("healthy result should not say UNHEALTHY: %s", res.Message)
	}
}

func TestDcgmResultFromLogsUnhealthy(t *testing.T) {
	res := dcgmResultFromLogs(dcgmUnhealthyJSON)
	if res.Status != chmv1alpha1.CheckStatusUnhealthy {
		t.Fatalf("status=%v want Unhealthy: %s", res.Status, res.Message)
	}
	if res.ErrorCode != GPUErrCodeRunFailed {
		t.Errorf("errorCode=%q want %q", res.ErrorCode, GPUErrCodeRunFailed)
	}
	if !strings.Contains(res.Message, "FAILED") || !strings.Contains(res.Message, "memory") {
		t.Errorf("expected memory FAILED in message: %s", res.Message)
	}
	if !strings.Contains(res.Message, "GPU 3 ECC error") {
		t.Errorf("expected warning detail in message: %s", res.Message)
	}
}

func TestDcgmResultFromLogsUnparseable(t *testing.T) {
	res := dcgmResultFromLogs("not json at all")
	if res.Status != chmv1alpha1.CheckStatusUnknown || res.ErrorCode != GPUErrCodeDiagError {
		t.Errorf("got status=%v code=%q want Unknown/%s", res.Status, res.ErrorCode, GPUErrCodeDiagError)
	}
}

func TestDeriveTestStatus(t *testing.T) {
	tests := []struct {
		name    string
		results []dcgmResult
		want    string
	}{
		{"any fail", []dcgmResult{{Status: "Pass"}, {Status: "Fail"}}, "Fail"},
		{"all pass", []dcgmResult{{Status: "Pass"}, {Status: "Pass"}}, "Pass"},
		{"all skip", []dcgmResult{{Status: "Skip"}}, "Skip"},
		{"empty", nil, "Skip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveTestStatus(tt.results); got != tt.want {
				t.Errorf("deriveTestStatus()=%q want %q", got, tt.want)
			}
		})
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
