package checknodehealth

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/gpu"
)

func TestGPUNodeInfoFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// node is nil when the target node does not exist.
		node          *corev1.Node
		wantIsGPUNode bool
		wantClaimable int64
		wantSKU       string
	}{
		{
			name: "node with advertised nvidia GPU resource",
			// this simulates a default fully managed AKS GPU node
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "with-plugin",
					Labels: map[string]string{
						gpuAcceleratorLabel: "nvidia",
						instanceTypeLabel:   "Standard_ND96isr_H100_v5",
					},
				},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						nvidiaGPUResourceName: *resource.NewQuantity(8, resource.DecimalSI),
					},
					Allocatable: corev1.ResourceList{
						nvidiaGPUResourceName: *resource.NewQuantity(8, resource.DecimalSI),
					},
				},
			},
			wantIsGPUNode: true,
			wantClaimable: 8,
			wantSKU:       "Standard_ND96isr_H100_v5",
		},
		{
			// The device plugin registered every GPU but reports them all unhealthy, so none can be
			// claimed. Registered but unhealthy GPUs do not count.
			name: "device plugin reporting every GPU unhealthy",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "all-unhealthy",
					Labels: map[string]string{
						gpuAcceleratorLabel: "nvidia",
						instanceTypeLabel:   "Standard_ND96isr_H100_v5",
					},
				},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						nvidiaGPUResourceName: *resource.NewQuantity(8, resource.DecimalSI),
					},
					Allocatable: corev1.ResourceList{
						nvidiaGPUResourceName: *resource.NewQuantity(0, resource.DecimalSI),
					},
				},
			},
			wantIsGPUNode: true,
			wantClaimable: 0,
			wantSKU:       "Standard_ND96isr_H100_v5",
		},
		{
			name: "node with only accelerator label with nvidia value",
			// this simulates a driver-only AKS GPU node, or a node whose device plugin has not
			// registered yet
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "driver-only",
					Labels: map[string]string{
						gpuAcceleratorLabel: "nvidia",
						instanceTypeLabel:   "Standard_ND96isr_H100_v5",
					},
				},
			},
			wantIsGPUNode: true,
			wantSKU:       "Standard_ND96isr_H100_v5",
		},
		{
			// Kubelet reports a resource whose device plugin went away as zero capacity.
			name: "device plugin resource reported with zero capacity",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "plugin-gone",
					Labels: map[string]string{gpuAcceleratorLabel: "nvidia"},
				},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						nvidiaGPUResourceName: *resource.NewQuantity(0, resource.DecimalSI),
					},
					Allocatable: corev1.ResourceList{
						nvidiaGPUResourceName: *resource.NewQuantity(0, resource.DecimalSI),
					},
				},
			},
			wantIsGPUNode: true,
		},
		{
			name: "accelerator label value is case insensitive",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mixed-case-label",
					Labels: map[string]string{
						gpuAcceleratorLabel: "Nvidia",
					},
				},
			},
			wantIsGPUNode: true,
		},
		{
			name: "non-GPU node",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "no-gpu",
					Labels: map[string]string{instanceTypeLabel: "Standard_D8d_v5"},
				},
			},
			wantIsGPUNode: false,
			wantSKU:       "Standard_D8d_v5",
		},
		{
			// A deleted node is reported as a non-GPU node so the check can still reach a terminal
			// state, matching how IsSupported treats a missing node.
			name: "target node does not exist",
			node: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info := gpuNodeInfoFrom(tt.node)

			if info.isGPUNode != tt.wantIsGPUNode {
				t.Errorf("isGPUNode = %v, want %v", info.isGPUNode, tt.wantIsGPUNode)
			}
			if info.claimableGPUs != tt.wantClaimable {
				t.Errorf("claimableGPUs = %d, want %d", info.claimableGPUs, tt.wantClaimable)
			}
			if info.sku != tt.wantSKU {
				t.Errorf("sku = %q, want %q", info.sku, tt.wantSKU)
			}
		})
	}
}

