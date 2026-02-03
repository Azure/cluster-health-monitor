package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	unipv1alpha1 "github.com/Azure/cluster-health-monitor/apis/upgradenodeinprogresses/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/controller/upgradenodeinprogress"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Helper functions for UpgradeNodeInProgress CR operations
func createUpgradeNodeInProgressCR(ctx context.Context, k8sClient client.Client, name, nodeName string) error {
	unip := &unipv1alpha1.UpgradeNodeInProgress{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: unipv1alpha1.UpgradeNodeInProgressSpec{
			NodeRef: unipv1alpha1.NodeReference{
				Name: nodeName,
			},
		},
	}
	return k8sClient.Create(ctx, unip)
}

func getUpgradeNodeInProgressCR(ctx context.Context, k8sClient client.Client, name string) (*unipv1alpha1.UpgradeNodeInProgress, error) {
	unip := &unipv1alpha1.UpgradeNodeInProgress{}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, unip)
	if err != nil {
		return nil, err
	}
	return unip, nil
}

func deleteUpgradeNodeInProgressCR(ctx context.Context, k8sClient client.Client, name string) error {
	unip := &unipv1alpha1.UpgradeNodeInProgress{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	err := k8sClient.Delete(ctx, unip)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func upgradeNodeInProgressCRExists(ctx context.Context, k8sClient client.Client, name string) bool {
	unip := &unipv1alpha1.UpgradeNodeInProgress{}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, unip)
	return err == nil
}

// Helper functions for HealthSignal CR operations
func getHealthSignalCR(ctx context.Context, k8sClient client.Client, name string) (*unipv1alpha1.HealthSignal, error) {
	hs := &unipv1alpha1.HealthSignal{}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, hs)
	if err != nil {
		return nil, err
	}
	return hs, nil
}

func healthSignalCRExists(ctx context.Context, k8sClient client.Client, name string) bool {
	hs := &unipv1alpha1.HealthSignal{}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, hs)
	return err == nil
}

