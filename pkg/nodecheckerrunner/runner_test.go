package nodecheckerrunner

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
		name       string
		checkers   []NodeChecker
		existingCR *chmv1alpha1.CheckNodeHealth
		// interceptors lets a case fail specific status writes. The zero value passes everything through.
		interceptors interceptor.Funcs
		// wantErr is a substring of the expected error, empty when the run should succeed.
		wantErr      string
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
		{
			name: "a failed write keeps the other results and the remaining checkers",
			checkers: []NodeChecker{
				&mockChecker{name: "First", result: checker.Healthy()},
				&mockChecker{name: "Second", result: checker.Healthy()},
				&mockChecker{name: "Third", result: checker.Healthy()},
			},
			existingCR: &chmv1alpha1.CheckNodeHealth{
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
				},
			},
			interceptors: interceptor.Funcs{
				SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string,
					obj client.Object, opts ...client.SubResourceUpdateOption) error {
					cnh, ok := obj.(*chmv1alpha1.CheckNodeHealth)
					if ok && hasResult(cnh.Status.Results, "Second") {
						return apierrors.NewInternalError(errors.New("status write rejected"))
					}
					return c.Status().Update(ctx, obj, opts...)
				},
			},
			wantErr: "Second",
			validateFunc: func(t *testing.T, cnh *chmv1alpha1.CheckNodeHealth, checkers []NodeChecker) {
				for _, c := range checkers {
					if mock := c.(*mockChecker); mock.calls == 0 {
						t.Errorf("%s never ran, want every checker to run regardless of earlier write failures", mock.name)
					}
				}
				for name, want := range map[string]bool{"First": true, "Second": false, "Third": true} {
					if got := hasResult(cnh.Status.Results, name); got != want {
						t.Errorf("%s recorded = %v, want %v (results: %+v)", name, got, want, cnh.Status.Results)
					}
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
				WithInterceptorFuncs(tt.interceptors).
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

			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("runCheckers() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("runCheckers() = %v, want an error containing %q", err, tt.wantErr)
			}

			// Get updated CR
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

func hasResult(results []chmv1alpha1.CheckResult, name string) bool {
	for _, result := range results {
		if result.Name == name {
			return true
		}
	}
	return false
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
