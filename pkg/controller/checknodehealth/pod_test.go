package checknodehealth

import (
	"context"
	"strings"
	"testing"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/google/go-cmp/cmp"
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

func TestHasCheckerReportedResult(t *testing.T) {
	r := &CheckNodeHealthReconciler{}
	tests := []struct {
		name     string
		results  []chmv1alpha1.CheckResult
		expected bool
	}{
		{
			name:     "no results",
			results:  nil,
			expected: false,
		},
		{
			name: "only PodStartup (controller-owned)",
			results: []chmv1alpha1.CheckResult{
				{Name: "PodStartup", Status: chmv1alpha1.CheckStatusUnhealthy},
			},
			expected: false,
		},
		{
			name: "checker-reported PodNetwork present",
			results: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
			},
			expected: true,
		},
		{
			name: "PodStartup plus checker-reported PodNetwork",
			results: []chmv1alpha1.CheckResult{
				{Name: "PodStartup", Status: chmv1alpha1.CheckStatusUnhealthy},
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cnh := &chmv1alpha1.CheckNodeHealth{
				Status: chmv1alpha1.CheckNodeHealthStatus{Results: tt.results},
			}
			if got := r.hasCheckerReportedResult(cnh); got != tt.expected {
				t.Errorf("hasCheckerReportedResult() = %v, want %v", got, tt.expected)
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
				Args:            []string{"--name=cnh-1", "--enable-gpu-checks", "--sku=Standard_ND96isr_H100_v5"},
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
				Args:            []string{"--name=cnh-1", "--enable-gpu-checks", "--sku=Standard_ND96isr_H100_v5"},
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

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("pod shape mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestUpdatePodstartCheckerResult_TimeoutWithCheckerResult reproduces the observed production
// case: the checker container ran and reported PodNetwork: Healthy into the CR, but the pod's
// observed status remained Pending past the timeout (kubelet never reported the Running
// transition to the API server). PodStartup must NOT be marked Unhealthy in this case.
func TestUpdatePodstartCheckerResult_TimeoutWithCheckerResult(t *testing.T) {
	reconciler, fakeClient, _ := setupTest()

	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-stale-status"},
		Spec:       chmv1alpha1.CheckNodeHealthSpec{NodeRef: chmv1alpha1.NodeReference{Name: "test-node"}},
		Status: chmv1alpha1.CheckNodeHealthStatus{
			Results: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy, Message: "network healthy"},
			},
		},
	}
	if err := fakeClient.Create(context.Background(), cnh); err != nil {
		t.Fatalf("failed to create CR: %v", err)
	}

	// Pod is stuck Pending past the 2m timeout with no container statuses (stale/never-updated status).
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "check-node-health-boot-stale-status",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-3 * time.Minute)),
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}

	if err := reconciler.updatePodstartCheckerResult(context.Background(), cnh, pod, PodTimeout); err != nil {
		t.Fatalf("updatePodstartCheckerResult returned error: %v", err)
	}

	found, result := reconciler.findResult(cnh, "PodStartup")
	if !found {
		t.Fatal("expected PodStartup result to be recorded")
	}
	if result.Status != chmv1alpha1.CheckStatusHealthy {
		t.Errorf("expected PodStartup Healthy (checker ran despite stale pod status), got %v", result.Status)
	}
}

// TestUpdatePodstartCheckerResult_TimeoutWithoutCheckerResult verifies the genuine startup-failure
// path is preserved: pod stuck Pending past timeout with no checker-reported result -> Unhealthy.
func TestUpdatePodstartCheckerResult_TimeoutWithoutCheckerResult(t *testing.T) {
	reconciler, fakeClient, _ := setupTest()

	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-real-failure"},
		Spec:       chmv1alpha1.CheckNodeHealthSpec{NodeRef: chmv1alpha1.NodeReference{Name: "test-node"}},
	}
	if err := fakeClient.Create(context.Background(), cnh); err != nil {
		t.Fatalf("failed to create CR: %v", err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "check-node-health-boot-real-failure",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-3 * time.Minute)),
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}

	if err := reconciler.updatePodstartCheckerResult(context.Background(), cnh, pod, PodTimeout); err != nil {
		t.Fatalf("updatePodstartCheckerResult returned error: %v", err)
	}

	found, result := reconciler.findResult(cnh, "PodStartup")
	if !found {
		t.Fatal("expected PodStartup result to be recorded")
	}
	if result.Status != chmv1alpha1.CheckStatusUnhealthy {
		t.Errorf("expected PodStartup Unhealthy (genuine startup failure), got %v", result.Status)
	}
}
