package checknodehealth

import (
	"context"
	"fmt"
	"maps"
	"slices"
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
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/gpu"
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/podnetwork"
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
		WithStatusSubresource(&chmv1alpha1.CheckNodeHealth{}, &corev1.Node{}). // Enable status subresource
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
		APIReader:           fakeClient,
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

// getNodeConditionByType retrieves a condition of the given type from a Node's status
func getNodeConditionByType(conditions []corev1.NodeCondition, conditionType corev1.NodeConditionType) *corev1.NodeCondition {
	for i, condition := range conditions {
		if condition.Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// getNodeHealthyCondition retrieves the NodeHealthy condition from a Node's status
func getNodeHealthyCondition(conditions []corev1.NodeCondition) *corev1.NodeCondition {
	return getNodeConditionByType(conditions, NodeConditionNodeHealthy)
}

func TestReconcile(t *testing.T) {
	tests := []struct {
		name                string
		existingCR          *chmv1alpha1.CheckNodeHealth
		existingPod         *corev1.Pod
		existingNode        *corev1.Node
		enableNodeCondition bool
		circuitBreakers     *NodeConditionCircuitBreakers
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
			name: "handles pod failed immediately and cleans up",
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
				// Admission rejection to simulate an invalid request where the controller tried to create a pod requesting all the gpu on
				// a node when they were already in use.
				Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "UnexpectedAdmissionError"},
			},
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false, // Pod already exists
			expectedPodDeleted: true,  // Pod should be cleaned up
			validateFunc: func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth) {
				updatedCnh := &chmv1alpha1.CheckNodeHealth{}
				if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, updatedCnh); err != nil {
					t.Fatalf("Failed to get updated CheckNodeHealth: %v", err)
				}
				if updatedCnh.Status.FinishedAt == nil {
					t.Error("Expected CheckNodeHealth to be marked as completed")
				}

				// PodStartup result should be recorded as Unknown because the pod could have failed to start due to being an invalid
				// request. There is not sufficient evidence to flag node as unhealthy yet.
				var podStartup *chmv1alpha1.CheckResult
				for i := range updatedCnh.Status.Results {
					if updatedCnh.Status.Results[i].Name == "PodStartup" {
						podStartup = &updatedCnh.Status.Results[i]
					}
				}
				if podStartup == nil {
					t.Fatal("Expected a PodStartup result to be recorded")
				}
				if podStartup.Status != chmv1alpha1.CheckStatusUnknown {
					t.Errorf("PodStartup status = %v, want %v", podStartup.Status, chmv1alpha1.CheckStatusUnknown)
				}
				if podStartup.ErrorCode != ErrorCodeCheckNotReported {
					t.Errorf("PodStartup error code = %q, want %q", podStartup.ErrorCode, ErrorCodeCheckNotReported)
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
			existingNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node", Labels: map[string]string{"kubernetes.io/os": "linux"}},
			},
			enableNodeCondition: true,
			circuitBreakers:     NewNodeConditionCircuitBreakers(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown),
			expectedResult:      ctrl.Result{},
			expectError:         false,
			expectedPodCreated:  false,
			expectedPodDeleted:  true,
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

				// Verify node condition is set to False
				node := &corev1.Node{}
				if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "test-node"}, node); err != nil {
					t.Fatalf("Failed to get node: %v", err)
				}
				nodeCondition := getNodeHealthyCondition(node.Status.Conditions)
				if nodeCondition == nil {
					t.Fatal("Expected NodeHealthy condition on node, but not found")
				}
				if nodeCondition.Status != corev1.ConditionFalse {
					t.Errorf("Expected node condition status False, got %v", nodeCondition.Status)
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
			existingNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node", Labels: map[string]string{"kubernetes.io/os": "linux"}},
			},
			enableNodeCondition: true,
			circuitBreakers:     NewNodeConditionCircuitBreakers(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown),
			expectedResult:      ctrl.Result{},
			expectError:         false,
			expectedPodCreated:  false,
			expectedPodDeleted:  true,
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

				// Verify node condition is set to False
				node := &corev1.Node{}
				if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "test-node"}, node); err != nil {
					t.Fatalf("Failed to get node: %v", err)
				}
				nodeCondition := getNodeHealthyCondition(node.Status.Conditions)
				if nodeCondition == nil {
					t.Fatal("Expected NodeHealthy condition on node, but not found")
				}
				if nodeCondition.Status != corev1.ConditionFalse {
					t.Errorf("Expected node condition status False, got %v", nodeCondition.Status)
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
			existingNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node", Labels: map[string]string{"kubernetes.io/os": "linux"}},
			},
			enableNodeCondition: true,
			circuitBreakers:     NewNodeConditionCircuitBreakers(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown),
			expectedResult:      ctrl.Result{},
			expectError:         false,
			expectedPodCreated:  false,
			expectedPodDeleted:  true,
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

				// Verify node condition is set to True
				node := &corev1.Node{}
				if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "test-node"}, node); err != nil {
					t.Fatalf("Failed to get node: %v", err)
				}
				nodeCondition := getNodeHealthyCondition(node.Status.Conditions)
				if nodeCondition == nil {
					t.Fatal("Expected NodeHealthy condition on node, but not found")
				}
				if nodeCondition.Status != corev1.ConditionTrue {
					t.Errorf("Expected node condition status True, got %v", nodeCondition.Status)
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
		{
			name: "missing required result - Healthy condition is Unknown",
			existingCR: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-missing-result"},
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
						// PodNetwork is missing - this should cause Unknown status
					},
				},
			},
			existingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "check-node-health-test-missing-result",
					Namespace: "default",
					Labels: map[string]string{
						CheckNodeHealthLabel: "test-missing-result",
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

				// Verify Healthy condition is Unknown when required result is missing
				healthyCondition := getHealthyCondition(updatedCR.Status.Conditions)
				if healthyCondition == nil {
					t.Fatal("Healthy condition not found in status")
				}

				if healthyCondition.Status != metav1.ConditionUnknown {
					t.Errorf("Expected condition status Unknown, got %v", healthyCondition.Status)
				}
			},
		},
		{
			name: "circuit breaker open - skips node condition update",
			existingCR: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-circuit-breaker"},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
				Status: chmv1alpha1.CheckNodeHealthStatus{
					Results: []chmv1alpha1.CheckResult{
						{
							Name:    "PodStartup",
							Status:  chmv1alpha1.CheckStatusUnhealthy,
							Message: "Pod stuck in Pending",
						},
					},
				},
			},
			existingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "check-node-health-test-circuit-breaker",
					Namespace:         "default",
					CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
					Labels: map[string]string{
						CheckNodeHealthLabel: "test-circuit-breaker",
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			existingNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node", Labels: map[string]string{"kubernetes.io/os": "linux"}},
			},
			enableNodeCondition: true,
			circuitBreakers: func() *NodeConditionCircuitBreakers {
				// Create breakers whose non-GPU one is already open
				cbs := NewNodeConditionCircuitBreakers(1, 15*time.Minute, 10*time.Minute)
				cbs.For(gpuNodeInfo{}).RecordUnhealthyNode() // This trips the breaker (threshold=1)
				return cbs
			}(),
			expectedResult:     ctrl.Result{},
			expectError:        false,
			expectedPodCreated: false,
			expectedPodDeleted: true,
			validateFunc: func(t *testing.T, fakeClient client.Client, cnh *chmv1alpha1.CheckNodeHealth) {
				updatedCR := &chmv1alpha1.CheckNodeHealth{}
				if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, updatedCR); err != nil {
					t.Fatalf("Failed to get updated CheckNodeHealth: %v", err)
				}

				// Verify Healthy condition is False (CR still gets marked)
				healthyCondition := getHealthyCondition(updatedCR.Status.Conditions)
				if healthyCondition == nil {
					t.Fatal("Healthy condition not found in status")
				}
				if healthyCondition.Status != metav1.ConditionFalse {
					t.Errorf("Expected condition status False, got %v", healthyCondition.Status)
				}

				// Verify node condition is NOT set (circuit breaker blocked it)
				node := &corev1.Node{}
				if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "test-node"}, node); err != nil {
					t.Fatalf("Failed to get node: %v", err)
				}
				nodeCondition := getNodeHealthyCondition(node.Status.Conditions)
				if nodeCondition != nil {
					t.Error("Expected no NodeHealthy condition on node when circuit breaker is open")
				}
			},
		},
		{
			name: "extra result with all required results healthy - Healthy condition is True",
			existingCR: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{Name: "test-extra-result"},
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
						{
							Name:    "ExtraCheck",
							Status:  chmv1alpha1.CheckStatusHealthy,
							Message: "Extra check passed",
						},
					},
				},
			},
			existingPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "check-node-health-test-extra-result",
					Namespace: "default",
					Labels: map[string]string{
						CheckNodeHealthLabel: "test-extra-result",
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

				// Verify Healthy condition is True when all required results are healthy
				// (extra healthy results are fine)
				healthyCondition := getHealthyCondition(updatedCR.Status.Conditions)
				if healthyCondition == nil {
					t.Fatal("Healthy condition not found in status")
				}

				if healthyCondition.Status != metav1.ConditionTrue {
					t.Errorf("Expected condition status True, got %v", healthyCondition.Status)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler, fakeClient, _ := setupTest()
			reconciler.EnableNodeCondition = tt.enableNodeCondition
			reconciler.CircuitBreakers = tt.circuitBreakers
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
			if tt.existingNode != nil {
				if err := fakeClient.Create(ctx, tt.existingNode); err != nil {
					t.Fatalf("Failed to create Node: %v", err)
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

func TestUpdateNodeCondition_NilHealthyCondition(t *testing.T) {
	reconciler, fakeClient, _ := setupTest()
	reconciler.EnableNodeCondition = true
	reconciler.CircuitBreakers = NewNodeConditionCircuitBreakers(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown)

	ctx := context.Background()

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node", Labels: map[string]string{"kubernetes.io/os": "linux"}},
	}
	if err := fakeClient.Create(ctx, node); err != nil {
		t.Fatalf("Failed to create node: %v", err)
	}

	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "test-nil-cond"},
		Spec: chmv1alpha1.CheckNodeHealthSpec{
			NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
		},
		Status: chmv1alpha1.CheckNodeHealthStatus{
			Conditions: nil, // No Healthy condition
		},
	}

	// updateNodeCondition should return nil without updating the node
	err := reconciler.updateNodeCondition(ctx, cnh, gpuNodeInfo{})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify node was NOT updated (no NodeHealthy condition added)
	updatedNode := &corev1.Node{}
	if err := fakeClient.Get(ctx, client.ObjectKey{Name: "test-node"}, updatedNode); err != nil {
		t.Fatalf("Failed to get node: %v", err)
	}
	nodeCondition := getNodeHealthyCondition(updatedNode.Status.Conditions)
	if nodeCondition != nil {
		t.Error("Expected no NodeHealthy condition when Healthy condition is nil")
	}
}

func TestDetermineHealthyCondition(t *testing.T) {
	r := &CheckNodeHealthReconciler{}

	tests := []struct {
		name        string
		results     []chmv1alpha1.CheckResult
		wantStatus  metav1.ConditionStatus
		wantReason  string
		wantMessage string
	}{
		{
			name: "all results healthy reports True",
			results: []chmv1alpha1.CheckResult{
				{Name: "PodStartup", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantStatus:  metav1.ConditionTrue,
			wantReason:  ReasonCheckPassed,
			wantMessage: "PodStartup: Healthy\nPodNetwork: Healthy",
		},
		{
			name: "any result unhealthy reports false. Takes precedence over unknown",
			results: []chmv1alpha1.CheckResult{
				{Name: "PodStartup", Status: chmv1alpha1.CheckStatusUnknown},
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusUnhealthy},
			},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  ReasonCheckFailed,
			wantMessage: "PodStartup: Unknown\nPodNetwork: Unhealthy",
		},
		{
			name: "any result unknown reports Unknown",
			results: []chmv1alpha1.CheckResult{
				{Name: "PodStartup", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusUnknown},
			},
			wantStatus:  metav1.ConditionUnknown,
			wantReason:  ReasonCheckUnknown,
			wantMessage: "PodStartup: Healthy\nPodNetwork: Unknown",
		},
		{
			// markCompleted fills in anything unreported, so reaching here with nothing means there
			// was nothing to judge.
			name:        "no results present reports Unknown",
			results:     nil,
			wantStatus:  metav1.ConditionUnknown,
			wantReason:  ReasonCheckUnknown,
			wantMessage: "",
		},
		{
			name: "gpu check results are taken into account",
			results: []chmv1alpha1.CheckResult{
				{Name: "PodStartup", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusUnhealthy},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusUnknown},
			},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  ReasonCheckFailed,
			wantMessage: "PodStartup: Healthy\nPodNetwork: Healthy\nNcclAllReduce: Unhealthy\nGpuHostBandwidth: Unknown",
		},
		{
			name: "all checks healthy on a gpu node reports True",
			results: []chmv1alpha1.CheckResult{
				{Name: "PodStartup", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuPeerBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantStatus:  metav1.ConditionTrue,
			wantReason:  ReasonCheckPassed,
			wantMessage: "PodStartup: Healthy\nPodNetwork: Healthy\nNcclAllReduce: Healthy\nGpuHostBandwidth: Healthy\nGpuPeerBandwidth: Healthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cnh := &chmv1alpha1.CheckNodeHealth{
				Status: chmv1alpha1.CheckNodeHealthStatus{Results: tt.results},
			}
			gotStatus, gotReason, gotMessage := r.determineHealthyCondition(cnh)
			if gotStatus != tt.wantStatus {
				t.Errorf("status: got %q, want %q", gotStatus, tt.wantStatus)
			}
			if gotReason != tt.wantReason {
				t.Errorf("reason: got %q, want %q", gotReason, tt.wantReason)
			}
			if gotMessage != tt.wantMessage {
				t.Errorf("message:\n got: %q\nwant: %q", gotMessage, tt.wantMessage)
			}
		})
	}
}

func testCNH(name string) *chmv1alpha1.CheckNodeHealth {
	return &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: chmv1alpha1.CheckNodeHealthSpec{
			NodeRef: chmv1alpha1.NodeReference{Name: "node-1"},
		},
	}
}

