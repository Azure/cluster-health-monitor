package podstartup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestStorageResources(t *testing.T) {
	g := NewWithT(t)
	checker := &PodStartupChecker{
		config: &config.PodStartupConfig{
			EnabledCSIs:           []config.CSIType{config.CSITypeAzureDisk, config.CSITypeAzureFile, config.CSITypeAzureBlob},
			SyntheticPodNamespace: "default",
		},
	}
	pods := checker.generateSyntheticPod("timestampstr")

	g.Expect(pods).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes).To(HaveLen(3)) // Expect 3 volumes for AzureDisk, AzureFile, and AzureBlob

	g.Expect(pods.Spec.Volumes[0]).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes[0].PersistentVolumeClaim).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes[0].PersistentVolumeClaim.ClaimName).To(Equal(checker.azureDiskPVC("timestampstr").Name))

	g.Expect(pods.Spec.Volumes[1]).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes[1].PersistentVolumeClaim).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes[1].PersistentVolumeClaim.ClaimName).To(Equal(checker.azureFilePVC("timestampstr").Name))

	g.Expect(pods.Spec.Volumes[2]).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes[2].PersistentVolumeClaim).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes[2].PersistentVolumeClaim.ClaimName).To(Equal(checker.azureBlobPVC("timestampstr").Name))
}

func TestCreateCSIResources(t *testing.T) {
	testCases := []struct {
		name         string
		enabledCSIs  []config.CSIType
		k8sClient    *k8sfake.Clientset
		validateFunc func(g *WithT, err error, k8sClient *k8sfake.Clientset)
	}{
		{
			name:        "CSI tests disabled",
			enabledCSIs: []config.CSIType{},
			k8sClient:   k8sfake.NewClientset(),
			validateFunc: func(g *WithT, err error, k8sClient *k8sfake.Clientset) {
				g.Expect(err).ToNot(HaveOccurred())
			},
		},
		{
			name:        "CSI tests enabled - successful creation",
			enabledCSIs: []config.CSIType{config.CSITypeAzureDisk, config.CSITypeAzureBlob, config.CSITypeAzureFile},
			k8sClient:   k8sfake.NewClientset(),
			validateFunc: func(g *WithT, err error, k8sClient *k8sfake.Clientset) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(k8sClient.Actions()).To(HaveLen(4)) // Expect 4 create actions for 3 PVCs and 1 StorageClass
			},
		},
		{
			name:        "CSI tests enabled - error on creating StorageClass",
			enabledCSIs: []config.CSIType{config.CSITypeAzureDisk, config.CSITypeAzureBlob, config.CSITypeAzureFile},
			k8sClient: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("create", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("internal error")
				})
				return client
			}(),
			validateFunc: func(g *WithT, err error, k8sClient *k8sfake.Clientset) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("internal error"))
				g.Expect(k8sClient.Actions()).To(HaveLen(3)) // Expect 3 create actions for 2 PVCs and 1 StorageClass
			},
		},
		{
			name:        "CSI tests enabled - error on creating azure disk PVC",
			enabledCSIs: []config.CSIType{config.CSITypeAzureDisk},
			k8sClient: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("create", "persistentvolumeclaims", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("internal error")
				})
				return client
			}(),
			validateFunc: func(g *WithT, err error, k8sClient *k8sfake.Clientset) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("internal error"))
				g.Expect(k8sClient.Actions()).To(HaveLen(1)) // Expect 1 create action for 1 PVC
			},
		},
		{
			name:        "CSI tests enabled - error on creating azure blob PVC",
			enabledCSIs: []config.CSIType{config.CSITypeAzureBlob},
			k8sClient: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("create", "persistentvolumeclaims", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("internal error")
				})
				return client
			}(),
			validateFunc: func(g *WithT, err error, k8sClient *k8sfake.Clientset) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("internal error"))
				g.Expect(k8sClient.Actions()).To(HaveLen(1)) // Expect 1 create action for 1 PVC
			},
		},
		{
			name:        "CSI tests enabled - error on creating azure file PVC",
			enabledCSIs: []config.CSIType{config.CSITypeAzureFile},
			k8sClient: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("create", "persistentvolumeclaims", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("internal error")
				})
				return client
			}(),
			validateFunc: func(g *WithT, err error, k8sClient *k8sfake.Clientset) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("internal error"))
				g.Expect(k8sClient.Actions()).To(HaveLen(2)) // Expect 2 create actions for 1 PVC and 1 StorageClass
			},
		},
	}
	for _, tc := range testCases {
		checker := &PodStartupChecker{
			config: &config.PodStartupConfig{
				EnabledCSIs: tc.enabledCSIs,
			},
			k8sClientset: tc.k8sClient,
		}
		err := checker.createCSIResources(context.Background(), "timestampstr")
		g := NewWithT(t)
		tc.validateFunc(g, err, tc.k8sClient)
	}
}

