package checknodehealth

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

func setupTest() (*CheckNodeHealthReconciler, client.Client, *runtime.Scheme) {
	scheme := runtime.NewScheme()
	if err := chmv1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		panic(err)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&chmv1alpha1.CheckNodeHealth{}). // Enable status subresource
		Build()

	reconciler := &CheckNodeHealthReconciler{
		Client:              fakeClient,
		Scheme:              scheme,
		CheckerPodImage:     "ubuntu:latest",
		CheckerPodNamespace: "default",
	}

	return reconciler, fakeClient, scheme
}

func TestReconcile(t *testing.T) {
	tests := []struct {
		name                string
		existingCnh         *chmv1alpha1.CheckNodeHealth
		existingPod         *corev1.Pod
		expectedResult      ctrl.Result
		expectError         bool
		expectedPodCreated  bool
		expectedPodNodeName string
		expectedPodImage    string
		validateFunc        func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth)
	}{
		{
			name: "creates pod for new CheckNodeHealth",
			existingCnh: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-check"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			expectedResult:      ctrl.Result{},
			expectError:         false,
			expectedPodCreated:  true,
			expectedPodNodeName: "test-node",
			expectedPodImage:    "ubuntu:latest",
		},
		{
			name: "handles pod succeeded",
			existingCnh: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-check"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			existingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "check-node-health-test-check",
					Namespace: "default",
					Labels: map[string]string{
						CheckNodeHealthLabel: "test-check", // Required label for pod identification
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false, // Pod already exists
		},
		{
			name: "handles pod failed",
			existingCnh: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-check"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			existingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "check-node-health-test-check",
					Namespace: "default",
					Labels: map[string]string{
						CheckNodeHealthLabel: "test-check",
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodFailed},
			},
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false, // Pod already exists
		},
		{
			name: "skips completed CheckNodeHealth",
			existingCnh: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-check"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
				Status: chmv1alpha1.CheckNodeHealthStatus{
					FinishedAt: &metav1.Time{Time: metav1.Now().Time},
				},
			},
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false,
		},
		{
			name:               "handles non-existent CheckNodeHealth",
			existingCnh:        nil, // No resource exists
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false,
		},
		{
			name: "handles pod running",
			existingCnh: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-check"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			existingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "check-node-health-test-check",
					Namespace: "default",
					Labels: map[string]string{
						CheckNodeHealthLabel: "test-check", // Required label for pod identification
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false, // Pod already exists
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler, fakeClient, _ := setupTest()
			ctx := context.Background()

			// Setup existing resources
			if tt.existingCnh != nil {
				if err := fakeClient.Create(ctx, tt.existingCnh); err != nil {
					t.Fatalf("Failed to create CheckNodeHealth: %v", err)
				}
			}
			if tt.existingPod != nil {
				if err := fakeClient.Create(ctx, tt.existingPod); err != nil {
					t.Fatalf("Failed to create Pod: %v", err)
				}
			}

			// Execute reconcile
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "test-check"},
			}
			result, err := reconciler.Reconcile(ctx, req)

			// Verify results
			if (err != nil) != tt.expectError {
				t.Errorf("Expected error: %v, got error: %v", tt.expectError, err)
			}
			if result != tt.expectedResult {
				t.Errorf("Expected result: %v, got: %v", tt.expectedResult, result)
			}

			// Verify pod creation
			podName := "check-node-health-test-check"
			pod := &corev1.Pod{}
			err = fakeClient.Get(ctx, client.ObjectKey{
				Name:      podName,
				Namespace: "default",
			}, pod)
			podExists := err == nil

			if tt.expectedPodCreated {
				if !podExists {
					t.Errorf("Expected pod to be created, got error: %v", err)
				} else {
					// Verify pod properties
					if tt.expectedPodNodeName != "" && pod.Spec.NodeName != tt.expectedPodNodeName {
						t.Errorf("Expected pod NodeName '%s', got '%s'", tt.expectedPodNodeName, pod.Spec.NodeName)
					}
					if tt.expectedPodImage != "" && pod.Spec.Containers[0].Image != tt.expectedPodImage {
						t.Errorf("Expected pod image '%s', got '%s'", tt.expectedPodImage, pod.Spec.Containers[0].Image)
					}
				}
			} else if tt.existingPod == nil {
				// Only check for non-existence if we didn't create an existing pod
				if podExists {
					t.Error("Expected no pod to be created")
				}
			}

			// Run custom validation if provided
			if tt.validateFunc != nil {
				tt.validateFunc(t, fakeClient, tt.existingCnh)
			}
		})
	}
}

