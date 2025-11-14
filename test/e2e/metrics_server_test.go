package e2e

import (
	"github.com/Azure/cluster-health-monitor/pkg/checker/metricsserver"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

const (
	checkerTypeMetricsServer = string(config.CheckTypeMetricsServer)

	metricsServerUnavailableErrorCode = metricsserver.ErrCodeMetricsServerUnavailable
)

var (
	// Note that metricsServerCheckerNames must match with the configmap in manifests/overlays/test.
	metricsServerCheckerNames = []string{"TestMetricsServer"}
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
		time0Metrics, err := getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			timeNMetrics, err := getMetrics(localPort)
			Expect(err).NotTo(HaveOccurred())

			allIncreased, increasedCheckers, err := verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics,
				checkerResultMetricName, metricsServerCheckerNames, checkerTypeMetricsServer, metricsHealthyStatus, metricsHealthyErrorCode,
			)
			Expect(err).NotTo(HaveOccurred())

			if !allIncreased {
				GinkgoWriter.Printf("Expected increase in healthy results for metrics server checkers: %v, Actual: %v\n", metricsServerCheckerNames, increasedCheckers)
				return false
			}
			GinkgoWriter.Printf("Found increase in healthy results for metrics server checkers %v\n", increasedCheckers)
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
		time0Metrics, err := getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			timeNMetrics, err := getMetrics(localPort)
			Expect(err).NotTo(HaveOccurred())

			allIncreased, increasedCheckers, err := verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics,
				checkerResultMetricName, metricsServerCheckerNames, checkerTypeMetricsServer, metricsUnhealthyStatus, metricsServerUnavailableErrorCode,
			)
			Expect(err).NotTo(HaveOccurred())

			if !allIncreased {
				GinkgoWriter.Printf("Expected increase in unhealthy results for metrics server checkers: %v, Actual: %v\n", metricsServerCheckerNames, increasedCheckers)
				return false
			}
			GinkgoWriter.Printf("Found increase in unhealthy results for metrics server checkers %v\n", increasedCheckers)
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
		time0Metrics, err = getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			timeNMetrics, err := getMetrics(localPort)
			Expect(err).NotTo(HaveOccurred())

			allIncreased, increasedCheckers, err := verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics,
				checkerResultMetricName, metricsServerCheckerNames, checkerTypeMetricsServer, metricsHealthyStatus, metricsHealthyErrorCode,
			)
			Expect(err).NotTo(HaveOccurred())

			if !allIncreased {
				GinkgoWriter.Printf("Expected increase in healthy results for metrics server checkers after restoration: %v, Actual: %v\n", metricsServerCheckerNames, increasedCheckers)
				return false
			}
			GinkgoWriter.Printf("Found increase in healthy results for metrics server checkers %v after restoration\n", increasedCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Metrics server checker did not report healthy status after restoration within the timeout period")
	})
})
