package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

const (
	checkerTypePodStartup               = "podStartup"
	PodStartupDurationExceededErrorCode = "pod_startup_duration_exceeded"

	kubernetesAzureClusterLabel = "kubernetes.azure.com/cluster"
)

var (
	// Note that podStartupCheckerName must match with the configmap in manifests/overlays/test.
	podStartupCheckerNames = []string{"test-pod-startup-checker"}
)

var _ = Describe("Pod startup checker", Ordered, func() {
	var (
		session   *gexec.Session
		localPort int
	)

	BeforeEach(func() {
		addLabelsToAllNodes(clientset, map[string]string{
			kubernetesAzureClusterLabel: "",
			"kubernetes.azure.com/mode": "system",
		})
		session, localPort = setupMetricsPortforwarding(clientset)
	})

	AfterEach(func() {
		safeSessionKill(session)
	})

	It("should report healthy status for pod startup checker", func() {
		By("Waiting for pod startup checker metrics to report healthy status")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, checkerResultMetricName, podStartupCheckerNames, checkerTypePodStartup, metricsHealthyStatus, metricsHealthyErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected pod startup checkers to be healthy: %v, found: %v\n", podStartupCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found healthy pod startup checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Pod startup checker metrics did not report healthy status within the timeout period")
	})

	It("should report unhealthy status when pods cannot be scheduled", func() {
		By("Removing required label from all nodes to prevent pod scheduling")

		removeLabelsFromAllNodes(clientset, map[string]string{kubernetesAzureClusterLabel: ""})
		defer func() {
			By("Removing the required label from all nodes")
			addLabelsToAllNodes(clientset, map[string]string{kubernetesAzureClusterLabel: ""})
		}()

		By("Waiting for pod startup checker to report unhealthy status")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, checkerResultMetricName, podStartupCheckerNames, checkerTypePodStartup, metricsUnhealthyStatus, PodStartupDurationExceededErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected pod startup checkers to be unhealthy and pods not ready: %v, found: %v\n", podStartupCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found unhealthy and pods not ready pod startup checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Pod startup checker did not report unhealthy status within the timeout period")

		By("Adding required label to all nodes")
		addLabelsToAllNodes(clientset, map[string]string{kubernetesAzureClusterLabel: ""})

		By("Waiting for pod startup checker to report healthy status after adding label back")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, checkerResultMetricName, podStartupCheckerNames, checkerTypePodStartup, metricsHealthyStatus, metricsHealthyErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected pod startup checkers to be healthy: %v, found: %v\n", podStartupCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found healthy pod startup checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Pod startup checker did not return to healthy status after adding back label within the timeout period")
	})
})
