package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

const (
	checkerTypePodStartup               = "podStartup"
	podStartupDurationExceededErrorCode = "pod_startup_duration_exceeded"
)

var (
	// Note that podStartupCheckerName must match with the configmap in manifests/overlays/test.
	podStartupCheckerNames = []string{"test-pod-startup"}

	// These labels are required on nodes for the synthetic pods created by the pod startup checker to meet node affinity requirements
	// and be scheduled. These are specified in the synthetic pod spec used by the podstartup checker.
	requiredNodeLabelsForSchedulingSyntheticPods = map[string]string{
		"kubernetes.azure.com/cluster": "",
		"kubernetes.azure.com/mode":    "system",
	}
)

var _ = Describe("Pod startup checker", Ordered, ContinueOnFailure, func() {
	var (
		session   *gexec.Session
		localPort int
	)

	BeforeEach(func() {
		addLabelsToAllNodes(clientset, requiredNodeLabelsForSchedulingSyntheticPods)
		session, localPort = setupMetricsPortforwarding(clientset)
	})

	AfterEach(func() {
		safeSessionKill(session)
	})

	It("should report healthy status for pod startup checker", func() {
		By("Waiting for pod startup checker metrics to report healthy status")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, podStartupCheckerNames, checkerTypePodStartup, metricsHealthyStatus, metricsHealthyErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected pod startup checkers to be healthy: %v, found: %v\n", podStartupCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found healthy pod startup checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Pod startup checker metrics did not report healthy status within the timeout period")
	})

	It("should report unhealthy status when pods cannot be scheduled", func() {
		By("Removing required labels from all nodes to prevent pod scheduling")

		removeLabelsFromAllNodes(clientset, requiredNodeLabelsForSchedulingSyntheticPods)
		DeferCleanup(func() {
			By("Adding required labels back to all nodes")
			addLabelsToAllNodes(clientset, requiredNodeLabelsForSchedulingSyntheticPods)
		})

		By("Waiting for pod startup checker to report unhealthy status")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, podStartupCheckerNames, checkerTypePodStartup, metricsUnhealthyStatus, podStartupDurationExceededErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected pod startup checkers to be unhealthy and pod startup duration exceeded: %v, found: %v\n", podStartupCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found unhealthy and pod startup duration exceeded pod startup checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Pod startup checker did not report unhealthy status within the timeout period")

		By("Adding required labels back to all nodes")
		addLabelsToAllNodes(clientset, requiredNodeLabelsForSchedulingSyntheticPods)

		By("Waiting for pod startup checker to report healthy status after adding label back")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, podStartupCheckerNames, checkerTypePodStartup, metricsHealthyStatus, metricsHealthyErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected pod startup checkers to be healthy: %v, found: %v\n", podStartupCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found healthy pod startup checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Pod startup checker did not return to healthy status after adding back label within the timeout period")
	})
})
