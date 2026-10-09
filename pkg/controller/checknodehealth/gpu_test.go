package checknodehealth

import (
	"context"
	"slices"
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
				if err := fakeClient.Create(ctx, finishedCheckerPod(cnh.Name)); err != nil {
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

func TestPodTimeoutFor(t *testing.T) {
	t.Parallel()

	pod := func(annotations map[string]string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: annotations}}
	}

	tests := []struct {
		name string
		pod  *corev1.Pod
		want time.Duration
	}{
		{name: "recorded timeout", pod: pod(map[string]string{annotationCheckerTimeout: "15m0s"}), want: 15 * time.Minute},
		{name: "no recorded timeout", pod: pod(nil), want: PodTimeout},
		{name: "unparsable timeout", pod: pod(map[string]string{annotationCheckerTimeout: "soon"}), want: PodTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := podTimeoutFor(tt.pod); got != tt.want {
				t.Errorf("podTimeoutFor() = %s, want %s", got, tt.want)
			}
		})
	}
}