// finishedCheckerPod returns a checker pod the reconciler treats as finished, with a container that
// started so PodStartup is recorded as Healthy.
func finishedCheckerPod(cnhName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "check-node-health-" + cnhName,
			Namespace: "default",
			Labels:    map[string]string{CheckNodeHealthLabel: cnhName},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodSucceeded,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "checker",
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{StartedAt: metav1.Now()},
				},
			}},
		},
	}
}

func TestCheckerSpecFor(t *testing.T) {
	t.Parallel()

	baseNames := []string{"PodStartup", "PodNetwork"}
	gpuNames := []string{"PodStartup", "PodNetwork", "NcclAllReduce", "GpuHostBandwidth", "GpuPeerBandwidth"}

	tests := []struct {
		name            string
		info            gpuNodeInfo
		enableGPUChecks bool
		wantImage       string
		wantTimeout     time.Duration
		wantNames       []string
		// wantGPU is true when the spec must carry the GPU parameters, which is also what makes the
		// pod get the GPU shape.
		wantGPU bool
	}{
		{
			name:            "non-gpu node with the gate off",
			info:            gpuNodeInfo{isGPUNode: false, gpuCount: 0, sku: "Standard_D8d_v5"},
			enableGPUChecks: false,
			wantImage:       "base-image",
			wantTimeout:     PodTimeout,
			wantNames:       baseNames,
			wantGPU:         false,
		},
		{
			name:            "non-gpu node with the gate on",
			info:            gpuNodeInfo{isGPUNode: false, gpuCount: 0, sku: "Standard_D8d_v5"},
			enableGPUChecks: true,
			wantImage:       "base-image",
			wantTimeout:     PodTimeout,
			wantNames:       baseNames,
			wantGPU:         false,
		},
		{
			// The node still reports per-check conditions, but it runs the ordinary checker.
			name:            "gpu node with the gate off",
			info:            gpuNodeInfo{isGPUNode: true, gpuCount: 8, sku: "Standard_ND96isr_H100_v5"},
			enableGPUChecks: false,
			wantImage:       "base-image",
			wantTimeout:     PodTimeout,
			wantNames:       baseNames,
			wantGPU:         false,
		},
		{
			name:            "gpu node with the gate on",
			info:            gpuNodeInfo{isGPUNode: true, gpuCount: 8, sku: "Standard_ND96isr_H100_v5"},
			enableGPUChecks: true,
			wantImage:       "gpu-image",
			wantTimeout:     GPUPodTimeout,
			wantNames:       gpuNames,
			wantGPU:         true,
		},
		{
			name:            "gpu node only expects the gpu checks its sku runs",
			info:            gpuNodeInfo{isGPUNode: true, gpuCount: 2, sku: "Standard_NV72ads_A10_v5"},
			enableGPUChecks: true,
			wantImage:       "gpu-image",
			wantTimeout:     GPUPodTimeout,
			// The A10 has no NVLink, so peer bandwidth is not one of its checks.
			wantNames: []string{"PodStartup", "PodNetwork", "NcclAllReduce", "GpuHostBandwidth"},
			wantGPU:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &CheckNodeHealthReconciler{
				CheckerPodImage:    "base-image",
				GPUCheckerPodImage: "gpu-image",
				EnableGPUChecks:    tt.enableGPUChecks,
			}

			spec := r.checkerSpecFor(tt.info)
			if spec.image != tt.wantImage {
				t.Errorf("image = %q, want %q", spec.image, tt.wantImage)
			}
			if spec.podTimeout != tt.wantTimeout {
				t.Errorf("podTimeout = %s, want %s", spec.podTimeout, tt.wantTimeout)
			}
			if !slices.Equal(spec.checkerNames, tt.wantNames) {
				t.Errorf("checkerNames = %v, want %v", spec.checkerNames, tt.wantNames)
			}
			if gotGPU := spec.gpu != nil; gotGPU != tt.wantGPU {
				t.Errorf("gpu set = %v, want %v", gotGPU, tt.wantGPU)
			}
			if tt.wantGPU && *spec.gpu != tt.info {
				t.Errorf("gpu = %+v, want %+v", *spec.gpu, tt.info)
			}
		})
	}
}

