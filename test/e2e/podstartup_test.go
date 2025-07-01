package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	corev1 "k8s.io/api/core/v1"
)

const (
	checkerTypePodStartup                 = "podStartup"
	podStartupPodCreationTimeoutErrorCode = "pod_creation_timeout"
)

var (
	// Note that podStartupCheckerName must match with the configmap in manifests/overlays/test.
	podStartupCheckerNames = []string{"pod-startup-checker"}
)

var _ = Describe("Pod startup checker", Ordered, ContinueOnFailure, func() {
	var (
		session   *gexec.Session
		localPort int
	)

	BeforeEach(func() {
		addLabelsToAllNodes(clientset, map[string]string{
			"kubernetes.azure.com/cluster": "",
			"kubernetes.azure.com/mode":    "system",
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
		By("Adding a taint to all nodes to prevent pod scheduling")

		noScheduleTaintKey := "chm-test-no-schedule"
		noScheduleTaint := corev1.Taint{
			Key:    noScheduleTaintKey,
			Effect: corev1.TaintEffectNoSchedule,
		}
		taintAllNodes(clientset, []corev1.Taint{noScheduleTaint})
		defer func() {
			By("Removing the taint from all nodes")
			removeTaintsFromAllNodes(clientset, []string{noScheduleTaintKey})
		}()

		By("Waiting for pod startup checker to report unhealthy status")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, checkerResultMetricName, podStartupCheckerNames, checkerTypePodStartup, metricsUnhealthyStatus, podStartupPodCreationTimeoutErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected pod startup checkers to be unhealthy and pods not ready: %v, found: %v\n", podStartupCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found unhealthy and pods not ready pod startup checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Pod startup checker did not report unhealthy status within the timeout period")

		By("Removing the taint from all nodes")
		removeTaintsFromAllNodes(clientset, []string{noScheduleTaintKey})

		By("Waiting for pod startup checker to report healthy status after removing taint")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, checkerResultMetricName, podStartupCheckerNames, checkerTypePodStartup, metricsHealthyStatus, metricsHealthyErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected pod startup checkers to be healthy: %v, found: %v\n", podStartupCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found healthy pod startup checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Pod startup checker did not return to healthy status after taint removal within the timeout period")
	})
})
