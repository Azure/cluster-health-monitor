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

	It("should not create CheckNodeHealth CRs for nodes older than the threshold", func() {
		By("Waiting until all nodes are older than the NewNodeThreshold")
		Eventually(func() bool {
			nodeList, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			if err != nil {
				return false
			}
			for _, node := range nodeList.Items {
				if time.Since(node.CreationTimestamp.Time) < nodecontroller.NewNodeThreshold {
					GinkgoWriter.Printf("Node %s is still young (age: %s), waiting...\n",
						node.Name, time.Since(node.CreationTimestamp.Time))
					return false
				}
			}
			return true
		}, "10m", "30s").Should(BeTrue(), "Timed out waiting for all nodes to age past the threshold")

		By("Deleting any pre-existing boot-* CheckNodeHealth CRs")
		cnhList := &chmv1alpha1.CheckNodeHealthList{}
		err := k8sClient.List(ctx, cnhList)
		Expect(err).NotTo(HaveOccurred())
		for i := range cnhList.Items {
			cnh := &cnhList.Items[i]
			if len(cnh.Name) >= len("boot-") && cnh.Name[:len("boot-")] == "boot-" {
				GinkgoWriter.Printf("Cleaning up pre-existing boot CNH: %s\n", cnh.Name)
				err = deleteCheckNodeHealthCR(ctx, k8sClient, cnh.Name)
				Expect(err).NotTo(HaveOccurred())
			}
		}

		By("Waiting for pre-existing boot-* CRs to be fully deleted")
		Eventually(func() int {
			list := &chmv1alpha1.CheckNodeHealthList{}
			if err := k8sClient.List(ctx, list); err != nil {
				return -1
			}
			count := 0
			for _, cnh := range list.Items {
				if len(cnh.Name) >= len("boot-") && cnh.Name[:len("boot-")] == "boot-" {
					count++
				}
			}
			return count
		}, "30s", "2s").Should(Equal(0), "Pre-existing boot-* CRs should be deleted")

		By("Removing bootID annotations from all nodes so the controller treats them as first-seen")
		nodeList, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		for _, node := range nodeList.Items {
			if _, ok := node.Annotations[nodecontroller.AnnotationLastBootID]; ok {
				delete(node.Annotations, nodecontroller.AnnotationLastBootID)
				_, err = clientset.CoreV1().Nodes().Update(ctx, &node, metav1.UpdateOptions{})
				Expect(err).NotTo(HaveOccurred())
				GinkgoWriter.Printf("Removed bootID annotation from node %s\n", node.Name)
			}
		}

		By("Restarting the checknodehealth-controller to trigger re-sync")
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

		By("Waiting for the controller to process all nodes (annotations set)")
		Eventually(func() bool {
			nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			if err != nil {
				return false
			}
			for _, node := range nodes.Items {
				if _, ok := node.Annotations[nodecontroller.AnnotationLastBootID]; !ok {
					return false
				}
			}
			return true
		}, "60s", "2s").Should(BeTrue(), "Controller should set bootID annotations on all nodes")

		By("Verifying no boot-* CheckNodeHealth CRs were created for old nodes")
		cnhList = &chmv1alpha1.CheckNodeHealthList{}
		err = k8sClient.List(ctx, cnhList)
		Expect(err).NotTo(HaveOccurred())
		for _, cnh := range cnhList.Items {
			if len(cnh.Name) >= len("boot-") && cnh.Name[:len("boot-")] == "boot-" {
				Fail(fmt.Sprintf("Unexpected boot CR %s created for node %s which is older than the threshold",
					cnh.Name, cnh.Spec.NodeRef.Name))
			}
		}
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

		By("Snapshotting the current reboot-triggered CR count")
		cnhList := &chmv1alpha1.CheckNodeHealthList{}
		err = k8sClient.List(ctx, cnhList)
		Expect(err).NotTo(HaveOccurred())

		initialRebootCNHCount := 0
		for _, cnh := range cnhList.Items {
			if len(cnh.Name) >= len("boot-") && cnh.Name[:len("boot-")] == "boot-" {
				initialRebootCNHCount++
			}
		}
		GinkgoWriter.Printf("Initial reboot CNH count: %d\n", initialRebootCNHCount)

		By("Waiting to confirm no additional reboot-triggered CRs are created")
		time.Sleep(10 * time.Second)

		cnhList = &chmv1alpha1.CheckNodeHealthList{}
		err = k8sClient.List(ctx, cnhList)
		Expect(err).NotTo(HaveOccurred())

		finalRebootCNHCount := 0
		for _, cnh := range cnhList.Items {
			if len(cnh.Name) >= len("boot-") && cnh.Name[:len("boot-")] == "boot-" {
				finalRebootCNHCount++
			}
		}
		Expect(finalRebootCNHCount).To(Equal(initialRebootCNHCount),
			"No additional reboot-triggered CRs should be created when bootID hasn't changed")
	})
})