// TestReconcileNodeConditionRouting checks which Node conditions a check publishes: a single
// aggregate NodeHealthy for non-GPU nodes, and granular conditions for GPU nodes.
func TestReconcileNodeConditionRouting(t *testing.T) {
	tests := []struct {
		name string
		// gpuNode makes the target node a GPU node.
		gpuNode bool
		// sku overrides the GPU node's sku when set (default: Standard_ND96isr_H100_v5).
		sku string
		// enableGPUChecks gates the intrusive GPU checks.
		enableGPUChecks bool
		// seededResults stand in for the results the checker pod would have written. PodStartup is
		// recorded by the controller itself, so it is not seeded here.
		seededResults []chmv1alpha1.CheckResult
		// wantHealthy is the aggregate condition on the CheckNodeHealth.
		wantHealthy metav1.ConditionStatus
		// wantNodeConditions is exactly the set of managed conditions the node may carry.
		wantNodeConditions map[corev1.NodeConditionType]corev1.ConditionStatus
	}{
		{
			name:            "failing gpu check fails only its own condition",
			gpuNode:         true,
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusUnhealthy, ErrorCode: gpu.ErrorCodeCorrectness},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuPeerBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy: metav1.ConditionFalse,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionPodStartupHealthy: corev1.ConditionTrue,
				NodeConditionPodNetworkHealthy: corev1.ConditionTrue,
				// Only the condition the failing check is explicitly tied to goes False. NCCL got past
				// its GPU count check, but its bus bandwidth is unknown since NCCL did not pass.
				NodeConditionGPUCountHealthy:              corev1.ConditionTrue,
				NodeConditionGPUHostBandwidthHealthy:      corev1.ConditionTrue,
				NodeConditionGPUPeerBandwidthHealthy:      corev1.ConditionTrue,
				NodeConditionGPUAllReduceBandwidthHealthy: corev1.ConditionUnknown,
				NodeConditionGPUCorrectnessHealthy:        corev1.ConditionFalse,
			},
		},
		{
			name:            "all checks passing on a gpu node",
			gpuNode:         true,
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuPeerBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy: metav1.ConditionTrue,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionPodStartupHealthy:            corev1.ConditionTrue,
				NodeConditionPodNetworkHealthy:            corev1.ConditionTrue,
				NodeConditionGPUCountHealthy:              corev1.ConditionTrue,
				NodeConditionGPUHostBandwidthHealthy:      corev1.ConditionTrue,
				NodeConditionGPUPeerBandwidthHealthy:      corev1.ConditionTrue,
				NodeConditionGPUAllReduceBandwidthHealthy: corev1.ConditionTrue,
				NodeConditionGPUCorrectnessHealthy:        corev1.ConditionTrue,
			},
		},
		{
			name:    "gpu node without nvlink gets no peer bandwidth condition",
			gpuNode: true,
			// The A10 has no NVLink, so it never runs peer bandwidth and gets no condition for it.
			sku:             "Standard_NV72ads_A10_v5",
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy: metav1.ConditionTrue,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionPodStartupHealthy:            corev1.ConditionTrue,
				NodeConditionPodNetworkHealthy:            corev1.ConditionTrue,
				NodeConditionGPUCountHealthy:              corev1.ConditionTrue,
				NodeConditionGPUHostBandwidthHealthy:      corev1.ConditionTrue,
				NodeConditionGPUAllReduceBandwidthHealthy: corev1.ConditionTrue,
				NodeConditionGPUCorrectnessHealthy:        corev1.ConditionTrue,
			},
		},
		{
			// A base check failing on a GPU node leaves the GPU conditions alone.
			name:            "base check failure on a gpu node",
			gpuNode:         true,
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusUnhealthy, ErrorCode: podnetwork.ErrorCodeNetworkConnectivityFailed},
				{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuPeerBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy: metav1.ConditionFalse,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionPodStartupHealthy:            corev1.ConditionTrue,
				NodeConditionPodNetworkHealthy:            corev1.ConditionFalse,
				NodeConditionGPUCountHealthy:              corev1.ConditionTrue,
				NodeConditionGPUHostBandwidthHealthy:      corev1.ConditionTrue,
				NodeConditionGPUPeerBandwidthHealthy:      corev1.ConditionTrue,
				NodeConditionGPUAllReduceBandwidthHealthy: corev1.ConditionTrue,
				NodeConditionGPUCorrectnessHealthy:        corev1.ConditionTrue,
			},
		},
		{
			// The routing is independent of whether the gpu checks are enabled, so a GPU node
			// running only the base checks still stays off NodeHealthy. The checks that did not run
			// get no condition at all.
			name:            "gpu node with the intrusive checks disabled",
			gpuNode:         true,
			enableGPUChecks: false,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy: metav1.ConditionTrue,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionPodStartupHealthy: corev1.ConditionTrue,
				NodeConditionPodNetworkHealthy: corev1.ConditionTrue,
			},
		},
		{
			// Non-GPU nodes keep the single aggregate condition existing automation acts on.
			name:            "non-gpu node reports only NodeHealthy",
			gpuNode:         false,
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy: metav1.ConditionTrue,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionNodeHealthy: corev1.ConditionTrue,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler, fakeClient, _ := setupTest()
			reconciler.EnableGPUChecks = tt.enableGPUChecks
			reconciler.EnableNodeCondition = true
			reconciler.CircuitBreakers = NewNodeConditionCircuitBreakers(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown)

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"kubernetes.io/os": "linux"}},
			}
			if tt.gpuNode {
				node = gpuNode("node-1")
				if tt.sku != "" {
					node.Labels[instanceTypeLabel] = tt.sku
				}
			}
			if err := fakeClient.Create(ctx, node); err != nil {
				t.Fatalf("creating the node: %v", err)
			}

			cnh := testCNH("cnh-routing")
			if err := fakeClient.Create(ctx, cnh); err != nil {
				t.Fatalf("creating the CR: %v", err)
			}
			cnh.Status.Results = tt.seededResults
			if err := fakeClient.Status().Update(ctx, cnh); err != nil {
				t.Fatalf("seeding reported results: %v", err)
			}
			if err := fakeClient.Create(ctx, finishedCheckerPod(cnh.Name)); err != nil {
				t.Fatalf("creating the checker pod: %v", err)
			}

			key := types.NamespacedName{Name: cnh.Name}
			for i := 0; i < 3; i++ {
				if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
					t.Fatalf("reconcile %d returned error: %v", i, err)
				}
			}

			updated := &chmv1alpha1.CheckNodeHealth{}
			if err := fakeClient.Get(ctx, key, updated); err != nil {
				t.Fatalf("reading the CR: %v", err)
			}
			if updated.Status.FinishedAt == nil {
				t.Fatal("the CR never reached a terminal state")
			}

			healthy := getHealthyCondition(updated.Status.Conditions)
			if healthy == nil {
				t.Fatal("Healthy condition not found on the CR")
			}
			if healthy.Status != tt.wantHealthy {
				t.Errorf("Healthy = %q, want %q (results: %+v)", healthy.Status, tt.wantHealthy, updated.Status.Results)
			}

			updatedNode := &corev1.Node{}
			if err := fakeClient.Get(ctx, client.ObjectKey{Name: "node-1"}, updatedNode); err != nil {
				t.Fatalf("reading the node: %v", err)
			}

			assertManagedNodeConditions(t, updatedNode, tt.wantNodeConditions)
		})
	}
}

