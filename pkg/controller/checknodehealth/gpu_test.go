package checknodehealth

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// gpuTestReconciler builds a reconciler backed by fake clients (with a fake typed clientset
// for pod logs) and the GPU feature enabled, seeded with the given objects.
func gpuTestReconciler(objs ...client.Object) (*CheckNodeHealthReconciler, client.Client) {
	scheme := runtime.NewScheme()
	if err := chmv1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&chmv1alpha1.CheckNodeHealth{}, &corev1.Node{}).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				ts := obj.GetCreationTimestamp()
				if ts.IsZero() {
					obj.SetCreationTimestamp(metav1.Now())
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		Build()

	r := &CheckNodeHealthReconciler{
		Client:               fc,
		Scheme:               scheme,
		APIReader:            fc,
		Clientset:            k8sfake.NewSimpleClientset(),
		CheckerPodNamespace:  "kube-system",
		GPUCheckImage:        "example.azurecr.io/nccl-tests:latest",
		EnableGPUHealthCheck: true,
	}
	return r, fc
}

func gpuNode(name string, gpus string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{GPUResourceName: resource.MustParse(gpus)},
		},
	}
}

func gpuCNH(name, node string, checks ...chmv1alpha1.NodeCheckSpec) *chmv1alpha1.CheckNodeHealth {
	return &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: chmv1alpha1.CheckNodeHealthSpec{
			NodeRef: chmv1alpha1.NodeReference{Name: node},
			Checks:  checks,
		},
	}
}

func TestGPUChecks(t *testing.T) {
	cnh := gpuCNH("c", "n",
		chmv1alpha1.NodeCheckSpec{Name: "Nccl", Profile: chmv1alpha1.NodeCheckProfileGPUIntrusive},
		chmv1alpha1.NodeCheckSpec{Name: "Other", Profile: ""},
	)
	got := gpuChecks(cnh)
	if len(got) != 1 || got[0].Name != "Nccl" {
		t.Errorf("expected only the GPUIntrusive check, got %v", got)
	}
}

// gpuCheckLogs renders a representative run-gpu-checks.sh transcript.
func gpuCheckLogs(busbw string, outOfBounds int, h2d, d2h, p2p float64) string {
	nccl := strings.Join([]string{
		"#       size         count      type   redop    root     time   algbw   busbw #wrong",
		"      8388608       2097152     float     sum      -1    123.4  67.98  118.97      0",
		fmt.Sprintf("# Out of bounds values : %d %s", outOfBounds, map[bool]string{true: "OK", false: "FAILED"}[outOfBounds == 0]),
		"# Avg bus bandwidth    : " + busbw,
		"#",
	}, "\n")

	nvbw := strings.Join([]string{
		"Running host_to_device_memcpy_ce.",
		"memcpy CE CPU(row) -> GPU(column) bandwidth (GB/s)",
		"          0         1",
		fmt.Sprintf("0     %.2f     %.2f", h2d, h2d+1),
		"",
		"Running device_to_host_memcpy_ce.",
		"memcpy CE CPU(row) <- GPU(column) bandwidth (GB/s)",
		"          0         1",
		fmt.Sprintf("0     %.2f     %.2f", d2h, d2h+1),
		"",
		"Running device_to_device_memcpy_read_ce.",
		"memcpy CE GPU(row) -> GPU(column) bandwidth (GB/s)",
		"          0         1",
		fmt.Sprintf("0       N/A     %.2f", p2p),
		fmt.Sprintf("1     %.2f       N/A", p2p+2),
	}, "\n")

	return strings.Join([]string{
		"=== CHM CHECK: nccl_all_reduce ===",
		nccl,
		"=== CHM CHECK END: nccl_all_reduce rc=0 ===",
		"=== CHM CHECK: nvbandwidth ===",
		nvbw,
		"=== CHM CHECK END: nvbandwidth rc=0 ===",
	}, "\n")
}

func subByName(subs []chmv1alpha1.SubCheckResult, name string) chmv1alpha1.SubCheckResult {
	for _, s := range subs {
		if s.Name == name {
			return s
		}
	}
	return chmv1alpha1.SubCheckResult{}
}

