package node

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

// setupRebootTest builds a NodeRebootReconciler backed by a fake client
// seeded with the given objects. If no CoreDNS Deployment is provided, a
// fully Ready one is injected automatically so callers that don't care
// about CoreDNS readiness see it as available. Tests that need to
// exercise CoreDNS-not-ready paths can pass their own CoreDNS Deployment.
func setupRebootTest(objs ...client.Object) (*NodeRebootReconciler, client.Client) {
	scheme := runtime.NewScheme()
	if err := chmv1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		panic(err)
	}

	if !hasCoreDNSDeployment(objs) {
		objs = append(objs, newCoreDNSDeployment(2, 2))
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&corev1.Node{}).
		Build()

	reconciler := &NodeRebootReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	return reconciler, fakeClient
}

// hasCoreDNSDeployment reports whether objs already contains the CoreDNS
// Deployment in kube-system.
func hasCoreDNSDeployment(objs []client.Object) bool {
	for _, o := range objs {
		d, ok := o.(*appsv1.Deployment)
		if !ok {
			continue
		}
		if d.Namespace == coreDNSNamespace && d.Name == coreDNSDeploymentName {
			return true
		}
	}
	return false
}

// newCoreDNSDeployment returns a CoreDNS Deployment in kube-system with
// Spec.Replicas, Status.Replicas, and Status.ReadyReplicas all set from the
// given values. The desired replica count is taken from `replicas`.
func newCoreDNSDeployment(replicas, readyReplicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      coreDNSDeploymentName,
			Namespace: coreDNSNamespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: readyReplicas,
		},
	}
}

func newNode(name, bootID string, annotations map[string]string) *corev1.Node {
	return newNodeWithCreationTime(name, bootID, annotations, time.Time{})
}

func newNodeWithCreationTime(name, bootID string, annotations map[string]string, creationTime time.Time) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Annotations:       annotations,
			CreationTimestamp: metav1.NewTime(creationTime),
		},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				BootID: bootID,
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

// newNotReadyNode is like newNode but the Ready condition is False. The
// Ready condition's LastTransitionTime is set to the node's creationTime so
// callers can control how long the node has been not Ready.
func newNotReadyNode(name, bootID string, annotations map[string]string, creationTime time.Time) *corev1.Node {
	n := newNodeWithCreationTime(name, bootID, annotations, creationTime)
	n.Status.Conditions = []corev1.NodeCondition{
		{
			Type:               corev1.NodeReady,
			Status:             corev1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(creationTime),
		},
	}
	return n
}

// newKarpenterNode returns a Ready=True node managed by Karpenter. If
// initialized is true, the karpenter.sh/initialized=true label is set;
// otherwise the label is absent (simulating a node that Karpenter has not
// yet finished initializing).
func newKarpenterNode(name, bootID string, annotations map[string]string, creationTime time.Time, initialized bool) *corev1.Node {
	n := newNodeWithCreationTime(name, bootID, annotations, creationTime)
	if n.Labels == nil {
		n.Labels = map[string]string{}
	}
	n.Labels[KarpenterCapacityTypeLabel] = "spot"
	if initialized {
		n.Labels[KarpenterInitializedLabel] = "true"
	}
	return n
}

