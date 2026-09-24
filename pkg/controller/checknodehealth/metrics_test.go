package checknodehealth

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/metrics"
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/gpu"
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/podnetwork"
)

// The collectors are process-wide, so the tests in this file assert on the change in a counter
// across the code under test rather than on absolute values. They must stay sequential to avoid
// race conditions.

// labelKey renders a label set as a comparable key, since a Go map cannot be keyed by another map.
func labelKey(labels map[string]string) string {
	pairs := make([]string, 0, len(labels))
	for name, value := range labels {
		pairs = append(pairs, name+"="+value)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

// counterSnapshot collects every child of a counter vector, keyed by label set.
func counterSnapshot(t *testing.T, vec *prometheus.CounterVec) map[string]float64 {
	t.Helper()

	ch := make(chan prometheus.Metric, 256)
	go func() {
		vec.Collect(ch)
		close(ch)
	}()

	snapshot := map[string]float64{}
	for metric := range ch {
		var pb dto.Metric
		if err := metric.Write(&pb); err != nil {
			t.Fatalf("writing metric: %v", err)
		}
		labels := map[string]string{}
		for _, pair := range pb.GetLabel() {
			labels[pair.GetName()] = pair.GetValue()
		}
		snapshot[labelKey(labels)] = pb.GetCounter().GetValue()
	}
	return snapshot
}

// counterDelta returns the series that changed between two snapshots, keyed by label set and
// valued by how much each one moved. Unchanged series are omitted.
func counterDelta(before, after map[string]float64) map[string]float64 {
	delta := map[string]float64{}
	for key, value := range after {
		if diff := value - before[key]; diff != 0 {
			delta[key] = diff
		}
	}
	return delta
}

const (
	testCheckerPodNamespace = "kube-system"
	testCheckerPodImage     = "mcr.microsoft.com/aks/cluster-health-monitor/cluster-health-monitor:latest"
)

// succeededCheckerPod returns a checker pod that the reconciler treats as finished. It carries no
// container statuses, so the PodStartup check is left unreported.
func succeededCheckerPod(cnhName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "check-node-health-" + cnhName,
			Namespace: testCheckerPodNamespace,
			Labels:    map[string]string{CheckNodeHealthLabel: cnhName},
		},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
}

// startedCheckerPod returns a finished checker pod whose container started, which is what makes the
// reconciler record PodStartup as Healthy.
func startedCheckerPod(cnhName string) *corev1.Pod {
	pod := succeededCheckerPod(cnhName)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "checker",
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{StartedAt: metav1.Now()},
		},
	}}
	return pod
}

// errSimulatedTerminalUpdate is injected in place of the status update that sets FinishedAt.
var errSimulatedTerminalUpdate = errors.New("simulated terminal status update failure")

// newMetricsReconciler builds a reconciler over a fake client. When failTerminalUpdate is set, the
// status update that sets FinishedAt is rejected, so a reconcile reaches markCompleted but the CR
// never completes.
func newMetricsReconciler(t *testing.T, failTerminalUpdate bool) (*CheckNodeHealthReconciler, client.Client) {
	t.Helper()

	if !failTerminalUpdate {
		reconciler, fakeClient, _ := setupTest()
		// setupTest is shared with the rest of the package and still uses placeholder values.
		reconciler.CheckerPodImage = testCheckerPodImage
		reconciler.CheckerPodNamespace = testCheckerPodNamespace
		return reconciler, fakeClient
	}

	scheme := runtime.NewScheme()
	if err := chmv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding chm scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding core scheme: %v", err)
	}

	failingClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&chmv1alpha1.CheckNodeHealth{}, &corev1.Node{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if ts := obj.GetCreationTimestamp(); ts.IsZero() {
					obj.SetCreationTimestamp(metav1.Now())
				}
				return c.Create(ctx, obj, opts...)
			},
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				// Only the terminal update is rejected. markStarted writes the status too, and that
				// write has to succeed for the reconcile to reach markCompleted at all.
				if cnh, ok := obj.(*chmv1alpha1.CheckNodeHealth); ok && cnh.Status.FinishedAt != nil {
					return errSimulatedTerminalUpdate
				}
				return c.SubResource(subResourceName).Update(ctx, obj, opts...)
			},
		}).
		Build()

	return &CheckNodeHealthReconciler{
		Client:              failingClient,
		Scheme:              scheme,
		APIReader:           failingClient,
		CheckerPodImage:     testCheckerPodImage,
		CheckerPodNamespace: testCheckerPodNamespace,
	}, failingClient
}

// maxReconciles bounds the reconcile loops below so a regression does not spin forever. Chosen arbitrarily.
const maxReconciles = 5