// envValue returns the value of the named container env var, or "" when unset.
func envValue(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func TestParseGPUChecksResult(t *testing.T) {
	const h100 = "Standard_ND96isr_H100_v5" // nccl 460, pcie 48, p2p 335

	t.Run("all above thresholds is healthy with per-tool sub-results", func(t *testing.T) {
		res := parseGPUChecksResult("GpuDiagnostic", corev1.PodSucceeded, gpuCheckLogs("480.18", 0, 52.5, 51.0, 340.0), h100)
		if res.Status != chmv1alpha1.CheckStatusHealthy || res.ErrorCode != "" {
			t.Errorf("expected Healthy/no code, got status=%s code=%s msg=%q", res.Status, res.ErrorCode, res.Message)
		}
		if len(res.SubResults) != 4 {
			t.Fatalf("expected 4 sub-results (nccl + 3 nvbandwidth), got %d: %+v", len(res.SubResults), res.SubResults)
		}
		nccl := subByName(res.SubResults, ncclSubCheckName)
		if nccl.Observations["busBandwidthGBps"] != "480.18" || nccl.Observations["thresholdGBps"] != "460" {
			t.Errorf("unexpected nccl observations: %v", nccl.Observations)
		}
		p2p := subByName(res.SubResults, nvbwDeviceToDevice)
		if p2p.Status != chmv1alpha1.CheckStatusHealthy || p2p.Observations["minBandwidthGBps"] != "340.000" {
			t.Errorf("unexpected p2p sub-result: %+v", p2p)
		}
	})

	t.Run("nvbandwidth below threshold is unhealthy", func(t *testing.T) {
		res := parseGPUChecksResult("GpuDiagnostic", corev1.PodSucceeded, gpuCheckLogs("480.18", 0, 52.5, 51.0, 300.0), h100)
		if res.Status != chmv1alpha1.CheckStatusUnhealthy || res.ErrorCode != ErrorCodeGpuLowBandwidth {
			t.Errorf("expected Unhealthy/%s, got status=%s code=%s", ErrorCodeGpuLowBandwidth, res.Status, res.ErrorCode)
		}
		if subByName(res.SubResults, ncclSubCheckName).Status != chmv1alpha1.CheckStatusHealthy {
			t.Errorf("expected nccl to remain Healthy when only nvbandwidth fails")
		}
	})

	t.Run("nccl below threshold is unhealthy", func(t *testing.T) {
		res := parseGPUChecksResult("GpuDiagnostic", corev1.PodSucceeded, gpuCheckLogs("120.5", 0, 52.5, 51.0, 340.0), h100)
		if res.ErrorCode != ErrorCodeNcclLowBandwidth {
			t.Errorf("expected %s, got %s", ErrorCodeNcclLowBandwidth, res.ErrorCode)
		}
	})

	t.Run("out of bounds values are a correctness failure", func(t *testing.T) {
		res := parseGPUChecksResult("GpuDiagnostic", corev1.PodSucceeded, gpuCheckLogs("480.18", 3, 52.5, 51.0, 340.0), h100)
		if res.ErrorCode != ErrorCodeNcclCorrectness {
			t.Errorf("expected %s, got %s", ErrorCodeNcclCorrectness, res.ErrorCode)
		}
	})

	t.Run("unknown sku reports measurements without verdict", func(t *testing.T) {
		res := parseGPUChecksResult("GpuDiagnostic", corev1.PodSucceeded, gpuCheckLogs("12.5", 0, 1.0, 1.0, 1.0), "Standard_NC6s_v3")
		if res.Status != chmv1alpha1.CheckStatusHealthy {
			t.Errorf("expected Healthy when no thresholds configured, got %+v", res)
		}
		if subByName(res.SubResults, ncclSubCheckName).Observations["thresholdGBps"] != "" {
			t.Errorf("expected no threshold observation for unknown SKU")
		}
	})

	t.Run("no sections is a failure carrying log tail", func(t *testing.T) {
		res := parseGPUChecksResult("GpuDiagnostic", corev1.PodFailed, "CUDA failure common.cu:891 'no CUDA-capable device'", h100)
		if res.Status != chmv1alpha1.CheckStatusUnhealthy || res.ErrorCode != ErrorCodeGPUCheckFailed {
			t.Errorf("expected Unhealthy/%s, got %+v", ErrorCodeGPUCheckFailed, res)
		}
		if !strings.Contains(res.Message, "CUDA failure") {
			t.Errorf("expected log tail in message, got %q", res.Message)
		}
	})
}

func TestGPUThresholdsFor(t *testing.T) {
	for _, sku := range []string{"Standard_ND96isr_H100_v5", "nd96isr_h100_v5", "STANDARD_ND96ISR_H100_V5"} {
		v, ok := gpuThresholdsFor(sku)
		if !ok || v.NcclBusGBps != 460.0 || v.PCIeGBps != 48.0 || v.P2PGBps != 335.0 {
			t.Errorf("gpuThresholdsFor(%q)=(%+v,%v), want {460 48 335},true", sku, v, ok)
		}
	}
	if _, ok := gpuThresholdsFor("Standard_D4s_v5"); ok {
		t.Errorf("expected no thresholds for a non-GPU SKU")
	}
}

func TestIsAdvisoryResult(t *testing.T) {
	r := &CheckNodeHealthReconciler{}
	cnh := gpuCNH("c", "n",
		chmv1alpha1.NodeCheckSpec{Name: "Advisory", Profile: chmv1alpha1.NodeCheckProfileGPUIntrusive},
		chmv1alpha1.NodeCheckSpec{Name: "Enforcing", Profile: chmv1alpha1.NodeCheckProfileGPUIntrusive, Enforcement: chmv1alpha1.CheckEnforcementEnforcing},
	)
	if !r.isAdvisoryResult(cnh, "Advisory") {
		t.Errorf("expected default (empty) enforcement to be advisory")
	}
	if r.isAdvisoryResult(cnh, "Enforcing") {
		t.Errorf("expected Enforcing check to not be advisory")
	}
	if r.isAdvisoryResult(cnh, "PodStartup") {
		t.Errorf("expected built-in checks to be enforcing")
	}
}

func TestReconcileGPUChecks(t *testing.T) {
	check := chmv1alpha1.NodeCheckSpec{Name: "GpuDiagnostic", Profile: chmv1alpha1.NodeCheckProfileGPUIntrusive}

	t.Run("creates pod when missing and reports not done", func(t *testing.T) {
		cnh := gpuCNH("gpu-diag", "gpu-node", check)
		r, c := gpuTestReconciler(cnh, gpuNode("gpu-node", "8"))

		done, err := r.reconcileGPUChecks(context.Background(), cnh)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if done {
			t.Errorf("expected not done while pod runs")
		}
		pod := &corev1.Pod{}
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: generateGPUCheckPodName(cnh, &check)}, pod); err != nil {
			t.Errorf("expected GPU check pod to be created: %v", err)
		}
	})

	t.Run("records result and deletes pod when succeeded", func(t *testing.T) {
		cnh := gpuCNH("gpu-diag", "gpu-node", check)
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: generateGPUCheckPodName(cnh, &check), Namespace: "kube-system"},
			Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
		}
		r, c := gpuTestReconciler(cnh, gpuNode("gpu-node", "8"), pod)

		done, err := r.reconcileGPUChecks(context.Background(), cnh)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !done {
			t.Errorf("expected done after recording result")
		}
		// The fake clientset returns placeholder logs with no nccl-tests summary, so the
		// parsed result is a failure; what matters here is that a result was recorded.
		found, res := r.findResult(cnh, check.Name)
		if !found || res.ErrorCode != ErrorCodeGPUCheckFailed {
			t.Errorf("expected a recorded result, got found=%v res=%+v", found, res)
		}
		got := &corev1.Pod{}
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: pod.Name}, got); err == nil {
			t.Errorf("expected finished pod to be deleted")
		}
	})

	t.Run("attempts run on GPU node without device plugin (accelerator label)", func(t *testing.T) {
		cnh := gpuCNH("gpu-diag", "driveronly-node", check)
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:   "driveronly-node",
			Labels: map[string]string{"kubernetes.azure.com/accelerator": "nvidia"},
		}}
		r, c := gpuTestReconciler(cnh, node)

		done, err := r.reconcileGPUChecks(context.Background(), cnh)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if done {
			t.Errorf("expected not done while attempting the run")
		}
		if found, _ := r.findResult(cnh, check.Name); found {
			t.Errorf("expected no terminal result yet; the check should attempt to run")
		}
		pod := &corev1.Pod{}
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: generateGPUCheckPodName(cnh, &check)}, pod); err != nil {
			t.Errorf("expected GPU check pod to be created on driver-only GPU node: %v", err)
		}
	})

	t.Run("records NotGPUNode only for a genuinely non-GPU node", func(t *testing.T) {
		cnh := gpuCNH("gpu-diag", "cpu-node", check)
		r, _ := gpuTestReconciler(cnh, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cpu-node"}})

		done, err := r.reconcileGPUChecks(context.Background(), cnh)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !done {
			t.Errorf("expected done (terminal Unknown) for non-GPU node")
		}
		found, res := r.findResult(cnh, check.Name)
		if !found || res.Status != chmv1alpha1.CheckStatusUnknown || res.ErrorCode != ErrorCodeNotGPUNode {
			t.Errorf("expected Unknown/%s, got found=%v res=%+v", ErrorCodeNotGPUNode, found, res)
		}
	})
}