func TestNodeRebootReconcile(t *testing.T) {
	tests := []struct {
		name           string
		node           *corev1.Node
		existingCNH    *chmv1alpha1.CheckNodeHealth
		expectCNH      bool          // expect a CheckNodeHealth CR to exist after reconcile
		expectBootAnno string        // expected bootID annotation value after reconcile
		expectRequeue  time.Duration // expected RequeueAfter value
	}{
		{
			name:           "first time seeing an existing node — sets annotation, no CNH created",
			node:           newNode("node-1", "boot-aaa", nil),
			expectCNH:      false,
			expectBootAnno: "boot-aaa",
			expectRequeue:  NodeConditionTTL,
		},
		{
			name:           "new node (recently created) — creates CheckNodeHealth CR",
			node:           newNodeWithCreationTime("node-1", "boot-aaa", nil, time.Now()),
			expectCNH:      true,
			expectBootAnno: "boot-aaa",
			expectRequeue:  NodeConditionTTL,
		},
		{
			name: "same bootID — no-op",
			node: newNode("node-1", "boot-aaa", map[string]string{
				AnnotationLastBootID: "boot-aaa",
			}),
			expectCNH:      false,
			expectBootAnno: "boot-aaa",
			expectRequeue:  0,
		},
		{
			name: "bootID changed — creates CheckNodeHealth CR",
			node: newNode("node-1", "boot-bbb", map[string]string{
				AnnotationLastBootID: "boot-aaa",
			}),
			expectCNH:      true,
			expectBootAnno: "boot-bbb",
			expectRequeue:  NodeConditionTTL,
		},
		{
			name: "duplicate reconcile — AlreadyExists is ignored",
			node: newNode("node-1", "boot-bbb", map[string]string{
				AnnotationLastBootID: "boot-aaa",
			}),
			existingCNH: &chmv1alpha1.CheckNodeHealth{
				ObjectMeta: metav1.ObjectMeta{
					Name: GenerateCNHName("node-1", "boot-bbb"),
				},
				Spec: chmv1alpha1.CheckNodeHealthSpec{
					NodeRef: chmv1alpha1.NodeReference{Name: "node-1"},
				},
			},
			expectCNH:      true,
			expectBootAnno: "boot-bbb",
			expectRequeue:  NodeConditionTTL,
		},
		{
			name:           "node with empty bootID — skipped",
			node:           newNode("node-1", "", nil),
			expectCNH:      false,
			expectBootAnno: "",
			expectRequeue:  0,
		},
		{
			name:           "new node not Ready yet — no CNH, annotation not set, requeues",
			node:           newNotReadyNode("node-1", "boot-aaa", nil, time.Now()),
			expectCNH:      false,
			expectBootAnno: "",
			expectRequeue:  NodeReadyRequeueInterval,
		},
		{
			name: "reboot detected but node not Ready yet — no CNH, annotation unchanged, requeues",
			node: newNotReadyNode("node-1", "boot-bbb", map[string]string{
				AnnotationLastBootID: "boot-aaa",
			}, time.Now()),
			expectCNH:      false,
			expectBootAnno: "boot-aaa",
			expectRequeue:  NodeReadyRequeueInterval,
		},
		{
			name: "reboot detected, node not Ready past max wait — no CNH, annotation unchanged, no requeue",
			node: newNotReadyNode("node-1", "boot-bbb", map[string]string{
				AnnotationLastBootID: "boot-aaa",
			}, time.Now().Add(-(NodeReadyMaxWait + time.Minute))),
			expectCNH:      false,
			expectBootAnno: "boot-aaa",
			expectRequeue:  0,
		},
		{
			name:           "new Karpenter node not initialized — no CNH, annotation not set, requeues",
			node:           newKarpenterNode("node-1", "boot-aaa", nil, time.Now(), false),
			expectCNH:      false,
			expectBootAnno: "",
			expectRequeue:  NodeReadyRequeueInterval,
		},
		{
			name: "reboot detected on Karpenter node not initialized — no CNH, annotation unchanged, requeues",
			node: newKarpenterNode("node-1", "boot-bbb", map[string]string{
				AnnotationLastBootID: "boot-aaa",
			}, time.Now(), false),
			expectCNH:      false,
			expectBootAnno: "boot-aaa",
			expectRequeue:  NodeReadyRequeueInterval,
		},
		{
			name: "reboot detected on Karpenter node not initialized past max wait — no CNH, annotation unchanged, no requeue",
			node: newKarpenterNode("node-1", "boot-bbb", map[string]string{
				AnnotationLastBootID: "boot-aaa",
			}, time.Now().Add(-(NodeReadyMaxWait + time.Minute)), false),
			expectCNH:      false,
			expectBootAnno: "boot-aaa",
			expectRequeue:  0,
		},
		{
			name:           "new Karpenter node already initialized — creates CNH",
			node:           newKarpenterNode("node-1", "boot-aaa", nil, time.Now(), true),
			expectCNH:      true,
			expectBootAnno: "boot-aaa",
			expectRequeue:  NodeConditionTTL,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{tc.node}
			if tc.existingCNH != nil {
				objs = append(objs, tc.existingCNH)
			}
			r, fc := setupRebootTest(objs...)

			result, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: tc.node.Name},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.RequeueAfter != tc.expectRequeue {
				t.Errorf("RequeueAfter = %v, want %v", result.RequeueAfter, tc.expectRequeue)
			}

			// Check annotation
			updatedNode := &corev1.Node{}
			if err := fc.Get(context.Background(), client.ObjectKeyFromObject(tc.node), updatedNode); err != nil {
				t.Fatalf("failed to get node: %v", err)
			}
			gotAnno := updatedNode.Annotations[AnnotationLastBootID]
			if gotAnno != tc.expectBootAnno {
				t.Errorf("bootID annotation = %q, want %q", gotAnno, tc.expectBootAnno)
			}

			// Check CNH existence
			if tc.node.Status.NodeInfo.BootID != "" {
				cnhName := GenerateCNHName(tc.node.Name, tc.node.Status.NodeInfo.BootID)
				cnh := &chmv1alpha1.CheckNodeHealth{}
				err := fc.Get(context.Background(), client.ObjectKey{Name: cnhName}, cnh)
				if tc.expectCNH {
					if err != nil {
						t.Errorf("expected CheckNodeHealth %q to exist, got error: %v", cnhName, err)
					} else if cnh.Spec.NodeRef.Name != tc.node.Name {
						t.Errorf("CNH nodeRef = %q, want %q", cnh.Spec.NodeRef.Name, tc.node.Name)
					}
				} else {
					if err == nil {
						t.Errorf("expected no CheckNodeHealth, but %q exists", cnhName)
					}
				}
			}
		})
	}
}