// TestReconcileWaitsForClaimableGPUs covers a GPU node whose device plugin has not reported any healthy
// GPUs yet. After a node boots, a CheckNodeHealth can be created before its device plugin registers;
// on AKS the GPUs reached the Node 10-33s later, so the GPU checks wait for them.
func TestReconcileWaitsForClaimableGPUs(t *testing.T) {
	gpus := func(n int64) resource.Quantity { return *resource.NewQuantity(n, resource.DecimalSI) }

	tests := []struct {
		name string
		// registered and allocatable set the node's nvidia.com/gpu capacity and allocatable. Nil
		// leaves the resource off the node, as before the device plugin registers.
		registered, allocatable *resource.Quantity
		enableGPUChecks         bool
		// age is how long ago the CheckNodeHealth was created.
		age time.Duration
		// existingPod is a checker pod created before this reconcile.
		existingPod bool
		// wantWait means the reconcile requeues without creating the checker pod.
		wantWait bool
		// wantImage and wantArgs describe the checker pod when one is created.
		wantImage string
		wantArgs  []string
		wantGPUs  string
	}{
		{
			name:            "waits while the device plugin has not registered",
			enableGPUChecks: true,
			wantWait:        true,
		},
		{
			// Kubelet reports this while the device plugin is restarting.
			name:            "waits while the device plugin reports no healthy gpus",
			registered:      ptr.To(gpus(8)),
			allocatable:     ptr.To(gpus(0)),
			enableGPUChecks: true,
			wantWait:        true,
		},
		{
			name:            "starts the gpu checks as soon as the gpus are healthy",
			registered:      ptr.To(gpus(8)),
			allocatable:     ptr.To(gpus(8)),
			enableGPUChecks: true,
			wantImage:       "gpu-image",
			wantArgs:        []string{"--enable-gpu-checks", "--sku=Standard_ND96isr_H100_v5"},
			wantGPUs:        "8",
		},
		{
			name:            "reports a driver-only node once the wait is over",
			enableGPUChecks: true,
			age:             3 * time.Minute,
			wantImage:       "gpu-image",
			wantArgs:        []string{"--enable-gpu-checks", "--gpu-skip-reason=GPUsNotClaimable"},
		},
		{
			name:            "reports no claimable gpus when the gpus stay unhealthy past the wait",
			registered:      ptr.To(gpus(8)),
			allocatable:     ptr.To(gpus(0)),
			enableGPUChecks: true,
			age:             3 * time.Minute,
			wantImage:       "gpu-image",
			wantArgs:        []string{"--enable-gpu-checks", "--gpu-skip-reason=GPUsNotClaimable"},
		},
		{
			name:      "does not wait when the gpu checks are off",
			wantImage: "default-image",
		},
		{
			// The node's GPU state can change while a check runs, e.g. a device plugin restart. The
			// check that is already underway carries on.
			name:            "does not hold back a check that already has its pod",
			registered:      ptr.To(gpus(8)),
			allocatable:     ptr.To(gpus(0)),
			enableGPUChecks: true,
			existingPod:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler, fakeClient, _ := setupTest()
			reconciler.CheckerPodImage = "default-image"
			reconciler.GPUCheckerPodImage = "gpu-image"
			reconciler.EnableGPUChecks = tt.enableGPUChecks
			reconciler.GPUWait = DefaultGPUWait

			node := gpuNode("node-1")
			node.Status = corev1.NodeStatus{Capacity: corev1.ResourceList{}, Allocatable: corev1.ResourceList{}}
			if tt.registered != nil {
				node.Status.Capacity[nvidiaGPUResourceName] = *tt.registered
			}
			if tt.allocatable != nil {
				node.Status.Allocatable[nvidiaGPUResourceName] = *tt.allocatable
			}
			if err := fakeClient.Create(ctx, node); err != nil {
				t.Fatalf("creating the node: %v", err)
			}

			cnh := testCNH("cnh-wait")
			cnh.CreationTimestamp = metav1.NewTime(time.Now().Add(-tt.age))
			if err := fakeClient.Create(ctx, cnh); err != nil {
				t.Fatalf("creating the CR: %v", err)
			}
			if tt.existingPod {
				if err := fakeClient.Create(ctx, finishedCheckerPod(cnh.Name, true, node.Labels[instanceTypeLabel])); err != nil {
					t.Fatalf("creating the checker pod: %v", err)
				}
			}

			key := types.NamespacedName{Name: cnh.Name}
			result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			if err != nil {
				t.Fatalf("reconcile returned error: %v", err)
			}

			pods := &corev1.PodList{}
			if err := fakeClient.List(ctx, pods, client.MatchingLabels{CheckNodeHealthLabel: cnh.Name}); err != nil {
				t.Fatalf("listing checker pods: %v", err)
			}

			if tt.wantWait {
				if result.RequeueAfter <= 0 || result.RequeueAfter > gpuPollInterval {
					t.Errorf("RequeueAfter = %s, want a requeue within %s", result.RequeueAfter, gpuPollInterval)
				}
				if len(pods.Items) != 0 {
					t.Errorf("created %d checker pods while waiting, want none", len(pods.Items))
				}
				return
			}

			if tt.existingPod {
				updated := &chmv1alpha1.CheckNodeHealth{}
				if err := fakeClient.Get(ctx, key, updated); err != nil {
					t.Fatalf("reading the CR: %v", err)
				}
				if updated.Status.FinishedAt == nil {
					t.Errorf("the check with an existing pod did not complete")
				}
				return
			}

			if len(pods.Items) != 1 {
				t.Fatalf("got %d checker pods, want 1", len(pods.Items))
			}
			c := pods.Items[0].Spec.Containers[0]
			if c.Image != tt.wantImage {
				t.Errorf("image = %q, want %q", c.Image, tt.wantImage)
			}
			for _, arg := range tt.wantArgs {
				if !slices.Contains(c.Args, arg) {
					t.Errorf("args = %v, want them to contain %q", c.Args, arg)
				}
			}
			gotGPUs := ""
			if q, ok := c.Resources.Limits[nvidiaGPUResourceName]; ok {
				gotGPUs = q.String()
			}
			if gotGPUs != tt.wantGPUs {
				t.Errorf("gpu limit = %q, want %q", gotGPUs, tt.wantGPUs)
			}
		})
	}
}