func TestBuildGPUCheckPod(t *testing.T) {
	r, _, _ := setupTest()
	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-diag"},
		Spec: chmv1alpha1.CheckNodeHealthSpec{
			NodeRef: chmv1alpha1.NodeReference{Name: "aks-gpupool-000000"},
		},
	}

	t.Run("defaults to AzNHC image and requests all GPUs", func(t *testing.T) {
		check := &chmv1alpha1.NodeCheckSpec{Name: "GpuDiagnostic", Profile: chmv1alpha1.NodeCheckProfileGPUIntrusive}

		pod, err := r.buildGPUCheckPod(cnh, check, 8)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if pod.Spec.NodeName != "aks-gpupool-000000" {
			t.Errorf("expected pod pinned to node, got %q", pod.Spec.NodeName)
		}
		if got := pod.Spec.Containers[0].Image; got != r.GPUCheckImage {
			t.Errorf("expected configured default image %q, got %q", r.GPUCheckImage, got)
		}
		sc := pod.Spec.Containers[0].SecurityContext
		if sc == nil || (sc.Privileged != nil && *sc.Privileged) {
			t.Errorf("expected non-privileged container")
		}
		q := pod.Spec.Containers[0].Resources.Requests[GPUResourceName]
		if q.Value() != 8 {
			t.Errorf("expected GPU request 8, got %d", q.Value())
		}
		if l := pod.Spec.Containers[0].Resources.Limits[GPUResourceName]; l.Value() != 8 {
			t.Errorf("expected GPU limit 8, got %d", l.Value())
		}
		if pod.Labels[CheckNodeHealthLabel] != cnh.Name || pod.Labels[NodeCheckLabel] != check.Name {
			t.Errorf("unexpected labels: %v", pod.Labels)
		}
		if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Name != cnh.Name {
			t.Errorf("expected owner reference to CheckNodeHealth, got %v", pod.OwnerReferences)
		}
		if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
			t.Errorf("expected RestartPolicyNever, got %q", pod.Spec.RestartPolicy)
		}
	})

	t.Run("runs nccl-tests unprivileged with a default all-reduce sweep", func(t *testing.T) {
		check := &chmv1alpha1.NodeCheckSpec{Name: "GpuDiagnostic", Profile: chmv1alpha1.NodeCheckProfileGPUIntrusive}

		pod, err := r.buildGPUCheckPod(cnh, check, 8)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Args are left to the image defaults unless the requester overrides them.
		if args := envValue(pod.Spec.Containers[0].Env, "NCCL_ARGS"); args != "" {
			t.Errorf("expected NCCL_ARGS unset by default, got %q", args)
		}
		if got := envValue(pod.Spec.Containers[0].Env, "NGPUS"); got != "8" {
			t.Errorf("expected NGPUS=8, got %q", got)
		}
		if len(pod.Spec.Containers[0].Command) != 0 {
			t.Errorf("expected image entrypoint to be used, got command %v", pod.Spec.Containers[0].Command)
		}

		sc := pod.Spec.Containers[0].SecurityContext
		if sc == nil {
			t.Fatalf("expected a security context")
		}
		if sc.Privileged != nil && *sc.Privileged {
			t.Errorf("expected non-privileged container")
		}
		if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
			t.Errorf("expected allowPrivilegeEscalation=false")
		}
		if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
			t.Errorf("expected runAsNonRoot=true")
		}
		if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
			t.Errorf("expected all capabilities dropped, got %+v", sc.Capabilities)
		}
		if pod.Spec.HostNetwork {
			t.Errorf("expected HostNetwork disabled")
		}

		var shm *corev1.Volume
		for i := range pod.Spec.Volumes {
			if pod.Spec.Volumes[i].Name == "dshm" {
				shm = &pod.Spec.Volumes[i]
			}
		}
		if shm == nil || shm.EmptyDir == nil || shm.EmptyDir.Medium != corev1.StorageMediumMemory {
			t.Errorf("expected in-memory /dev/shm volume, got %v", pod.Spec.Volumes)
		}
	})

	t.Run("honors args override", func(t *testing.T) {
		check := &chmv1alpha1.NodeCheckSpec{
			Name:    "GpuDiagnostic",
			Profile: chmv1alpha1.NodeCheckProfileGPUIntrusive,
			Args:    []string{"-b", "1G", "-e", "1G", "-g", "2"},
		}

		pod, err := r.buildGPUCheckPod(cnh, check, 8)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := envValue(pod.Spec.Containers[0].Env, "NCCL_ARGS"); got != "-b 1G -e 1G -g 2" {
			t.Errorf("expected args override in NCCL_ARGS, got %q", got)
		}
	})

	t.Run("honors image override", func(t *testing.T) {
		check := &chmv1alpha1.NodeCheckSpec{
			Name:    "GpuDiagnostic",
			Profile: chmv1alpha1.NodeCheckProfileGPUIntrusive,
			Image:   "example.azurecr.io/custom:v1",
		}

		pod, err := r.buildGPUCheckPod(cnh, check, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := pod.Spec.Containers[0].Image; got != "example.azurecr.io/custom:v1" {
			t.Errorf("expected override image, got %q", got)
		}
	})

	t.Run("omits GPU resources and exposes all devices when count is zero", func(t *testing.T) {
		check := &chmv1alpha1.NodeCheckSpec{Name: "GpuDiagnostic", Profile: chmv1alpha1.NodeCheckProfileGPUIntrusive}

		pod, err := r.buildGPUCheckPod(cnh, check, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pod.Spec.Containers[0].Resources.Requests) != 0 {
			t.Errorf("expected no resource requests, got %v", pod.Spec.Containers[0].Resources.Requests)
		}
		var visible string
		for _, e := range pod.Spec.Containers[0].Env {
			if e.Name == "NVIDIA_VISIBLE_DEVICES" {
				visible = e.Value
			}
		}
		if visible != "all" {
			t.Errorf("expected NVIDIA_VISIBLE_DEVICES=all fallback, got %q", visible)
		}
		// NGPUS must be left unset so the entrypoint detects the real count via nvidia-smi;
		// the API reports 0 on driver-only pools, which would run a 1-GPU all-reduce.
		if got := envValue(pod.Spec.Containers[0].Env, "NGPUS"); got != "" {
			t.Errorf("expected NGPUS unset when no allocatable GPUs, got %q", got)
		}
	})
}

