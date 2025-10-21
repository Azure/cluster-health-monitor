// Package e2e contains end-to-end tests for the cluster health monitor.
package e2e

import (
	"github.com/Azure/cluster-health-monitor/pkg/metrics"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

const (
	metricsHealthyStatus    = metrics.HealthyStatus
	metricsHealthyErrorCode = metricsHealthyStatus
	metricsUnhealthyStatus  = metrics.UnhealthyStatus
)

var _ = Describe("Metrics endpoint", func() {
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

	It("should provide metrics data", func() {
		By("Waiting for metrics endpoint to contain data")
		Eventually(func() bool {
			metricsData, err := getMetrics(localPort)
			if err != nil {
				GinkgoWriter.Printf("Error getting metrics: %v\n", err)
				return false
			}
			return len(metricsData) > 0
		}, "30s", "10s").Should(BeTrue(), "No metrics data found within timeout period")

		By("Verifying metrics endpoint contains expected metrics")
		metricsData, err := getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred(), "Failed to get metrics")
		Expect(metricsData).To(HaveKey(checkerResultMetricName), "Expected %s metric not found", checkerResultMetricName)
		Expect(metricsData).To(HaveKey(podHealthResultMetricName), "Expected %s metric not found", podHealthResultMetricName)
	})
})