// TestRecordedRun checks that a checker pod's annotations, not the node's current state, decide how
// its results are handled.
func TestRecordedRun(t *testing.T) {
	t.Parallel()

	h100 := "Standard_ND96isr_H100_v5"
	gpuNames := []string{"PodStartup", "PodNetwork", "NcclAllReduce", "GpuHostBandwidth", "GpuPeerBandwidth"}
	pod := func(annotations map[string]string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: annotations}}
	}

	tests := []struct {
		name string
		pod  *corev1.Pod
		// currentGpuNodeInfo is what the node reports now.
		currentGpuNodeInfo gpuNodeInfo
		wantGPUNode        bool
		wantSKU            string
		wantNames          []string
		wantTimeout        time.Duration
	}{
		{
			name:               "an existing gpu run stays treated as a gpu run when the node stops looking like a gpu node",
			pod:                pod(map[string]string{annotationNodeKind: NodeKindGPU, annotationGPUChecksSKU: h100}),
			currentGpuNodeInfo: gpuNodeInfo{},
			wantGPUNode:        true,
			wantSKU:            h100,
			wantNames:          gpuNames,
			wantTimeout:        GPUPodTimeout,
		},
		{
			name:               "gpu node without the gpu checks still reports granular conditions",
			pod:                pod(map[string]string{annotationNodeKind: NodeKindGPU}),
			currentGpuNodeInfo: gpuNodeInfo{},
			wantGPUNode:        true,
			wantNames:          baseCheckerNames,
			wantTimeout:        PodTimeout,
		},
		{
			name:               "an existing standard run stays treated as standard when the node starts looking like a gpu node",
			pod:                pod(map[string]string{annotationNodeKind: NodeKindStandard}),
			currentGpuNodeInfo: gpuNodeInfo{isGPUNode: true, claimableGPUs: 8, sku: h100},
			wantGPUNode:        false,
			wantSKU:            h100,
			wantNames:          baseCheckerNames,
			wantTimeout:        PodTimeout,
		},
		{
			name:               "pod missing the annotations falls back to the live node",
			pod:                pod(nil),
			currentGpuNodeInfo: gpuNodeInfo{isGPUNode: true, sku: h100},
			wantGPUNode:        true,
			wantSKU:            h100,
			wantNames:          baseCheckerNames,
			wantTimeout:        PodTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info, spec := recordedRun(tt.pod, tt.currentGpuNodeInfo)
			if info.isGPUNode != tt.wantGPUNode || spec.gpuNode != tt.wantGPUNode {
				t.Errorf("gpu node = %v (info) / %v (spec), want %v", info.isGPUNode, spec.gpuNode, tt.wantGPUNode)
			}
			if info.sku != tt.wantSKU {
				t.Errorf("sku = %q, want %q", info.sku, tt.wantSKU)
			}
			if !slices.Equal(spec.checkerNames, tt.wantNames) {
				t.Errorf("checkerNames = %v, want %v", spec.checkerNames, tt.wantNames)
			}
			if spec.timeout != tt.wantTimeout {
				t.Errorf("timeout = %s, want %s", spec.timeout, tt.wantTimeout)
			}
		})
	}
}

