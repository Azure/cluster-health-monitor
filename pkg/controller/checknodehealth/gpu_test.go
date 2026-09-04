package checknodehealth

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

func gpuNode(name string, gpuCount int64, sku string) *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{instanceTypeLabel: sku},
		},
	}
	if gpuCount > 0 {
		node.Status.Allocatable = corev1.ResourceList{
			GPUResourceName: *resource.NewQuantity(gpuCount, resource.DecimalSI),
		}
		node.Labels[nvidiaAcceleratorLabel] = "nvidia"
	}
	return node
}

func TestGPUNodeInfoFor(t *testing.T) {
	reconciler, fakeClient, _ := setupTest()
	reconciler.EnableGPUChecks = true
	ctx := context.Background()

	nodes := []*corev1.Node{
		gpuNode("with-plugin", 8, "Standard_ND96isr_H100_v5"),
		gpuNode("no-gpu", 0, "Standard_D8d_v5"),
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "driver-only",
				Labels: map[string]string{
					nvidiaAcceleratorLabel: "nvidia",
					instanceTypeLabel:      "Standard_NC6s_v3",
				},
			},
		},
	}
	for _, node := range nodes {
		if err := fakeClient.Create(ctx, node); err != nil {
			t.Fatalf("Failed to create node: %v", err)
		}
	}

	tests := []struct {
		nodeName      string
		wantIsGPUNode bool
		wantCount     int64
		wantSKU       string
	}{
		{"with-plugin", true, 8, "Standard_ND96isr_H100_v5"},
		{"no-gpu", false, 0, "Standard_D8d_v5"},
		{"driver-only", true, 0, "Standard_NC6s_v3"},
	}

	for _, tt := range tests {
		t.Run(tt.nodeName, func(t *testing.T) {
			info, err := reconciler.gpuNodeInfoFor(ctx, tt.nodeName)
			if err != nil {
				t.Fatalf("gpuNodeInfoFor returned error: %v", err)
			}
			if info.isGPUNode != tt.wantIsGPUNode {
				t.Errorf("isGPUNode = %v, want %v", info.isGPUNode, tt.wantIsGPUNode)
			}
			if info.gpuCount != tt.wantCount {
				t.Errorf("gpuCount = %d, want %d", info.gpuCount, tt.wantCount)
			}
			if info.sku != tt.wantSKU {
				t.Errorf("sku = %q, want %q", info.sku, tt.wantSKU)
			}
		})
	}
}

// The feature gate must keep the controller off the node read path entirely.
func TestGPUNodeInfoForDisabled(t *testing.T) {
	reconciler, _, _ := setupTest()
	reconciler.EnableGPUChecks = false

	info, err := reconciler.gpuNodeInfoFor(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("gpuNodeInfoFor returned error: %v", err)
	}
	if info.isGPUNode {
		t.Error("isGPUNode = true, want false when GPU checks are disabled")
	}
}

func TestBuildHealthCheckPod(t *testing.T) {
	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cnh"},
		Spec:       chmv1alpha1.CheckNodeHealthSpec{NodeRef: chmv1alpha1.NodeReference{Name: "test-node"}},
	}

	tests := []struct {
		name            string
		info            gpuNodeInfo
		wantImage       string
		wantGPULimit    int64
		wantArgs        []string
		wantShmVolume   bool
		wantTolerateAll bool
	}{
		{
			name:      "non-GPU node keeps the standard shape",
			info:      gpuNodeInfo{},
			wantImage: "ubuntu:latest",
			wantArgs:  []string{"--name=test-cnh"},
		},
		{
			name:            "GPU node with device plugin requests every GPU",
			info:            gpuNodeInfo{isGPUNode: true, gpuCount: 8, sku: "Standard_ND96isr_H100_v5"},
			wantImage:       "gpu-image:latest",
			wantGPULimit:    8,
			wantArgs:        []string{"--name=test-cnh", "--enable-gpu-checks", "--sku=Standard_ND96isr_H100_v5", "--gpu-count=8"},
			wantShmVolume:   true,
			wantTolerateAll: true,
		},
		{
			name:            "driver-only node falls back to runtime device injection",
			info:            gpuNodeInfo{isGPUNode: true, gpuCount: 0, sku: "Standard_NC6s_v3"},
			wantImage:       "gpu-image:latest",
			wantGPULimit:    0,
			wantArgs:        []string{"--name=test-cnh", "--enable-gpu-checks", "--sku=Standard_NC6s_v3", "--gpu-count=0"},
			wantShmVolume:   true,
			wantTolerateAll: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler, _, _ := setupTest()
			reconciler.GPUCheckerPodImage = "gpu-image:latest"

			pod, err := reconciler.buildHealthCheckPod(cnh, tt.info)
			if err != nil {
				t.Fatalf("buildHealthCheckPod returned error: %v", err)
			}

			container := pod.Spec.Containers[0]
			if container.Image != tt.wantImage {
				t.Errorf("image = %q, want %q", container.Image, tt.wantImage)
			}
			if len(container.Args) != len(tt.wantArgs) {
				t.Fatalf("args = %v, want %v", container.Args, tt.wantArgs)
			}
			for i, want := range tt.wantArgs {
				if container.Args[i] != want {
					t.Errorf("args[%d] = %q, want %q", i, container.Args[i], want)
				}
			}

			gpus := container.Resources.Limits[GPUResourceName]
			if gpus.Value() != tt.wantGPULimit {
				t.Errorf("GPU limit = %d, want %d", gpus.Value(), tt.wantGPULimit)
			}

			hasShm := false
			for _, v := range pod.Spec.Volumes {
				if v.Name == "dshm" {
					hasShm = true
				}
			}
			if hasShm != tt.wantShmVolume {
				t.Errorf("dshm volume present = %v, want %v", hasShm, tt.wantShmVolume)
			}

			tolerateAll := len(pod.Spec.Tolerations) == 1 && pod.Spec.Tolerations[0].Operator == corev1.TolerationOpExists
			if tolerateAll != tt.wantTolerateAll {
				t.Errorf("tolerate-all = %v, want %v", tolerateAll, tt.wantTolerateAll)
			}
		})
	}
}