// reconcileUntilTerminal reconciles until the CR has a FinishedAt, and returns it.
func reconcileUntilTerminal(t *testing.T, r *CheckNodeHealthReconciler, c client.Client, key types.NamespacedName) *chmv1alpha1.CheckNodeHealth {
	t.Helper()

	ctx := context.Background()
	for i := 0; i < maxReconciles; i++ {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("reconcile %d returned error: %v", i, err)
		}
		current := &chmv1alpha1.CheckNodeHealth{}
		if err := c.Get(ctx, key, current); err != nil {
			t.Fatalf("reading the CR: %v", err)
		}
		if current.Status.FinishedAt != nil {
			return current
		}
	}
	t.Fatalf("the CR never reached a terminal state in %d reconciles", maxReconciles)
	return nil
}

// reconcileUntilTerminalUpdateFails is the counterpart to reconcileUntilTerminal for a reconciler
// whose terminal status update is rejected. It stops on errSimulatedTerminalUpdate rather than on a
// terminal state, and checks the CR was left incomplete.
func reconcileUntilTerminalUpdateFails(t *testing.T, r *CheckNodeHealthReconciler, c client.Client, key types.NamespacedName) {
	t.Helper()

	ctx := context.Background()
	for i := 0; i < maxReconciles; i++ {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		if err == nil {
			continue
		}
		// Only errSimulatedTerminalUpdate counts so that the test does not pass unrelated errors.
		if !errors.Is(err, errSimulatedTerminalUpdate) {
			t.Fatalf("reconcile %d failed before the terminal update: %v", i, err)
		}

		current := &chmv1alpha1.CheckNodeHealth{}
		if err := c.Get(ctx, key, current); err != nil {
			t.Fatalf("reading the CR: %v", err)
		}
		if current.Status.FinishedAt != nil {
			t.Fatal("the CR was marked finished even though the terminal update failed")
		}
		return
	}
	t.Fatalf("the terminal status update never failed a reconcile in %d attempts", maxReconciles)
}

// gpuNode returns a supported GPU node.
func gpuNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"kubernetes.io/os":  "linux",
				gpuAcceleratorLabel: "nvidia",
				instanceTypeLabel:   "Standard_ND96isr_H100_v5",
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{nvidiaGPUResourceName: resource.MustParse("8")},
		},
	}
}

// nodeCheckMetricsCase describes one terminal path through the reconciler and the exact series
// each counter must gain as a result.
type nodeCheckMetricsCase struct {
	name string
	// node, when set, is created before reconciling. Absent means the reconciler treats the target
	// as a supported node.
	node *corev1.Node
	// pod, when set, builds the checker pod to create before reconciling.
	pod func(cnhName string) *corev1.Pod
	// seededResults stand in for the results the checker pod would have written to the CR.
	seededResults []chmv1alpha1.CheckResult
	// failTerminalUpdate rejects the status update that sets FinishedAt, so the reconcile reaches
	// markCompleted but the CR never completes.
	failTerminalUpdate bool
	// enableGPUChecks turns on the GPU gate, which is what makes the reconciler read the node and
	// expect the GPU checks on top of the base ones.
	enableGPUChecks bool
	// wantCHMNodeCheckTotal is the one cluster_health_monitor_node_check_total series that must gain 1, or
	// nil when the path must emit nothing.
	wantCHMNodeCheckTotal map[string]string
	// wantCHMNodeCheckResultTotal are the cluster_health_monitor_node_check_result_total series that must gain 1.
	wantCHMNodeCheckResultTotal []map[string]string
}