func TestGPUCheckTimeout(t *testing.T) {
	if got := gpuCheckTimeout(&chmv1alpha1.NodeCheckSpec{}); got != DefaultGPUCheckTimeout {
		t.Errorf("expected default timeout %v, got %v", DefaultGPUCheckTimeout, got)
	}

	custom := &metav1.Duration{Duration: 5 * time.Minute}
	if got := gpuCheckTimeout(&chmv1alpha1.NodeCheckSpec{Timeout: custom}); got != 5*time.Minute {
		t.Errorf("expected 5m timeout, got %v", got)
	}
}

func TestGenerateGPUCheckPodName(t *testing.T) {
	cnh := &chmv1alpha1.CheckNodeHealth{ObjectMeta: metav1.ObjectMeta{Name: "diag"}}
	check := &chmv1alpha1.NodeCheckSpec{Name: "GpuDiagnostic"}
	if got := generateGPUCheckPodName(cnh, check); got != "gpu-check-diag-gpudiagnostic" {
		t.Errorf("unexpected pod name %q", got)
	}

	long := &chmv1alpha1.CheckNodeHealth{ObjectMeta: metav1.ObjectMeta{Name: string(make([]byte, 300))}}
	if got := generateGPUCheckPodName(long, check); len(got) > maxPodNameLength {
		t.Errorf("pod name length %d exceeds %d", len(got), maxPodNameLength)
	}
}
