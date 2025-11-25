package nodecheckerrunner

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

// mockChecker implements the NodeChecker interface for testing
type mockChecker struct {
	name   string
	result *checker.Result
	err    error
	calls  int
}

func (m *mockChecker) Name() string {
	return m.name
}

func (m *mockChecker) Run(ctx context.Context) (*checker.Result, error) {
	m.calls++
	return m.result, m.err
}

func TestRunCheckers(t *testing.T) {
	tests := []struct {
		name           string
		checkers       []NodeChecker
		existingCR     *chmv1alpha1.CheckNodeHealth
		expectedStatus int // number of results expected
		expectError    bool
		validateFunc   func(t *testing.T, cnh *chmv1alpha1.CheckNodeHealth, checkers []NodeChecker)
	}{
		{
			name: "single checker succeeds",
			checkers: []NodeChecker{
				&mockChecker{
					name:   "TestChecker",
					result: checker.Healthy(),
					err:    nil,
				},
			},
			existingCR: &chmv1alpha1.CheckNodeHealth{
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			expectedStatus: 1,
			expectError:    false,
			validateFunc: func(t *testing.T, cnh *chmv1alpha1.CheckNodeHealth, checkers []NodeChecker) {
				if len(cnh.Status.Results) != 1 {
					t.Errorf("Expected 1 result, got %d", len(cnh.Status.Results))
				}
				if cnh.Status.Results[0].Status != chmv1alpha1.CheckStatusHealthy {
					t.Errorf("Expected Healthy status, got %s", cnh.Status.Results[0].Status)
				}
				if cnh.Status.Results[0].Name != "TestChecker" {
					t.Errorf("Expected name TestChecker, got %s", cnh.Status.Results[0].Name)
				}
			},
		},
		{
			name: "multiple checkers succeed",
			checkers: []NodeChecker{
				&mockChecker{
					name:   "Checker1",
					result: checker.Healthy(),
					err:    nil,
				},
				&mockChecker{
					name:   "Checker2",
					result: checker.Unhealthy("ERR001", "Test error"),
					err:    nil,
				},
			},
			existingCR: &chmv1alpha1.CheckNodeHealth{
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			expectedStatus: 2,
			expectError:    false,
			validateFunc: func(t *testing.T, cnh *chmv1alpha1.CheckNodeHealth, checkers []NodeChecker) {
				if len(cnh.Status.Results) != 2 {
					t.Errorf("Expected 2 results, got %d", len(cnh.Status.Results))
				}
				// Convert to map for easier validation
				resultMap := make(map[string]chmv1alpha1.CheckResult)
				for _, result := range cnh.Status.Results {
					resultMap[result.Name] = result
				}

				// Verify Checker1
				if result, ok := resultMap["Checker1"]; !ok {
					t.Error("Checker1 not found in results")
				} else if result.Status != chmv1alpha1.CheckStatusHealthy {
					t.Errorf("Checker1 expected Healthy, got %s", result.Status)
				}

				// Verify Checker2
				if result, ok := resultMap["Checker2"]; !ok {
					t.Error("Checker2 not found in results")
				} else {
					if result.Status != chmv1alpha1.CheckStatusUnhealthy {
						t.Errorf("Checker2 expected Unhealthy, got %s", result.Status)
					}
					if result.ErrorCode != "ERR001" {
						t.Errorf("Checker2 expected error code ERR001, got %s", result.ErrorCode)
					}
				}
			},
		},
		{
			name: "checker returns error - recorded as Unknown after retries",
			checkers: []NodeChecker{
				&mockChecker{
					name:   "FailingChecker",
					result: nil,
					err:    errors.New("test error"),
				},
			},
			existingCR: &chmv1alpha1.CheckNodeHealth{
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			expectedStatus: 1,
			expectError:    false,
			validateFunc: func(t *testing.T, cnh *chmv1alpha1.CheckNodeHealth, checkers []NodeChecker) {
				if len(cnh.Status.Results) != 1 {
					t.Errorf("Expected 1 result, got %d", len(cnh.Status.Results))
				}
				if cnh.Status.Results[0].Status != chmv1alpha1.CheckStatusUnknown {
					t.Errorf("Expected Unknown status, got %s", cnh.Status.Results[0].Status)
				}
				// Verify the checker was retried
				mock := checkers[0].(*mockChecker)
				if mock.calls != maxRetryAttempts {
					t.Errorf("Expected %d retry attempts, got %d", maxRetryAttempts, mock.calls)
				}
			},
		},

		{
			name: "mixed results - some succeed, some fail",
			checkers: []NodeChecker{
				&mockChecker{
					name:   "SuccessChecker",
					result: checker.Healthy(),
					err:    nil,
				},
				&mockChecker{
					name:   "FailChecker",
					result: nil,
					err:    errors.New("fail error"),
				},
				&mockChecker{
					name:   "UnknownChecker",
					result: checker.Unknown("unknown state"),
					err:    nil,
				},
			},
			existingCR: &chmv1alpha1.CheckNodeHealth{
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			expectedStatus: 3,
			expectError:    false,
			validateFunc: func(t *testing.T, cnh *chmv1alpha1.CheckNodeHealth, checkers []NodeChecker) {
				if len(cnh.Status.Results) != 3 {
					t.Errorf("Expected 3 results, got %d", len(cnh.Status.Results))
				}
				// All three checkers should have results
				resultMap := make(map[string]chmv1alpha1.CheckResult)
				for _, result := range cnh.Status.Results {
					resultMap[result.Name] = result
				}

				if result, ok := resultMap["SuccessChecker"]; !ok || result.Status != chmv1alpha1.CheckStatusHealthy {
					t.Error("SuccessChecker should be Healthy")
				}
				if result, ok := resultMap["FailChecker"]; !ok || result.Status != chmv1alpha1.CheckStatusUnknown {
					t.Error("FailChecker should be Unknown after error")
				}
				if result, ok := resultMap["UnknownChecker"]; !ok || result.Status != chmv1alpha1.CheckStatusUnknown {
					t.Error("UnknownChecker should be Unknown")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup fake client
			scheme := runtime.NewScheme()
			if err := chmv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatalf("Failed to add scheme: %v", err)
			}

			// Set a name for the CR
			tt.existingCR.Name = "test-cr"

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingCR).
				WithStatusSubresource(&chmv1alpha1.CheckNodeHealth{}).
				Build()

			ctx := context.Background()

			// Create runner and run the checkers
			runner := &Runner{
				clientset: nil, // not needed for this test
				chmClient: fakeClient,
				nodeName:  "test-node",
				crName:    "test-cr",
				checkers:  tt.checkers,
			}
			err := runner.runCheckers(ctx)

			// Check error expectation
			if (err != nil) != tt.expectError {
				t.Errorf("Expected error: %v, got error: %v", tt.expectError, err)
			} // Get updated CR
			updatedCR := &chmv1alpha1.CheckNodeHealth{}
			if err := fakeClient.Get(ctx, client.ObjectKey{Name: "test-cr"}, updatedCR); err != nil {
				t.Fatalf("Failed to get updated CR: %v", err)
			}

			// Validate results
			if tt.validateFunc != nil {
				tt.validateFunc(t, updatedCR, tt.checkers)
			}
		})
	}
}