// assertManagedNodeConditions checks the node carries exactly the wanted managed conditions with
// the wanted statuses, and no other condition this controller owns.
func assertManagedNodeConditions(t *testing.T, node *corev1.Node, want map[corev1.NodeConditionType]corev1.ConditionStatus) {
	t.Helper()

	got := map[corev1.NodeConditionType]corev1.ConditionStatus{}
	for _, c := range node.Status.Conditions {
		if slices.Contains(managedNodeConditionTypes, c.Type) {
			got[c.Type] = c.Status
		}
	}

	if !maps.Equal(got, want) {
		t.Errorf("managed node conditions = %v, want %v", got, want)
	}
}

// TestUpdateNodeConditionClearsStaleConditions covers a node that already carries conditions it is
// no longer meant to have. That happens when a node starts or stops being seen as a GPU node, and
// when the intrusive GPU checks are gated off so their checks stop reporting.
func TestUpdateNodeConditionClearsStaleConditions(t *testing.T) {
	tests := []struct {
		name string
		// stale are the managed conditions already on the node before the check runs.
		stale []corev1.NodeConditionType
		info  gpuNodeInfo
		// results are what the completed check reported.
		results []chmv1alpha1.CheckResult
		want    map[corev1.NodeConditionType]corev1.ConditionStatus
	}{
		{
			name:  "node newly seen as a gpu node drops its leftover NodeHealthy",
			stale: []corev1.NodeConditionType{NodeConditionNodeHealthy},
			info:  gpuNodeInfo{isGPUNode: true},
			results: []chmv1alpha1.CheckResult{
				{Name: "PodStartup", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
			},
			want: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionPodStartupHealthy: corev1.ConditionTrue,
				NodeConditionPodNetworkHealthy: corev1.ConditionTrue,
			},
		},
		{
			name: "node no longer seen as a gpu node drops its granular conditions",
			stale: []corev1.NodeConditionType{
				NodeConditionPodStartupHealthy,
				NodeConditionGPUCorrectnessHealthy,
			},
			info: gpuNodeInfo{},
			results: []chmv1alpha1.CheckResult{
				{Name: "PodStartup", Status: chmv1alpha1.CheckStatusHealthy},
			},
			want: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionNodeHealthy: corev1.ConditionTrue,
			},
		},
		{
			name: "gpu node whose gpu checks stopped running drops its gpu conditions",
			stale: []corev1.NodeConditionType{
				NodeConditionGPUCountHealthy,
				NodeConditionGPUHostBandwidthHealthy,
				NodeConditionGPUPeerBandwidthHealthy,
				NodeConditionGPUAllReduceBandwidthHealthy,
				NodeConditionGPUCorrectnessHealthy,
			},
			info: gpuNodeInfo{isGPUNode: true},
			results: []chmv1alpha1.CheckResult{
				{Name: "PodStartup", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
			},
			want: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionPodStartupHealthy: corev1.ConditionTrue,
				NodeConditionPodNetworkHealthy: corev1.ConditionTrue,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler, fakeClient, _ := setupTest()
			reconciler.EnableNodeCondition = true
			reconciler.CircuitBreakers = NewNodeConditionCircuitBreakers(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown)

			conditions := []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
			for _, stale := range tt.stale {
				conditions = append(conditions, corev1.NodeCondition{
					Type: stale, Status: corev1.ConditionFalse, Reason: ReasonCheckFailed,
				})
			}
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"kubernetes.io/os": "linux"}},
				Status:     corev1.NodeStatus{Conditions: conditions},
			}
			if err := fakeClient.Create(ctx, node); err != nil {
				t.Fatalf("creating the node: %v", err)
			}

			cnh := testCNH("cnh-switch")
			cnh.Status.Results = tt.results
			cnh.Status.Conditions = []metav1.Condition{{
				Type:               ConditionTypeHealthy,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             ReasonCheckPassed,
			}}
			if err := reconciler.updateNodeCondition(ctx, cnh, tt.info); err != nil {
				t.Fatalf("updateNodeCondition returned error: %v", err)
			}

			updatedNode := &corev1.Node{}
			if err := fakeClient.Get(ctx, client.ObjectKey{Name: "node-1"}, updatedNode); err != nil {
				t.Fatalf("reading the node: %v", err)
			}

			assertManagedNodeConditions(t, updatedNode, tt.want)

			// Unrelated conditions owned by other controllers must survive.
			if getNodeConditionByType(updatedNode.Status.Conditions, corev1.NodeReady) == nil {
				t.Error("the Ready condition was dropped")
			}
		})
	}
}

