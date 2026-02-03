package upgradenodeinprogress

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	unipv1alpha1 "github.com/Azure/cluster-health-monitor/apis/upgradenodeinprogresses/v1alpha1"
)

func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	if err := chmv1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := unipv1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	return scheme
}

func newFakeClientBuilder(scheme *runtime.Scheme) *fake.ClientBuilder {
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&unipv1alpha1.HealthSignal{}, &chmv1alpha1.CheckNodeHealth{}).
		WithIndex(&unipv1alpha1.HealthSignal{}, healthSignalOwnerUIDIndex, func(obj client.Object) []string {
			hs := obj.(*unipv1alpha1.HealthSignal)
			var uids []string
			for _, ref := range hs.OwnerReferences {
				if ref.Kind == "UpgradeNodeInProgress" {
					uids = append(uids, string(ref.UID))
				}
			}
			return uids
		}).
		WithIndex(&chmv1alpha1.CheckNodeHealth{}, checkNodeHealthOwnerUIDIndex, func(obj client.Object) []string {
			cnh := obj.(*chmv1alpha1.CheckNodeHealth)
			var uids []string
			for _, ref := range cnh.OwnerReferences {
				if ref.Kind == "HealthSignal" {
					uids = append(uids, string(ref.UID))
				}
			}
			return uids
		})
}

