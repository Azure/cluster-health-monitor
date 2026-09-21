package checknodehealth

import (
	"strings"
	"testing"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/kylelemons/godebug/pretty"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestGenerateHealthCheckPodName(t *testing.T) {
	tests := []struct {
		name        string
		cnhName     string
		expectedPod string
		description string
	}{
		{
			name:        "simple name",
			cnhName:     "test-check",
			expectedPod: "check-node-health-test-check",
			description: "Simple name should be prefixed normally",
		},
		{
			name:        "name at limit",
			cnhName:     strings.Repeat("a", 253-len("check-node-health-")),
			expectedPod: "check-node-health-" + strings.Repeat("a", 253-len("check-node-health-")),
			description: "Name at exact limit should not be truncated",
		},
		{
			name:        "name exceeding limit by 1",
			cnhName:     strings.Repeat("a", 253-len("check-node-health-")+1),
			expectedPod: "", // Will be verified by length check instead
			description: "Name exceeding limit should be truncated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cnh := &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: tt.cnhName},
			}
			podName := generateHealthCheckPodName(cnh)

			// Verify the result length never exceeds the limit
			if len(podName) > 253 {
				t.Errorf("Pod name length %d exceeds maximum 253", len(podName))
			}

			// For long names that should be truncated, verify truncation behavior
			if len(tt.cnhName)+len("check-node-health-") > 253 {
				if !strings.HasPrefix(podName, "check-node-health-") {
					t.Errorf("Expected truncated name to start with prefix, got %q", podName)
				}
				if len(podName) != 253 {
					t.Errorf("Expected truncated name to be exactly 253 characters, got %d", len(podName))
				}
			} else if tt.expectedPod != "" {
				// For non-truncation cases, verify exact match
				if podName != tt.expectedPod {
					t.Errorf("Expected pod name '%s', got '%s'", tt.expectedPod, podName)
				}
			}
		})
	}
}

func TestBuildHealthCheckPodShape(t *testing.T) {
	t.Parallel()

	// podShape is the part of a built checker pod that buildHealthCheckPod is responsible for.
	type podShape struct {
		Image           string
		Args            []string
		GPULimit        string
		GPURequest      string
		Env             map[string]string
		SecurityContext *corev1.SecurityContext
	}

	wantSecurityContext := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}

	tests := []struct {
		name string
		info gpuNodeInfo
		want podShape
	}{
		{
			name: "non-gpu node is unchanged",
			info: gpuNodeInfo{},
			want: podShape{
				Image:           "default-image",
				Args:            []string{"--name=cnh-1"},
				SecurityContext: wantSecurityContext,
			},
		},
		{
			// Fully managed pools advertise the extended resource, so the device plugin assigns
			// the devices and no env var is needed.
			name: "device plugin node requests its gpus",
			info: gpuNodeInfo{isGPUNode: true, gpuCount: 8, sku: "Standard_ND96isr_H100_v5"},
			want: podShape{
				Image:           "gpu-image",
				Args:            []string{"--name=cnh-1", "--enable-gpu-checks", "--sku=Standard_ND96isr_H100_v5", "--run-timeout=10m0s"},
				GPULimit:        "8",
				GPURequest:      "8",
				SecurityContext: wantSecurityContext,
			},
		},
		{
			// Driver-only pools run no device plugin, so there is no resource to request and the
			// runtime has to be told to expose the devices.
			name: "driver only node asks the runtime for the devices",
			info: gpuNodeInfo{isGPUNode: true, gpuCount: 0, sku: "Standard_ND96isr_H100_v5"},
			want: podShape{
				Image:           "gpu-image",
				Args:            []string{"--name=cnh-1", "--enable-gpu-checks", "--sku=Standard_ND96isr_H100_v5", "--run-timeout=10m0s"},
				Env:             map[string]string{"NVIDIA_VISIBLE_DEVICES": "all"},
				SecurityContext: wantSecurityContext,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reconciler, _, _ := setupTest()
			reconciler.CheckerPodImage = "default-image"
			reconciler.GPUCheckerPodImage = "gpu-image"

			pod, err := reconciler.buildHealthCheckPod(testCNH("cnh-1"), tt.info)
			if err != nil {
				t.Fatalf("buildHealthCheckPod returned error: %v", err)
			}

			c := pod.Spec.Containers[0]

			// set up the struct to compare fields we care about
			got := podShape{
				Image:           c.Image,
				Args:            c.Args,
				SecurityContext: c.SecurityContext,
			}
			if q, ok := c.Resources.Limits[nvidiaGPUResourceName]; ok {
				got.GPULimit = q.String()
			}
			if q, ok := c.Resources.Requests[nvidiaGPUResourceName]; ok {
				got.GPURequest = q.String()
			}
			for _, e := range c.Env {
				if got.Env == nil {
					got.Env = map[string]string{}
				}
				got.Env[e.Name] = e.Value
			}

			if diff := pretty.Compare(tt.want, got); diff != "" {
				t.Errorf("pod shape mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
