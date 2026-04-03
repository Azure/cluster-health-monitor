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
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestStorageResources(t *testing.T) {
	g := NewWithT(t)
	checker := &PodStartupChecker{
		config: &config.PodStartupConfig{
			EnabledCSIs:           csiConfigsFromTypes([]config.CSIType{config.CSITypeAzureDisk, config.CSITypeAzureFile, config.CSITypeAzureBlob}),
			SyntheticPodNamespace: "default",
		},
	}
	pods := checker.generateSyntheticPod("timestampstr")

	g.Expect(pods).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes).To(HaveLen(3)) // Expect 3 volumes for AzureDisk, AzureFile, and AzureBlob

	g.Expect(pods.Spec.Volumes[0]).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes[0].PersistentVolumeClaim).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes[0].PersistentVolumeClaim.ClaimName).To(Equal(checker.azureDiskPVC("timestampstr", testAzureDiskStorageClass).Name))

	g.Expect(pods.Spec.Volumes[1]).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes[1].PersistentVolumeClaim).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes[1].PersistentVolumeClaim.ClaimName).To(Equal(checker.azureFilePVC("timestampstr", testAzureFileStorageClass).Name))

	g.Expect(pods.Spec.Volumes[2]).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes[2].PersistentVolumeClaim).ToNot(BeNil())
	g.Expect(pods.Spec.Volumes[2].PersistentVolumeClaim.ClaimName).To(Equal(checker.azureBlobPVC("timestampstr", testAzureBlobStorageClass).Name))
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
				g.Expect(k8sClient.Actions()).To(HaveLen(3)) // Expect 3 create actions for 3 PVCs
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
				g.Expect(k8sClient.Actions()).To(HaveLen(1)) // Expect 1 PVC create
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
				g.Expect(k8sClient.Actions()).To(HaveLen(1)) // Expect 1 create action for 1 PVC
			},
		},
	}
	for _, tc := range testCases {
		checker := &PodStartupChecker{
			config: &config.PodStartupConfig{
				EnabledCSIs: csiConfigsFromTypes(tc.enabledCSIs),
			},
			k8sClientset: tc.k8sClient,
		}
		err := checker.createCSIResources(context.Background(), "timestampstr")
		g := NewWithT(t)
		tc.validateFunc(g, err, tc.k8sClient)
	}
}