func TestReconcile(t *testing.T) {
	now := metav1.Now()
	controller := true
	blockOwnerDeletion := true

	tests := []struct {
		name                    string
		existingObjects         []client.Object
		reconcileName           string
		expectedResult          ctrl.Result
		expectError             bool
		expectedHealthSignals   int
		expectedCheckNodeHealth int
		validateFunc            func(t *testing.T, c client.Client)
	}{
		{
			name: "creates HealthSignal and CheckNodeHealth for new UpgradeNodeInProgress",
			existingObjects: []client.Object{
				&unipv1alpha1.UpgradeNodeInProgress{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-unip",
						UID:  types.UID("test-unip-uid"),
					},
					Spec: unipv1alpha1.UpgradeNodeInProgressSpec{
						NodeRef: unipv1alpha1.NodeReference{Name: "test-node"},
					},
				},
			},
			reconcileName:           "test-unip",
			expectedResult:          ctrl.Result{RequeueAfter: 5 * time.Second},
			expectError:             false,
			expectedHealthSignals:   1,
			expectedCheckNodeHealth: 1,
			validateFunc: func(t *testing.T, c client.Client) {
				ctx := context.Background()

				// Verify HealthSignal was created with correct fields
				hsList := &unipv1alpha1.HealthSignalList{}
				if err := c.List(ctx, hsList); err != nil {
					t.Fatalf("Failed to list HealthSignals: %v", err)
				}
				hs := &hsList.Items[0]
				if hs.Name != "test-unip-ClusterHealthMonitor" {
					t.Errorf("Expected HealthSignal name 'test-unip-ClusterHealthMonitor', got %s", hs.Name)
				}
				if hs.Spec.Source != HealthSignalSource {
					t.Errorf("Expected source %s, got %s", HealthSignalSource, hs.Spec.Source)
				}
				if hs.Spec.Target.NodeName != "test-node" {
					t.Errorf("Expected node name 'test-node', got %s", hs.Spec.Target.NodeName)
				}
				// Verify HealthSignal owner reference
				if len(hs.OwnerReferences) != 1 {
					t.Errorf("Expected 1 owner reference on HealthSignal, got %d", len(hs.OwnerReferences))
				} else {
					if hs.OwnerReferences[0].Kind != "UpgradeNodeInProgress" {
						t.Errorf("Expected owner kind 'UpgradeNodeInProgress', got %s", hs.OwnerReferences[0].Kind)
					}
					if hs.OwnerReferences[0].UID != types.UID("test-unip-uid") {
						t.Errorf("Expected owner UID 'test-unip-uid', got %s", hs.OwnerReferences[0].UID)
					}
				}

				// Verify CheckNodeHealth was created with correct fields
				cnhList := &chmv1alpha1.CheckNodeHealthList{}
				if err := c.List(ctx, cnhList); err != nil {
					t.Fatalf("Failed to list CheckNodeHealths: %v", err)
				}
				cnh := &cnhList.Items[0]
				if cnh.Spec.NodeRef.Name != "test-node" {
					t.Errorf("Expected node name 'test-node', got %s", cnh.Spec.NodeRef.Name)
				}
				// Verify CheckNodeHealth owner reference with Controller=true
				if len(cnh.OwnerReferences) != 1 {
					t.Errorf("Expected 1 owner reference on CheckNodeHealth, got %d", len(cnh.OwnerReferences))
				} else {
					ownerRef := cnh.OwnerReferences[0]
					if ownerRef.Kind != "HealthSignal" {
						t.Errorf("Expected owner kind 'HealthSignal', got %s", ownerRef.Kind)
					}
					if ownerRef.Controller == nil || !*ownerRef.Controller {
						t.Error("Expected Controller=true in CheckNodeHealth owner reference")
					}
					if ownerRef.BlockOwnerDeletion == nil || !*ownerRef.BlockOwnerDeletion {
						t.Error("Expected BlockOwnerDeletion=true in CheckNodeHealth owner reference")
					}
				}
			},
		},
		{
			name:                    "handles non-existent UpgradeNodeInProgress",
			existingObjects:         []client.Object{},
			reconcileName:           "non-existent",
			expectedResult:          ctrl.Result{},
			expectError:             false,
			expectedHealthSignals:   0,
			expectedCheckNodeHealth: 0,
		},
		{
			name: "skips UpgradeNodeInProgress being deleted",
			existingObjects: []client.Object{
				&unipv1alpha1.UpgradeNodeInProgress{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-unip",
						UID:               types.UID("test-unip-uid"),
						DeletionTimestamp: &now,
						Finalizers:        []string{"test-finalizer"},
					},
					Spec: unipv1alpha1.UpgradeNodeInProgressSpec{
						NodeRef: unipv1alpha1.NodeReference{Name: "test-node"},
					},
				},
			},
			reconcileName:           "test-unip",
			expectedResult:          ctrl.Result{},
			expectError:             false,
			expectedHealthSignals:   0,
			expectedCheckNodeHealth: 0,
		},
		{
			name: "reuses existing HealthSignal and CheckNodeHealth on subsequent reconcile",
			existingObjects: func() []client.Object {
				unip := &unipv1alpha1.UpgradeNodeInProgress{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-unip",
						UID:  types.UID("test-unip-uid"),
					},
					Spec: unipv1alpha1.UpgradeNodeInProgressSpec{
						NodeRef: unipv1alpha1.NodeReference{Name: "test-node"},
					},
				}
				hs := &unipv1alpha1.HealthSignal{
					ObjectMeta: metav1.ObjectMeta{
						Name: "existing-hs",
						UID:  types.UID("existing-hs-uid"),
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: unipv1alpha1.SchemeGroupVersion.String(),
								Kind:       "UpgradeNodeInProgress",
								Name:       unip.Name,
								UID:        unip.UID,
							},
						},
					},
					Spec: unipv1alpha1.HealthSignalSpec{
						Source: HealthSignalSource,
						Type:   unipv1alpha1.HealthSignalTypeNodeHealth,
						Target: unipv1alpha1.HealthSignalTarget{NodeName: "test-node"},
					},
				}
				cnh := &chmv1alpha1.CheckNodeHealth{
					ObjectMeta: metav1.ObjectMeta{
						Name: "existing-cnh",
						UID:  types.UID("existing-cnh-uid"),
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         unipv1alpha1.SchemeGroupVersion.String(),
								Kind:               "HealthSignal",
								Name:               hs.Name,
								UID:                hs.UID,
								Controller:         &controller,
								BlockOwnerDeletion: &blockOwnerDeletion,
							},
						},
					},
					Spec: chmv1alpha1.CheckNodeHealthSpec{
						NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
					},
					// Status.FinishedAt is nil, so still in progress
				}
				return []client.Object{unip, hs, cnh}
			}(),
			reconcileName:           "test-unip",
			expectedResult:          ctrl.Result{RequeueAfter: 5 * time.Second},
			expectError:             false,
			expectedHealthSignals:   1,
			expectedCheckNodeHealth: 1,
			validateFunc: func(t *testing.T, c client.Client) {
				ctx := context.Background()

				// Verify the existing HealthSignal was reused (not created new)
				hsList := &unipv1alpha1.HealthSignalList{}
				if err := c.List(ctx, hsList); err != nil {
					t.Fatalf("Failed to list HealthSignals: %v", err)
				}
				if hsList.Items[0].Name != "existing-hs" {
					t.Errorf("Expected existing HealthSignal 'existing-hs', got %s", hsList.Items[0].Name)
				}

				// Verify the existing CheckNodeHealth was reused
				cnhList := &chmv1alpha1.CheckNodeHealthList{}
				if err := c.List(ctx, cnhList); err != nil {
					t.Fatalf("Failed to list CheckNodeHealths: %v", err)
				}
				if cnhList.Items[0].Name != "existing-cnh" {
					t.Errorf("Expected existing CheckNodeHealth 'existing-cnh', got %s", cnhList.Items[0].Name)
				}
			},
		},
		{
			name: "syncs status when CheckNodeHealth is completed",
			existingObjects: func() []client.Object {
				unip := &unipv1alpha1.UpgradeNodeInProgress{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-unip",
						UID:  types.UID("test-unip-uid"),
					},
					Spec: unipv1alpha1.UpgradeNodeInProgressSpec{
						NodeRef: unipv1alpha1.NodeReference{Name: "test-node"},
					},
				}
				hs := &unipv1alpha1.HealthSignal{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-unip-ClusterHealthMonitor",
						UID:  types.UID("test-hs-uid"),
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: unipv1alpha1.SchemeGroupVersion.String(),
								Kind:       "UpgradeNodeInProgress",
								Name:       unip.Name,
								UID:        unip.UID,
							},
						},
					},
					Spec: unipv1alpha1.HealthSignalSpec{
						Source: HealthSignalSource,
						Type:   unipv1alpha1.HealthSignalTypeNodeHealth,
						Target: unipv1alpha1.HealthSignalTarget{NodeName: "test-node"},
					},
				}
				cnh := &chmv1alpha1.CheckNodeHealth{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-unip-ClusterHealthMonitor",
						UID:  types.UID("test-cnh-uid"),
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         unipv1alpha1.SchemeGroupVersion.String(),
								Kind:               "HealthSignal",
								Name:               hs.Name,
								UID:                hs.UID,
								Controller:         &controller,
								BlockOwnerDeletion: &blockOwnerDeletion,
							},
						},
					},
					Spec: chmv1alpha1.CheckNodeHealthSpec{
						NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
					},
					Status: chmv1alpha1.CheckNodeHealthStatus{
						StartedAt:  &now,
						FinishedAt: &now,
						Conditions: []metav1.Condition{
							{
								Type:               "Healthy",
								Status:             metav1.ConditionTrue,
								Reason:             "AllChecksPassed",
								Message:            "All health checks passed",
								LastTransitionTime: now,
							},
							{
								Type:               "DNSCheck",
								Status:             metav1.ConditionTrue,
								Reason:             "DNSResolved",
								Message:            "DNS resolution successful",
								LastTransitionTime: now,
							},
						},
					},
				}
				return []client.Object{unip, hs, cnh}
			}(),
			reconcileName:           "test-unip",
			expectedResult:          ctrl.Result{},
			expectError:             false,
			expectedHealthSignals:   1,
			expectedCheckNodeHealth: 1,
			validateFunc: func(t *testing.T, c client.Client) {
				ctx := context.Background()
				hs := &unipv1alpha1.HealthSignal{}
				if err := c.Get(ctx, client.ObjectKey{Name: "test-unip-ClusterHealthMonitor"}, hs); err != nil {
					t.Fatalf("Failed to get HealthSignal: %v", err)
				}
				// Verify status was synced
				if hs.Status.StartedAt == nil {
					t.Error("Expected HealthSignal StartedAt to be set")
				}
				if hs.Status.FinishedAt == nil {
					t.Error("Expected HealthSignal FinishedAt to be set")
				}
				if len(hs.Status.Condition) != 2 {
					t.Errorf("Expected 2 conditions, got %d", len(hs.Status.Condition))
				}
				// Verify conditions were copied correctly
				foundHealthy := false
				foundDNS := false
				for _, cond := range hs.Status.Condition {
					if cond.Type == "Healthy" && cond.Status == metav1.ConditionTrue {
						foundHealthy = true
					}
					if cond.Type == "DNSCheck" && cond.Status == metav1.ConditionTrue {
						foundDNS = true
					}
				}
				if !foundHealthy {
					t.Error("Expected Healthy condition to be synced")
				}
				if !foundDNS {
					t.Error("Expected DNSCheck condition to be synced")
				}
			},
		},
		{
			name: "syncs unhealthy status when CheckNodeHealth fails",
			existingObjects: func() []client.Object {
				unip := &unipv1alpha1.UpgradeNodeInProgress{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-unip",
						UID:  types.UID("test-unip-uid"),
					},
					Spec: unipv1alpha1.UpgradeNodeInProgressSpec{
						NodeRef: unipv1alpha1.NodeReference{Name: "test-node"},
					},
				}
				hs := &unipv1alpha1.HealthSignal{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-unip-ClusterHealthMonitor",
						UID:  types.UID("test-hs-uid"),
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: unipv1alpha1.SchemeGroupVersion.String(),
								Kind:       "UpgradeNodeInProgress",
								Name:       unip.Name,
								UID:        unip.UID,
							},
						},
					},
					Spec: unipv1alpha1.HealthSignalSpec{
						Source: HealthSignalSource,
						Type:   unipv1alpha1.HealthSignalTypeNodeHealth,
						Target: unipv1alpha1.HealthSignalTarget{NodeName: "test-node"},
					},
				}
				cnh := &chmv1alpha1.CheckNodeHealth{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-unip-ClusterHealthMonitor",
						UID:  types.UID("test-cnh-uid"),
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         unipv1alpha1.SchemeGroupVersion.String(),
								Kind:               "HealthSignal",
								Name:               hs.Name,
								UID:                hs.UID,
								Controller:         &controller,
								BlockOwnerDeletion: &blockOwnerDeletion,
							},
						},
					},
					Spec: chmv1alpha1.CheckNodeHealthSpec{
						NodeRef: chmv1alpha1.NodeReference{Name: "test-node"},
					},
					Status: chmv1alpha1.CheckNodeHealthStatus{
						StartedAt:  &now,
						FinishedAt: &now,
						Conditions: []metav1.Condition{
							{
								Type:               "Healthy",
								Status:             metav1.ConditionFalse,
								Reason:             "CheckFailed",
								Message:            "DNS resolution failed",
								LastTransitionTime: now,
							},
						},
					},
				}
				return []client.Object{unip, hs, cnh}
			}(),
			reconcileName:           "test-unip",
			expectedResult:          ctrl.Result{},
			expectError:             false,
			expectedHealthSignals:   1,
			expectedCheckNodeHealth: 1,
			validateFunc: func(t *testing.T, c client.Client) {
				ctx := context.Background()
				hs := &unipv1alpha1.HealthSignal{}
				if err := c.Get(ctx, client.ObjectKey{Name: "test-unip-ClusterHealthMonitor"}, hs); err != nil {
					t.Fatalf("Failed to get HealthSignal: %v", err)
				}
				if len(hs.Status.Condition) != 1 {
					t.Errorf("Expected 1 condition, got %d", len(hs.Status.Condition))
				}
				if hs.Status.Condition[0].Status != metav1.ConditionFalse {
					t.Errorf("Expected Healthy condition to be False, got %s", hs.Status.Condition[0].Status)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newScheme()
			fakeClient := newFakeClientBuilder(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			reconciler := &UpgradeNodeInProgressReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			ctx := context.Background()
			req := ctrl.Request{NamespacedName: client.ObjectKey{Name: tt.reconcileName}}
			result, err := reconciler.Reconcile(ctx, req)

			if tt.expectError && err == nil {
				t.Error("Expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if result != tt.expectedResult {
				t.Errorf("Expected result %v, got %v", tt.expectedResult, result)
			}

			// Verify counts
			hsList := &unipv1alpha1.HealthSignalList{}
			if err := fakeClient.List(ctx, hsList); err != nil {
				t.Fatalf("Failed to list HealthSignals: %v", err)
			}
			if len(hsList.Items) != tt.expectedHealthSignals {
				t.Errorf("Expected %d HealthSignals, got %d", tt.expectedHealthSignals, len(hsList.Items))
			}

			cnhList := &chmv1alpha1.CheckNodeHealthList{}
			if err := fakeClient.List(ctx, cnhList); err != nil {
				t.Fatalf("Failed to list CheckNodeHealths: %v", err)
			}
			if len(cnhList.Items) != tt.expectedCheckNodeHealth {
				t.Errorf("Expected %d CheckNodeHealths, got %d", tt.expectedCheckNodeHealth, len(cnhList.Items))
			}

			if tt.validateFunc != nil {
				tt.validateFunc(t, fakeClient)
			}
		})
	}
}
