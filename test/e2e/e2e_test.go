// Package e2e contains end-to-end tests for the cluster health monitor.
package e2e

import (
	"os"
	"os/exec"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes"
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var (
	clientset          *kubernetes.Clientset
	skipClusterSetup   = os.Getenv("E2E_SKIP_CLUSTER_SETUP") == "true"
	skipAllCleanup     = os.Getenv("E2E_SKIP_ALL_CLEANUP") == "true"
	skipClusterCleanup = os.Getenv("E2E_SKIP_CLUSTER_CLEANUP") == "true"
)

// globalSetup runs once before all test processes.
func globalSetup() []byte {
	if !skipClusterSetup {
		By("Setting up a Kind cluster for E2E")
		cmd := exec.Command("make", "kind-test-local")
		output, err := run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to setup Kind cluster for E2E: %s", string(output))
		GinkgoWriter.Println(string(output))
	}

	// Initialize Kubernetes client.
	clientset, err := getKubeClient()
	Expect(err).NotTo(HaveOccurred())

	By("Waiting for CoreDNS pods to be running")
	Eventually(func() bool {
		podList, err := getCoreDNSPodList(clientset)
		if err != nil {
			return false
		}
		for _, pod := range podList.Items {
			if pod.Status.Phase != "Running" || pod.Status.PodIP == "" {
				return false
			}
		}
		return len(podList.Items) > 0
	}, "180s", "2s").Should(BeTrue(), "CoreDNS pods are not running")

	By("Waiting for cluster health monitor deployment to become ready")
	Eventually(func() bool {
		deployment, err := getClusterHealthMonitorDeployment(clientset)
		if err != nil {
			return false
		}
		return deployment.Status.ReadyReplicas == *deployment.Spec.Replicas
	}, "90s", "2s").Should(BeTrue())

	By("Listing all pods in all namespaces")
	cmd := exec.Command("kubectl", "get", "po", "-A")
	output, err := run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to list pods: %s", string(output))
	GinkgoWriter.Println(string(output))

	return nil
}

// perProcessSetup runs once per test process.
func perProcessSetup(_ []byte) {
	var err error
	clientset, err = getKubeClient()
	Expect(err).NotTo(HaveOccurred())
}

// globalTeardown runs once after all test processes have finished.
func globalTeardown() {
	if skipAllCleanup {
		GinkgoWriter.Println("Skipping all cleanup as E2E_SKIP_ALL_CLEANUP is set to true")
		return
	}

	if skipClusterCleanup {
		By("Deleting the test deployment")
		cmd := exec.Command("make", "kind-delete-deployment")
		output, err := run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete deployment: %s", string(output))
		GinkgoWriter.Println(string(output))
		return
	}

	By("Deleting the Kind cluster")
	cmd := exec.Command("make", "kind-delete-cluster")
	output, err := run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to delete Kind cluster: %s", string(output))
	GinkgoWriter.Println(string(output))
}

var _ = SynchronizedBeforeSuite(globalSetup, perProcessSetup)
var _ = SynchronizedAfterSuite(func() {}, globalTeardown)