// TestGPURunKeepsGPURoutingWhenNodeLosesGPUs is the case where a node is recognized as a GPU node
// at time of checker pod creation but the node is updated and appears to lose GPU status while the
// GPU checks run. The result must still go to the granular conditions, GPU metrics and GPU circuit
// breaker, and not use the NodeHealthy condition, which existing remediation acts on.
func TestGPURunKeepsGPURoutingWhenNodeLosesGPUs(t *testing.T) {
	ctx := context.Background()
	reconciler, fakeClient, _ := setupTest()
	reconciler.GPUCheckerPodImage = "gpu-image"
	reconciler.EnableGPUChecks = true
	reconciler.GPUWait = DefaultGPUWait
	reconciler.EnableNodeCondition = true
	reconciler.CircuitBreakers = NewNodeConditionCircuitBreakers(1, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown)

	gpus := *resource.NewQuantity(8, resource.DecimalSI)
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{
			"kubernetes.io/os": "linux",
			instanceTypeLabel:  "Standard_ND96isr_H100_v5",
		}},
		Status: corev1.NodeStatus{
			Capacity:    corev1.ResourceList{nvidiaGPUResourceName: gpus},
			Allocatable: corev1.ResourceList{nvidiaGPUResourceName: gpus},
		},
	}
	if err := fakeClient.Create(ctx, node); err != nil {
		t.Fatalf("creating the node: %v", err)
	}
	cnh := testCNH("cnh-gpus-lost")
	if err := fakeClient.Create(ctx, cnh); err != nil {
		t.Fatalf("creating the CR: %v", err)
	}
	key := types.NamespacedName{Name: cnh.Name}

	// Starting the GPU checks while the node has its GPUs.
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}
	pods := &corev1.PodList{}
	if err := fakeClient.List(ctx, pods, client.MatchingLabels{CheckNodeHealthLabel: cnh.Name}); err != nil || len(pods.Items) != 1 {
		t.Fatalf("listing checker pods: %d pods, err %v", len(pods.Items), err)
	}
	pod := pods.Items[0]
	if pod.Annotations[annotationNodeKind] != NodeKindGPU || pod.Annotations[annotationGPUChecksSKU] == "" {
		t.Fatalf("checker pod annotations = %v, want a recorded GPU run", pod.Annotations)
	}

	// Losing the node's only GPU signal mid-run.
	if err := fakeClient.Get(ctx, client.ObjectKey{Name: node.Name}, node); err != nil {
		t.Fatalf("reading the node: %v", err)
	}
	delete(node.Status.Capacity, nvidiaGPUResourceName)
	delete(node.Status.Allocatable, nvidiaGPUResourceName)
	if err := fakeClient.Status().Update(ctx, node); err != nil {
		t.Fatalf("removing the node's GPUs: %v", err)
	}

	// Finishing the run with an unhealthy GPU result.
	if err := fakeClient.Get(ctx, key, cnh); err != nil {
		t.Fatalf("reading the CR: %v", err)
	}
	cnh.Status.Results = append(cnh.Status.Results,
		chmv1alpha1.CheckResult{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
		chmv1alpha1.CheckResult{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusUnhealthy, ErrorCode: gpu.ErrorCodeLowBandwidth},
		chmv1alpha1.CheckResult{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
		chmv1alpha1.CheckResult{Name: "GpuPeerBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
	)
	if err := fakeClient.Status().Update(ctx, cnh); err != nil {
		t.Fatalf("seeding reported results: %v", err)
	}
	pod.Status = corev1.PodStatus{
		Phase: corev1.PodSucceeded,
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "node-health-checker",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{StartedAt: metav1.Now()}},
		}},
	}
	if err := fakeClient.Status().Update(ctx, &pod); err != nil {
		t.Fatalf("finishing the checker pod: %v", err)
	}

	resultsBefore := counterSnapshot(t, checkResultCounter)
	for i := 0; i < 3; i++ {
		if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile %d returned error: %v", i, err)
		}
	}
	if err := fakeClient.Get(ctx, key, cnh); err != nil || cnh.Status.FinishedAt == nil {
		t.Fatalf("the CR never reached a terminal state (err %v)", err)
	}

	updatedNode := &corev1.Node{}
	if err := fakeClient.Get(ctx, client.ObjectKey{Name: node.Name}, updatedNode); err != nil {
		t.Fatalf("reading the node: %v", err)
	}
	if c := getNodeConditionByType(updatedNode.Status.Conditions, NodeConditionNodeHealthy); c != nil {
		t.Errorf("NodeHealthy = %s/%s, want it unset on a GPU run", c.Status, c.Reason)
	}
	if c := getNodeConditionByType(updatedNode.Status.Conditions, NodeConditionGPUAllReduceBandwidthHealthy); c == nil || c.Status != corev1.ConditionFalse {
		t.Errorf("GPUAllReduceBandwidthHealthy = %+v, want False", c)
	}

	for series := range counterDelta(resultsBefore, counterSnapshot(t, checkResultCounter)) {
		if !strings.Contains(series, "node_kind="+NodeKindGPU) {
			t.Errorf("result series %s, want node_kind=%s", series, NodeKindGPU)
		}
	}

	if reconciler.CircuitBreakers.For(gpuNodeInfo{isGPUNode: true}).Allow() {
		t.Error("the GPU node breaker did not record the unhealthy result")
	}
	if !reconciler.CircuitBreakers.For(gpuNodeInfo{}).Allow() {
		t.Error("the NodeHealthy breaker recorded the GPU result")
	}
}
