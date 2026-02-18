package e2e

import (
	"context"
	"fmt"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	nodecontroller "github.com/Azure/cluster-health-monitor/pkg/controller/node"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("NodeReboot Controller", Ordered, ContinueOnFailure, Label("node-reboot"), func() {
	var (
		ctx       context.Context
		k8sClient client.Client
	)

	BeforeAll(func() {
		ctx = context.Background()

		err := chmv1alpha1.AddToScheme(scheme.Scheme)
		Expect(err).NotTo(HaveOccurred())

		restConfig, err := getKubeConfig()
		Expect(err).NotTo(HaveOccurred())

		k8sClient, err = client.New(restConfig, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
	})

	It("should initialize bootID annotations on all nodes", func() {
		By("Getting all nodes from the cluster")
		nodeList, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(nodeList.Items).NotTo(BeEmpty(), "No nodes found in cluster")

		By("Verifying each node has the last-boot-id annotation set")
		Eventually(func() bool {
			nodeList, err = clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			if err != nil {
				GinkgoWriter.Printf("Failed to list nodes: %v\n", err)
				return false
			}
			for _, node := range nodeList.Items {
				bootIDAnno, ok := node.Annotations[nodecontroller.AnnotationLastBootID]
				if !ok || bootIDAnno == "" {
					GinkgoWriter.Printf("Node %s missing bootID annotation\n", node.Name)
					return false
				}
			}
			return true
		}, "60s", "2s").Should(BeTrue(), "Not all nodes have the last-boot-id annotation")

		By("Verifying annotation matches the node's actual bootID")
		for _, node := range nodeList.Items {
			anno := node.Annotations[nodecontroller.AnnotationLastBootID]
			actualBootID := node.Status.NodeInfo.BootID
			GinkgoWriter.Printf("Node %s: annotation=%s, actual bootID=%s\n", node.Name, anno, actualBootID)
			Expect(anno).To(Equal(actualBootID),
				fmt.Sprintf("Node %s: annotation bootID does not match actual bootID", node.Name))
		}
	})

	It("should not create CheckNodeHealth CRs on startup when no reboot occurred", func() {
		By("Waiting a bit to ensure the controller has processed all nodes")
		time.Sleep(10 * time.Second)

		By("Listing all CheckNodeHealth CRs with reboot prefix")
		cnhList := &chmv1alpha1.CheckNodeHealthList{}
		err := k8sClient.List(ctx, cnhList)
		Expect(err).NotTo(HaveOccurred())

		rebootCNHCount := 0
		for _, cnh := range cnhList.Items {
			if len(cnh.Name) >= len("reboot-") && cnh.Name[:len("reboot-")] == "reboot-" {
				rebootCNHCount++
				GinkgoWriter.Printf("Found reboot-triggered CNH: %s (node: %s)\n", cnh.Name, cnh.Spec.NodeRef.Name)
			}
		}
		Expect(rebootCNHCount).To(Equal(0),
			"No reboot-triggered CheckNodeHealth CRs should exist on clean startup")
	})

	It("should create CheckNodeHealth CR when bootID annotation is stale", func() {
		By("Getting the first node")
		nodeList, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(nodeList.Items).NotTo(BeEmpty())
		node := nodeList.Items[0]

		actualBootID := node.Status.NodeInfo.BootID
		Expect(actualBootID).NotTo(BeEmpty(), "Node should have a bootID")

		By(fmt.Sprintf("Setting a stale bootID annotation on node %s", node.Name))
		staleBootID := "stale-boot-id-for-e2e-test"
		node.Annotations[nodecontroller.AnnotationLastBootID] = staleBootID
		_, err = clientset.CoreV1().Nodes().Update(ctx, &node, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Set stale bootID %q on node %s (actual: %s)\n", staleBootID, node.Name, actualBootID)

		By("Restarting the checknodehealth-controller to trigger re-sync with stale annotation")
		// The predicate only fires on bootID status changes, so we need the controller
		// to re-discover the node (via CreateFunc on cache re-sync) to detect the
		// annotation vs actual bootID mismatch.
		err = clientset.CoreV1().Pods(checkerNamespace).DeleteCollection(ctx,
			metav1.DeleteOptions{},
			metav1.ListOptions{LabelSelector: "app=checknodehealth-controller"},
		)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Println("Deleted checknodehealth-controller pod to trigger re-sync")

		By("Waiting for the controller pod to come back")
		Eventually(func() bool {
			pods, err := clientset.CoreV1().Pods(checkerNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: "app=checknodehealth-controller",
			})
			if err != nil || len(pods.Items) == 0 {
				return false
			}
			for _, pod := range pods.Items {
				if pod.Status.Phase == "Running" && pod.DeletionTimestamp == nil {
					return true
				}
			}
			return false
		}, "60s", "2s").Should(BeTrue(), "Controller pod did not restart within timeout")

		By("Waiting for the controller to detect the 'reboot' and create a CheckNodeHealth CR")
		expectedCNHName := nodecontroller.GenerateCNHName(node.Name, actualBootID)
		GinkgoWriter.Printf("Expecting CheckNodeHealth CR: %s\n", expectedCNHName)

		var cnh *chmv1alpha1.CheckNodeHealth
		Eventually(func() error {
			cnh = &chmv1alpha1.CheckNodeHealth{}
			return k8sClient.Get(ctx, client.ObjectKey{Name: expectedCNHName}, cnh)
		}, "60s", "2s").Should(Succeed(), "CheckNodeHealth CR was not created for rebooted node")

		By("Verifying the CR references the correct node")
		Expect(cnh.Spec.NodeRef.Name).To(Equal(node.Name))

		By("Verifying the bootID annotation was updated to the current bootID")
		Eventually(func() string {
			updatedNode, err := clientset.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return ""
			}
			return updatedNode.Annotations[nodecontroller.AnnotationLastBootID]
		}, "30s", "2s").Should(Equal(actualBootID),
			"bootID annotation should be updated to current bootID after detecting reboot")

		By("Waiting for the CheckNodeHealth to complete (health check runs)")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, client.ObjectKey{Name: expectedCNHName}, cnh)
			if err != nil {
				return false
			}
			return cnh.Status.FinishedAt != nil
		}, "120s", "2s").Should(BeTrue(), "CheckNodeHealth did not complete within timeout")

		GinkgoWriter.Printf("CheckNodeHealth %s completed with condition: %v\n",
			expectedCNHName, cnh.Status.Conditions)

		By("Cleaning up the reboot-triggered CheckNodeHealth CR")
		err = deleteCheckNodeHealthCR(ctx, k8sClient, expectedCNHName)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			return !checkNodeHealthCRExists(ctx, k8sClient, expectedCNHName)
		}, "30s", "1s").Should(BeTrue(), "CheckNodeHealth CR was not deleted within timeout")
	})

	It("should not create duplicate CheckNodeHealth CRs for the same bootID", func() {
		By("Getting the first node")
		nodeList, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(nodeList.Items).NotTo(BeEmpty())
		node := nodeList.Items[0]

		By("Verifying the annotation is set correctly (no stale value)")
		Eventually(func() bool {
			updatedNode, err := clientset.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return false
			}
			return updatedNode.Annotations[nodecontroller.AnnotationLastBootID] == updatedNode.Status.NodeInfo.BootID
		}, "30s", "2s").Should(BeTrue(), "bootID annotation should match actual bootID")

		By("Waiting to confirm no reboot-triggered CRs are created")
		time.Sleep(10 * time.Second)

		cnhList := &chmv1alpha1.CheckNodeHealthList{}
		err = k8sClient.List(ctx, cnhList)
		Expect(err).NotTo(HaveOccurred())

		rebootCNHCount := 0
		for _, cnh := range cnhList.Items {
			if len(cnh.Name) >= len("reboot-") && cnh.Name[:len("reboot-")] == "reboot-" {
				rebootCNHCount++
			}
		}
		Expect(rebootCNHCount).To(Equal(0),
			"No reboot-triggered CRs should be created when bootID hasn't changed")
	})
})
