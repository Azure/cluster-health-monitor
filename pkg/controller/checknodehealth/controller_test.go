package checknodehealth

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
		Recorder:            record.NewFakeRecorder(100),
	}

	return reconciler, fakeClient, scheme
}

// recordedEvents drains the events the reconciler's fake recorder has emitted so far.
func recordedEvents(t *testing.T, r *CheckNodeHealthReconciler) []string {
	t.Helper()

	recorder, ok := r.Recorder.(*record.FakeRecorder)
	if !ok {
		t.Fatalf("recorder is %T, want *record.FakeRecorder", r.Recorder)
	}
	var events []string
	for {
		select {
		case event := <-recorder.Events:
			events = append(events, event)
		default:
			return events
		}
	}
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
		circuitBreaker      *NodeConditionCircuitBreaker
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
					Annotations: runAnnotations(false, ""),
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
					Annotations: runAnnotations(false, ""),
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
					Annotations: runAnnotations(false, ""),
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
					Annotations: runAnnotations(false, ""),
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
					Annotations: runAnnotations(false, ""),
				},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			existingNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node", Labels: map[string]string{"kubernetes.io/os": "linux"}},
			},
			enableNodeCondition: true,
			circuitBreaker:      NewNodeConditionCircuitBreaker(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown),
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
					Annotations: runAnnotations(false, ""),
				},
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
			existingNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node", Labels: map[string]string{"kubernetes.io/os": "linux"}},
			},
			enableNodeCondition: true,
			circuitBreaker:      NewNodeConditionCircuitBreaker(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown),
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
					Annotations: runAnnotations(false, ""),
				},
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
			existingNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node", Labels: map[string]string{"kubernetes.io/os": "linux"}},
			},
			enableNodeCondition: true,
			circuitBreaker:      NewNodeConditionCircuitBreaker(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown),
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
					Annotations: runAnnotations(false, ""),
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
					Annotations: runAnnotations(false, ""),
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
					Annotations: runAnnotations(false, ""),
				},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			existingNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node", Labels: map[string]string{"kubernetes.io/os": "linux"}},
			},
			enableNodeCondition: true,
			circuitBreaker: func() *NodeConditionCircuitBreaker {
				// Create a breaker that is already open
				cb := NewNodeConditionCircuitBreaker(1, 15*time.Minute, 10*time.Minute)
				cb.RecordUnhealthyNode() // This trips the breaker (threshold=1)
				return cb
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
					Annotations: runAnnotations(false, ""),
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
			reconciler.CircuitBreaker = tt.circuitBreaker
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
	reconciler.CircuitBreaker = NewNodeConditionCircuitBreaker(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown)

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
// started so PodStartup is recorded as Healthy. It carries the run annotations the controller puts on
// the checker pods it creates; see runAnnotations.
func finishedCheckerPod(cnhName string, gpuNode bool, sku string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "check-node-health-" + cnhName,
			Namespace:   "default",
			Labels:      map[string]string{CheckNodeHealthLabel: cnhName},
			Annotations: runAnnotations(gpuNode, sku),
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

// runAnnotations returns the annotations the controller puts on the checker pods it creates. An empty
// sku means the pod does not run the GPU checks.
func runAnnotations(gpuNode bool, sku string) map[string]string {
	annotations := map[string]string{annotationNodeKind: NodeKindStandard}
	if gpuNode {
		annotations[annotationNodeKind] = NodeKindGPU
	}
	if sku != "" {
		annotations[annotationGPUChecksSKU] = sku
	}
	return annotations
}

// withoutRunAnnotations strips the run annotations, as on a checker pod created before the controller
// recorded them.
func withoutRunAnnotations(pod *corev1.Pod) *corev1.Pod {
	delete(pod.Annotations, annotationNodeKind)
	delete(pod.Annotations, annotationGPUChecksSKU)
	return pod
}

// TestCheckTimestampsComeFromThePodAfterRestart covers a controller that restarted mid-check. A
// restart only loses in-memory state, so it is simulated by having a fresh reconciler reconcile the
// state a restarted controller would find: the controller had created the checker pod but went down
// before recording StartedAt, and the pod finished before it came back.
func TestCheckTimestampsComeFromThePodAfterRestart(t *testing.T) {
	reconciler, fakeClient, _ := setupTest()
	ctx := context.Background()

	// The pod was created and finished well before now.
	podCreated := metav1.NewTime(time.Now().Add(-10 * time.Minute).Truncate(time.Second))
	containerFinished := metav1.NewTime(podCreated.Add(40 * time.Second))

	cnh := testCNH("restarted-mid-check")
	if err := fakeClient.Create(ctx, cnh); err != nil {
		t.Fatalf("creating the CR: %v", err)
	}

	pod := finishedCheckerPod(cnh.Name, false, "")
	pod.CreationTimestamp = podCreated
	pod.Status.ContainerStatuses[0].State.Terminated = &corev1.ContainerStateTerminated{
		StartedAt:  metav1.NewTime(podCreated.Add(5 * time.Second)),
		FinishedAt: containerFinished,
	}
	if err := fakeClient.Create(ctx, pod); err != nil {
		t.Fatalf("creating the checker pod: %v", err)
	}

	got := reconcileUntilTerminal(t, reconciler, fakeClient, types.NamespacedName{Name: cnh.Name})

	// The timestamps come from the pod, so the CR records when the check actually ran rather than
	// when the restarted controller got to it.
	if got.Status.StartedAt == nil || !got.Status.StartedAt.Equal(&podCreated) {
		t.Errorf("StartedAt = %v, want the pod's creation time %v", got.Status.StartedAt, podCreated)
	}
	if got.Status.FinishedAt == nil || !got.Status.FinishedAt.Equal(&containerFinished) {
		t.Errorf("FinishedAt = %v, want the container's termination time %v", got.Status.FinishedAt, containerFinished)
	}
}

func TestCheckerSpecFor(t *testing.T) {
	t.Parallel()

	baseNames := []string{"PodStartup", "PodNetwork"}
	gpuNames := []string{"PodStartup", "PodNetwork", "NcclAllReduce", "GpuHostBandwidth", "GpuPeerBandwidth"}
	h100 := "Standard_ND96isr_H100_v5"
	wait := 2 * time.Minute

	tests := []struct {
		name            string
		info            gpuNodeInfo
		enableGPUChecks bool
		// age is how long ago the CheckNodeHealth was created.
		age  time.Duration
		want checkerSpec
	}{
		{
			name:            "non-gpu node with the gate off",
			info:            gpuNodeInfo{sku: "Standard_D8d_v5"},
			enableGPUChecks: false,
			want:            checkerSpec{image: "base-image", timeout: PodTimeout, checkerNames: baseNames},
		},
		{
			name:            "non-gpu node with the gate on",
			info:            gpuNodeInfo{sku: "Standard_D8d_v5"},
			enableGPUChecks: true,
			want:            checkerSpec{image: "base-image", timeout: PodTimeout, checkerNames: baseNames},
		},
		{
			// The node still reports per-check conditions, but it runs the ordinary checker.
			name:            "gpu node with the gate off",
			info:            gpuNodeInfo{isGPUNode: true, claimableGPUs: 8, sku: h100},
			enableGPUChecks: false,
			want:            checkerSpec{gpuNode: true, image: "base-image", timeout: PodTimeout, checkerNames: baseNames},
		},
		{
			name:            "gpu node with healthy gpus benchmarks them",
			info:            gpuNodeInfo{isGPUNode: true, claimableGPUs: 8, sku: h100},
			enableGPUChecks: true,
			want: checkerSpec{
				gpuNode: true,
				image:   "gpu-image", timeout: GPUPodTimeout, checkerNames: gpuNames,
				gpuChecks: &gpuChecks{sku: h100, gpus: 8},
			},
		},
		{
			// Only the healthy GPUs can be claimed, and the GPU count check reports the rest missing.
			name:            "gpu node with some unhealthy gpus benchmarks the healthy ones",
			info:            gpuNodeInfo{isGPUNode: true, claimableGPUs: 7, sku: h100},
			enableGPUChecks: true,
			want: checkerSpec{
				gpuNode: true,
				image:   "gpu-image", timeout: GPUPodTimeout, checkerNames: gpuNames,
				gpuChecks: &gpuChecks{sku: h100, gpus: 7},
			},
		},
		{
			name:            "gpu node only expects the gpu checks its sku runs",
			info:            gpuNodeInfo{isGPUNode: true, claimableGPUs: 2, sku: "Standard_NV72ads_A10_v5"},
			enableGPUChecks: true,
			want: checkerSpec{
				gpuNode: true,
				image:   "gpu-image", timeout: GPUPodTimeout,
				// The A10 has no NVLink, so peer bandwidth is not one of its checks.
				checkerNames: []string{"PodStartup", "PodNetwork", "NcclAllReduce", "GpuHostBandwidth"},
				gpuChecks:    &gpuChecks{sku: "Standard_NV72ads_A10_v5", gpus: 2},
			},
		},
		{
			name:            "single gpu a10 only expects host bandwidth",
			info:            gpuNodeInfo{isGPUNode: true, claimableGPUs: 1, sku: "Standard_NV36ads_A10_v5"},
			enableGPUChecks: true,
			want: checkerSpec{
				gpuNode: true,
				image:   "gpu-image", timeout: GPUPodTimeout,
				checkerNames: []string{"PodStartup", "PodNetwork", "GpuHostBandwidth"},
				gpuChecks:    &gpuChecks{sku: "Standard_NV36ads_A10_v5", gpus: 1},
			},
		},
		{
			name:            "memory optimized single gpu a10 only expects host bandwidth",
			info:            gpuNodeInfo{isGPUNode: true, claimableGPUs: 1, sku: "Standard_NV36adms_A10_v5"},
			enableGPUChecks: true,
			want: checkerSpec{
				gpuNode: true,
				image:   "gpu-image", timeout: GPUPodTimeout,
				checkerNames: []string{"PodStartup", "PodNetwork", "GpuHostBandwidth"},
				gpuChecks:    &gpuChecks{sku: "Standard_NV36adms_A10_v5", gpus: 1},
			},
		},
		{
			name:            "gpu node without claimable gpus waits for them",
			info:            gpuNodeInfo{isGPUNode: true, sku: h100},
			enableGPUChecks: true,
			age:             10 * time.Second,
			want: checkerSpec{
				gpuNode: true,
				image:   "gpu-image", timeout: GPUPodTimeout, checkerNames: gpuNames,
				wait: gpuPollInterval,
			},
		},
		{
			name:            "healthy gpus wait for a missing sku label",
			info:            gpuNodeInfo{isGPUNode: true, claimableGPUs: 2},
			enableGPUChecks: true,
			want: checkerSpec{
				gpuNode: true,
				image:   "gpu-image", timeout: GPUPodTimeout, checkerNames: gpuNames,
				wait: gpuPollInterval,
			},
		},
		{
			name:            "missing sku shares the remaining startup budget",
			info:            gpuNodeInfo{isGPUNode: true, claimableGPUs: 2},
			enableGPUChecks: true,
			age:             wait - time.Second,
			want: checkerSpec{
				gpuNode: true,
				image:   "gpu-image", timeout: GPUPodTimeout, checkerNames: gpuNames,
				wait: time.Second,
			},
		},
		{
			name:            "missing sku at the deadline does not claim gpus",
			info:            gpuNodeInfo{isGPUNode: true, claimableGPUs: 2},
			enableGPUChecks: true,
			age:             wait,
			want: checkerSpec{
				gpuNode: true,
				image:   "gpu-image", timeout: GPUPodTimeout, checkerNames: gpuNames,
				gpuChecks: &gpuChecks{skipReason: gpu.ErrorCodeNodeSKUNotAvailable},
			},
		},
		{
			name:            "nonempty unsupported sku is not treated as missing metadata",
			info:            gpuNodeInfo{isGPUNode: true, claimableGPUs: 2, sku: "unrecognized_sku"},
			enableGPUChecks: true,
			want: checkerSpec{
				gpuNode: true,
				image:   "gpu-image", timeout: GPUPodTimeout, checkerNames: gpuNames,
				gpuChecks: &gpuChecks{sku: "unrecognized_sku", gpus: 2},
			},
		},
		{
			name:            "the last wait only runs out the gpu wait",
			info:            gpuNodeInfo{isGPUNode: true, sku: h100},
			enableGPUChecks: true,
			age:             wait - time.Second,
			want: checkerSpec{
				gpuNode: true,
				image:   "gpu-image", timeout: GPUPodTimeout, checkerNames: gpuNames,
				wait: time.Second,
			},
		},
		{
			// Driver-only, or every GPU unhealthy. The checker still owes the gpu results, reporting why it
			// cannot run them.
			name:            "gpu node without claimable gpus after the wait reports so",
			info:            gpuNodeInfo{isGPUNode: true, sku: h100},
			enableGPUChecks: true,
			age:             wait,
			want: checkerSpec{
				gpuNode: true,
				image:   "gpu-image", timeout: GPUPodTimeout, checkerNames: gpuNames,
				gpuChecks: &gpuChecks{sku: h100, skipReason: gpu.ErrorCodeGPUsNotClaimable},
			},
		},
		{
			name:            "driver-only gpu node with the gate off",
			info:            gpuNodeInfo{isGPUNode: true, sku: h100},
			enableGPUChecks: false,
			want:            checkerSpec{gpuNode: true, image: "base-image", timeout: PodTimeout, checkerNames: baseNames},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &CheckNodeHealthReconciler{
				CheckerPodImage:    "base-image",
				GPUCheckerPodImage: "gpu-image",
				EnableGPUChecks:    tt.enableGPUChecks,
				GPUWait:            wait,
			}

			got := r.checkerSpecFor(testCNH("cnh-1"), tt.info, tt.age)
			if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(checkerSpec{}, gpuChecks{})); diff != "" {
				t.Errorf("checkerSpecFor() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMissingSKUWithZeroGPUWait(t *testing.T) {
	r := &CheckNodeHealthReconciler{EnableGPUChecks: true, GPUCheckerPodImage: "gpu-image"}
	spec := r.checkerSpecFor(testCNH("boot-no-wait"), gpuNodeInfo{isGPUNode: true, claimableGPUs: 2}, 0)
	if spec.wait != 0 || spec.gpuChecks == nil || spec.gpuChecks.gpus != 0 ||
		spec.gpuChecks.skipReason != gpu.ErrorCodeNodeSKUNotAvailable {
		t.Fatalf("zero wait must diagnose missing metadata without claiming GPUs: %+v", spec)
	}
}

// TestReconcileNodeConditionRouting checks what a completed check publishes: a single NodeHealthy
// condition on non-GPU nodes and no condition at all on GPU nodes, plus a Warning event on the node
// for any check that did not pass.
func TestReconcileNodeConditionRouting(t *testing.T) {
	tests := []struct {
		name string
		// gpuNode makes the target node a GPU node.
		gpuNode bool
		// driverOnly drops the device plugin's GPUs from a GPU node, leaving only the accelerator label.
		driverOnly bool
		// sku overrides the GPU node's sku when set (default: Standard_ND96isr_H100_v5).
		sku string
		// enableGPUChecks gates the intrusive GPU checks.
		enableGPUChecks bool
		// removePodAnnotations strips the run annotations off the checker pod.
		removePodAnnotations bool
		// seededResults stand in for the results the checker pod would have written. PodStartup is
		// recorded by the controller itself, so it is not seeded here.
		seededResults []chmv1alpha1.CheckResult
		// wantHealthy is the aggregate condition on the CheckNodeHealth.
		wantHealthy metav1.ConditionStatus
		// wantNodeConditions is exactly the set of managed conditions the node may carry.
		wantNodeConditions map[corev1.NodeConditionType]corev1.ConditionStatus
		// wantEventReason is the reason of the single Warning event on the node, or empty for none.
		wantEventReason string
		// wantEventLines must each appear in the event message.
		wantEventLines []string
	}{
		{
			name:            "failing gpu check reports an event and no node condition",
			gpuNode:         true,
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusUnhealthy, ErrorCode: gpu.ErrorCodeCorrectness, Message: "3 out-of-bounds values"},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy, Message: "50.000 GB/s"},
				{Name: "GpuPeerBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy:        metav1.ConditionFalse,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{},
			wantEventReason:    ReasonCheckFailed,
			wantEventLines: []string{
				"CheckNodeHealth cnh-routing results:",
				"PodStartup: Healthy: All containers started successfully",
				"PodNetwork: Healthy",
				"NcclAllReduce: Unhealthy [" + gpu.ErrorCodeCorrectness + "]: 3 out-of-bounds values",
				"GpuHostBandwidth: Healthy: 50.000 GB/s",
				"GpuPeerBandwidth: Healthy",
			},
		},
		{
			name:            "all checks passing on a gpu node report nothing",
			gpuNode:         true,
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuPeerBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy:        metav1.ConditionTrue,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{},
		},
		{
			name:    "gpu node without nvlink only owes the checks for its sku",
			gpuNode: true,
			// The A10 has no NVLink, so it never runs peer bandwidth and does not owe a result for it.
			sku:             "Standard_NV72ads_A10_v5",
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy:        metav1.ConditionTrue,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{},
		},
		{
			name:            "single gpu a10 does not owe nccl or peer results",
			gpuNode:         true,
			sku:             "Standard_NV36ads_A10_v5",
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy:        metav1.ConditionTrue,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{},
		},
		{
			name:            "memory optimized single gpu a10 does not owe nccl or peer results",
			gpuNode:         true,
			sku:             "Standard_NV36adms_A10_v5",
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy:        metav1.ConditionTrue,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{},
		},
		{
			name:            "base check failure on a gpu node reports an event",
			gpuNode:         true,
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusUnhealthy, ErrorCode: podnetwork.ErrorCodeNetworkConnectivityFailed},
				{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "GpuPeerBandwidth", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy:        metav1.ConditionFalse,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{},
			wantEventReason:    ReasonCheckFailed,
			wantEventLines:     []string{"PodNetwork: Unhealthy [" + podnetwork.ErrorCodeNetworkConnectivityFailed + "]"},
		},
		{
			// Whether a node gets NodeHealthy is independent of whether the gpu checks are enabled,
			// so a GPU node running only the base checks still stays off NodeHealthy.
			name:            "gpu node with the intrusive checks disabled",
			gpuNode:         true,
			enableGPUChecks: false,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy:        metav1.ConditionTrue,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{},
		},
		{
			// The checker on a driver-only node reports every gpu check as not claimable. The base
			// checks still run.
			name:            "driver only gpu node reports an unknown event saying why",
			gpuNode:         true,
			driverOnly:      true,
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "NcclAllReduce", Status: chmv1alpha1.CheckStatusUnknown, ErrorCode: gpu.ErrorCodeGPUsNotClaimable},
				{Name: "GpuHostBandwidth", Status: chmv1alpha1.CheckStatusUnknown, ErrorCode: gpu.ErrorCodeGPUsNotClaimable},
				{Name: "GpuPeerBandwidth", Status: chmv1alpha1.CheckStatusUnknown, ErrorCode: gpu.ErrorCodeGPUsNotClaimable},
			},
			wantHealthy:        metav1.ConditionUnknown,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{},
			wantEventReason:    ReasonCheckUnknown,
			wantEventLines: []string{
				"NcclAllReduce: Unknown [" + gpu.ErrorCodeGPUsNotClaimable + "]",
				"GpuHostBandwidth: Unknown [" + gpu.ErrorCodeGPUsNotClaimable + "]",
				"GpuPeerBandwidth: Unknown [" + gpu.ErrorCodeGPUsNotClaimable + "]",
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
		{
			name:            "failing check on a non-gpu node reports NodeHealthy and an event",
			gpuNode:         false,
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusUnhealthy, ErrorCode: podnetwork.ErrorCodeNetworkConnectivityFailed, Message: "dial timeout"},
			},
			wantHealthy: metav1.ConditionFalse,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionNodeHealthy: corev1.ConditionFalse,
			},
			wantEventReason: ReasonCheckFailed,
			wantEventLines: []string{
				"PodStartup: Healthy: All containers started successfully",
				"PodNetwork: Unhealthy [" + podnetwork.ErrorCodeNetworkConnectivityFailed + "]: dial timeout",
			},
		},
		// The below two test cases should pretty much never happen as the annotations will always be set in the pod spec.
		// Mainly for coverage.
		{
			name:                 "pod without annotations on a non-gpu node falls back to the live node",
			gpuNode:              false,
			enableGPUChecks:      true,
			removePodAnnotations: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy: metav1.ConditionTrue,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionNodeHealthy: corev1.ConditionTrue,
			},
		},
		{
			// The live node decides the routing, but the pod recorded no GPU checks SKU, so it owes only
			// the base checks even with the gate on. Expecting the GPU checks would leave them Unknown.
			name:                 "pod without annotations on a gpu node falls back to the live node",
			gpuNode:              true,
			enableGPUChecks:      true,
			removePodAnnotations: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
			},
			wantHealthy:        metav1.ConditionTrue,
			wantNodeConditions: map[corev1.NodeConditionType]corev1.ConditionStatus{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler, fakeClient, _ := setupTest()
			reconciler.EnableGPUChecks = tt.enableGPUChecks
			reconciler.EnableNodeCondition = true
			reconciler.CircuitBreaker = NewNodeConditionCircuitBreaker(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown)

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"kubernetes.io/os": "linux"}},
			}
			if tt.gpuNode {
				node = gpuNode("node-1")
				if tt.sku != "" {
					node.Labels[instanceTypeLabel] = tt.sku
				}
				if tt.driverOnly {
					node.Status.Allocatable = nil
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
			sku := ""
			if tt.gpuNode && tt.enableGPUChecks {
				sku = node.Labels[instanceTypeLabel]
			}
			pod := finishedCheckerPod(cnh.Name, tt.gpuNode, sku)
			if tt.removePodAnnotations {
				withoutRunAnnotations(pod)
			}
			if err := fakeClient.Create(ctx, pod); err != nil {
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

			// The extra reconciles after completion must not repeat the event.
			events := recordedEvents(t, reconciler)
			if tt.wantEventReason == "" {
				if len(events) != 0 {
					t.Errorf("events = %q, want none", events)
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("events = %q, want exactly one", events)
			}
			wantPrefix := corev1.EventTypeWarning + " " + tt.wantEventReason + " "
			if !strings.HasPrefix(events[0], wantPrefix) {
				t.Errorf("event = %q, want it to start with %q", events[0], wantPrefix)
			}
			lines := strings.Split(strings.TrimPrefix(events[0], wantPrefix), "\n")
			for _, want := range tt.wantEventLines {
				if !slices.Contains(lines, want) {
					t.Errorf("event message lines = %q, want a line %q", lines, want)
				}
			}
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

// TestReconcileGPUCheckEvents covers the event a GPU node gets for each kind of GPU check result that
// did not pass. GPU nodes carry no node condition, so the event is the only place these show up on the
// node. Each case runs a full H100 check where every result is Healthy unless overridden.
func TestReconcileGPUCheckEvents(t *testing.T) {
	tests := []struct {
		name string
		// overrides replace the Healthy result of the GPU checks they name.
		overrides []chmv1alpha1.CheckResult
		// wantReason is the reason of the single Warning event on the node.
		wantReason string
	}{
		{
			name: "unexpected gpu count",
			overrides: []chmv1alpha1.CheckResult{
				{Name: CheckerGpuHostBandwidth, Status: chmv1alpha1.CheckStatusUnhealthy, ErrorCode: gpu.ErrorCodeUnexpectedGPUCount, Message: "found 7 GPUs, expected 8"},
			},
			wantReason: ReasonCheckFailed,
		},
		{
			name: "nccl all-reduce bandwidth below threshold",
			overrides: []chmv1alpha1.CheckResult{
				{Name: CheckerNcclAllReduce, Status: chmv1alpha1.CheckStatusUnhealthy, ErrorCode: gpu.ErrorCodeLowBandwidth, Message: "400.000 GB/s is below the 460.000 GB/s threshold"},
			},
			wantReason: ReasonCheckFailed,
		},
		{
			name: "unknown sku",
			overrides: []chmv1alpha1.CheckResult{
				{Name: CheckerNcclAllReduce, Status: chmv1alpha1.CheckStatusUnknown, ErrorCode: gpu.ErrorCodeUnknownSKU, Message: "no GPU check profile for SKU"},
			},
			wantReason: ReasonCheckUnknown,
		},
		{
			// Unhealthy wins over Unknown, and the event still lists both.
			name: "unhealthy and unknown results together",
			overrides: []chmv1alpha1.CheckResult{
				{Name: CheckerNcclAllReduce, Status: chmv1alpha1.CheckStatusUnhealthy, ErrorCode: gpu.ErrorCodeLowBandwidth, Message: "400.000 GB/s is below the 460.000 GB/s threshold"},
				{Name: CheckerGpuPeerBandwidth, Status: chmv1alpha1.CheckStatusUnknown, ErrorCode: gpu.ErrorCodeToolFailed, Message: "nvbandwidth produced no output"},
			},
			wantReason: ReasonCheckFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler, fakeClient, _ := setupTest()
			reconciler.EnableGPUChecks = true
			reconciler.EnableNodeCondition = true
			reconciler.CircuitBreaker = NewNodeConditionCircuitBreaker(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown)

			node := gpuNode("node-1")
			if err := fakeClient.Create(ctx, node); err != nil {
				t.Fatalf("creating the node: %v", err)
			}

			results := []chmv1alpha1.CheckResult{{Name: CheckerPodNetwork, Status: chmv1alpha1.CheckStatusHealthy}}
			for _, name := range []string{CheckerNcclAllReduce, CheckerGpuHostBandwidth, CheckerGpuPeerBandwidth} {
				result := chmv1alpha1.CheckResult{Name: name, Status: chmv1alpha1.CheckStatusHealthy}
				if i := slices.IndexFunc(tt.overrides, func(r chmv1alpha1.CheckResult) bool { return r.Name == name }); i >= 0 {
					result = tt.overrides[i]
				}
				results = append(results, result)
			}

			cnh := testCNH("cnh-gpu-event")
			if err := fakeClient.Create(ctx, cnh); err != nil {
				t.Fatalf("creating the CR: %v", err)
			}
			cnh.Status.Results = results
			if err := fakeClient.Status().Update(ctx, cnh); err != nil {
				t.Fatalf("seeding reported results: %v", err)
			}
			if err := fakeClient.Create(ctx, finishedCheckerPod(cnh.Name, true, node.Labels[instanceTypeLabel])); err != nil {
				t.Fatalf("creating the checker pod: %v", err)
			}

			key := types.NamespacedName{Name: cnh.Name}
			for i := 0; i < 3; i++ {
				if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
					t.Fatalf("reconcile %d returned error: %v", i, err)
				}
			}

			events := recordedEvents(t, reconciler)
			if len(events) != 1 {
				t.Fatalf("events = %q, want exactly one", events)
			}
			wantPrefix := corev1.EventTypeWarning + " " + tt.wantReason + " "
			if !strings.HasPrefix(events[0], wantPrefix) {
				t.Errorf("event = %q, want it to start with %q", events[0], wantPrefix)
			}
			lines := strings.Split(strings.TrimPrefix(events[0], wantPrefix), "\n")
			for _, override := range tt.overrides {
				want := fmt.Sprintf("%s: %s [%s]: %s", override.Name, override.Status, override.ErrorCode, override.Message)
				if !slices.Contains(lines, want) {
					t.Errorf("event message lines = %q, want a line %q", lines, want)
				}
			}

			updatedNode := &corev1.Node{}
			if err := fakeClient.Get(ctx, client.ObjectKey{Name: node.Name}, updatedNode); err != nil {
				t.Fatalf("reading the node: %v", err)
			}
			// Checked by prefix rather than against the managed types, so a condition this controller
			// no longer manages, such as the removed GPU conditions, would be caught too.
			for _, c := range updatedNode.Status.Conditions {
				if strings.HasPrefix(string(c.Type), nodeConditionPrefix) {
					t.Errorf("node condition %s = %s, want no condition on a GPU node", c.Type, c.Status)
				}
			}
		})
	}
}

func TestCheckEventMessage(t *testing.T) {
	t.Parallel()

	cnh := testCNH("cnh-message")
	cnh.Status.Results = []chmv1alpha1.CheckResult{
		{Name: CheckerPodStartup, Status: chmv1alpha1.CheckStatusHealthy},
		{Name: CheckerPodNetwork, Status: chmv1alpha1.CheckStatusHealthy, Message: "reached 3 endpoints\nDNS resolved"},
		{Name: CheckerNcclAllReduce, Status: chmv1alpha1.CheckStatusUnhealthy, ErrorCode: gpu.ErrorCodeLowBandwidth, Message: "400.000 GB/s is below the 460.000 GB/s threshold"},
		{Name: CheckerGpuHostBandwidth, Status: chmv1alpha1.CheckStatusUnknown, ErrorCode: ErrorCodeCheckNotReported},
	}

	want := strings.Join([]string{
		"CheckNodeHealth cnh-message results:",
		"PodStartup: Healthy",
		"PodNetwork: Healthy: reached 3 endpoints; DNS resolved",
		"NcclAllReduce: Unhealthy [" + gpu.ErrorCodeLowBandwidth + "]: 400.000 GB/s is below the 460.000 GB/s threshold",
		"GpuHostBandwidth: Unknown [" + ErrorCodeCheckNotReported + "]",
	}, "\n")
	if got := checkEventMessage(cnh); got != want {
		t.Errorf("checkEventMessage() =\n%s\nwant\n%s", got, want)
	}
}

func TestEventResultMessage(t *testing.T) {
	t.Parallel()

	atLimit := strings.Repeat("a", maxEventResultMessageLen)
	// "é" is two bytes, so this string's limit falls in the middle of the last character.
	splitRune := strings.Repeat("a", maxEventResultMessageLen-1) + "é"

	tests := []struct {
		name string
		msg  string
		want string
	}{
		{name: "short message is unchanged", msg: "found 8 of 8 GPUs", want: "found 8 of 8 GPUs"},
		{
			name: "line breaks are flattened",
			msg:  "host_to_device_memcpy_ce: min 50.000 GB/s\r\ndevice_to_host_memcpy_ce: min 49.000 GB/s\n",
			want: "host_to_device_memcpy_ce: min 50.000 GB/s; device_to_host_memcpy_ce: min 49.000 GB/s",
		},
		{name: "message at the limit is kept", msg: atLimit, want: atLimit},
		{name: "message over the limit is cut", msg: atLimit + "b", want: atLimit + "... (truncated)"},
		{
			name: "cut does not split a character",
			msg:  splitRune,
			want: strings.Repeat("a", maxEventResultMessageLen-1) + "... (truncated)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := eventResultMessage(tt.msg)
			if got != tt.want {
				t.Errorf("eventResultMessage() = %q (%d bytes), want %q (%d bytes)", got, len(got), tt.want, len(tt.want))
			}
			if !utf8.ValidString(got) {
				t.Errorf("eventResultMessage() = %q, which is not valid UTF-8", got)
			}
		})
	}
}

func TestCheckEventMessageCap(t *testing.T) {
	t.Parallel()

	filler := strings.Repeat("a", 1000)
	results := func(n int) []chmv1alpha1.CheckResult {
		out := make([]chmv1alpha1.CheckResult, 0, n)
		for i := range n {
			out = append(out, chmv1alpha1.CheckResult{Name: fmt.Sprintf("Check%d", i), Status: chmv1alpha1.CheckStatusHealthy, Message: filler})
		}
		return out
	}

	t.Run("results that fit are all listed", func(t *testing.T) {
		t.Parallel()

		cnh := testCNH("cnh-cap")
		cnh.Status.Results = results(5)
		lines := strings.Split(checkEventMessage(cnh), "\n")
		if len(lines) != 6 {
			t.Fatalf("got %d lines, want the header and 5 results", len(lines))
		}
		if last := lines[len(lines)-1]; last != "Check4: Healthy: "+filler {
			t.Errorf("last line = %q, want the last result", last)
		}
	})

	t.Run("results past the cap are counted", func(t *testing.T) {
		t.Parallel()

		cnh := testCNH("cnh-cap")
		cnh.Status.Results = results(20)
		msg := checkEventMessage(cnh)
		if len(msg) > maxEventMessageLen {
			t.Errorf("message is %d bytes, want at most %d", len(msg), maxEventMessageLen)
		}
		lines := strings.Split(msg, "\n")
		listed := lines[1 : len(lines)-1]
		for i, line := range listed {
			if want := fmt.Sprintf("Check%d: Healthy: %s", i, filler); line != want {
				t.Errorf("line %d = %q, want the whole result", i+1, line)
			}
		}
		want := fmt.Sprintf("... (%d more, see CheckNodeHealth cnh-cap)", 20-len(listed))
		if last := lines[len(lines)-1]; last != want {
			t.Errorf("last line = %q, want %q", last, want)
		}
	})

	t.Run("last result may use the room kept for the omitted line", func(t *testing.T) {
		t.Parallel()

		cnh := testCNH("cnh-cap")
		cnh.Status.Results = results(7)
		size := len(checkEventMessage(cnh))
		// Sized so the message ends exactly at the cap, leaving no room for an omitted line.
		lastPrefix := "Last: Healthy: "
		cnh.Status.Results = append(cnh.Status.Results, chmv1alpha1.CheckResult{
			Name:    "Last",
			Status:  chmv1alpha1.CheckStatusHealthy,
			Message: strings.Repeat("b", maxEventMessageLen-size-1-len(lastPrefix)),
		})

		msg := checkEventMessage(cnh)
		if len(msg) != maxEventMessageLen {
			t.Errorf("message is %d bytes, want exactly %d", len(msg), maxEventMessageLen)
		}
		lines := strings.Split(msg, "\n")
		if last := lines[len(lines)-1]; !strings.HasPrefix(last, lastPrefix) {
			t.Errorf("last line = %.40q..., want the last result", last)
		}
	})
}

func TestRecordCheckEvent(t *testing.T) {
	t.Parallel()

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	tests := []struct {
		name string
		// healthy is the CheckNodeHealth Healthy condition status, or empty for none.
		healthy metav1.ConditionStatus
		reason  string
		node    *corev1.Node
		want    []string
	}{
		{name: "failed check", healthy: metav1.ConditionFalse, reason: ReasonCheckFailed, node: node,
			want: []string{"Warning CheckFailed CheckNodeHealth cnh-event results:\nPodStartup: Unhealthy"}},
		{name: "unknown check", healthy: metav1.ConditionUnknown, reason: ReasonCheckUnknown, node: node,
			want: []string{"Warning CheckUnknown CheckNodeHealth cnh-event results:\nPodStartup: Unhealthy"}},
		{name: "passed check", healthy: metav1.ConditionTrue, reason: ReasonCheckPassed, node: node},
		{name: "check not completed", node: node},
		{name: "node deleted", healthy: metav1.ConditionFalse, reason: ReasonCheckFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &CheckNodeHealthReconciler{Recorder: record.NewFakeRecorder(10)}
			cnh := testCNH("cnh-event")
			cnh.Status.Results = []chmv1alpha1.CheckResult{{Name: CheckerPodStartup, Status: chmv1alpha1.CheckStatusUnhealthy}}
			if tt.healthy != "" {
				cnh.Status.Conditions = []metav1.Condition{{Type: ConditionTypeHealthy, Status: tt.healthy, Reason: tt.reason}}
			}

			r.recordCheckEvent(cnh, tt.node)

			if diff := cmp.Diff(tt.want, recordedEvents(t, r)); diff != "" {
				t.Errorf("events mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestUpdateNodeConditionClearsStaleConditions covers a node that already carries a condition it is
// no longer meant to have, which happens when a node starts or stops being seen as a GPU node.
func TestUpdateNodeConditionClearsStaleConditions(t *testing.T) {
	tests := []struct {
		name string
		// stale are the managed conditions already on the node before the check runs.
		stale []corev1.NodeConditionType
		info  gpuNodeInfo
		want  map[corev1.NodeConditionType]corev1.ConditionStatus
	}{
		{
			name:  "node newly seen as a gpu node drops its leftover NodeHealthy",
			stale: []corev1.NodeConditionType{NodeConditionNodeHealthy},
			info:  gpuNodeInfo{isGPUNode: true},
			want:  map[corev1.NodeConditionType]corev1.ConditionStatus{},
		},
		{
			name: "gpu node without NodeHealthy is left alone",
			info: gpuNodeInfo{isGPUNode: true},
			want: map[corev1.NodeConditionType]corev1.ConditionStatus{},
		},
		{
			name:  "node no longer seen as a gpu node gets NodeHealthy",
			stale: nil,
			info:  gpuNodeInfo{},
			want: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionNodeHealthy: corev1.ConditionTrue,
			},
		},
		{
			name:  "non-gpu node updates its NodeHealthy",
			stale: []corev1.NodeConditionType{NodeConditionNodeHealthy},
			info:  gpuNodeInfo{},
			want: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionNodeHealthy: corev1.ConditionTrue,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler, fakeClient, _ := setupTest()
			reconciler.EnableNodeCondition = true
			reconciler.CircuitBreaker = NewNodeConditionCircuitBreaker(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown)

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
			cnh.Status.Results = []chmv1alpha1.CheckResult{
				{Name: "PodStartup", Status: chmv1alpha1.CheckStatusHealthy},
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
			}
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

// TestUpdateNodeConditionWithOpenBreaker checks that an open circuit breaker stops NodeHealthy being
// published on non-GPU nodes, but does not stop a leftover NodeHealthy being removed from a GPU node.
func TestUpdateNodeConditionWithOpenBreaker(t *testing.T) {
	tests := []struct {
		name string
		info gpuNodeInfo
		want map[corev1.NodeConditionType]corev1.ConditionStatus
	}{
		{
			name: "non-gpu node keeps its NodeHealthy unchanged",
			info: gpuNodeInfo{},
			want: map[corev1.NodeConditionType]corev1.ConditionStatus{
				NodeConditionNodeHealthy: corev1.ConditionFalse,
			},
		},
		{
			name: "gpu node still drops a leftover NodeHealthy",
			info: gpuNodeInfo{isGPUNode: true, claimableGPUs: 8},
			want: map[corev1.NodeConditionType]corev1.ConditionStatus{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler, fakeClient, _ := setupTest()
			reconciler.EnableNodeCondition = true
			reconciler.CircuitBreaker = NewNodeConditionCircuitBreaker(1, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown)
			reconciler.CircuitBreaker.RecordUnhealthyNode()

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"kubernetes.io/os": "linux"}},
				Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
					{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					{Type: NodeConditionNodeHealthy, Status: corev1.ConditionFalse, Reason: ReasonCheckFailed},
				}},
			}
			if err := fakeClient.Create(ctx, node); err != nil {
				t.Fatalf("creating the node: %v", err)
			}

			cnh := testCNH("cnh-open-breaker")
			cnh.Status.Results = []chmv1alpha1.CheckResult{{Name: CheckerPodStartup, Status: chmv1alpha1.CheckStatusHealthy}}
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
			if getNodeConditionByType(updatedNode.Status.Conditions, corev1.NodeReady) == nil {
				t.Error("the Ready condition was dropped")
			}
		})
	}
}

func TestInapplicableNodeConditionTypes(t *testing.T) {
	t.Parallel()

	if got := InapplicableNodeConditionTypes(gpuNode("node-1")); !slices.Equal(got, []corev1.NodeConditionType{NodeConditionNodeHealthy}) {
		t.Errorf("gpu node = %v, want only NodeHealthy", got)
	}

	plain := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	if got := InapplicableNodeConditionTypes(plain); len(got) != 0 {
		t.Errorf("non-gpu node = %v, want none", got)
	}
}

// TestCircuitBreakerOnlyTracksNonGPUNodes checks that the NodeHealthy circuit breaker only opens on a
// run of failures from the non-GPU nodes it guards. GPU nodes never get NodeHealthy, so their
// failures must not stop it being published elsewhere.
func TestCircuitBreakerOnlyTracksNonGPUNodes(t *testing.T) {
	tests := []struct {
		name string
		// failingNodeIsGPU picks which kind of node produces the run of failures.
		failingNodeIsGPU bool
		wantOpen         bool
	}{
		{name: "gpu failures leave the breaker closed", failingNodeIsGPU: true, wantOpen: false},
		{name: "non-gpu failures open the breaker", failingNodeIsGPU: false, wantOpen: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler, fakeClient, _ := setupTest()
			reconciler.EnableGPUChecks = true
			reconciler.EnableNodeCondition = true
			reconciler.CircuitBreaker = NewNodeConditionCircuitBreaker(DefaultCircuitBreakerThreshold, DefaultCircuitBreakerWindow, DefaultCircuitBreakerCooldown)

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
				sku := ""
				if tt.failingNodeIsGPU {
					sku = node.Labels[instanceTypeLabel]
				}
				if err := fakeClient.Create(ctx, finishedCheckerPod(cnh.Name, tt.failingNodeIsGPU, sku)); err != nil {
					t.Fatalf("creating the checker pod: %v", err)
				}

				key := types.NamespacedName{Name: cnh.Name}
				for j := 0; j < 3; j++ {
					if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
						t.Fatalf("reconcile of %s returned error: %v", cnh.Name, err)
					}
				}
			}

			if open := !reconciler.CircuitBreaker.Allow(); open != tt.wantOpen {
				t.Errorf("breaker open = %v, want %v", open, tt.wantOpen)
			}
			// The breaker only guards NodeHealthy, so every failure is still reported as an event.
			if events := recordedEvents(t, reconciler); len(events) != DefaultCircuitBreakerThreshold+1 {
				t.Errorf("got %d events, want one per failure (%d)", len(events), DefaultCircuitBreakerThreshold+1)
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
			name:            "single gpu a10 only fills host bandwidth",
			info:            gpuNodeInfo{isGPUNode: true, sku: "Standard_NV36ads_A10_v5"},
			enableGPUChecks: true,
			reported:        []string{"PodStartup", "PodNetwork"},
			wantAdded:       []string{"GpuHostBandwidth"},
		},
		{
			name:            "memory optimized single gpu a10 only fills host bandwidth",
			info:            gpuNodeInfo{isGPUNode: true, sku: "Standard_NV36adms_A10_v5"},
			enableGPUChecks: true,
			reported:        []string{"PodStartup", "PodNetwork"},
			wantAdded:       []string{"GpuHostBandwidth"},
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

			if err := reconciler.recordMissingResults(context.Background(), cnh, reconciler.checkerSpecFor(cnh, tt.info, 0)); err != nil {
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
