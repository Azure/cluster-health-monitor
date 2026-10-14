package nodecheckerrunner

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

// A conflict should retry updating the status rather than immediately returning
// an error and losing the result.
func TestRecordResultRetriesOnConflict(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := chmv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add scheme: %v", err)
	}

	cnh := &chmv1alpha1.CheckNodeHealth{}
	cnh.Name = "test-cr"

	attempts := 0
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cnh).
		WithStatusSubresource(&chmv1alpha1.CheckNodeHealth{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				attempts++
				if attempts == 1 {
					return apierrors.NewConflict(
						chmv1alpha1.Resource("checknodehealths"), obj.GetName(), context.Canceled)
				}
				return c.Status().Update(ctx, obj, opts...)
			},
		}).
		Build()

	r := &Runner{chmClient: fakeClient, nodeName: "test-node", crName: "test-cr"}

	err := r.recordResult(context.Background(), "PodNetwork", checker.Healthy())
	if err != nil {
		t.Fatalf("recordResult() = %v, want nil", err)
	}
	if attempts != 2 {
		t.Errorf("status update attempts = %d, want 2", attempts)
	}

	got := &chmv1alpha1.CheckNodeHealth{}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "test-cr"}, got); err != nil {
		t.Fatalf("Get() failed: %v", err)
	}
	if len(got.Status.Results) != 1 {
		t.Fatalf("Results = %+v, want exactly one entry", got.Status.Results)
	}
	if got.Status.Results[0].Name != "PodNetwork" {
		t.Errorf("Results[0].Name = %q, want %q", got.Status.Results[0].Name, "PodNetwork")
	}
}

// The controller writes PodStartup into the same array, so the pod must replace its own result
// without dropping the controller's.
func TestRecordResultUpserts(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := chmv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add scheme: %v", err)
	}

	cnh := &chmv1alpha1.CheckNodeHealth{}
	cnh.Name = "test-cr"
	cnh.Status.Results = []chmv1alpha1.CheckResult{
		{Name: "PodStartup", Status: chmv1alpha1.CheckStatusHealthy},
		{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusUnknown, Message: "stale"},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cnh).
		WithStatusSubresource(&chmv1alpha1.CheckNodeHealth{}).
		Build()

	r := &Runner{chmClient: fakeClient, nodeName: "test-node", crName: "test-cr"}

	err := r.recordResult(context.Background(), "PodNetwork", checker.Unhealthy("NetworkConnectivityFailed", "fresh"))
	if err != nil {
		t.Fatalf("recordResult() = %v, want nil", err)
	}

	got := &chmv1alpha1.CheckNodeHealth{}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "test-cr"}, got); err != nil {
		t.Fatalf("Get() failed: %v", err)
	}
	if len(got.Status.Results) != 2 {
		t.Fatalf("Results = %+v, want 2 entries (no duplicate PodNetwork)", got.Status.Results)
	}

	byName := map[string]chmv1alpha1.CheckResult{}
	for _, r := range got.Status.Results {
		byName[r.Name] = r
	}
	if byName["PodStartup"].Status != chmv1alpha1.CheckStatusHealthy {
		t.Errorf("PodStartup = %+v, want it left untouched", byName["PodStartup"])
	}
	if byName["PodNetwork"].Message != "fresh" {
		t.Errorf("PodNetwork.Message = %q, want %q", byName["PodNetwork"].Message, "fresh")
	}
}

func TestUpsertResult(t *testing.T) {
	t.Parallel()

	results := []chmv1alpha1.CheckResult{{Name: "pre-existing", Message: "old"}}

	upsertResult(&results, chmv1alpha1.CheckResult{Name: "new", Message: "inserted"})

	if len(results) != 2 {
		t.Fatalf("after insert: results = %+v, want 2 entries", results)
	}
	if results[0].Message != "old" {
		t.Errorf("after insert: results[0].Message = %q, want it untouched (%q)", results[0].Message, "old")
	}
	if results[1].Name != "new" || results[1].Message != "inserted" {
		t.Errorf("after insert: results[1] = %+v, want it appended as {new inserted}", results[1])
	}

	upsertResult(&results, chmv1alpha1.CheckResult{Name: "pre-existing", Message: "updated"})

	if len(results) != 2 {
		t.Fatalf("after update: results = %+v, want 2 entries", results)
	}
	if results[0].Name != "pre-existing" || results[0].Message != "updated" {
		t.Errorf("after update: results[0] = %+v, want {pre-existing updated}", results[0])
	}
	if results[1].Message != "inserted" {
		t.Errorf("after update: results[1].Message = %q, want it untouched (%q)", results[1].Message, "inserted")
	}
}
