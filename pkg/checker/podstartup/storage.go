package podstartup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	azureDiskPVCNamePrefix = "clusterhealthmonitor-azuredisk-pvc"
	azureFilePVCNamePrefix = "clusterhealthmonitor-azurefile-pvc"
	azureBlobPVCNamePrefix = "clusterhealthmonitor-azureblob-pvc"
)

var (
	// Storage class names must be variables to get their pointers.
	azureDiskStorageClassName       = "managed-csi"                       // builtin storage class for AKS
	azureFileStorageClassName       = "azurefile-csi"                     // builtin storage class for AKS
	azureBlobStorageClassNamePrefix = "clusterhealthmonitor-azureblob-sc" // custom storage class for Azure Blob CSI
)

func (c *PodStartupChecker) azureDiskPVC(timestampStr string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PersistentVolumeClaim",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", azureDiskPVCNamePrefix, timestampStr),
			Namespace: c.config.SyntheticPodNamespace,
			Labels:    c.syntheticPodLabels(),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			StorageClassName: &azureDiskStorageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("5Gi"),
				},
			},
		},
	}
}

func (c *PodStartupChecker) azureBlobStorageClass(timestampStr string) *storagev1.StorageClass {
	allowVolumeExpansion := true
	reclaimPolicy := corev1.PersistentVolumeReclaimDelete
	volumeBindingMode := storagev1.VolumeBindingImmediate
	return &storagev1.StorageClass{
		TypeMeta: metav1.TypeMeta{
			Kind:       "StorageClass",
			APIVersion: "storage.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("%s-%s", azureBlobStorageClassNamePrefix, timestampStr),
			Labels: c.syntheticPodLabels(),
		},
		Provisioner:          "blob.csi.azure.com",
		AllowVolumeExpansion: &allowVolumeExpansion,
		ReclaimPolicy:        &reclaimPolicy,
		VolumeBindingMode:    &volumeBindingMode,
		Parameters: map[string]string{
			"skuName":  "Standard_LRS",
			"protocol": "fuse",
		},
	}
}

func (c *PodStartupChecker) azureFilePVC(timestampStr string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PersistentVolumeClaim",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", azureFilePVCNamePrefix, timestampStr),
			Namespace: c.config.SyntheticPodNamespace,
			Labels:    c.syntheticPodLabels(),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			StorageClassName: &azureFileStorageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("5Gi"),
				},
			},
		},
	}
}

func (c *PodStartupChecker) azureBlobPVC(timestampStr string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PersistentVolumeClaim",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", azureBlobPVCNamePrefix, timestampStr),
			Namespace: c.config.SyntheticPodNamespace,
			Labels:    c.syntheticPodLabels(),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			StorageClassName: &c.azureBlobStorageClass(timestampStr).Name,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("5Gi"),
				},
			},
		},
	}
}

func (c *PodStartupChecker) createCSIResources(ctx context.Context, timestampStr string) error {
	for _, csiType := range c.config.EnabledCSIs {
		switch csiType {
		case config.CSITypeAzureDisk:
			_, err := c.k8sClientset.CoreV1().PersistentVolumeClaims(c.config.SyntheticPodNamespace).Create(ctx, c.azureDiskPVC(timestampStr), metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create Azure Disk PVC: %w", err)
			}
		case config.CSITypeAzureFile:
			_, err := c.k8sClientset.CoreV1().PersistentVolumeClaims(c.config.SyntheticPodNamespace).Create(ctx, c.azureFilePVC(timestampStr), metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create Azure File PVC: %w", err)
			}
		case config.CSITypeAzureBlob:
			_, err := c.k8sClientset.StorageV1().StorageClasses().Create(ctx, c.azureBlobStorageClass(timestampStr), metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create Azure Blob StorageClass: %w", err)
			}
			_, err = c.k8sClientset.CoreV1().PersistentVolumeClaims(c.config.SyntheticPodNamespace).Create(ctx, c.azureBlobPVC(timestampStr), metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create Azure Blob PVC: %w", err)
			}
		default:
			return fmt.Errorf("failed to create resources for unsupported CSI type: %s", csiType)
		}
	}
	return nil
}