// TestCircuitBreakersAreIndependent checks that a run of failures on one kind of node does not open
// the breaker guarding the other kind's condition.
func TestCircuitBreakersAreIndependent(t *testing.T) {
	tests := []struct {
		name string
		// failingNodeIsGPU picks which kind of node produces the run of failures.
		failingNodeIsGPU bool
	}{
		{name: "gpu failures leave the NodeHealthy breaker closed", failingNodeIsGPU: true},
		{name: "non-gpu failures leave the gpu node breaker closed", failingNodeIsGPU: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler, fakeClient, _ := setupTest()
			reconciler.EnableGPUChecks = true
			reconciler.EnableNodeCondition = true
			reconciler.CircuitBreakers = NewNodeConditionCircuitBreakers(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown)

			failing := gpuNodeInfo{isGPUNode: tt.failingNodeIsGPU}
			other := gpuNodeInfo{isGPUNode: !tt.failingNodeIsGPU}

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"kubernetes.io/os": "linux"}},
			}
			if tt.failingNodeIsGPU {
				node = gpuNode("node-1")
			}
			if err := fakeClient.Create(ctx, node); err != nil {
				t.Fatalf("creating the node: %v", err)
			}

			// Run more consecutive failures than the threshold on one kind of node.
			for i := 0; i < DefaultCircuitBreakerThreshold+1; i++ {
				cnh := testCNH(fmt.Sprintf("cnh-failure-%d", i))
				if err := fakeClient.Create(ctx, cnh); err != nil {
					t.Fatalf("creating the CR: %v", err)
				}
				cnh.Status.Results = []chmv1alpha1.CheckResult{
					{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusUnhealthy, ErrorCode: "NetworkConnectivityFailed"},
					{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusHealthy},
					{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
					{Name: "GpuPeerBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
				}
				if err := fakeClient.Status().Update(ctx, cnh); err != nil {
					t.Fatalf("seeding reported results: %v", err)
				}
				if err := fakeClient.Create(ctx, finishedCheckerPod(cnh.Name)); err != nil {
					t.Fatalf("creating the checker pod: %v", err)
				}

				key := types.NamespacedName{Name: cnh.Name}
				for j := 0; j < 3; j++ {
					if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
						t.Fatalf("reconcile of %s returned error: %v", cnh.Name, err)
					}
				}
			}

			if reconciler.CircuitBreakers.For(failing).Allow() {
				t.Error("the breaker for the failing node kind stayed closed, want it open")
			}
			if !reconciler.CircuitBreakers.For(other).Allow() {
				t.Error("the breaker for the other node kind opened, want it closed")
			}
		})
	}
}

func TestRecordMissingResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info gpuNodeInfo
		// enableGPUChecks gates the intrusive checks, which is what makes them expected.
		enableGPUChecks bool
		// reported is seeded before the call and must survive it.
		reported  []string
		wantAdded []string
	}{
		{
			name:      "non-gpu node fills the base checks",
			info:      gpuNodeInfo{},
			reported:  []string{"PodStartup"},
			wantAdded: []string{"PodNetwork"},
		},
		{
			name:            "gpu node also fills the gpu checks",
			info:            gpuNodeInfo{isGPUNode: true},
			enableGPUChecks: true,
			reported:        []string{"PodStartup", "PodNetwork", "NcclAllReduce"},
			wantAdded:       []string{"GpuHostBandwidth", "GpuPeerBandwidth"},
		},
		{
			name: "gpu node only fills the gpu checks its sku runs",
			// The A10 has no NVLink, so peer bandwidth never runs and is not expected.
			info:            gpuNodeInfo{isGPUNode: true, sku: "Standard_NV72ads_A10_v5"},
			enableGPUChecks: true,
			reported:        []string{"PodStartup", "PodNetwork"},
			wantAdded:       []string{"NcclAllReduce", "GpuHostBandwidth"},
		},
		{
			name:      "gpu node without the checks only fills the base checks",
			info:      gpuNodeInfo{isGPUNode: true},
			reported:  []string{"PodStartup"},
			wantAdded: []string{"PodNetwork"},
		},
		{
			name:      "non-gpu node does not fill the gpu checks",
			info:      gpuNodeInfo{},
			reported:  []string{"PodStartup", "PodNetwork"},
			wantAdded: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reconciler, c, _ := setupTest()
			reconciler.EnableGPUChecks = tt.enableGPUChecks
			cnh := testCNH("cnh-1")
			if err := c.Create(context.Background(), cnh); err != nil {
				t.Fatalf("creating the CR: %v", err)
			}
			for _, name := range tt.reported {
				cnh.Status.Results = append(cnh.Status.Results, chmv1alpha1.CheckResult{
					Name: name, Status: chmv1alpha1.CheckStatusHealthy, Message: "measured",
				})
			}
			if err := c.Status().Update(context.Background(), cnh); err != nil {
				t.Fatalf("seeding results: %v", err)
			}

			if err := reconciler.recordMissingResults(context.Background(), cnh, reconciler.checkerSpecFor(tt.info)); err != nil {
				t.Fatalf("recordMissingResults returned error: %v", err)
			}

			got := map[string]chmv1alpha1.CheckResult{}
			for _, result := range cnh.Status.Results {
				got[result.Name] = result
			}

			for _, name := range tt.reported {
				if r := got[name]; r.Status != chmv1alpha1.CheckStatusHealthy || r.Message != "measured" {
					t.Errorf("%s = %+v, want the reported result left alone", name, r)
				}
			}
			for _, name := range tt.wantAdded {
				added, ok := got[name]
				if !ok {
					t.Errorf("%s was not recorded, results = %+v", name, cnh.Status.Results)
					continue
				}
				if added.Status != chmv1alpha1.CheckStatusUnknown {
					t.Errorf("%s status = %q, want %q", name, added.Status, chmv1alpha1.CheckStatusUnknown)
				}
				if added.ErrorCode != ErrorCodeCheckNotReported {
					t.Errorf("%s code = %q, want %q", name, added.ErrorCode, ErrorCodeCheckNotReported)
				}
			}
			if want := len(tt.reported) + len(tt.wantAdded); len(got) != want {
				t.Errorf("got %d results, want %d: %+v", len(got), want, cnh.Status.Results)
			}
		})
	}
}
