package podstartup

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kubectl/pkg/scheme"
	karpenter "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func (c *PodStartupChecker) isKarpenterNodePoolCRDPresent(ctx context.Context) (bool, error) {
	// Check if the Karpenter NodePool CRD is present in the cluster.
	customerresourcedefinitionsGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
	_, err := c.dynamicClient.Resource(customerresourcedefinitionsGVR).Get(ctx, "nodepools.karpenter.sh", metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil // CRD doesn't exist, but this is not an error condition
		}
		return false, err // Other errors should be returned
	}
	return true, nil
}

func (c *PodStartupChecker) createKarpenterNodePool(ctx context.Context, nodePool *karpenter.NodePool) error {
	unstructuredNodePool := &unstructured.Unstructured{}
	scheme.Scheme.AddKnownTypes(NodePoolGVR.GroupVersion(), nodePool)
	if err := scheme.Scheme.Convert(nodePool, unstructuredNodePool, nil); err != nil {
		return err
	}

	// Create the NodePool resource.
	if _, err := c.dynamicClient.Resource(NodePoolGVR).Create(ctx, unstructuredNodePool, metav1.CreateOptions{}); err != nil {
		return err
	}

	return nil
}

func (c *PodStartupChecker) deleteKarpenterNodePool(ctx context.Context, nodePoolName string) error {
	// Delete the NodePool resource.
	err := c.dynamicClient.Resource(NodePoolGVR).Delete(ctx, nodePoolName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}

func (c *PodStartupChecker) deleteAllKarpenterNodePools(ctx context.Context) error {
	var errs []error

	// List all NodePools.
	nodePools, err := c.dynamicClient.Resource(NodePoolGVR).List(ctx, metav1.ListOptions{
		LabelSelector: c.config.SyntheticPodLabelKey,
	})
	if err != nil {
		return err
	}

	// Iterate over the NodePools and delete each one.
	for _, nodePool := range nodePools.Items {
		nodePoolName, nameFound, err := unstructured.NestedString(nodePool.Object, "metadata", "name")
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to parse Karpenter Node Pool name: %w", err))
			continue
		}

		if !nameFound {
			errs = append(errs, fmt.Errorf("got Karpenter NodePool with no name field in metadata"))
			continue
		}

		if err := c.deleteKarpenterNodePool(ctx, nodePoolName); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete old Karpenter Node Pool %s: %w", nodePoolName, err))
		}
	}

	return errors.Join(errs...)
}

func (c *PodStartupChecker) karpenterNodePool(nodePoolName, timestampStr string) *karpenter.NodePool {
	return &karpenter.NodePool{
		TypeMeta: metav1.TypeMeta{
			Kind:       "NodePool",
			APIVersion: "karpenter.sh/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: nodePoolName,
			Labels: map[string]string{
				c.config.SyntheticPodLabelKey: timestampStr,
			},
		},
		Spec: karpenter.NodePoolSpec{
			Template: karpenter.NodeClaimTemplate{
				ObjectMeta: karpenter.ObjectMeta{
					Labels: map[string]string{
						c.config.SyntheticPodLabelKey: timestampStr,
					},
				},
				Spec: karpenter.NodeClaimTemplateSpec{
					NodeClassRef: &karpenter.NodeClassReference{
						Group: "karpenter.azure.com",
						Kind:  "AKSNodeClass",
						Name:  "default",
					},
					Requirements: []karpenter.NodeSelectorRequirementWithMinValues{},
				},
			},
		},
	}
}