func (c *PodStartupChecker) deleteCSIResources(ctx context.Context, timestampStr string) error {
	for _, csiType := range c.config.EnabledCSIs {
		switch csiType {
		case config.CSITypeAzureDisk:
			err := c.k8sClientset.CoreV1().PersistentVolumeClaims(c.config.SyntheticPodNamespace).Delete(ctx, c.azureDiskPVC(timestampStr).Name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete Azure Disk PVC: %w", err)
			}
		case config.CSITypeAzureFile:
			err := c.k8sClientset.CoreV1().PersistentVolumeClaims(c.config.SyntheticPodNamespace).Delete(ctx, c.azureFilePVC(timestampStr).Name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete Azure File PVC: %w", err)
			}
		case config.CSITypeAzureBlob:
			err := c.k8sClientset.StorageV1().StorageClasses().Delete(ctx, c.azureBlobStorageClass(timestampStr).Name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete Azure Blob StorageClass: %w", err)
			}
			err = c.k8sClientset.CoreV1().PersistentVolumeClaims(c.config.SyntheticPodNamespace).Delete(ctx, c.azureBlobPVC(timestampStr).Name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete Azure Blob PVC: %w", err)
			}
		default:
			return fmt.Errorf("failed to delete resources for unsupported CSI type: %s", csiType)
		}
	}
	return nil
}

func (c *PodStartupChecker) persistentVolumeClaimGarbageCollection(ctx context.Context) error {
	pvcs, err := c.k8sClientset.CoreV1().PersistentVolumeClaims(c.config.SyntheticPodNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set(c.syntheticPodLabels())).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to list persistent volume claims: %w", err)
	}

	var errs []error

	for _, pvc := range pvcs.Items {
		if time.Since(pvc.CreationTimestamp.Time) > c.timeout {
			err := c.k8sClientset.CoreV1().PersistentVolumeClaims(c.config.SyntheticPodNamespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("failed to delete outdated persistent volume claim %s: %w", pvc.Name, err))
			}
		}
	}

	return errors.Join(errs...)
}

func (c *PodStartupChecker) storageClassGarbageCollection(ctx context.Context) error {
	scs, err := c.k8sClientset.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set(c.syntheticPodLabels())).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to list storage classes: %w", err)
	}

	var errs []error
	for _, sc := range scs.Items {
		if time.Since(sc.CreationTimestamp.Time) > c.timeout {
			err := c.k8sClientset.StorageV1().StorageClasses().Delete(ctx, sc.Name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("failed to delete outdated storage class %s: %w", sc.Name, err))
			}
		}
	}

	return errors.Join(errs...)
}

func (c *PodStartupChecker) checkCSIResourceLimit(ctx context.Context) error {
	if len(c.config.EnabledCSIs) == 0 {
		return nil
	}

	// List PVCs to check the current number of synthetic PVCs. Do not run the checker if the maximum number of synthetic PVCs has been reached.
	pvcs, err := c.k8sClientset.CoreV1().PersistentVolumeClaims(c.config.SyntheticPodNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set(c.syntheticPodLabels())).String(),
	})
	if err != nil {
		return err
	}
	if len(pvcs.Items) >= c.config.MaxSyntheticPods*len(c.config.EnabledCSIs) {
		return fmt.Errorf("maximum number of PVCs reached, current: %d, max allowed: %d, delete some PVCs before running the checker again",
			len(pvcs.Items), c.config.MaxSyntheticPods*len(c.config.EnabledCSIs))
	}

	for _, csiType := range c.config.EnabledCSIs {
		if csiType == config.CSITypeAzureBlob {
			// Azure Blob CSI requires a StorageClass to be created, so we need to check the quota for StorageClasses as well.
			// List StorageClasses to check the current number of synthetic StorageClasses. Do not run the checker if the maximum number of synthetic StorageClasses has been reached.
			scs, err := c.k8sClientset.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{
				LabelSelector: labels.SelectorFromSet(labels.Set(c.syntheticPodLabels())).String(),
			})
			if err != nil {
				return err
			}
			if len(scs.Items) >= c.config.MaxSyntheticPods {
				return fmt.Errorf("maximum number of storage classes reached, current: %d, max allowed: %d, delete some storage classes before running the checker again",
					len(scs.Items), c.config.MaxSyntheticPods)
			}
		}
	}

	return nil
}