func TestDeleteCSIResources(t *testing.T) {
	testCases := []struct {
		name         string
		k8sClient    *k8sfake.Clientset
		enabledCSIs  []config.CSIType
		validateFunc func(g *WithT, err error, k8sClient *k8sfake.Clientset)
	}{
		{
			name:        "all resources successfully deleted",
			k8sClient:   k8sfake.NewClientset(),
			enabledCSIs: []config.CSIType{config.CSITypeAzureDisk, config.CSITypeAzureFile, config.CSITypeAzureBlob},
			validateFunc: func(g *WithT, err error, k8sClient *k8sfake.Clientset) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(k8sClient.Actions()).To(HaveLen(4)) // Expect 4 delete actions for 3 PVCs and 1 StorageClass
			},
		},
		{
			name:        "resources successfully deleted with some resources not found",
			enabledCSIs: []config.CSIType{config.CSITypeAzureFile},
			k8sClient: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("delete", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "storage.k8s.io", Resource: "storageclasses"}, "azurefile-csi")
				})
				return client
			}(),
			validateFunc: func(g *WithT, err error, k8sClient *k8sfake.Clientset) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(k8sClient.Actions()).To(HaveLen(2)) // Expect 1 StorageClass deletion and 1 PVC deletion
			},
		},
		{
			name:        "deletion error",
			enabledCSIs: []config.CSIType{config.CSITypeAzureFile},
			k8sClient: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("delete", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("unexpected error occurred while deleting storage class")
				})
				return client
			}(),
			validateFunc: func(g *WithT, err error, k8sClient *k8sfake.Clientset) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("unexpected error occurred while deleting storage class"))
				g.Expect(k8sClient.Actions()).To(HaveLen(1)) // Expect 1 delete action for 1 StorageClass
			},
		},
	}
	for _, tc := range testCases {
		checker := &PodStartupChecker{
			config: &config.PodStartupConfig{
				SyntheticPodNamespace: "test-namespace",
				EnabledCSIs:           tc.enabledCSIs,
			},
			k8sClientset: tc.k8sClient,
		}
		err := checker.deleteCSIResources(context.Background(), "timestampstr")
		g := NewWithT(t)
		tc.validateFunc(g, err, tc.k8sClient)
	}
}

