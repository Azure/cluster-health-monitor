// Package e2e contains end-to-end tests for the cluster health monitor.
package e2e

import (
	"time"

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
		waitForCheckerResultsMetricsValueIncrease(localPort,
			podHealthResultMetricName, coreDNSPerPodCheckers, checkerTypeDNS, metricsHealthyStatus, metricsHealthyErrorCode,
			60*time.Second, 5*time.Second,
			"CoreDNSPerPod checker metrics did not report healthy status within the timeout period")
	})

	It("should report unhealthy status for CoreDNSPerPod checkers with minimal query timeout", func() {
		By("Waiting for CoreDNSPerPod checker metrics to report unhealthy status")
		waitForCheckerResultsMetricsValueIncrease(localPort,
			podHealthResultMetricName, coreDNSPerPodCheckersWithMinimalTimeout, checkerTypeDNS, metricsUnhealthyStatus, podTimeoutErrorCode,
			60*time.Second, 5*time.Second,
			"CoreDNSPerPod checker metrics did not report unhealthy status within the timeout period")
	})
})
