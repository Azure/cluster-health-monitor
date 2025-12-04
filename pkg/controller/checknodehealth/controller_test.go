package checknodehealth

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				// Set CreationTimestamp if not already set
				ts := obj.GetCreationTimestamp()
				if ts.IsZero() {
					obj.SetCreationTimestamp(metav1.Now())
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		Build()

	reconciler := &CheckNodeHealthReconciler{
		Client:              fakeClient,
		Scheme:              scheme,
		CheckerPodImage:     "ubuntu:latest",
		CheckerPodNamespace: "default",
	}

	return reconciler, fakeClient, scheme
}

// getHealthyCondition retrieves the Healthy condition from CheckNodeHealth status
func getHealthyCondition(conditions []metav1.Condition) *metav1.Condition {
	for i, condition := range conditions {
		if condition.Type == ConditionTypeHealthy {
			return &conditions[i]
		}
	}
	return nil
}

func TestReconcile(t *testing.T) {
	tests := []struct {
		name                string
		existingCR          *chmv1alpha1.CheckNodeHealth
		existingPod         *corev1.Pod
		triggerDeletion     bool // If true, call Delete() before Reconcile()
		expectedResult      ctrl.Result
		expectError         bool
		expectedPodCreated  bool
		expectedPodDeleted  bool
		expectedPodNodeName string
		expectedPodImage    string
		validateFunc        func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth)
	}{
		{
			name: "create pod and adds finalizer to new CheckNodeHealth",
			existingCR: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-finalizer"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			triggerDeletion:     false,
			expectedResult:      ctrl.Result{RequeueAfter: 30 * time.Second}, // Requeue to check pod status
			expectError:         false,
			expectedPodCreated:  true, // Pod is created after finalizer is added
			expectedPodDeleted:  false,
			expectedPodNodeName: "test-node",
			expectedPodImage:    "ubuntu:latest",
			validateFunc: func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth) {
				// Fetch the updated CheckNodeHealth to verify finalizer
				updatedCnh := &chmv1alpha1.CheckNodeHealth{}
				err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, updatedCnh)
				if err != nil {
					t.Errorf("Failed to get updated CheckNodeHealth: %v", err)
					return
				}

				// Verify finalizer was added
				hasFinalizer := false
				for _, f := range updatedCnh.Finalizers {
					if f == CheckNodeHealthFinalizer {
						hasFinalizer = true
						break
					}
				}

				if !hasFinalizer {
					t.Errorf("Expected finalizer %q to be added, but it wasn't. Finalizers: %v",
						CheckNodeHealthFinalizer, updatedCnh.Finalizers)
				}
			},
		},
		{
			name: "handles pod succeeded and cleans up",
			existingCR: &chmv1alpha1.CheckNodeHealth{
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
			expectedPodDeleted: true,  // Pod should be cleaned up
			validateFunc: func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth) {
				// Verify CheckNodeHealth is marked as completed
				cr := &chmv1alpha1.CheckNodeHealth{}
				err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, cr)
				if err != nil {
					t.Errorf("Failed to get updated CheckNodeHealth: %v", err)
				} else if cr.Status.FinishedAt == nil {
					t.Error("Expected CheckNodeHealth to be marked as completed")
				}
			},
		},
		{
			name: "handles pod failed and cleans up",
			existingCR: &chmv1alpha1.CheckNodeHealth{
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
			expectedPodDeleted: true,  // Pod should be cleaned up
			validateFunc: func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth) {
				// Verify CheckNodeHealth is marked as completed
				updatedCnh := &chmv1alpha1.CheckNodeHealth{}
				err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, updatedCnh)
				if err != nil {
					t.Errorf("Failed to get updated CheckNodeHealth: %v", err)
				} else if updatedCnh.Status.FinishedAt == nil {
					t.Error("Expected CheckNodeHealth to be marked as completed")
				}
			},
		},
		{
			name: "skips completed CheckNodeHealth",
			existingCR: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-check"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
				Status: chmv1alpha1.CheckNodeHealthStatus{
					// FinishedAt != nil means the resource is completed
					FinishedAt: &metav1.Time{Time: metav1.Now().Time},
				},
			},
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false,
			expectedPodDeleted: false,
		},
		{
			name:               "handles non-existent CheckNodeHealth",
			existingCR:         nil, // No resource exists
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false,
			expectedPodDeleted: false,
		},
		{
			name: "handles pod running without cleanup",
			existingCR: &chmv1alpha1.CheckNodeHealth{
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
			expectedResult:     ctrl.Result{RequeueAfter: 30 * time.Second}, // Requeue to check completion
			expectError:        false,
			expectedPodCreated: false, // Pod already exists
			expectedPodDeleted: false, // Running pod should not be deleted
		},
		{
			name: "removes finalizer after successful pod cleanup on deletion",
			existingCR: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-deletion",
					Finalizers: []string{CheckNodeHealthFinalizer},
				},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			existingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "check-node-health-test-deletion",
					Namespace: "default",
					Labels: map[string]string{
						CheckNodeHealthLabel: "test-deletion",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "checker",
							Image: "ubuntu:latest",
						},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			triggerDeletion:    true, // Call Delete() to set DeletionTimestamp
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false,
			expectedPodDeleted: true,
			validateFunc: func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth) {
				// The CheckNodeHealth should be fully deleted now
				deletedCnh := &chmv1alpha1.CheckNodeHealth{}
				err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, deletedCnh)
				if !apierrors.IsNotFound(err) {
					t.Error("Expected CheckNodeHealth to be fully deleted after finalizer removal, but it still exists")
				}
			},
		},
		{
			name: "pod stuck in pending - PodStartup is Unhealthy, Healthy condition is False",
			existingCR: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pending"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			existingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "check-node-health-test-pending",
					Namespace:         "default",
					CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Minute)), // Old enough to timeout
					Labels: map[string]string{
						CheckNodeHealthLabel: "test-pending",
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false,
			expectedPodDeleted: true,
			validateFunc: func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth) {
				updatedCR := &chmv1alpha1.CheckNodeHealth{}
				if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, updatedCR); err != nil {
					t.Fatalf("Failed to get updated CheckNodeHealth: %v", err)
				}

				// Verify check completed
				if updatedCR.Status.FinishedAt == nil {
					t.Error("Expected FinishedAt to be set after check completion")
				}

				// Verify Healthy condition is False
				healthyCondition := getHealthyCondition(updatedCR.Status.Conditions)
				if healthyCondition == nil {
					t.Fatal("Healthy condition not found in status")
				}

				if healthyCondition.Status != metav1.ConditionFalse {
					t.Errorf("Expected condition status False, got %v", healthyCondition.Status)
				}

				if healthyCondition.Reason != ReasonCheckFailed {
					t.Errorf("Expected reason %q, got %q", ReasonCheckFailed, healthyCondition.Reason)
				}
			},
		},
		{
			name: "deletes expired CheckNodeHealth CR",
			existingCR: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-check",
					// creation timestamp is more than 6 hours ago
					CreationTimestamp: metav1.Time{Time: time.Now().Add(-7 * time.Hour)},
				},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
				Status: chmv1alpha1.CheckNodeHealthStatus{},
			},
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false,
			expectedPodDeleted: false,
			validateFunc: func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth) {
				// Verify CheckNodeHealth is deleted
				updatedCnh := &chmv1alpha1.CheckNodeHealth{}
				err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, updatedCnh)
				if !apierrors.IsNotFound(err) {
					t.Error("Expected CheckNodeHealth to be deleted, but it still exists")
				}
			},
		},
		{
			name: "some result is Unhealthy - Healthy condition is False",
			existingCR: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-unhealthy"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
				Status: chmv1alpha1.CheckNodeHealthStatus{
					Results: []chmv1alpha1.CheckResult{
						{
							Name:    "PodStartup",
							Status:  chmv1alpha1.CheckStatusUnknown,
							Message: "Pod started unknown",
						},
						{
							Name:      "SomeChecker",
							Status:    chmv1alpha1.CheckStatusUnhealthy,
							Message:   "Check failed",
							ErrorCode: "CheckFailed",
						},
					},
				},
			},
			existingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "check-node-health-test-unhealthy",
					Namespace: "default",
					Labels: map[string]string{
						CheckNodeHealthLabel: "test-unhealthy",
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false,
			expectedPodDeleted: true,
			validateFunc: func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth) {
				updatedCR := &chmv1alpha1.CheckNodeHealth{}
				if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, updatedCR); err != nil {
					t.Fatalf("Failed to get updated CheckNodeHealth: %v", err)
				}

				// Verify Healthy condition is False
				healthyCondition := getHealthyCondition(updatedCR.Status.Conditions)
				if healthyCondition == nil {
					t.Fatal("Healthy condition not found in status")
				}

				if healthyCondition.Status != metav1.ConditionFalse {
					t.Errorf("Expected condition status False, got %v", healthyCondition.Status)
				}

				if healthyCondition.Reason != ReasonCheckFailed {
					t.Errorf("Expected reason %q, got %q", ReasonCheckFailed, healthyCondition.Reason)
				}
			},
		},
		{
			name: "all checker results are Healthy - Healthy condition is True",
			existingCR: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-all-healthy"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
				Status: chmv1alpha1.CheckNodeHealthStatus{
					Results: []chmv1alpha1.CheckResult{
						{
							Name:    "PodStartup",
							Status:  chmv1alpha1.CheckStatusHealthy,
							Message: "Pod started successfully",
						},
						{
							Name:    "PodNetwork",
							Status:  chmv1alpha1.CheckStatusHealthy,
							Message: "Network check passed",
						},
					},
				},
			},
			existingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "check-node-health-test-all-healthy",
					Namespace: "default",
					Labels: map[string]string{
						CheckNodeHealthLabel: "test-all-healthy",
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false,
			expectedPodDeleted: true,
			validateFunc: func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth) {
				updatedCR := &chmv1alpha1.CheckNodeHealth{}
				if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, updatedCR); err != nil {
					t.Fatalf("Failed to get updated CheckNodeHealth: %v", err)
				}

				// Verify Healthy condition is True
				healthyCondition := getHealthyCondition(updatedCR.Status.Conditions)
				if healthyCondition == nil {
					t.Fatal("Healthy condition not found in status")
				}

				if healthyCondition.Status != metav1.ConditionTrue {
					t.Errorf("Expected condition status True, got %v", healthyCondition.Status)
				}

				if healthyCondition.Reason != ReasonCheckPassed {
					t.Errorf("Expected reason %q, got %q", ReasonCheckPassed, healthyCondition.Reason)
				}
			},
		},
		{
			name: "any checker result is Unknown - Healthy condition is Unknown",
			existingCR: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-unknown"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
				Status: chmv1alpha1.CheckNodeHealthStatus{
					Results: []chmv1alpha1.CheckResult{
						{
							Name:    "PodStartup",
							Status:  chmv1alpha1.CheckStatusHealthy,
							Message: "Pod started successfully",
						},
						{
							Name:    "SomeChecker",
							Status:  chmv1alpha1.CheckStatusUnknown,
							Message: "Unable to determine status",
						},
					},
				},
			},
			existingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "check-node-health-test-unknown",
					Namespace: "default",
					Labels: map[string]string{
						CheckNodeHealthLabel: "test-unknown",
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false,
			expectedPodDeleted: true,
			validateFunc: func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth) {
				updatedCR := &chmv1alpha1.CheckNodeHealth{}
				if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, updatedCR); err != nil {
					t.Fatalf("Failed to get updated CheckNodeHealth: %v", err)
				}

				// Verify Healthy condition is Unknown
				healthyCondition := getHealthyCondition(updatedCR.Status.Conditions)
				if healthyCondition == nil {
					t.Fatal("Healthy condition not found in status")
				}

				if healthyCondition.Status != metav1.ConditionUnknown {
					t.Errorf("Expected condition status Unknown, got %v", healthyCondition.Status)
				}

				if healthyCondition.Reason != ReasonCheckUnknown {
					t.Errorf("Expected reason %q, got %q", ReasonCheckUnknown, healthyCondition.Reason)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler, fakeClient, _ := setupTest()
			ctx := context.Background()

			// Setup existing resources
			if tt.existingCR != nil {
				if err := fakeClient.Create(ctx, tt.existingCR); err != nil {
					t.Fatalf("Failed to create CheckNodeHealth: %v", err)
				}
			}
			if tt.existingPod != nil {
				if err := fakeClient.Create(ctx, tt.existingPod); err != nil {
					t.Fatalf("Failed to create Pod: %v", err)
				}
			}

			// Trigger deletion if requested (sets DeletionTimestamp)
			if tt.triggerDeletion && tt.existingCR != nil {
				if err := fakeClient.Delete(ctx, tt.existingCR); err != nil {
					t.Fatalf("Failed to delete CheckNodeHealth: %v", err)
				}
			}

			// Execute reconcile
			cnhName := "test-check"
			if tt.existingCR != nil {
				cnhName = tt.existingCR.Name
			}
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{Name: cnhName},
			}
			result, err := reconciler.Reconcile(ctx, req)

			// Verify results
			if (err != nil) != tt.expectError {
				t.Errorf("Expected error: %v, got error: %v", tt.expectError, err)
			}
			if result != tt.expectedResult {
				t.Errorf("Expected result: %v, got: %v", tt.expectedResult, result)
			}

			// Verify pod creation/deletion
			podName := "check-node-health-" + cnhName
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
			}

			if tt.expectedPodDeleted {
				if podExists {
					t.Errorf("Expected pod to be deleted, but it still exists")
				}
			} else if tt.existingPod != nil {
				// Pod should still exist if we're not expecting deletion
				if !podExists {
					t.Errorf("Expected pod to remain, but it was deleted")
				}
			} else if !tt.expectedPodCreated {
				// Only check for non-existence if we didn't create an existing pod and don't expect creation
				if podExists {
					t.Error("Expected no pod to be created")
				}
			}

			// Run custom validation if provided
			if tt.validateFunc != nil && tt.existingCR != nil {
				tt.validateFunc(t, fakeClient, tt.existingCR)
			}
		})
	}
}