func TestPersistentVolumeClaimGarbageCollection(t *testing.T) {
	checkerName := "chk"
	syntheticPodNamespace := "checker-ns"
	checkerTimeout := 5 * time.Second
	syntheticPodLabelKey := "cluster-health-monitor/checker-name"

	tests := []struct {
		name        string
		client      *k8sfake.Clientset
		validateRes func(g *WithT, pvcs *corev1.PersistentVolumeClaimList, err error)
	}{
		{
			name: "only removes pvcs older than timeout",
			client: k8sfake.NewClientset(
				pvcWithLabels("chk-synthetic-old", syntheticPodNamespace, map[string]string{syntheticPodLabelKey: checkerName}, time.Now().Add(-2*time.Hour)),
				pvcWithLabels("chk-synthetic-new", syntheticPodNamespace, map[string]string{syntheticPodLabelKey: checkerName}, time.Now()),
			),
			validateRes: func(g *WithT, pvcs *corev1.PersistentVolumeClaimList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pvcs.Items).To(HaveLen(1))
				g.Expect(pvcs.Items[0].Name).To(Equal("chk-synthetic-new"))
			},
		},
		{
			name: "no pvcs to delete",
			client: k8sfake.NewClientset(
				pvcWithLabels("chk-synthetic-too-new", syntheticPodNamespace, map[string]string{syntheticPodLabelKey: checkerName}, time.Now()), // pvc too new
				pvcWithLabels("chk-synthetic-no-labels", syntheticPodNamespace, map[string]string{}, time.Now().Add(-2*time.Hour)),              // old pvc wrong labels
				pvcWithLabels("no-name-prefix", syntheticPodNamespace, map[string]string{}, time.Now().Add(-2*time.Hour)),                       // pvc missing name prefix
			),
			validateRes: func(g *WithT, pvcs *corev1.PersistentVolumeClaimList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pvcs.Items).To(HaveLen(3))
				actualNames := make([]string, len(pvcs.Items))
				for i, pvc := range pvcs.Items {
					actualNames[i] = pvc.Name
				}
				g.Expect(actualNames).To(ConsistOf([]string{"chk-synthetic-too-new", "chk-synthetic-no-labels", "no-name-prefix"}))
			},
		},
		{
			name: "only removes pvc with checker labels",
			client: k8sfake.NewClientset(
				pvcWithLabels("chk-synthetic-pvc", syntheticPodNamespace, map[string]string{syntheticPodLabelKey: checkerName}, time.Now().Add(-2*time.Hour)),
				pvcWithLabels("chk-synthetic-no-label-pvc", syntheticPodNamespace, map[string]string{}, time.Now().Add(-2*time.Hour)),
			),
			validateRes: func(g *WithT, pvcs *corev1.PersistentVolumeClaimList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pvcs.Items).To(HaveLen(1))
				g.Expect(pvcs.Items[0].Name).To(Equal("chk-synthetic-no-label-pvc"))
			},
		},
		{
			name: "error listing PVCs",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("list", "persistentvolumeclaims", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					// fail the List call in garbageCollect because it uses a label selector. This prevents breaking the test which also
					// lists PVCs but does not use a selector.
					listAction, ok := action.(k8stesting.ListAction)
					if ok && listAction.GetListRestrictions().Labels.String() != "" {
						return true, nil, errors.New("error bad things")
					}
					return false, nil, nil
				})
				return client
			}(),
			validateRes: func(g *WithT, pvcs *corev1.PersistentVolumeClaimList, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to list persistent volume claims"))
			},
		},
		{
			name: "error deleting pvc",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset(
					pvcWithLabels("chk-synthetic-pvc-1", syntheticPodNamespace, map[string]string{syntheticPodLabelKey: checkerName}, time.Now().Add(-2*time.Hour)),
					pvcWithLabels("chk-synthetic-pvc-2", syntheticPodNamespace, map[string]string{syntheticPodLabelKey: checkerName}, time.Now().Add(-2*time.Hour)),
				)
				// only fail the Delete call for old-pvc-1
				client.PrependReactor("delete", "persistentvolumeclaims", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					deleteAction, ok := action.(k8stesting.DeleteAction)
					if ok && deleteAction.GetName() == "chk-synthetic-pvc-1" {
						return true, nil, errors.New("error bad things")
					}
					return false, nil, nil
				})
				return client
			}(),
			validateRes: func(g *WithT, pvcs *corev1.PersistentVolumeClaimList, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to delete outdated persistent volume claim chk-synthetic-pvc-1"))
				g.Expect(pvcs.Items).To(HaveLen(1)) // one PVC should be deleted
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			checker := &PodStartupChecker{
				name: checkerName,
				config: &config.PodStartupConfig{
					SyntheticPodNamespace:      syntheticPodNamespace,
					SyntheticPodLabelKey:       syntheticPodLabelKey,
					SyntheticPodStartupTimeout: 3 * time.Second,
					MaxSyntheticPods:           5,
				},
				timeout:      checkerTimeout,
				k8sClientset: tt.client,
			}

			// Run garbage collect
			err := checker.persistentVolumeClaimGarbageCollection(context.Background())

			// Get PVCs and SCs for validation
			pvcs, listErr := tt.client.CoreV1().PersistentVolumeClaims(syntheticPodNamespace).List(context.Background(), metav1.ListOptions{})
			g.Expect(listErr).NotTo(HaveOccurred())

			tt.validateRes(g, pvcs, err)
		})
	}
}