var _ = Describe("UpgradeNodeInProgress Controller", Ordered, ContinueOnFailure, func() {
	var (
		ctx          context.Context
		k8sClient    client.Client
		testNodeName string
	)

	BeforeAll(func() {
		ctx = context.Background()

		// Register CRD schemes
		err := chmv1alpha1.AddToScheme(scheme.Scheme)
		Expect(err).NotTo(HaveOccurred())
		err = unipv1alpha1.AddToScheme(scheme.Scheme)
		Expect(err).NotTo(HaveOccurred())

		// Create controller-runtime client for CRD operations
		restConfig, err := getKubeConfig()
		Expect(err).NotTo(HaveOccurred())

		k8sClient, err = client.New(restConfig, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())

		// Get a valid node name from the cluster
		By("Getting a valid node name from the cluster")
		nodeList, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(len(nodeList.Items)).To(BeNumerically(">", 0), "No nodes found in cluster")

		// Log all nodes
		GinkgoWriter.Printf("Found %d nodes in cluster:\n", len(nodeList.Items))
		for _, node := range nodeList.Items {
			GinkgoWriter.Printf("  - %s\n", node.Name)
		}

		// Get all CoreDNS pods and collect their node names
		corednsPods, err := clientset.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
			LabelSelector: "k8s-app=kube-dns",
		})
		Expect(err).NotTo(HaveOccurred())

		// Find the first node not running CoreDNS (to avoid interference)
		corednsNodeSet := make(map[string]struct{})
		for _, pod := range corednsPods.Items {
			if pod.Spec.NodeName != "" {
				corednsNodeSet[pod.Spec.NodeName] = struct{}{}
			}
		}

		testNodeName = ""
		for _, node := range nodeList.Items {
			if _, found := corednsNodeSet[node.Name]; !found {
				testNodeName = node.Name
				break
			}
		}
		Expect(testNodeName).NotTo(BeEmpty(), "No node found that does not run CoreDNS")
		GinkgoWriter.Printf("Using node %s for UpgradeNodeInProgress tests\n", testNodeName)
	})

	var (
		unipName string
	)

	AfterEach(func() {
		if unipName != "" {
			By("Cleaning up UpgradeNodeInProgress CR")
			err := deleteUpgradeNodeInProgressCR(ctx, k8sClient, unipName)
			if err != nil {
				GinkgoWriter.Printf("Warning: Failed to delete UpgradeNodeInProgress %s: %v\n", unipName, err)
			}

			// Wait for CR to be deleted (and cascading deletion of HealthSignal and CheckNodeHealth)
			Eventually(func() bool {
				return !upgradeNodeInProgressCRExists(ctx, k8sClient, unipName)
			}, "60s", "1s").Should(BeTrue(), "UpgradeNodeInProgress CR was not deleted within timeout")

			// Also verify HealthSignal is deleted (garbage collected)
			expectedHSName := strings.ToLower(fmt.Sprintf("%s-%s", unipName, upgradenodeinprogress.HealthSignalSource))
			Eventually(func() bool {
				return !healthSignalCRExists(ctx, k8sClient, expectedHSName)
			}, "30s", "1s").Should(BeTrue(), "HealthSignal CR was not garbage collected within timeout")

			unipName = ""
		}
	})

	It("should create HealthSignal and CheckNodeHealth, then sync status when completed", func() {
		By("Creating an UpgradeNodeInProgress CR")
		unipName = fmt.Sprintf("test-unip-%d", time.Now().Unix())
		err := createUpgradeNodeInProgressCR(ctx, k8sClient, unipName, testNodeName)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Created UpgradeNodeInProgress CR: %s for node: %s\n", unipName, testNodeName)

		expectedHSName := strings.ToLower(fmt.Sprintf("%s-%s", unipName, upgradenodeinprogress.HealthSignalSource))

		By("Verifying HealthSignal is created with correct owner reference")
		var hs *unipv1alpha1.HealthSignal
		Eventually(func() bool {
			hs, err = getHealthSignalCR(ctx, k8sClient, expectedHSName)
			return err == nil
		}, "30s", "2s").Should(BeTrue(), "HealthSignal was not created within timeout")

		// Verify HealthSignal spec
		Expect(hs.Spec.Source).To(Equal(upgradenodeinprogress.HealthSignalSource))
		Expect(hs.Spec.Type).To(Equal(unipv1alpha1.HealthSignalTypeNodeHealth))
		Expect(hs.Spec.Target.NodeName).To(Equal(testNodeName))

		// Verify owner reference
		Expect(hs.OwnerReferences).To(HaveLen(1))
		Expect(hs.OwnerReferences[0].Kind).To(Equal("UpgradeNodeInProgress"))
		Expect(hs.OwnerReferences[0].Name).To(Equal(unipName))

		By("Verifying CheckNodeHealth is created with correct owner reference to HealthSignal")
		var cnh *chmv1alpha1.CheckNodeHealth
		Eventually(func() bool {
			cnh, err = getCheckNodeHealthCR(ctx, k8sClient, expectedHSName)
			return err == nil
		}, "30s", "2s").Should(BeTrue(), "CheckNodeHealth was not created within timeout")

		// Verify CheckNodeHealth spec
		Expect(cnh.Spec.NodeRef.Name).To(Equal(testNodeName))

		// Verify owner reference to HealthSignal
		Expect(cnh.OwnerReferences).To(HaveLen(1))
		Expect(cnh.OwnerReferences[0].Kind).To(Equal("HealthSignal"))
		Expect(cnh.OwnerReferences[0].Name).To(Equal(expectedHSName))
		Expect(cnh.OwnerReferences[0].Controller).NotTo(BeNil())
		Expect(*cnh.OwnerReferences[0].Controller).To(BeTrue())

		By("Waiting for CheckNodeHealth to complete")
		Eventually(func() bool {
			cnh, err = getCheckNodeHealthCR(ctx, k8sClient, expectedHSName)
			if err != nil {
				return false
			}
			return cnh.Status.FinishedAt != nil
		}, "120s", "2s").Should(BeTrue(), "CheckNodeHealth did not complete within timeout")

		By("Verifying HealthSignal status is synced from CheckNodeHealth")
		Eventually(func() bool {
			hs, err = getHealthSignalCR(ctx, k8sClient, expectedHSName)
			if err != nil {
				return false
			}
			return hs.Status.FinishedAt != nil
		}, "30s", "2s").Should(BeTrue(), "HealthSignal status was not synced within timeout")

		// Verify timing information is synced
		Expect(hs.Status.StartedAt).NotTo(BeNil())
		Expect(hs.Status.FinishedAt).NotTo(BeNil())

		// Verify conditions are synced
		Expect(hs.Status.Condition).NotTo(BeEmpty())
		var healthyCondition *metav1.Condition
		for i := range hs.Status.Condition {
			if hs.Status.Condition[i].Type == "Healthy" {
				healthyCondition = &hs.Status.Condition[i]
				break
			}
		}
		Expect(healthyCondition).NotTo(BeNil(), "Healthy condition not found in HealthSignal")
		Expect(healthyCondition.Status).To(Equal(metav1.ConditionTrue), "Expected Healthy condition to be True")

		GinkgoWriter.Printf("HealthSignal %s completed with Healthy=%s\n", expectedHSName, healthyCondition.Status)

		By("Deleting the UpgradeNodeInProgress CR")
		err = deleteUpgradeNodeInProgressCR(ctx, k8sClient, unipName)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Deleted UpgradeNodeInProgress CR: %s\n", unipName)

		By("Verifying UpgradeNodeInProgress is deleted")
		Eventually(func() bool {
			return !upgradeNodeInProgressCRExists(ctx, k8sClient, unipName)
		}, "30s", "1s").Should(BeTrue(), "UpgradeNodeInProgress CR was not deleted within timeout")

		By("Verifying HealthSignal is garbage collected")
		Eventually(func() bool {
			return !healthSignalCRExists(ctx, k8sClient, expectedHSName)
		}, "30s", "1s").Should(BeTrue(), "HealthSignal CR was not garbage collected within timeout")
		GinkgoWriter.Printf("HealthSignal %s was garbage collected\n", expectedHSName)

		By("Verifying CheckNodeHealth is garbage collected")
		Eventually(func() bool {
			return !checkNodeHealthCRExists(ctx, k8sClient, expectedHSName)
		}, "30s", "1s").Should(BeTrue(), "CheckNodeHealth CR was not garbage collected within timeout")
		GinkgoWriter.Printf("CheckNodeHealth %s was garbage collected\n", expectedHSName)

		// Clear unipName so AfterEach doesn't try to delete again
		unipName = ""
	})
})
