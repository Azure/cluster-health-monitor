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
			expectedPodDeleted:  false,
			expectedPodNodeName: "test-node",
			expectedPodImage:    "ubuntu:latest",
		},
		{
			name: "handles pod succeeded and cleans up",
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
			name: "handles pod failed and cleans up",
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
			existingCnh: &chmv1alpha1.CheckNodeHealth{
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
			existingCnh:        nil, // No resource exists
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false,
			expectedPodDeleted: false,
		},
		{
			name: "handles pod running without cleanup",
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
			expectedPodDeleted: false, // Running pod should not be deleted
		},
		{
			name: "adds finalizer to new CheckNodeHealth",
			existingCnh: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-finalizer"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			triggerDeletion:     false,
			expectedResult:      ctrl.Result{},
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
			name: "removes finalizer after successful pod cleanup on deletion",
			existingCnh: &chmv1alpha1.CheckNodeHealth{
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
				if err == nil {
					t.Error("Expected CheckNodeHealth to be fully deleted after finalizer removal, but it still exists")
				}
			},
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

			// Trigger deletion if requested (sets DeletionTimestamp)
			if tt.triggerDeletion && tt.existingCnh != nil {
				if err := fakeClient.Delete(ctx, tt.existingCnh); err != nil {
					t.Fatalf("Failed to delete CheckNodeHealth: %v", err)
				}
			}

			// Execute reconcile
			cnhName := "test-check"
			if tt.existingCnh != nil {
				cnhName = tt.existingCnh.Name
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
			if tt.validateFunc != nil && tt.existingCnh != nil {
				tt.validateFunc(t, fakeClient, tt.existingCnh)
			}
		})
	}
}
