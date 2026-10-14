package nodecheckerrunner

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

// The annotation accumulates every recorded duration, and the patch leaves the rest of the CR alone,
// such as the controller's finalizer and other annotations.
func TestRecordDuration(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := chmv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add scheme: %v", err)
	}

	cnh := &chmv1alpha1.CheckNodeHealth{}
	cnh.Name = "test-cr"
	cnh.Annotations = map[string]string{"other": "kept"}
	cnh.Finalizers = []string{"clusterhealthmonitor.azure.com/finalizer"}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cnh).Build()
	r := &Runner{chmClient: fakeClient, crName: "test-cr"}

	ctx := context.Background()
	if err := r.recordDuration(ctx, "PodNetwork", 3100*time.Millisecond); err != nil {
		t.Fatalf("recordDuration(PodNetwork) = %v, want nil", err)
	}
	if err := r.recordDuration(ctx, "NcclAllReduce", 4*time.Minute); err != nil {
		t.Fatalf("recordDuration(NcclAllReduce) = %v, want nil", err)
	}

	got := &chmv1alpha1.CheckNodeHealth{}
	if err := fakeClient.Get(ctx, client.ObjectKey{Name: "test-cr"}, got); err != nil {
		t.Fatalf("Get() failed: %v", err)
	}

	if want := `{"NcclAllReduce":"4m0s","PodNetwork":"3.1s"}`; got.Annotations[CheckerDurationsAnnotation] != want {
		t.Errorf("annotation = %s, want %s", got.Annotations[CheckerDurationsAnnotation], want)
	}
	if got.Annotations["other"] != "kept" {
		t.Errorf("other annotation = %q, want it left untouched", got.Annotations["other"])
	}
	if diff := cmp.Diff(cnh.Finalizers, got.Finalizers); diff != "" {
		t.Errorf("finalizers changed (-want +got):\n%s", diff)
	}
}

func TestParseCheckerDurations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		annotations map[string]string
		want        map[string]time.Duration
		wantErr     bool
	}{
		{
			name: "no annotations",
		},
		{
			name:        "annotation absent",
			annotations: map[string]string{"other": "value"},
		},
		{
			name:        "durations",
			annotations: map[string]string{CheckerDurationsAnnotation: `{"PodNetwork":"3.1s","NcclAllReduce":"4m0s"}`},
			want:        map[string]time.Duration{"PodNetwork": 3100 * time.Millisecond, "NcclAllReduce": 4 * time.Minute},
		},
		{
			name:        "malformed json",
			annotations: map[string]string{CheckerDurationsAnnotation: `not json`},
			wantErr:     true,
		},
		{
			name:        "malformed duration",
			annotations: map[string]string{CheckerDurationsAnnotation: `{"PodNetwork":"soon"}`},
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseCheckerDurations(tt.annotations)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseCheckerDurations() error = %v, want error %v", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ParseCheckerDurations() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
