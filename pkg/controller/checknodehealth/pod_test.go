package checknodehealth

import (
	"strings"
	"testing"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
