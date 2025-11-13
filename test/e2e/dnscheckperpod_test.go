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
		session, localPort = setupMetricsPortforwarding(clientset)
	})

	AfterAll(func() {
		safeSessionKill(session)
	})

	It("should report healthy status for CoreDNSPerPod checkers", func() {
		By("Waiting for CoreDNSPerPod checker metrics to report healthy status")
		Eventually(func() bool {
			matched, foundCheckers := verifyCoreDNSPodCheckerResultMetrics(localPort, coreDNSPerPodCheckers, checkerTypeDNS, metricsHealthyStatus, metricsHealthyErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected CoreDNSPerPod checkers to be healthy: %v, found: %v\n", coreDNSPerPodCheckers, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found healthy CoreDNSPerPod checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "CoreDNSPerPod checker metrics did not report healthy status within the timeout period")
	})

	It("should report unhealthy status for CoreDNSPerPod checkers with minimal query timeout", func() {
		By("Waiting for CoreDNSPerPod checker metrics to report unhealthy status")
		Eventually(func() bool {
			matched, foundCheckers := verifyCoreDNSPodCheckerResultMetrics(localPort, coreDNSPerPodCheckersWithMinimalTimeout, checkerTypeDNS, metricsUnhealthyStatus, podTimeoutErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected CoreDNSPerPod checkers to be unhealthy: %v, found: %v\n", coreDNSPerPodCheckersWithMinimalTimeout, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found unhealthy CoreDNSPerPod checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "CoreDNSPerPod checker metrics did not report unhealthy status within the timeout period")
	})
})