// TestNodeCheckMetricsEmitted drives each terminal path to completion and asserts that exactly the
// expected series are emitted, including the paths that must emit nothing at all.
func TestNodeCheckMetricsEmitted(t *testing.T) {
	tests := []nodeCheckMetricsCase{
		{
			// The checker pod ended without writing any result, so recordMissingResults fills in
			// every expected check as Unknown.
			name: "checker pod ends without reporting",
			pod:  succeededCheckerPod,
			wantCHMNodeCheckTotal: map[string]string{
				"result": metrics.UnknownStatus,
				"reason": ReasonCheckUnknown,
			},
			wantCHMNodeCheckResultTotal: []map[string]string{
				{"checker_name": "PodStartup", "status": metrics.UnknownStatus, "error_code": ErrorCodeCheckNotReported},
				{"checker_name": "PodNetwork", "status": metrics.UnknownStatus, "error_code": ErrorCodeCheckNotReported},
			},
		},
		{
			name: "every check reports healthy",
			pod:  startedCheckerPod,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy, Message: "reported by the checker pod"},
			},
			wantCHMNodeCheckTotal: map[string]string{
				"result": metrics.HealthyStatus,
				"reason": ReasonCheckPassed,
			},
			wantCHMNodeCheckResultTotal: []map[string]string{
				{"checker_name": "PodStartup", "status": metrics.HealthyStatus, "error_code": metrics.HealthyCode},
				{"checker_name": "PodNetwork", "status": metrics.HealthyStatus, "error_code": metrics.HealthyCode},
			},
		},
		{
			name: "a check reports unhealthy with an error code",
			pod:  startedCheckerPod,
			seededResults: []chmv1alpha1.CheckResult{
				{
					Name:      "PodNetwork",
					Status:    chmv1alpha1.CheckStatusUnhealthy,
					ErrorCode: podnetwork.ErrorCodeNetworkConnectivityFailed,
					Message:   "reported by the checker pod",
				},
			},
			wantCHMNodeCheckTotal: map[string]string{
				"result": metrics.UnhealthyStatus,
				"reason": ReasonCheckFailed,
			},
			wantCHMNodeCheckResultTotal: []map[string]string{
				{"checker_name": "PodStartup", "status": metrics.HealthyStatus, "error_code": metrics.HealthyCode},
				{"checker_name": "PodNetwork", "status": metrics.UnhealthyStatus, "error_code": podnetwork.ErrorCodeNetworkConnectivityFailed},
			},
		},
		{
			name: "unsupported node is counted without any per-check results",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "node-1",
					Labels: map[string]string{"kubernetes.io/os": "windows"},
				},
			},
			wantCHMNodeCheckTotal: map[string]string{
				"result": metrics.UnknownStatus,
				"reason": ReasonCheckUnsupported,
			},
			// Unsupported nodes short circuit and do not run any checks.
			wantCHMNodeCheckResultTotal: nil,
		},
		{
			name:            "gpu node counts the gpu checks too",
			node:            gpuNode("node-1"),
			pod:             succeededCheckerPod,
			enableGPUChecks: true,
			wantCHMNodeCheckTotal: map[string]string{
				"result": metrics.UnknownStatus,
				"reason": ReasonCheckUnknown,
			},
			wantCHMNodeCheckResultTotal: []map[string]string{
				{"checker_name": "PodStartup", "status": metrics.UnknownStatus, "error_code": ErrorCodeCheckNotReported},
				{"checker_name": "PodNetwork", "status": metrics.UnknownStatus, "error_code": ErrorCodeCheckNotReported},
				{"checker_name": "NcclAllReduce", "status": metrics.UnknownStatus, "error_code": ErrorCodeCheckNotReported},
				{"checker_name": "GpuBandwidth", "status": metrics.UnknownStatus, "error_code": ErrorCodeCheckNotReported},
			},
		},
		{
			// A GPU node reporting a mix of statuses.
			name:            "gpu check reports unhealthy alongside healthy and unreported checks",
			node:            gpuNode("node-1"),
			pod:             startedCheckerPod,
			enableGPUChecks: true,
			seededResults: []chmv1alpha1.CheckResult{
				{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy, Message: "reported by the checker pod"},
				{
					Name:      "NcclAllReduce",
					Status:    chmv1alpha1.CheckStatusUnhealthy,
					ErrorCode: gpu.ErrorCodeCorrectness,
					Message:   "reported by the checker pod",
				},
			},
			wantCHMNodeCheckTotal: map[string]string{
				// Unhealthy because Unhealthy results take precedence over Unknown
				"result": metrics.UnhealthyStatus,
				"reason": ReasonCheckFailed,
			},
			wantCHMNodeCheckResultTotal: []map[string]string{
				{"checker_name": "PodStartup", "status": metrics.HealthyStatus, "error_code": metrics.HealthyCode},
				{"checker_name": "PodNetwork", "status": metrics.HealthyStatus, "error_code": metrics.HealthyCode},
				{"checker_name": "NcclAllReduce", "status": metrics.UnhealthyStatus, "error_code": gpu.ErrorCodeCorrectness},
				{"checker_name": "GpuBandwidth", "status": metrics.UnknownStatus, "error_code": ErrorCodeCheckNotReported},
			},
		},
		{
			// Only count completed checks. Emitting the metric before the status update succeeds
			// would count one that never completed and lead to double counting.
			name:                        "terminal status update fails",
			pod:                         succeededCheckerPod,
			failTerminalUpdate:          true,
			wantCHMNodeCheckTotal:       nil,
			wantCHMNodeCheckResultTotal: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler, fakeClient := newMetricsReconciler(t, tt.failTerminalUpdate)
			reconciler.EnableGPUChecks = tt.enableGPUChecks

			if tt.node != nil {
				if err := fakeClient.Create(ctx, tt.node); err != nil {
					t.Fatalf("creating the node: %v", err)
				}
			}

			cnh := testCNH("cnh-" + strings.ReplaceAll(tt.name, " ", "-"))
			if err := fakeClient.Create(ctx, cnh); err != nil {
				t.Fatalf("creating the CR: %v", err)
			}
			if tt.seededResults != nil {
				cnh.Status.Results = tt.seededResults
				if err := fakeClient.Status().Update(ctx, cnh); err != nil {
					t.Fatalf("seeding reported results: %v", err)
				}
			}
			if tt.pod != nil {
				if err := fakeClient.Create(ctx, tt.pod(cnh.Name)); err != nil {
					t.Fatalf("creating the checker pod: %v", err)
				}
			}

			wantOutcomeDelta := map[string]float64{}
			if tt.wantCHMNodeCheckTotal != nil {
				wantOutcomeDelta[labelKey(tt.wantCHMNodeCheckTotal)] = 1
			}
			wantResultsDelta := map[string]float64{}
			for _, labels := range tt.wantCHMNodeCheckResultTotal {
				wantResultsDelta[labelKey(labels)]++
			}

			outcomeBefore := counterSnapshot(t, checkCounter)
			resultsBefore := counterSnapshot(t, checkResultCounter)

			key := types.NamespacedName{Name: cnh.Name}
			if tt.failTerminalUpdate {
				reconcileUntilTerminalUpdateFails(t, reconciler, fakeClient, key)
			} else {
				reconcileUntilTerminal(t, reconciler, fakeClient, key)
			}

			outcomeAfter := counterSnapshot(t, checkCounter)
			resultsAfter := counterSnapshot(t, checkResultCounter)

			// Verify the first emission of the counters.
			if got := counterDelta(outcomeBefore, outcomeAfter); !reflect.DeepEqual(got, wantOutcomeDelta) {
				t.Errorf("outcome counter delta = %v, want %v", got, wantOutcomeDelta)
			}
			if got := counterDelta(resultsBefore, resultsAfter); !reflect.DeepEqual(got, wantResultsDelta) {
				t.Errorf("per-check counter delta = %v, want %v", got, wantResultsDelta)
			}

			// Verify that subsequent reconciles do not change the counters. Ensures we do not double count the same CR.
			for i := 0; i < 3; i++ {
				_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
				if err != nil && !tt.failTerminalUpdate {
					t.Fatalf("re-reconcile %d of a completed CR: %v", i, err)
				}
			}

			if got := counterDelta(outcomeAfter, counterSnapshot(t, checkCounter)); len(got) != 0 {
				t.Errorf("outcome counter moved on re-reconcile by %v, want no change", got)
			}
			if got := counterDelta(resultsAfter, counterSnapshot(t, checkResultCounter)); len(got) != 0 {
				t.Errorf("per-check counter moved on re-reconcile by %v, want no change", got)
			}
		})
	}
}