func TestGenerateCNHName(t *testing.T) {
	// Deterministic: same inputs → same output
	name1 := GenerateCNHName("node-1", "boot-abc")
	name2 := GenerateCNHName("node-1", "boot-abc")
	if name1 != name2 {
		t.Errorf("should be deterministic: %q != %q", name1, name2)
	}

	// Different bootID → different name
	name3 := GenerateCNHName("node-1", "boot-xyz")
	if name1 == name3 {
		t.Errorf("different bootIDs should produce different names: %q == %q", name1, name3)
	}

	// Prefix check: format is "boot-<hash8>-<nodeName>"
	expectedPrefix := cnhRebootPrefix + "-"
	if !strings.HasPrefix(name1, expectedPrefix) {
		t.Errorf("expected name to start with %q, got %q", expectedPrefix, name1)
	}
	if !strings.HasSuffix(name1, "-node-1") {
		t.Errorf("expected name to end with node name, got %q", name1)
	}

	// Length capped at 253
	longNode := ""
	for i := 0; i < 260; i++ {
		longNode += "a"
	}
	name := GenerateCNHName(longNode, "boot")
	if len(name) > maxCNHNameLength {
		t.Errorf("name length %d exceeds max %d", len(name), maxCNHNameLength)
	}
}

func TestNodeRebootPredicate(t *testing.T) {
	r := &NodeRebootReconciler{}
	pred := r.nodeRebootPredicate()

	t.Run("create event passes", func(t *testing.T) {
		if !pred.Create(event.CreateEvent{Object: newNode("n", "b", nil)}) {
			t.Error("expected create to pass")
		}
	})

	t.Run("delete event rejected", func(t *testing.T) {
		if pred.Delete(event.DeleteEvent{Object: newNode("n", "b", nil)}) {
			t.Error("expected delete to be rejected")
		}
	})

	t.Run("update with same bootID passes", func(t *testing.T) {
		old := newNode("n", "boot-1", nil)
		new := newNode("n", "boot-1", nil)
		if !pred.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: new}) {
			t.Error("expected same bootID update to pass")
		}
	})

	t.Run("update with changed bootID passes", func(t *testing.T) {
		old := newNode("n", "boot-1", nil)
		new := newNode("n", "boot-2", nil)
		if !pred.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: new}) {
			t.Error("expected different bootID update to pass")
		}
	})

	t.Run("generic event rejected", func(t *testing.T) {
		if pred.Generic(event.GenericEvent{Object: newNode("n", "b", nil)}) {
			t.Error("expected generic to be rejected")
		}
	})
}