func TestStorageClassGarbageCollection(t *testing.T) {
	checkerName := "chk"
	syntheticPodNamespace := "checker-ns"
	checkerTimeout := 5 * time.Second
	syntheticPodLabelKey := "cluster-health-monitor/checker-name"

	tests := []struct {
		name        string
		client      *k8sfake.Clientset
		validateRes func(g *WithT, scs *storagev1.StorageClassList, err error)
	}{
		{
			name: "only removes storage classes older than timeout",
			client: k8sfake.NewClientset(
				scWithLabels("chk-synthetic-old", map[string]string{syntheticPodLabelKey: checkerName}, time.Now().Add(-2*time.Hour)),
				scWithLabels("chk-synthetic-new", map[string]string{syntheticPodLabelKey: checkerName}, time.Now()),
			),
			validateRes: func(g *WithT, scs *storagev1.StorageClassList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(scs.Items).To(HaveLen(1))
				g.Expect(scs.Items[0].Name).To(Equal("chk-synthetic-new"))
			},
		},
		{
			name: "no storage classes to delete",
			client: k8sfake.NewClientset(
				scWithLabels("chk-synthetic-too-new", map[string]string{syntheticPodLabelKey: checkerName}, time.Now()), // sc too new
				scWithLabels("chk-synthetic-no-labels", map[string]string{}, time.Now().Add(-2*time.Hour)),              // old sc wrong labels
				scWithLabels("no-name-prefix", map[string]string{}, time.Now().Add(-2*time.Hour)),                       // sc missing name prefix
			),
			validateRes: func(g *WithT, scs *storagev1.StorageClassList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(scs.Items).To(HaveLen(3))
				actualNames := make([]string, len(scs.Items))
				for i, sc := range scs.Items {
					actualNames[i] = sc.Name
				}
				g.Expect(actualNames).To(ConsistOf([]string{"chk-synthetic-too-new", "chk-synthetic-no-labels", "no-name-prefix"}))
			},
		},
		{
			name: "only removes storage classes with checker labels",
			client: k8sfake.NewClientset(
				scWithLabels("chk-synthetic-sc", map[string]string{syntheticPodLabelKey: checkerName}, time.Now().Add(-2*time.Hour)),
				scWithLabels("chk-synthetic-no-label-sc", map[string]string{}, time.Now().Add(-2*time.Hour)),
			),
			validateRes: func(g *WithT, scs *storagev1.StorageClassList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(scs.Items).To(HaveLen(1))
				g.Expect(scs.Items[0].Name).To(Equal("chk-synthetic-no-label-sc"))
			},
		},
		{
			name: "error listing storage classes",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("list", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					// fail the List call in garbageCollect because it uses a label selector. This prevents breaking the test which also
					// lists storage classes but does not use a selector.
					listAction, ok := action.(k8stesting.ListAction)
					if ok && listAction.GetListRestrictions().Labels.String() != "" {
						return true, nil, errors.New("error bad things")
					}
					return false, nil, nil
				})
				return client
			}(),
			validateRes: func(g *WithT, scs *storagev1.StorageClassList, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to list storage classes"))
			},
		},
		{
			name: "error deleting storage class",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset(
					scWithLabels("chk-synthetic-sc-1", map[string]string{syntheticPodLabelKey: checkerName}, time.Now().Add(-2*time.Hour)),
					scWithLabels("chk-synthetic-sc-2", map[string]string{syntheticPodLabelKey: checkerName}, time.Now().Add(-2*time.Hour)),
				)
				// only fail the Delete call for old-sc-1
				client.PrependReactor("delete", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					deleteAction, ok := action.(k8stesting.DeleteAction)
					if ok && deleteAction.GetName() == "chk-synthetic-sc-1" {
						return true, nil, errors.New("error bad things")
					}
					return false, nil, nil
				})
				return client
			}(),
			validateRes: func(g *WithT, scs *storagev1.StorageClassList, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to delete outdated storage class chk-synthetic-sc-1"))
				g.Expect(scs.Items).To(HaveLen(1)) // one SC should be deleted
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			checker := &PodStartupChecker{
				name: checkerName,
				config: &config.PodStartupConfig{
					SyntheticPodNamespace:      syntheticPodNamespace,
					SyntheticPodLabelKey:       syntheticPodLabelKey,
					SyntheticPodStartupTimeout: 3 * time.Second,
					MaxSyntheticPods:           5,
				},
				timeout:      checkerTimeout,
				k8sClientset: tt.client,
			}

			// Run garbage collect
			err := checker.storageClassGarbageCollection(context.Background())

			// Get SCs for validation
			scs, listErr := tt.client.StorageV1().StorageClasses().List(context.Background(), metav1.ListOptions{})
			g.Expect(listErr).NotTo(HaveOccurred())

			tt.validateRes(g, scs, err)
		})
	}
}

func pvcWithLabels(name string, namespace string, labels map[string]string, creationTime time.Time) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			Labels:            labels,
			CreationTimestamp: metav1.NewTime(creationTime),
		},
	}
}

func scWithLabels(name string, labels map[string]string, creationTime time.Time) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Labels:            labels,
			CreationTimestamp: metav1.NewTime(creationTime),
		},
	}
}
