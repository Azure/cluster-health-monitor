package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

const (
	checkerTypeMetricsServer          = "metricsServer"
	metricsServerUnavailableErrorCode = "metrics_server_unavailable"
	metricsServerTimeoutErrorCode     = "metrics_server_timeout"
	metricsServerNamespace            = "kube-system"
	metricsServerDeploymentName       = "metrics-server"
)

var (
	// Note that metricsServerCheckerNames must match with the configmap in manifests/overlays/test.
	metricsServerCheckerNames = []string{"test-metrics-server"}
)

var _ = Describe("Metrics server checker", Ordered, ContinueOnFailure, func() {
	var (
		session   *gexec.Session
		localPort int
	)

	BeforeEach(func() {
		session, localPort = setupMetricsPortforwarding(clientset)
	})

	AfterEach(func() {
		safeSessionKill(session)
	})

	It("should report healthy status for metrics server checker", func() {
		By("Waiting for metrics server checker metrics to report healthy status")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, metricsServerCheckerNames, checkerTypeMetricsServer, metricsHealthyStatus, metricsHealthyErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected metrics server checkers to be healthy: %v, found: %v\n", metricsServerCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found healthy metrics server checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Metrics server checker metrics did not report healthy status within the timeout period")
	})

	It("should report unhealthy status when metrics server deployment is scaled down", func() {
		By("Getting the metrics server deployment")
		deployment, err := getMetricsServerDeployment(clientset)
		Expect(err).NotTo(HaveOccurred(), "Failed to get metrics server deployment")
		originalReplicas := *deployment.Spec.Replicas

		By("Scaling down metrics server deployment to 0 replicas to simulate unhealthy state")
		err = updateMetricsServerDeploymentReplicas(clientset, 0)
		Expect(err).NotTo(HaveOccurred(), "Failed to scale down metrics server deployment")

		By("Waiting for metrics server deployment to be scaled down")
		Eventually(func() bool {
			deployment, err := getMetricsServerDeployment(clientset)
			if err != nil {
				return false
			}
			return deployment.Status.ReadyReplicas == 0
		}, "60s", "5s").Should(BeTrue(), "Metrics server deployment was not scaled down within the timeout period")

		By("Waiting for metrics server checker to report unhealthy status")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, metricsServerCheckerNames, checkerTypeMetricsServer, metricsUnhealthyStatus, metricsServerUnavailableErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected metrics server checkers to be unhealthy due to deployment scaling: %v, found: %v\n", metricsServerCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found unhealthy metrics server checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Metrics server checker did not report unhealthy status within the timeout period")

		By("Restoring metrics server deployment to original replica count")
		err = updateMetricsServerDeploymentReplicas(clientset, originalReplicas)
		Expect(err).NotTo(HaveOccurred(), "Failed to restore metrics server deployment")

		By("Waiting for metrics server deployment to become ready again")
		Eventually(func() bool {
			deployment, err := getMetricsServerDeployment(clientset)
			if err != nil {
				return false
			}
			return deployment.Status.ReadyReplicas == *deployment.Spec.Replicas
		}, "120s", "5s").Should(BeTrue(), "Metrics server deployment did not become ready within the timeout period")

		By("Waiting for metrics server checker to report healthy status again")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, metricsServerCheckerNames, checkerTypeMetricsServer, metricsHealthyStatus, metricsHealthyErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected metrics server checkers to be healthy after restoration: %v, found: %v\n", metricsServerCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found healthy metrics server checker metric for %v after restoration\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Metrics server checker did not report healthy status after restoration within the timeout period")
	})
})