func TestRemoveStaleNodeCondition(t *testing.T) {
	tests := []struct {
		name            string
		node            *corev1.Node
		expectRemoved   bool
		expectCondCount int
	}{
		{
			name: "no NodeHealthy condition — no-op",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
				Status: corev1.NodeStatus{
					NodeInfo: corev1.NodeSystemInfo{BootID: "boot-1"},
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			},
			expectRemoved:   false,
			expectCondCount: 1,
		},
		{
			name: "fresh NodeHealthy condition — not removed",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
				Status: corev1.NodeStatus{
					NodeInfo: corev1.NodeSystemInfo{BootID: "boot-1"},
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
						{
							Type:               "kubernetes.azure.com/NodeHealthy",
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.Now(),
						},
					},
				},
			},
			expectRemoved:   false,
			expectCondCount: 2,
		},
		{
			name: "stale NodeHealthy condition — removed",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
				Status: corev1.NodeStatus{
					NodeInfo: corev1.NodeSystemInfo{BootID: "boot-1"},
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
						{
							Type:               "kubernetes.azure.com/NodeHealthy",
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
						},
					},
				},
			},
			expectRemoved:   true,
			expectCondCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, fc := setupRebootTest(tc.node)
			ctx := context.Background()

			err := r.removeStaleNodeCondition(ctx, tc.node)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			updatedNode := &corev1.Node{}
			if err := fc.Get(ctx, client.ObjectKeyFromObject(tc.node), updatedNode); err != nil {
				t.Fatalf("failed to get node: %v", err)
			}

			if len(updatedNode.Status.Conditions) != tc.expectCondCount {
				t.Errorf("expected %d conditions, got %d", tc.expectCondCount, len(updatedNode.Status.Conditions))
			}

			if tc.expectRemoved {
				for _, c := range updatedNode.Status.Conditions {
					if c.Type == "kubernetes.azure.com/NodeHealthy" {
						t.Error("expected NodeHealthy condition to be removed, but it still exists")
					}
				}
			}
		})
	}
}