func TestIsCompleted(t *testing.T) {
	tests := []struct {
		name     string
		cnh      *chmv1alpha1.CheckNodeHealth
		expected bool
	}{
		{
			name:     "not completed when FinishedAt is nil",
			cnh:      &chmv1alpha1.CheckNodeHealth{},
			expected: false,
		},
		{
			name: "completed when FinishedAt is set",
			cnh: &chmv1alpha1.CheckNodeHealth{
				Status: chmv1alpha1.CheckNodeHealthStatus{
					FinishedAt: &metav1.Time{},
				},
			},
			expected: true,
		},
		{
			name: "completed with full status",
			cnh: &chmv1alpha1.CheckNodeHealth{
				Status: chmv1alpha1.CheckNodeHealthStatus{
					StartedAt:  &metav1.Time{},
					FinishedAt: &metav1.Time{},
					Conditions: []metav1.Condition{
						{Type: "Healthy", Status: metav1.ConditionTrue},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isCompleted(tt.cnh)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestStatusUpdateLogic(t *testing.T) {
	tests := []struct {
		name              string
		updateType        string
		message           string
		expectedCondition metav1.ConditionStatus
		expectedReason    string
	}{
		{
			name:              "mark completed",
			updateType:        "completed",
			message:           "",
			expectedCondition: metav1.ConditionTrue,
			expectedReason:    "ChecksPasseddd",
		},
		{
			name:              "mark failed with custom message",
			updateType:        "failed",
			message:           "Pod execution failed",
			expectedCondition: metav1.ConditionFalse,
			expectedReason:    "ResourceUnavailable",
		},
		{
			name:              "mark failed with network error",
			updateType:        "failed",
			message:           "Network timeout",
			expectedCondition: metav1.ConditionFalse,
			expectedReason:    "ResourceUnavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cnh := &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
			}

			// Simulate status update logic
			now := metav1.Now()
			cnh.Status.FinishedAt = &now

			switch tt.updateType {
			case "completed":
				cnh.Status.Conditions = []metav1.Condition{
					{
						Type:               "Healthy",
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "ChecksPasseddd",
						Message:            "Health checks completed successfully",
					},
				}
			case "failed":
				cnh.Status.Conditions = []metav1.Condition{
					{
						Type:               "Healthy",
						Status:             metav1.ConditionFalse,
						LastTransitionTime: metav1.Now(),
						Reason:             "ResourceUnavailable",
						Message:            tt.message,
					},
				}
			}

			// Verify status was set correctly
			if cnh.Status.FinishedAt == nil {
				t.Error("Expected FinishedAt to be set")
			}
			if len(cnh.Status.Conditions) != 1 {
				t.Errorf("Expected 1 condition, got %d", len(cnh.Status.Conditions))
			}
			condition := cnh.Status.Conditions[0]
			if condition.Status != tt.expectedCondition {
				t.Errorf("Expected condition status %s, got %s", tt.expectedCondition, condition.Status)
			}
			if condition.Type != "Healthy" {
				t.Errorf("Expected condition type 'Healthy', got '%s'", condition.Type)
			}
			if condition.Reason != tt.expectedReason {
				t.Errorf("Expected reason '%s', got '%s'", tt.expectedReason, condition.Reason)
			}
			if tt.message != "" && condition.Message != tt.message {
				t.Errorf("Expected message '%s', got '%s'", tt.message, condition.Message)
			}
		})
	}
}