func TestResultLabel(t *testing.T) {
	t.Parallel()

	tests := map[metav1.ConditionStatus]string{
		metav1.ConditionTrue:    metrics.HealthyStatus,
		metav1.ConditionFalse:   metrics.UnhealthyStatus,
		metav1.ConditionUnknown: metrics.UnknownStatus,
	}

	for status, want := range tests {
		if got := resultLabel(status); got != want {
			t.Errorf("resultLabel(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestErrorCodeLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result chmv1alpha1.CheckResult
		want   string
	}{
		{
			name:   "an explicit code is used as is",
			result: chmv1alpha1.CheckResult{Status: chmv1alpha1.CheckStatusUnknown, ErrorCode: ErrorCodeCheckNotReported},
			want:   ErrorCodeCheckNotReported,
		},
		{
			name:   "a healthy result without a code is labeled healthy",
			result: chmv1alpha1.CheckResult{Status: chmv1alpha1.CheckStatusHealthy},
			want:   metrics.HealthyCode,
		},
		{
			name:   "an unknown result without a code falls back to unknown",
			result: chmv1alpha1.CheckResult{Status: chmv1alpha1.CheckStatusUnknown},
			want:   metrics.UnknownCode,
		},
		{
			// This is a fallback that should not happen because Unhealthy results are expected to carry a code.
			name:   "an unhealthy result without a code falls back to unknown",
			result: chmv1alpha1.CheckResult{Status: chmv1alpha1.CheckStatusUnhealthy},
			want:   metrics.UnknownCode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := errorCodeLabel(tt.result); got != tt.want {
				t.Errorf("errorCodeLabel(%+v) = %q, want %q", tt.result, got, tt.want)
			}
		})
	}
}