func TestIsNodeReadyForHealthCheck(t *testing.T) {
	tests := []struct {
		name    string
		node    *corev1.Node
		coreDNS []client.Object
		want    bool
		wantErr bool
	}{
		{
			name:    "non-Karpenter node condition Ready=True with fully Ready CoreDNS Deployment — ready for health check",
			node:    newNodeWithCreationTime("node-1", "boot-aaa", nil, time.Now()),
			coreDNS: []client.Object{newCoreDNSDeployment(2, 2)},
			want:    true,
		},
		{
			name:    "non-Karpenter node condition Ready=False — not ready for health check",
			node:    newNotReadyNode("node-1", "boot-aaa", nil, time.Now()),
			coreDNS: []client.Object{newCoreDNSDeployment(2, 2)},
			want:    false,
		},
		{
			name: "non-Karpenter no Ready condition — not ready for health check",
			node: func() *corev1.Node {
				n := newNodeWithCreationTime("node-1", "boot-aaa", nil, time.Now())
				n.Status.Conditions = nil
				return n
			}(),
			coreDNS: []client.Object{newCoreDNSDeployment(2, 2)},
			want:    false,
		},
		{
			name:    "non-Karpenter node Ready but CoreDNS Deployment has no Ready replicas — not ready for health check",
			node:    newNodeWithCreationTime("node-1", "boot-aaa", nil, time.Now()),
			coreDNS: []client.Object{newCoreDNSDeployment(2, 0)},
			want:    false,
		},
		{
			name:    "non-Karpenter node Ready but CoreDNS Deployment partially Ready — not ready for health check",
			node:    newNodeWithCreationTime("node-1", "boot-aaa", nil, time.Now()),
			coreDNS: []client.Object{newCoreDNSDeployment(2, 1)},
			want:    false,
		},
		{
			name:    "Karpenter initialized with fully Ready CoreDNS Deployment — ready for health check",
			node:    newKarpenterNode("node-1", "boot-aaa", nil, time.Now(), true),
			coreDNS: []client.Object{newCoreDNSDeployment(2, 2)},
			want:    true,
		},
		{
			name:    "Karpenter not initialized — not ready for health check",
			node:    newKarpenterNode("node-1", "boot-aaa", nil, time.Now(), false),
			coreDNS: []client.Object{newCoreDNSDeployment(2, 2)},
			want:    false,
		},
		{
			name: "non-Karpenter node Ready and CoreDNS Spec.Replicas nil with 1 Ready replica — ready (defaults to 1)",
			node: newNodeWithCreationTime("node-1", "boot-aaa", nil, time.Now()),
			coreDNS: []client.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      coreDNSDeploymentName,
						Namespace: coreDNSNamespace,
					},
					Status: appsv1.DeploymentStatus{
						ReadyReplicas: 1,
					},
				},
			},
			want: true,
		},
		{
			name: "non-Karpenter node Ready and CoreDNS Spec.Replicas nil with 0 Ready replicas — not ready (defaults to 1)",
			node: newNodeWithCreationTime("node-1", "boot-aaa", nil, time.Now()),
			coreDNS: []client.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      coreDNSDeploymentName,
						Namespace: coreDNSNamespace,
					},
					Status: appsv1.DeploymentStatus{
						ReadyReplicas: 0,
					},
				},
			},
			want: false,
		},
		{
			name:    "non-Karpenter node Ready but CoreDNS Spec.Replicas is 0 — error",
			node:    newNodeWithCreationTime("node-1", "boot-aaa", nil, time.Now()),
			coreDNS: []client.Object{newCoreDNSDeployment(0, 0)},
			want:    false,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]client.Object{tc.node}, tc.coreDNS...)
			r, _ := setupRebootTest(objs...)
			got, err := r.isNodeReadyForHealthCheck(context.Background(), tc.node, "boot-aaa")
			if (err != nil) != tc.wantErr {
				t.Fatalf("isNodeReadyForHealthCheck err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("isNodeReadyForHealthCheck = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCreateCheckNodeHealthSkipsWhenNotReady(t *testing.T) {
	t.Run("not Ready returns (false, nil) and creates no CR", func(t *testing.T) {
		node := newNotReadyNode("node-1", "boot-aaa", nil, time.Now())
		r, fc := setupRebootTest(node)

		created, err := r.createCheckNodeHealth(context.Background(), node, "boot-aaa")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if created {
			t.Fatal("expected created=false when node is not Ready")
		}

		cnh := &chmv1alpha1.CheckNodeHealth{}
		gotErr := fc.Get(context.Background(), client.ObjectKey{Name: GenerateCNHName("node-1", "boot-aaa")}, cnh)
		if !apierrors.IsNotFound(gotErr) {
			t.Errorf("expected NotFound error for CheckNodeHealth, got %v", gotErr)
		}
	})

	t.Run("Ready returns (true, nil) and creates CR", func(t *testing.T) {
		node := newNodeWithCreationTime("node-1", "boot-aaa", nil, time.Now())
		r, fc := setupRebootTest(node)

		created, err := r.createCheckNodeHealth(context.Background(), node, "boot-aaa")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !created {
			t.Fatal("expected created=true when node is Ready")
		}

		cnh := &chmv1alpha1.CheckNodeHealth{}
		if err := fc.Get(context.Background(), client.ObjectKey{Name: GenerateCNHName("node-1", "boot-aaa")}, cnh); err != nil {
			t.Errorf("expected CheckNodeHealth to be created: %v", err)
		}
	})

	t.Run("Karpenter node not initialized returns (false, nil) and creates no CR", func(t *testing.T) {
		node := newKarpenterNode("node-1", "boot-aaa", nil, time.Now(), false)
		r, fc := setupRebootTest(node)

		created, err := r.createCheckNodeHealth(context.Background(), node, "boot-aaa")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if created {
			t.Fatal("expected created=false when Karpenter node is not initialized")
		}

		cnh := &chmv1alpha1.CheckNodeHealth{}
		gotErr := fc.Get(context.Background(), client.ObjectKey{Name: GenerateCNHName("node-1", "boot-aaa")}, cnh)
		if !apierrors.IsNotFound(gotErr) {
			t.Errorf("expected NotFound error for CheckNodeHealth, got %v", gotErr)
		}
	})

	t.Run("Karpenter node initialized returns (true, nil) and creates CR", func(t *testing.T) {
		node := newKarpenterNode("node-1", "boot-aaa", nil, time.Now(), true)
		r, fc := setupRebootTest(node)

		created, err := r.createCheckNodeHealth(context.Background(), node, "boot-aaa")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !created {
			t.Fatal("expected created=true when Karpenter node is initialized")
		}

		cnh := &chmv1alpha1.CheckNodeHealth{}
		if err := fc.Get(context.Background(), client.ObjectKey{Name: GenerateCNHName("node-1", "boot-aaa")}, cnh); err != nil {
			t.Errorf("expected CheckNodeHealth to be created: %v", err)
		}
	})
}
