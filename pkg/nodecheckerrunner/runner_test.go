package nodecheckerrunner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
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
	// onRun runs before the result is returned, so a test can change the world mid-run.
	onRun func()
	calls int
}

func (m *mockChecker) Name() string {
	return m.name
}

func (m *mockChecker) Run(ctx context.Context) (*checker.Result, error) {
	m.calls++
	if m.onRun != nil {
		m.onRun()
	}
	return m.result, m.err
}

func TestRunCheckers(t *testing.T) {
	tests := []struct {
		name         string
		checkers     []NodeChecker
		existingCR   *chmv1alpha1.CheckNodeHealth
		expectError  bool
		validateFunc func(t *testing.T, cnh *chmv1alpha1.CheckNodeHealth, checkers []NodeChecker)
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
			expectError: false,
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
			expectError: false,
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
			expectError: false,
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
			expectError: false,
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

// The budget decides which checkers get to start. Whatever did run is still reported, and whatever
// did not is reported as Unknown rather than left silently absent.
func TestRunAll(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// checkers takes cancel so a case can wire exhausting the budget into a checker's run.
		checkers func(cancel context.CancelFunc) []*mockChecker
		// cancelBefore exhausts the budget before any checker starts.
		cancelBefore bool
		wantRan      map[string]bool
		wantStatus   map[string]checker.Status
	}{
		{
			name: "budget covers every checker",
			checkers: func(context.CancelFunc) []*mockChecker {
				return []*mockChecker{
					{name: "First", result: checker.Healthy()},
					{name: "Second", result: checker.Healthy()},
				}
			},
			wantRan:    map[string]bool{"First": true, "Second": true},
			wantStatus: map[string]checker.Status{"First": checker.StatusHealthy, "Second": checker.StatusHealthy},
		},
		{
			name: "budget exhausted mid run skips the rest",
			checkers: func(cancel context.CancelFunc) []*mockChecker {
				return []*mockChecker{
					{name: "First", result: checker.Healthy(), onRun: cancel},
					{name: "Second", result: checker.Healthy()},
				}
			},
			wantRan:    map[string]bool{"First": true, "Second": false},
			wantStatus: map[string]checker.Status{"First": checker.StatusHealthy, "Second": checker.StatusUnknown},
		},
		{
			name: "budget already exhausted skips everything",
			checkers: func(context.CancelFunc) []*mockChecker {
				return []*mockChecker{
					{name: "First", result: checker.Healthy()},
					{name: "Second", result: checker.Healthy()},
				}
			},
			cancelBefore: true,
			wantRan:      map[string]bool{"First": false, "Second": false},
			wantStatus:   map[string]checker.Status{"First": checker.StatusUnknown, "Second": checker.StatusUnknown},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mocks := tt.checkers(cancel)
			checkers := make([]NodeChecker, 0, len(mocks))
			for _, m := range mocks {
				checkers = append(checkers, m)
			}
			if tt.cancelBefore {
				cancel()
			}

			results := (&Runner{checkers: checkers}).runAll(ctx)

			for _, m := range mocks {
				if ran := m.calls > 0; ran != tt.wantRan[m.name] {
					t.Errorf("%s ran = %v, want %v", m.name, ran, tt.wantRan[m.name])
				}
				got, ok := results[m.name]
				if !ok {
					t.Errorf("%s has no result, want one", m.name)
					continue
				}
				if got.Status != tt.wantStatus[m.name] {
					t.Errorf("%s = %q, want %q", m.name, got.Status, tt.wantStatus[m.name])
				}
			}
		})
	}
}

func expiredContext() context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel() // Err is already DeadlineExceeded and does not change.
	return ctx
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestNotRunMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		runTimeout  time.Duration
		ctx         context.Context
		wantMessage string
	}{
		{
			name:        "expired context returns the time limit message",
			runTimeout:  25 * time.Minute,
			ctx:         expiredContext(),
			wantMessage: "the 25m0s time limit for all checks on this node was reached first",
		},
		{
			name:        "generic cancellation returns the canceled message",
			runTimeout:  25 * time.Minute,
			ctx:         canceledContext(),
			wantMessage: "the run was canceled before it started",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := (&Runner{runTimeout: tt.runTimeout}).notRunMessage(tt.ctx)
			if !strings.Contains(got, tt.wantMessage) {
				t.Errorf("message = %q, want it to contain %q", got, tt.wantMessage)
			}
		})
	}
}

func TestNewRunnerCheckers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{
			name: "non-gpu node runs only the core checkers",
			opts: Options{NodeName: "node-1", CRName: "cnh-1"},
			want: []string{"PodNetwork"},
		},
		{
			name: "gpu node adds the benchmarks",
			opts: Options{
				NodeName: "node-1",
				CRName:   "cnh-1",
				GPU:      &GPUOptions{SKU: "Standard_ND96isr_H100_v5"},
			},
			want: []string{"PodNetwork", "NcclAllReduce", "GpuBandwidth"},
		},
		{
			name: "gpu node with an unknown sku still wires the gpu checkers",
			opts: Options{
				NodeName: "node-1",
				CRName:   "cnh-1",
				GPU:      &GPUOptions{SKU: "unknown_sku"},
			},
			want: []string{"PodNetwork", "NcclAllReduce", "GpuBandwidth"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := NewRunner(kubefake.NewSimpleClientset(), nil, tt.opts)

			got := make([]string, 0, len(tt.want))
			for _, c := range r.checkers {
				got = append(got, c.Name())
			}

			if len(got) != len(tt.want) {
				t.Fatalf("checkers = %v, want %v", got, tt.want)
			}
			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("checkers[%d] = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}