func TestDeleteCSIResources(t *testing.T) {
	syntheticPodNamespace := "kube-system"
	checkerName := "csi-checker"
	syntheticPodLabelKey := "cluster-health-monitor/csi-checker"
	pvcLabels := map[string]string{syntheticPodLabelKey: checkerName}

	testCases := []struct {
		name         string
		k8sClient    *k8sfake.Clientset
		enabledCSIs  []config.CSIType
		validateFunc func(g *WithT, err error, k8sClient *k8sfake.Clientset)
	}{
		{
			name: "all resources successfully deleted",
			k8sClient: k8sfake.NewClientset(
				pvcWithLabels("clusterhealthmonitor-azuredisk-pvc-timestampstr", syntheticPodNamespace, pvcLabels, time.Now()),
				pvcWithLabels("clusterhealthmonitor-azurefile-pvc-timestampstr", syntheticPodNamespace, pvcLabels, time.Now()),
				pvcWithLabels("clusterhealthmonitor-azureblob-pvc-timestampstr", syntheticPodNamespace, pvcLabels, time.Now()),
			),
			enabledCSIs: []config.CSIType{config.CSITypeAzureDisk, config.CSITypeAzureFile, config.CSITypeAzureBlob},
			validateFunc: func(g *WithT, err error, k8sClient *k8sfake.Clientset) {
				g.Expect(err).ToNot(HaveOccurred())
				pvcs, listErr := k8sClient.CoreV1().PersistentVolumeClaims(syntheticPodNamespace).List(context.Background(), metav1.ListOptions{})
				g.Expect(listErr).ToNot(HaveOccurred())
				g.Expect(pvcs.Items).To(BeEmpty())
			},
		},
		{
			name:        "resources successfully deleted with some resources not found",
			enabledCSIs: []config.CSIType{config.CSITypeAzureDisk, config.CSITypeAzureFile, config.CSITypeAzureBlob},
			k8sClient: k8sfake.NewClientset(
				pvcWithLabels("clusterhealthmonitor-azuredisk-pvc-timestampstr", syntheticPodNamespace, pvcLabels, time.Now()),
				pvcWithLabels("clusterhealthmonitor-azurefile-pvc-timestampstr", syntheticPodNamespace, pvcLabels, time.Now()),
				// AzureBlob PVC intentionally missing to trigger not found
			),
			validateFunc: func(g *WithT, err error, k8sClient *k8sfake.Clientset) {
				g.Expect(err).ToNot(HaveOccurred())
				pvcs, listErr := k8sClient.CoreV1().PersistentVolumeClaims(syntheticPodNamespace).List(context.Background(), metav1.ListOptions{})
				g.Expect(listErr).ToNot(HaveOccurred())
				g.Expect(pvcs.Items).To(BeEmpty())
			},
		},
		{
			name:        "deletion error",
			enabledCSIs: []config.CSIType{config.CSITypeAzureFile, config.CSITypeAzureBlob},
			k8sClient: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset(
					pvcWithLabels("clusterhealthmonitor-azurefile-pvc-timestampstr", syntheticPodNamespace, pvcLabels, time.Now()),
					pvcWithLabels("clusterhealthmonitor-azureblob-pvc-timestampstr", syntheticPodNamespace, pvcLabels, time.Now()),
				)
				client.PrependReactor("delete", "persistentvolumeclaims", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("unexpected error occurred while deleting persistent volume claim")
				})
				return client
			}(),
			validateFunc: func(g *WithT, err error, k8sClient *k8sfake.Clientset) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to delete Azure File PVC"))
				g.Expect(err.Error()).To(ContainSubstring("unexpected error occurred while deleting persistent volume claim"))
			},
		},
	}
	for _, tc := range testCases {
		checker := &PodStartupChecker{
			name: checkerName,
			config: &config.PodStartupConfig{
				SyntheticPodNamespace: syntheticPodNamespace,
				SyntheticPodLabelKey:  syntheticPodLabelKey,
				EnabledCSIs:           csiConfigsFromTypes(tc.enabledCSIs),
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

func TestCheckPVCQuota(t *testing.T) {
	testCases := []struct {
		name          string
		EnabledCSIs   []config.CSIType
		k8sClient     *k8sfake.Clientset
		expectedError string
	}{
		{
			name:        "PVC quota check passed",
			EnabledCSIs: []config.CSIType{config.CSITypeAzureFile},
			k8sClient:   k8sfake.NewClientset(),
		},
		{
			name:      "PVC quota check passed - no CSI enabled",
			k8sClient: k8sfake.NewClientset(),
		},
		{
			name:        "PVC quota check failed to list PVCs",
			EnabledCSIs: []config.CSIType{config.CSITypeAzureFile},
			k8sClient: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("list", "persistentvolumeclaims", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("failed to list PVCs")
				})
				return client
			}(),
			expectedError: "failed to list PVCs",
		},
		{
			name:        "PVC quota exceeded",
			EnabledCSIs: []config.CSIType{config.CSITypeAzureFile},
			k8sClient: k8sfake.NewClientset(
				pvcWithLabels("pvc1", "test-namespace", map[string]string{"test-label": "testChecker"}, time.Now().Add(-10*time.Minute)),
			),
			expectedError: "maximum number of PVCs reached",
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			checker := &PodStartupChecker{
				name: "testChecker",
				config: &config.PodStartupConfig{
					EnabledCSIs:                csiConfigsFromTypes(tt.EnabledCSIs),
					SyntheticPodNamespace:      "test-namespace",
					SyntheticPodLabelKey:       "test-label",
					SyntheticPodStartupTimeout: 3 * time.Second,
					MaxSyntheticPods:           1,
				},
				k8sClientset: tt.k8sClient,
			}

			err := checker.checkCSIResourceLimit(context.Background())

			if tt.expectedError != "" {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring(tt.expectedError))
			} else {
				g.Expect(err).NotTo(HaveOccurred())
			}
		})
	}
}

func TestValidateStorageClasses(t *testing.T) {
	testCases := []struct {
		name        string
		enabledCSIs []config.CSIConfig
		k8sClient   *k8sfake.Clientset
		validateRes func(g *WithT, err error)
	}{
		{
			name:        "passes - no CSI enabled",
			enabledCSIs: []config.CSIConfig{},
			k8sClient:   k8sfake.NewClientset(),
			validateRes: func(g *WithT, err error) {
				g.Expect(err).NotTo(HaveOccurred())
			},
		},
		{
			name:        "passes - all storage classes exist",
			enabledCSIs: csiConfigsFromTypes([]config.CSIType{config.CSITypeAzureDisk, config.CSITypeAzureFile, config.CSITypeAzureBlob}),
			k8sClient: k8sfake.NewClientset(
				&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: testAzureDiskStorageClass}},
				&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: testAzureFileStorageClass}},
				&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: testAzureBlobStorageClass}},
			),
			validateRes: func(g *WithT, err error) {
				g.Expect(err).NotTo(HaveOccurred())
			},
		},
		{
			name:        "error - storage class not found",
			enabledCSIs: csiConfigsFromTypes([]config.CSIType{config.CSITypeAzureDisk, config.CSITypeAzureFile}),
			k8sClient: k8sfake.NewClientset(
				&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: testAzureDiskStorageClass}},
				// Azure File storage class is missing to trigger not found error
			),
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to get StorageClass"))
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			},
		},
		{
			name:        "error - non 404 error getting storage class",
			enabledCSIs: csiConfigsFromTypes([]config.CSIType{config.CSITypeAzureBlob}),
			k8sClient: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				client.PrependReactor("get", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("connection refused")
				})
				return client
			}(),
			validateRes: func(g *WithT, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to get StorageClass"))
				g.Expect(apierrors.IsNotFound(err)).To(BeFalse())
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			chk := &PodStartupChecker{
				config: &config.PodStartupConfig{
					EnabledCSIs: tc.enabledCSIs,
				},
				k8sClientset: tc.k8sClient,
			}

			err := chk.validateStorageClasses(context.Background())
			tc.validateRes(g, err)
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
