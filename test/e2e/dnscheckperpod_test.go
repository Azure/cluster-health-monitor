// Package e2e contains end-to-end tests for the cluster health monitor.
package e2e

import (
	"github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

const (
	errCodePodError = dnscheck.ErrCodePodError
)

var (
	// Expected DNS checkers.
	// Note that these checkers must match with the configmap in manifests/overlays/test.
	coreDNSPerPodCheckerNames = []string{"TestInternalCoreDNSPerPod", "TestExternalCoreDNSPerPod"}
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
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, coreDNSPerPodCheckerNames, checkerTypeDNS, metricsHealthyStatus, metricsHealthyErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected CoreDNSPerPod checkers to be healthy: %v, found: %v\n", coreDNSPerPodCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found healthy CoreDNSPerPod checker metric for %v\n", foundCheckers)
			return true
		}, "30s", "5s").Should(BeTrue(), "CoreDNSPerPod checker metrics did not report healthy status within the timeout period")
	})

	It("should report unhealthy status for CoreDNSPerPod checkers when CoreDNS pods are not ready", func() {
		By("Getting the CoreDNSPerPod deployment")
		deployment, err := getCoreDNSDeployment(clientset)
		Expect(err).NotTo(HaveOccurred(), "Failed to get CoreDNS deployment")
		originalReplicas := *deployment.Spec.Replicas

		By("Scaling down CoreDNSPerPod deployment to 0 replicas to simulate unhealthy state")
		err = updateCoreDNSDeploymentReplicas(clientset, 0)
		Expect(err).NotTo(HaveOccurred(), "Failed to scale down CoreDNS deployment")

		DeferCleanup(func() {
			By("Restoring CoreDNS deployment to original replica count")
			err := updateCoreDNSDeploymentReplicas(clientset, originalReplicas)
			Expect(err).NotTo(HaveOccurred(), "Failed to restore CoreDNS deployment")

			By("Waiting for CoreDNS pods to be ready again")
			Eventually(func() bool {
				deployment, err := getCoreDNSDeployment(clientset)
				if err != nil {
					return false
				}
				return deployment.Status.ReadyReplicas == originalReplicas
			}, "60s", "5s").Should(BeTrue(), "CoreDNS pods did not return to ready state")
		})

		By("Waiting for all CoreDNS pods to terminate")
		Eventually(func() bool {
			deployment, err := getCoreDNSDeployment(clientset)
			if err != nil {
				return false
			}
			return deployment.Status.AvailableReplicas == 0
		}, "30s", "2s").Should(BeTrue(), "Not all CoreDNS pods terminated")

		By("Waiting for CoreDNSPerPod checker metrics to report unhealthy status with pods with error")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, coreDNSPerPodCheckerNames, checkerTypeDNS, metricsUnhealthyStatus, errCodePodError)
			if !matched {
				GinkgoWriter.Printf("Expected CoreDNSPerPod checkers to be unhealthy and pods with error: %v, found: %v\n", coreDNSPerPodCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found unhealthy and pods with error CoreDNSPerPod checker metric for %v\n", foundCheckers)
			return true
		}, "30s", "5s").Should(BeTrue(), "CoreDNSPerPod checker metrics did not report unhealthy status and pods with error within the timeout period")
	})
})
