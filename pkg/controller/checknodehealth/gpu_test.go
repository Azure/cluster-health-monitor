package checknodehealth

import (
	"context"
	"testing"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type fakeGPUCheckRunner struct {
	done  bool
	calls int
}

func (f *fakeGPUCheckRunner) Reconcile(_ context.Context, _ *chmv1alpha1.CheckNodeHealth, _ *corev1.Node) (bool, error) {
	f.calls++
	return f.done, nil
}

func TestIsGPUNode(t *testing.T) {
	tests := []struct {
		name string
		node *corev1.Node
		want bool
	}{
		{
			name: "fully managed GPU node",
			node: fullyManagedGPUNode("test-node"),
			want: true,
		},
		{
			name: "driver-only GPU node",
			node: driverOnlyGPUNode("test-node"),
			want: true,
		},
		{
			name: "unlabeled node with allocatable NVIDIA GPU",
			node: &corev1.Node{Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{gpuResourceName: resource.MustParse("8")},
			}},
			want: true,
		},
		{
			name: "non-NVIDIA accelerator label",
			node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{gpuAcceleratorLabel: "amd"},
			}},
			want: false,
		},
		{name: "non-GPU node", node: &corev1.Node{}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isGPUNode(test.node); got != test.want {
				t.Errorf("isGPUNode() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestReconcileGPUChecks(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		node      *corev1.Node
		runner    *fakeGPUCheckRunner
		wantDone  bool
		wantCalls int
		wantErr   bool
	}{
		{
			name:      "disabled skips GPU node",
			enabled:   false,
			node:      fullyManagedGPUNode("test-node"),
			runner:    &fakeGPUCheckRunner{},
			wantDone:  true,
			wantCalls: 0,
		},
		{
			name:      "enabled skips non-GPU node",
			enabled:   true,
			node:      &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node"}},
			runner:    &fakeGPUCheckRunner{},
			wantDone:  true,
			wantCalls: 0,
		},
		{
			name:      "GPU runner in progress",
			enabled:   true,
			node:      fullyManagedGPUNode("test-node"),
			runner:    &fakeGPUCheckRunner{done: false},
			wantDone:  false,
			wantCalls: 1,
		},
		{
			name:      "fully managed GPU runner complete",
			enabled:   true,
			node:      fullyManagedGPUNode("test-node"),
			runner:    &fakeGPUCheckRunner{done: true},
			wantDone:  true,
			wantCalls: 1,
		},
		{
			name:      "driver-only GPU runner complete",
			enabled:   true,
			node:      driverOnlyGPUNode("test-node"),
			runner:    &fakeGPUCheckRunner{done: true},
			wantDone:  true,
			wantCalls: 1,
		},
		{
			name:    "enabled GPU node requires runner",
			enabled: true,
			node:    fullyManagedGPUNode("test-node"),
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reconciler, fakeClient, _ := setupTest()
			reconciler.EnableGPUChecks = test.enabled
			if test.runner != nil {
				reconciler.GPUCheckRunner = test.runner
			}
			if err := fakeClient.Create(context.Background(), test.node); err != nil {
				t.Fatal(err)
			}
			cnh := &chmv1alpha1.CheckNodeHealth{
				Spec: chmv1alpha1.CheckNodeHealthSpec{NodeRef: chmv1alpha1.NodeReference{Name: test.node.Name}},
			}

			gotDone, err := reconciler.reconcileGPUChecks(context.Background(), cnh)
			if (err != nil) != test.wantErr {
				t.Fatalf("reconcileGPUChecks() error = %v, wantErr %t", err, test.wantErr)
			}
			if gotDone != test.wantDone {
				t.Errorf("reconcileGPUChecks() done = %t, want %t", gotDone, test.wantDone)
			}
			if test.runner != nil && test.runner.calls != test.wantCalls {
				t.Errorf("runner calls = %d, want %d", test.runner.calls, test.wantCalls)
			}
		})
	}
}

func TestReconcileWaitsForGPUChecks(t *testing.T) {
	reconciler, fakeClient, _ := setupTest()
	runner := &fakeGPUCheckRunner{}
	reconciler.EnableGPUChecks = true
	reconciler.GPUCheckRunner = runner

	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gpu-orchestration", Finalizers: []string{CheckNodeHealthFinalizer}},
		Spec:       chmv1alpha1.CheckNodeHealthSpec{NodeRef: chmv1alpha1.NodeReference{Name: "test-node"}},
		Status: chmv1alpha1.CheckNodeHealthStatus{Results: []chmv1alpha1.CheckResult{
			{Name: "PodStartup", Status: chmv1alpha1.CheckStatusHealthy},
			{Name: "PodNetwork", Status: chmv1alpha1.CheckStatusHealthy},
		}},
	}
	corePod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generateHealthCheckPodName(cnh),
			Namespace: "default",
			Labels:    map[string]string{CheckNodeHealthLabel: cnh.Name},
		},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
	for _, object := range []client.Object{fullyManagedGPUNode("test-node"), cnh, corePod} {
		if err := fakeClient.Create(context.Background(), object); err != nil {
			t.Fatal(err)
		}
	}

	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: cnh.Name}}
	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("Reconcile() while GPU pending error = %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("RequeueAfter = %s, want 30s", result.RequeueAfter)
	}
	pending := &chmv1alpha1.CheckNodeHealth{}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, pending); err != nil {
		t.Fatal(err)
	}
	if pending.Status.FinishedAt != nil {
		t.Fatal("CheckNodeHealth completed before GPU checks finished")
	}

	runner.done = true
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() after GPU completion error = %v", err)
	}
	completed := &chmv1alpha1.CheckNodeHealth{}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, completed); err != nil {
		t.Fatal(err)
	}
	if completed.Status.FinishedAt == nil {
		t.Fatal("CheckNodeHealth did not complete after GPU checks finished")
	}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: corePod.Name, Namespace: corePod.Namespace}, &corev1.Pod{}); err == nil {
		t.Fatal("Core pod was not cleaned up after all checks completed")
	}
}

func TestReconcileRunsCoreBeforeGPUChecks(t *testing.T) {
	reconciler, fakeClient, _ := setupTest()
	runner := &fakeGPUCheckRunner{done: true}
	reconciler.EnableGPUChecks = true
	reconciler.GPUCheckRunner = runner

	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{Name: "test-core-before-gpu", Finalizers: []string{CheckNodeHealthFinalizer}},
		Spec:       chmv1alpha1.CheckNodeHealthSpec{NodeRef: chmv1alpha1.NodeReference{Name: "test-node"}},
	}
	corePod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generateHealthCheckPodName(cnh),
			Namespace: "default",
			Labels:    map[string]string{CheckNodeHealthLabel: cnh.Name},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	for _, object := range []client.Object{fullyManagedGPUNode("test-node"), cnh, corePod} {
		if err := fakeClient.Create(context.Background(), object); err != nil {
			t.Fatal(err)
		}
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: cnh.Name}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("RequeueAfter = %s, want 30s", result.RequeueAfter)
	}
	if runner.calls != 0 {
		t.Errorf("GPU runner called %d times before Core completed, want 0", runner.calls)
	}
	updated := &chmv1alpha1.CheckNodeHealth{}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: cnh.Name}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.FinishedAt != nil {
		t.Fatal("CheckNodeHealth completed while Core checks were still running")
	}
}

func fullyManagedGPUNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{gpuAcceleratorLabel: "nvidia"},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{gpuResourceName: resource.MustParse("8")},
		},
	}
}

func driverOnlyGPUNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{gpuAcceleratorLabel: "nvidia"},
		},
	}
}
