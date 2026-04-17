package node

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

func setupRebootTest(objs ...client.Object) (*NodeRebootReconciler, client.Client) {
	scheme := runtime.NewScheme()
	if err := chmv1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		panic(err)
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
		},
	}
}

func TestNodeRebootReconcile(t *testing.T) {
	tests := []struct {
		name           string
		node           *corev1.Node
		existingCNH    *chmv1alpha1.CheckNodeHealth
		expectCNH      bool   // expect a CheckNodeHealth CR to exist after reconcile
		expectBootAnno string // expected bootID annotation value after reconcile
	}{
		{
			name:           "first time seeing an existing node — sets annotation, no CNH created",
			node:           newNode("node-1", "boot-aaa", nil),
			expectCNH:      false,
			expectBootAnno: "boot-aaa",
		},
		{
			name:           "new node (recently created) — creates CheckNodeHealth CR",
			node:           newNodeWithCreationTime("node-1", "boot-aaa", nil, time.Now()),
			expectCNH:      true,
			expectBootAnno: "boot-aaa",
		},
		{
			name: "same bootID — no-op",
			node: newNode("node-1", "boot-aaa", map[string]string{
				AnnotationLastBootID: "boot-aaa",
			}),
			expectCNH:      false,
			expectBootAnno: "boot-aaa",
		},
		{
			name: "bootID changed — creates CheckNodeHealth CR",
			node: newNode("node-1", "boot-bbb", map[string]string{
				AnnotationLastBootID: "boot-aaa",
			}),
			expectCNH:      true,
			expectBootAnno: "boot-bbb",
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
		},
		{
			name:           "node with empty bootID — skipped",
			node:           newNode("node-1", "", nil),
			expectCNH:      false,
			expectBootAnno: "",
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
			if result.RequeueAfter != 0 {
				t.Errorf("unexpected requeue: %v", result)
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
