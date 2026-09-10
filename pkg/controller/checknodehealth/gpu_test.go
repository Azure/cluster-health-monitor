package checknodehealth

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGPUNodeInfoFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// node is nil when the target node does not exist.
		node          *corev1.Node
		wantErr       bool
		wantIsGPUNode bool
		wantCount     int64
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
					Allocatable: corev1.ResourceList{
						nvidiaGPUResourceName: *resource.NewQuantity(8, resource.DecimalSI),
					},
				},
			},
			wantIsGPUNode: true,
			wantCount:     8,
			wantSKU:       "Standard_ND96isr_H100_v5",
		},
		{
			name: "node with only accelerator label with nvidia value",
			// this simulates a driver-only AKS GPU node
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
			wantCount:     0,
			wantSKU:       "Standard_ND96isr_H100_v5",
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
			wantCount:     0,
			wantSKU:       "Standard_D8d_v5",
		},
		{
			name:    "target node does not exist",
			node:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reconciler, fakeClient, _ := setupTest()
			reconciler.EnableGPUChecks = true
			ctx := context.Background()

			nodeName := "does-not-exist"
			if tt.node != nil {
				if err := fakeClient.Create(ctx, tt.node); err != nil {
					t.Fatalf("Failed to create node: %v", err)
				}
				nodeName = tt.node.Name
			}

			info, err := reconciler.gpuNodeInfoFor(ctx, nodeName)
			if tt.wantErr {
				if err == nil {
					t.Fatal("gpuNodeInfoFor returned no error, want one")
				}
				return
			}
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

// With the gate off the controller must not read the Node at all, so a missing node is not
// an error.
func TestGPUNodeInfoForGateOffSkipsNodeRead(t *testing.T) {
	t.Parallel()

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
