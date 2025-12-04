package e2e

import (
	"context"
	"fmt"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/controller/checknodehealth"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	checkerNamespace = "kube-system"
)

// Helper functions for CheckNodeHealth CR operations using controller-runtime client
func createCheckNodeHealthCR(ctx context.Context, k8sClient client.Client, name, nodeName string) error {
	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: chmv1alpha1.CheckNodeHealthSpec{
			NodeRef: chmv1alpha1.NodeReference{
				Name: nodeName,
			},
		},
	}
	return k8sClient.Create(ctx, cnh)
}

func getCheckNodeHealthCR(ctx context.Context, k8sClient client.Client, name string) (*chmv1alpha1.CheckNodeHealth, error) {
	cnh := &chmv1alpha1.CheckNodeHealth{}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, cnh)
	if err != nil {
		return nil, err
	}
	return cnh, nil
}

func deleteCheckNodeHealthCR(ctx context.Context, k8sClient client.Client, name string) error {
	cnh := &chmv1alpha1.CheckNodeHealth{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	err := k8sClient.Delete(ctx, cnh)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func checkNodeHealthCRExists(ctx context.Context, k8sClient client.Client, name string) bool {
	cnh := &chmv1alpha1.CheckNodeHealth{}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, cnh)
	return err == nil
}

var _ = Describe("CheckNodeHealth Controller", Ordered, ContinueOnFailure, func() {
	var (
		ctx          context.Context
		k8sClient    client.Client
		testNodeName string
	)

	BeforeAll(func() {
		ctx = context.Background()

		// Register CheckNodeHealth CRD scheme
		err := chmv1alpha1.AddToScheme(scheme.Scheme)
		Expect(err).NotTo(HaveOccurred())

		// Create controller-runtime client for CRD operations
		// Use the same kubeconfig that was used to create the global clientset
		restConfig, err := getKubeConfig()
		Expect(err).NotTo(HaveOccurred())

		k8sClient, err = client.New(restConfig, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())

		// Get a valid node name from the cluster using the global clientset
		By("Getting a valid node name from the cluster")
		nodeList, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(len(nodeList.Items)).To(BeNumerically(">", 0), "No nodes found in cluster")
		testNodeName = nodeList.Items[0].Name
		GinkgoWriter.Printf("Using node %s for tests\n", testNodeName)
	})

	var (
		cnhName string
	)

	AfterEach(func() {
		// if cnhName != "" {
		// 	By("Cleaning up CheckNodeHealth CR")
		// 	err := deleteCheckNodeHealthCR(ctx, k8sClient, cnhName)
		// 	if err != nil {
		// 		GinkgoWriter.Printf("Warning: Failed to delete CheckNodeHealth %s: %v\n", cnhName, err)
		// 	}

		// 	// Wait for CR to be deleted
		// 	Eventually(func() bool {
		// 		return !checkNodeHealthCRExists(ctx, k8sClient, cnhName)
		// 	}, "30s", "1s").Should(BeTrue(), "CheckNodeHealth CR was not deleted within timeout")

		// 	cnhName = ""
		// }
	})

	It("should update CR status when pod completes successfully", func() {
		By("Creating a CheckNodeHealth CR")
		cnhName = fmt.Sprintf("test-cnh-success-%d", time.Now().Unix())
		err := createCheckNodeHealthCR(ctx, k8sClient, cnhName, testNodeName)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Created CheckNodeHealth CR: %s\n", cnhName)

		By("Verifying FinishedAt timestamp is set")
		var cnh *chmv1alpha1.CheckNodeHealth
		Eventually(func() bool {
			cnh, err = getCheckNodeHealthCR(ctx, k8sClient, cnhName)
			if err != nil {
				return false
			}
			return cnh.Status.FinishedAt != nil
		}, "60s", "2s").Should(BeTrue(), "FinishedAt timestamp was not set within timeout")

		By("Verifying Healthy condition is updated to True")
		Expect(cnh.Status.Conditions).To(HaveLen(1))
		Expect(cnh.Status.Conditions[0].Type).To(Equal("Healthy"))
		Expect(cnh.Status.Conditions[0].Status).To(Equal(metav1.ConditionTrue))

		By("Verifying status has two results: PodStartup and PodNetwork with Healthy status")
		Expect(cnh.Status.Results).To(HaveLen(2))

		// Find PodStartup result
		var foundPodStartup, foundPodNetwork bool
		for _, result := range cnh.Status.Results {
			if result.Name == "PodStartup" {
				foundPodStartup = true
				Expect(result.Status).To(Equal("Healthy"), "PodStartup should have Healthy status")
			}
			if result.Name == "PodNetwork" {
				foundPodNetwork = true
				Expect(result.Status).To(Equal("Healthy"), "PodNetwork should have Healthy status")
			}
		}
		Expect(foundPodStartup).To(BeTrue(), "PodStartup result not found")
		Expect(foundPodNetwork).To(BeTrue(), "PodNetwork result not found")

		By("Verifying the health check pod is cleaned up after completion")
		Eventually(func() int {
			podList, err := clientset.CoreV1().Pods(checkerNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s", checknodehealth.CheckNodeHealthLabel, cnhName),
			})
			if err != nil {
				GinkgoWriter.Printf("Failed to list pods: %v\n", err)
				return -1
			}
			return len(podList.Items)
		}, "60s", "2s").Should(Equal(0), "Health check pod was not cleaned up within timeout")
	})

	It("should handle pod timeout correctly", func() {
		By("Creating a CheckNodeHealth CR with a non-existent node to trigger timeout")
		cnhName = fmt.Sprintf("test-cnh-timeout-%d", time.Now().Unix())
		nonExistentNode := "fake-nonexistent-node-12345"
		err := createCheckNodeHealthCR(ctx, k8sClient, cnhName, nonExistentNode)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Created CheckNodeHealth CR: %s for non-existent node: %s\n", cnhName, nonExistentNode)

		By("Verifying that a health check pod is created")
		Eventually(func() bool {
			podList, err := clientset.CoreV1().Pods(checkerNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s", checknodehealth.CheckNodeHealthLabel, cnhName),
			})
			if err != nil || len(podList.Items) == 0 {
				return false
			}
			pod := &podList.Items[0]
			// Verify pod is bound to the non-existent node
			return pod.Spec.NodeName == nonExistentNode
		}, "30s", "2s").Should(BeTrue(), "Health check pod was not created")

		By("Verifying pod remains stuck in Pending phase")
		Consistently(func() corev1.PodPhase {
			podList, err := clientset.CoreV1().Pods(checkerNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s", checknodehealth.CheckNodeHealthLabel, cnhName),
			})
			if err != nil || len(podList.Items) == 0 {
				return corev1.PodUnknown
			}
			return podList.Items[0].Status.Phase
		}, "20s", "5s").Should(Equal(corev1.PodPending), "Pod should remain in Pending state")

		By("Waiting for pod timeout to be detected (PodTimeout = 30 seconds)")
		var cnh *chmv1alpha1.CheckNodeHealth
		Eventually(func() bool {
			cnh, err = getCheckNodeHealthCR(ctx, k8sClient, cnhName)
			if err != nil {
				return false
			}
			return cnh.Status.FinishedAt != nil
		}, "60s", "5s").Should(BeTrue(), "Pod timeout was not detected within 1 minutes")

		By("Verifying timeout condition is set correctly")
		Expect(cnh.Status.Conditions).To(HaveLen(1))
		Expect(cnh.Status.Conditions[0].Type).To(Equal("Healthy"))
		Expect(cnh.Status.Conditions[0].Status).To(Equal(metav1.ConditionFalse))

		By("Verifying PodStartup result is recorded as Unhealthy")
		var podStartupResult *chmv1alpha1.CheckResult
		for i := range cnh.Status.Results {
			if cnh.Status.Results[i].Name == "PodStartup" {
				podStartupResult = &cnh.Status.Results[i]
				break
			}
		}
		Expect(podStartupResult).NotTo(BeNil(), "PodStartup result should be present")
		Expect(podStartupResult.Status).To(Equal(chmv1alpha1.CheckStatusUnhealthy))
		Expect(podStartupResult.Message).To(ContainSubstring("timeout"))

		By("Verifying StartedAt timestamp is set")
		Expect(cnh.Status.StartedAt).NotTo(BeNil())

		By("Verifying the timed out pod is cleaned up")
		Eventually(func() int {
			podList, err := clientset.CoreV1().Pods(checkerNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s", checknodehealth.CheckNodeHealthLabel, cnhName),
			})
			if err != nil {
				GinkgoWriter.Printf("Failed to list pods: %v\n", err)
				return -1
			}
			return len(podList.Items)
		}, "30s", "2s").Should(Equal(0), "Timed out pod was not cleaned up within timeout")
	})

	It("should cleanup pod when CR is deleted", func() {
		By("Creating a CheckNodeHealth CR with non-existent node")
		cnhName = fmt.Sprintf("test-cnh-deletion-%d", time.Now().Unix())
		nonExistentNode := "fake-node-for-deletion-test"
		err := createCheckNodeHealthCR(ctx, k8sClient, cnhName, nonExistentNode)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Created CheckNodeHealth CR: %s with non-existent node\n", cnhName)

		By("Waiting for health check pod to be created and stuck in Pending")
		Eventually(func() bool {
			podList, err := clientset.CoreV1().Pods(checkerNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s", checknodehealth.CheckNodeHealthLabel, cnhName),
			})
			if err != nil || len(podList.Items) == 0 {
				return false
			}
			pod := &podList.Items[0]
			return pod.Spec.NodeName == nonExistentNode && pod.Status.Phase == corev1.PodPending
		}, "10s", "1s").Should(BeTrue(), "Health check pod was not created or not in Pending state")

		By("Deleting the CheckNodeHealth CR before timeout (within 5 seconds of creation)")
		err = deleteCheckNodeHealthCR(ctx, k8sClient, cnhName)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying the health check pod is deleted due to CR deletion (not timeout)")
		Eventually(func() bool {
			podList, err := clientset.CoreV1().Pods(checkerNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s", checknodehealth.CheckNodeHealthLabel, cnhName),
			})
			if err != nil {
				return false
			}
			return len(podList.Items) == 0
		}, "60s", "2s").Should(BeTrue(), "Health check pod was not deleted within timeout")

		By("Verifying the CheckNodeHealth CR is deleted")
		Eventually(func() bool {
			return !checkNodeHealthCRExists(ctx, k8sClient, cnhName)
		}, "30s", "1s").Should(BeTrue(), "CheckNodeHealth CR was not deleted within timeout")

		// Prevent cleanup in AfterEach since we already deleted it
		cnhName = ""
	})

	It("should add finalizer to prevent premature deletion", func() {
		By("Creating a CheckNodeHealth CR")
		cnhName = fmt.Sprintf("test-cnh-finalizer-%d", time.Now().Unix())
		err := createCheckNodeHealthCR(ctx, k8sClient, cnhName, testNodeName)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Created CheckNodeHealth CR: %s\n", cnhName)

		By("Verifying finalizer is added")
		var cnh *chmv1alpha1.CheckNodeHealth
		Eventually(func() bool {
			cnh, err = getCheckNodeHealthCR(ctx, k8sClient, cnhName)
			if err != nil {
				return false
			}
			for _, finalizer := range cnh.Finalizers {
				if finalizer == checknodehealth.CheckNodeHealthFinalizer {
					return true
				}
			}
			return false
		}, "30s", "2s").Should(BeTrue(), "Finalizer was not added within timeout")

		By("Verifying finalizer count")
		Expect(cnh.Finalizers).To(HaveLen(1))
	})
})
