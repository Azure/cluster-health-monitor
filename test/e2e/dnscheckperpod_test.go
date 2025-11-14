// Package e2e contains end-to-end tests for the cluster health monitor.
package e2e

import (
	"github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

const (
	podTimeoutErrorCode = dnscheck.ErrCodePodTimeout
)

var (
	// Expected DNS checkers.
	// Note that these checkers must match with the configmap in manifests/overlays/test.
	coreDNSPerPodCheckers                   = []string{"TestInternalCoreDNSPerPod", "TestExternalCoreDNSPerPod"}
	coreDNSPerPodCheckersWithMinimalTimeout = []string{"TestInternalCoreDNSPerPodTimeout", "TestExternalCoreDNSPerPodTimeout"}
)

var _ = Describe("DNS per pod checker metrics", Ordered, ContinueOnFailure, func() {
	var (
		session   *gexec.Session
		localPort int
	)

	BeforeAll(func() {
		err := enableMockLocalDNS(clientset)
		Expect(err).NotTo(HaveOccurred(), "Failed to enable mock LocalDNS")

		session, localPort = setupMetricsPortforwarding(clientset)
	})

	AfterAll(func() {
		safeSessionKill(session)
	})

	It("should report healthy status for CoreDNSPerPod checkers", func() {
		By("Waiting for CoreDNSPerPod checker metrics to report healthy status")
		time0Metrics, err := getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			timeNMetrics, err := getMetrics(localPort)
			Expect(err).NotTo(HaveOccurred())

			allIncreased, increasedCheckers, err := verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics,
				podHealthResultMetricName, coreDNSPerPodCheckers, checkerTypeDNS, metricsHealthyStatus, metricsHealthyErrorCode,
			)
			Expect(err).NotTo(HaveOccurred())

			if !allIncreased {
				GinkgoWriter.Printf("Expected increase in healthy results for CoreDNSPerPod checkers: %v, Actual: %v\n", coreDNSPerPodCheckers, increasedCheckers)
				return false
			}
			GinkgoWriter.Printf("Found increase in healthy results for CoreDNSPerPod checkers %v\n", increasedCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "CoreDNSPerPod checker metrics did not report healthy status within the timeout period")
	})

	It("should report unhealthy status for CoreDNSPerPod checkers with minimal query timeout", func() {
		By("Waiting for CoreDNSPerPod checker metrics to report unhealthy status")
		time0Metrics, err := getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			timeNMetrics, err := getMetrics(localPort)
			Expect(err).NotTo(HaveOccurred())

			allIncreased, increasedCheckers, err := verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics,
				podHealthResultMetricName, coreDNSPerPodCheckersWithMinimalTimeout, checkerTypeDNS, metricsUnhealthyStatus, podTimeoutErrorCode,
			)
			Expect(err).NotTo(HaveOccurred())

			if !allIncreased {
				GinkgoWriter.Printf("Expected increase in unhealthy results for CoreDNSPerPod checkers: %v, Actual: %v\n", coreDNSPerPodCheckersWithMinimalTimeout, increasedCheckers)
				return false
			}
			GinkgoWriter.Printf("Found increase in unhealthy results for CoreDNSPerPod checkers %v\n", increasedCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "CoreDNSPerPod checker metrics did not report unhealthy status within the timeout period")
	})
})
