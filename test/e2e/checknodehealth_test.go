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

		// Log all nodes
		GinkgoWriter.Printf("Found %d nodes in cluster:\n", len(nodeList.Items))
		for _, node := range nodeList.Items {
			GinkgoWriter.Printf("  - %s\n", node.Name)
		}

		// Wait for CoreDNS deployment to stabilize at its target replica count.
		// During rolling updates, extra pods may temporarily run on all nodes,
		// which would prevent us from finding a node without CoreDNS.
		By("Waiting for CoreDNS deployment to stabilize")
		Eventually(func() bool {
			deploy, err := clientset.AppsV1().Deployments("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
			if err != nil {
				return false
			}
			return deploy.Status.ReadyReplicas == *deploy.Spec.Replicas &&
				deploy.Status.UpdatedReplicas == *deploy.Spec.Replicas &&
				deploy.Status.AvailableReplicas == *deploy.Spec.Replicas
		}, "120s", "2s").Should(BeTrue(), "CoreDNS deployment did not stabilize")

		// Get all CoreDNS pods and collect their node names
		corednsPods, err := clientset.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
			LabelSelector: "k8s-app=kube-dns",
			FieldSelector: "status.phase=Running",
		})
		Expect(err).NotTo(HaveOccurred())

		// Log CoreDNS pod distribution
		GinkgoWriter.Printf("Found %d CoreDNS pods:\n", len(corednsPods.Items))
		corednsNodeSet := make(map[string]struct{})
		for _, pod := range corednsPods.Items {
			GinkgoWriter.Printf("  - %s on node %s (phase: %s, deleting: %v)\n", pod.Name, pod.Spec.NodeName, pod.Status.Phase, pod.DeletionTimestamp != nil)
			// Only count non-terminating pods to avoid counting pods from a rolling update
			if pod.Spec.NodeName != "" && pod.DeletionTimestamp == nil {
				corednsNodeSet[pod.Spec.NodeName] = struct{}{}
			}
		}

		// Find the first node not running CoreDNS
		testNodeName = ""
		for _, node := range nodeList.Items {
			if _, found := corednsNodeSet[node.Name]; !found {
				testNodeName = node.Name
				break
			}
		}
		Expect(testNodeName).NotTo(BeEmpty(), "No node found that does not run CoreDNS. Nodes with CoreDNS: %v", corednsNodeSet)
		GinkgoWriter.Printf("Using node %s for tests\n", testNodeName)
	})

	var (
		cnhName string
	)

	AfterEach(func() {
		if cnhName != "" {
			By("Cleaning up CheckNodeHealth CR")
			err := deleteCheckNodeHealthCR(ctx, k8sClient, cnhName)
			if err != nil {
				GinkgoWriter.Printf("Warning: Failed to delete CheckNodeHealth %s: %v\n", cnhName, err)
			}

			// Wait for CR to be deleted
			Eventually(func() bool {
				return !checkNodeHealthCRExists(ctx, k8sClient, cnhName)
			}, "30s", "1s").Should(BeTrue(), "CheckNodeHealth CR was not deleted within timeout")

			cnhName = ""
		}
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

		var foundPodStartup, foundPodNetwork bool
		for _, result := range cnh.Status.Results {
			if result.Name == "PodStartup" {
				foundPodStartup = true
				Expect(result.Status).To(Equal(chmv1alpha1.CheckStatusHealthy), "PodStartup should have Healthy status")
			}
			if result.Name == "PodNetwork" {
				foundPodNetwork = true
				Expect(result.Status).To(Equal(chmv1alpha1.CheckStatusHealthy), "PodNetwork should have Healthy status")
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

		By("Verifying NodeHealthy condition is set to True on the node")
		Eventually(func() *corev1.NodeCondition {
			node := &corev1.Node{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Name: testNodeName}, node); err != nil {
				return nil
			}
			for i, c := range node.Status.Conditions {
				if c.Type == checknodehealth.NodeConditionNodeHealthy {
					return &node.Status.Conditions[i]
				}
			}
			return nil
		}, "30s", "2s").ShouldNot(BeNil(), "Expected NodeHealthy condition on node, but not found")

		node := &corev1.Node{}
		err = k8sClient.Get(ctx, client.ObjectKey{Name: testNodeName}, node)
		Expect(err).NotTo(HaveOccurred())
		var nodeCondition *corev1.NodeCondition
		for i, c := range node.Status.Conditions {
			if c.Type == checknodehealth.NodeConditionNodeHealthy {
				nodeCondition = &node.Status.Conditions[i]
				break
			}
		}
		Expect(nodeCondition).NotTo(BeNil())
		Expect(nodeCondition.Status).To(Equal(corev1.ConditionTrue),
			"NodeHealthy condition should be True when health check passes")
		GinkgoWriter.Printf("NodeHealthy condition on %s: status=%s, reason=%s, message=%s\n",
			testNodeName, nodeCondition.Status, nodeCondition.Reason, nodeCondition.Message)
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
		}, "60s", "2s").Should(Equal(0), "Timed out pod was not cleaned up within timeout")
	})

	It("should set NodeHealthy condition on node when health check fails", func() {
		By("Creating a fake Node object for the test")
		fakeNodeName := fmt.Sprintf("fake-node-condition-test-%d", time.Now().Unix())
		// Use a finalizer to prevent the cloud-node-lifecycle-controller from deleting this fake node before the checknodehealth controller
		// can set the NodeHealthy condition on it. Otherwise the following may occur:
		// 15m Normal DeletingNode Node/fake-node-condition-test-1775085210 Deleting node fake-node-condition-test-1775085210 because it does not exist in the cloud provider
		fakeNodeFinalizer := "e2e-test/fake-node-protection"
		fakeNode := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:       fakeNodeName,
				Finalizers: []string{fakeNodeFinalizer},
			},
		}
		err := k8sClient.Create(ctx, fakeNode)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Created fake Node: %s\n", fakeNodeName)

		defer func() {
			By("Cleaning up fake Node")
			node := &corev1.Node{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Name: fakeNodeName}, node); err != nil {
				GinkgoWriter.Printf("Warning: Failed to get fake Node %s for cleanup: %v\n", fakeNodeName, err)
				return
			}
			// Remove finalizer so the node can be deleted
			node.Finalizers = nil
			if err := k8sClient.Update(ctx, node); err != nil {
				GinkgoWriter.Printf("Warning: Failed to remove finalizer from %s: %v\n", fakeNodeName, err)
			}
			if err := k8sClient.Delete(ctx, node); client.IgnoreNotFound(err) != nil {
				GinkgoWriter.Printf("Warning: Failed to delete fake Node %s: %v\n", fakeNodeName, err)
			}
		}()

		By("Creating a CheckNodeHealth CR targeting the fake node to trigger timeout")
		cnhName = fmt.Sprintf("test-cnh-nodecond-%d", time.Now().Unix())
		err = createCheckNodeHealthCR(ctx, k8sClient, cnhName, fakeNodeName)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Created CheckNodeHealth CR: %s for fake node: %s\n", cnhName, fakeNodeName)

		By("Waiting for pod timeout and FinishedAt to be set")
		var cnh *chmv1alpha1.CheckNodeHealth
		Eventually(func() bool {
			cnh, err = getCheckNodeHealthCR(ctx, k8sClient, cnhName)
			if err != nil {
				return false
			}
			return cnh.Status.FinishedAt != nil
		}, "60s", "5s").Should(BeTrue(), "Pod timeout was not detected within 1 minute")

		By("Verifying Healthy condition is False")
		Expect(cnh.Status.Conditions).To(HaveLen(1))
		Expect(cnh.Status.Conditions[0].Type).To(Equal("Healthy"))
		Expect(cnh.Status.Conditions[0].Status).To(Equal(metav1.ConditionFalse))

		By("Verifying NodeHealthy condition is set on the Node")
		var nodeCondition *corev1.NodeCondition
		Eventually(func() *corev1.NodeCondition {
			node := &corev1.Node{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Name: fakeNodeName}, node); err != nil {
				return nil
			}
			for i, c := range node.Status.Conditions {
				if c.Type == checknodehealth.NodeConditionNodeHealthy {
					return &node.Status.Conditions[i]
				}
			}
			return nil
		}, "30s", "2s").ShouldNot(BeNil(), "Expected NodeHealthy condition on node, but not found")

		// Re-fetch to verify details
		node := &corev1.Node{}
		err = k8sClient.Get(ctx, client.ObjectKey{Name: fakeNodeName}, node)
		Expect(err).NotTo(HaveOccurred())
		for i, c := range node.Status.Conditions {
			if c.Type == checknodehealth.NodeConditionNodeHealthy {
				nodeCondition = &node.Status.Conditions[i]
				break
			}
		}
		Expect(nodeCondition.Status).To(Equal(corev1.ConditionFalse))
		Expect(nodeCondition.Reason).NotTo(BeEmpty())
		GinkgoWriter.Printf("NodeHealthy condition on %s: status=%s, reason=%s, message=%s\n",
			fakeNodeName, nodeCondition.Status, nodeCondition.Reason, nodeCondition.Message)
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

		By("Deleting the CheckNodeHealth CR before pod timeout triggers")
		err = deleteCheckNodeHealthCR(ctx, k8sClient, cnhName)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying the health check pod is deleted or terminating due to CR deletion")
		Eventually(func() bool {
			podList, err := clientset.CoreV1().Pods(checkerNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("%s=%s", checknodehealth.CheckNodeHealthLabel, cnhName),
			})
			if err != nil {
				return false
			}
			// Pod is considered cleaned up if it's deleted or all pods are in Terminating state
			if len(podList.Items) == 0 {
				return true
			}
			// Check if all pods are in Terminating state (DeletionTimestamp is set)
			for _, pod := range podList.Items {
				if pod.DeletionTimestamp == nil {
					return false
				}
			}
			return true
		}, "30s", "2s").Should(BeTrue(), "Health check pod was not deleted or terminating within timeout")
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

	It("should set condition to Unknown when checker pod fails without writing results", func() {
		By("Creating a CheckNodeHealth CR with default service account (no permissions)")
		cnhName = fmt.Sprintf("test-cnh-no-perms-%d", time.Now().Unix())
		cnh := &chmv1alpha1.CheckNodeHealth{
			ObjectMeta: metav1.ObjectMeta{
				Name: cnhName,
				Annotations: map[string]string{
					checknodehealth.AnnotationCheckerServiceAccount: "default",
				},
			},
			Spec: chmv1alpha1.CheckNodeHealthSpec{
				NodeRef: chmv1alpha1.NodeReference{
					Name: testNodeName,
				},
			},
		}
		err := k8sClient.Create(ctx, cnh)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Created CheckNodeHealth CR: %s with default service account\n", cnhName)

		By("Waiting for FinishedAt timestamp to be set indicating controller finished processing")
		var updatedCnh *chmv1alpha1.CheckNodeHealth
		Eventually(func() bool {
			updatedCnh, err = getCheckNodeHealthCR(ctx, k8sClient, cnhName)
			if err != nil {
				return false
			}
			return updatedCnh.Status.FinishedAt != nil
		}, "60s", "2s").Should(BeTrue(), "FinishedAt timestamp was not set within timeout")

		By("Verifying that condition is set to Unknown when checker fails without writing results")
		Expect(updatedCnh.Status.Conditions).To(HaveLen(1))
		Expect(updatedCnh.Status.Conditions[0].Type).To(Equal("Healthy"))
		Expect(updatedCnh.Status.Conditions[0].Status).To(Equal(metav1.ConditionUnknown))

		By("Verifying PodNetwork results aren't recorded")
		var hasPodNetwork bool
		for _, result := range updatedCnh.Status.Results {
			if result.Name == "PodNetwork" {
				hasPodNetwork = true
				break
			}
		}
		Expect(hasPodNetwork).To(BeFalse(), "PodNetwork result should not exist when checker fails")

		By("Verifying PodStartup result is recorded as Healthy")
		var podStartupResult *chmv1alpha1.CheckResult
		for i := range updatedCnh.Status.Results {
			if updatedCnh.Status.Results[i].Name == "PodStartup" {
				podStartupResult = &updatedCnh.Status.Results[i]
				break
			}
		}
		Expect(podStartupResult).NotTo(BeNil(), "PodStartup result should be present")
		Expect(podStartupResult.Status).To(Equal(chmv1alpha1.CheckStatusHealthy), "PodStartup should be Healthy even if container fails after starting")
	})
})