func TestPodTimeoutFor(t *testing.T) {
	if got := podTimeoutFor(gpuNodeInfo{}); got != PodTimeout {
		t.Errorf("non-GPU timeout = %s, want %s", got, PodTimeout)
	}
	if got := podTimeoutFor(gpuNodeInfo{isGPUNode: true}); got != GPUPodTimeout {
		t.Errorf("GPU timeout = %s, want %s", got, GPUPodTimeout)
	}
}

func TestFillMissingGPUResults(t *testing.T) {
	reconciler, fakeClient, _ := setupTest()
	ctx := context.Background()

	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cnh"},
		Spec:       chmv1alpha1.CheckNodeHealthSpec{NodeRef: chmv1alpha1.NodeReference{Name: "test-node"}},
	}
	if err := fakeClient.Create(ctx, cnh); err != nil {
		t.Fatalf("Failed to create CheckNodeHealth: %v", err)
	}

	// The pod reported one GPU result before dying; only the other should be filled in.
	cnh.Status.Results = []chmv1alpha1.CheckResult{
		{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusHealthy, Message: "reported by pod"},
	}
	if err := fakeClient.Status().Update(ctx, cnh); err != nil {
		t.Fatalf("Failed to seed results: %v", err)
	}

	if err := reconciler.fillMissingGPUResults(ctx, cnh, ErrorCodeGPUPodFailed, "pod died"); err != nil {
		t.Fatalf("fillMissingGPUResults returned error: %v", err)
	}

	updated := &chmv1alpha1.CheckNodeHealth{}
	if err := fakeClient.Get(ctx, client.ObjectKey{Name: cnh.Name}, updated); err != nil {
		t.Fatalf("Failed to get CheckNodeHealth: %v", err)
	}

	found, nccl := reconciler.findResult(updated, "NcclAllReduce")
	if !found || nccl.Status != chmv1alpha1.CheckStatusHealthy || nccl.Message != "reported by pod" {
		t.Errorf("NcclAllReduce was overwritten: %+v", nccl)
	}

	found, bandwidth := reconciler.findResult(updated, "GpuBandwidth")
	if !found {
		t.Fatal("GpuBandwidth was not filled in")
	}
	if bandwidth.Status != chmv1alpha1.CheckStatusUnknown || bandwidth.ErrorCode != ErrorCodeGPUPodFailed {
		t.Errorf("GpuBandwidth = %+v, want Unknown/%s", bandwidth, ErrorCodeGPUPodFailed)
	}
}

func TestGPUNodeCreatesSingleCheckerPod(t *testing.T) {
	reconciler, fakeClient, _ := setupTest()
	reconciler.EnableGPUChecks = true
	reconciler.GPUCheckerPodImage = "gpu-image:latest"
	ctx := context.Background()

	node := gpuNode("gpu-node", 8, "Standard_ND96isr_H100_v5")
	if err := fakeClient.Create(ctx, node); err != nil {
		t.Fatalf("Failed to create node: %v", err)
	}
	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gpu"},
		Spec:       chmv1alpha1.CheckNodeHealthSpec{NodeRef: chmv1alpha1.NodeReference{Name: node.Name}},
	}
	if err := fakeClient.Create(ctx, cnh); err != nil {
		t.Fatalf("Failed to create CheckNodeHealth: %v", err)
	}

	info, err := reconciler.gpuNodeInfoFor(ctx, node.Name)
	if err != nil {
		t.Fatalf("gpuNodeInfoFor returned error: %v", err)
	}
	if _, err := reconciler.ensureHealthCheckPod(ctx, cnh, info); err != nil {
		t.Fatalf("ensureHealthCheckPod returned error: %v", err)
	}

	pods := &corev1.PodList{}
	if err := fakeClient.List(ctx, pods, client.MatchingLabels{CheckNodeHealthLabel: cnh.Name}); err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("expected exactly 1 checker pod, got %d", len(pods.Items))
	}
	if pods.Items[0].Spec.Containers[0].Image != "gpu-image:latest" {
		t.Errorf("pod image = %q, want gpu-image:latest", pods.Items[0].Spec.Containers[0].Image)
	}

	// A second reconcile must reuse the pod rather than create another.
	if _, err := reconciler.ensureHealthCheckPod(ctx, cnh, info); err != nil {
		t.Fatalf("second ensureHealthCheckPod returned error: %v", err)
	}
	if err := fakeClient.List(ctx, pods, client.MatchingLabels{CheckNodeHealthLabel: cnh.Name}); err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("expected pod to be reused, got %d pods", len(pods.Items))
	}
}

func TestIsPodTimeout(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute))},
	}
	if !isPodTimeout(pod, PodTimeout) {
		t.Error("expected timeout against the standard PodTimeout")
	}
	if isPodTimeout(pod, GPUPodTimeout) {
		t.Error("expected no timeout against the longer GPU timeout")
	}
}
