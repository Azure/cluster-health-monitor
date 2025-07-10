// Package e2e contains end-to-end tests for the cluster health monitor.
package e2e

import (
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

const (
	checkerTypeDNS             = "dns"
	dnsPodsNotReadyErrorCode   = "pods_not_ready"
	dnsServiceTimeoutErrorCode = "service_timeout"
	localDNSTimeoutErrorCode   = "local_dns_timeout"
)

var (
	// Expected DNS checkers.
	// Note that these checkers must match with the configmap in manifests/overlays/test.
	coreDNSCheckerNames  = []string{"test-internal-coredns", "test-external-coredns"}
	localDNSCheckerNames = []string{"test-internal-localdns", "test-external-localdns"}
	dnsCheckerNames      = append(coreDNSCheckerNames, localDNSCheckerNames...)
)

var _ = Describe("DNS checker metrics", Ordered, ContinueOnFailure, func() {
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

	It("should report healthy status for CoreDNS and LocalDNS checkers", func() {
		By("Verifying LocalDNS is properly configured in the pod via the DNS patch")
		pod, err := getClusterHealthMonitorPod(clientset)
		Expect(err).NotTo(HaveOccurred(), "Failed to get cluster health monitor pod")
		cmd := exec.Command("kubectl", "get", "pod", "-n", "kube-system", pod.Name, "-o", "jsonpath={.spec.dnsConfig}")
		output, err := run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to get pod DNS config")
		GinkgoWriter.Printf("Pod DNS config: %s\n", string(output))
		Expect(string(output)).To(ContainSubstring("169.254.10.11"), "LocalDNS IP not found in pod DNS config")

		By("Waiting for LocalDNS mock to be available")
		Eventually(func() bool {
			return isMockLocalDNSAvailable(clientset)
		}, "60s", "5s").Should(BeTrue(), "Mock LocalDNS is not available")

		By("Waiting for DNS checker metrics to report healthy status")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, dnsCheckerNames, checkerTypeDNS, metricsHealthyStatus, metricsHealthyErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected DNS checkers to be healthy: %v, found: %v\n", dnsCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found healthy DNS checker metric for %v\n", foundCheckers)
			return true
		}, "30s", "5s").Should(BeTrue(), "DNS checker metrics did not report healthy status within the timeout period")
	})

	It("should report unhealthy status for CoreDNS checkers when CoreDNS pods are not ready", func() {
		By("Getting the CoreDNS deployment")
		deployment, err := getCoreDNSDeployment(clientset)
		Expect(err).NotTo(HaveOccurred(), "Failed to get CoreDNS deployment")
		originalReplicas := *deployment.Spec.Replicas

		By("Scaling down CoreDNS deployment to 0 replicas to simulate unhealthy state")
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

		By("Waiting for DNS checker metrics to report unhealthy status with pods not ready")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, coreDNSCheckerNames, checkerTypeDNS, metricsUnhealthyStatus, dnsPodsNotReadyErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected DNS checkers to be unhealthy and pods not ready: %v, found: %v\n", coreDNSCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found unhealthy and pods not ready DNS checker metric for %v\n", foundCheckers)
			return true
		}, "30s", "5s").Should(BeTrue(), "DNS checker metrics did not report unhealthy status and pods not ready within the timeout period")
	})

	It("should report unhealthy status for CoreDNS checkers when DNS service has high latency", func() {
		By("Simulating high latency in DNS responses")
		originalCorefile, err := simulateCoreDNSHighLatency(clientset)
		Expect(err).NotTo(HaveOccurred(), "Failed to simulate high latency in DNS responses")

		By("Deleting CoreDNS pods to apply the changes")
		err = deleteCoreDNSPods(clientset)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete CoreDNS pods to apply high latency changes")

		DeferCleanup(func() {
			By("Restoring the original CoreDNS ConfigMap")
			err := restoreCoreDNSConfigMap(clientset, originalCorefile)
			Expect(err).NotTo(HaveOccurred(), "Failed to restore CoreDNS ConfigMap")

			By("Deleting CoreDNS pods to apply the original configuration")
			err = deleteCoreDNSPods(clientset)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete CoreDNS pods with original configuration")
		})

		By("Waiting for DNS checker metrics to report unhealthy status with service timeout")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, coreDNSCheckerNames, checkerTypeDNS, metricsUnhealthyStatus, dnsServiceTimeoutErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected DNS checkers to be unhealthy and service timeout: %v, found: %v\n", coreDNSCheckerNames, foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found unhealthy and service timeout DNS checker metric for %v\n", foundCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "DNS checker metrics did not report unhealthy status and service timeout within the timeout period")
	})

	It("should report unhealthy status with timeout for LocalDNS checkers when LocalDNS is unreachable", func() {
		By("Disabling LocalDNS mock")
		cmd := exec.Command("make", "kind-disable-localdns-mock")
		output, err := run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to disable LocalDNS mock: %s", string(output))
		GinkgoWriter.Println(string(output))

		DeferCleanup(func() {
			By("Re-enabling LocalDNS mock")
			cmd := exec.Command("make", "kind-enable-localdns-mock")
			output, err := run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to re-enable LocalDNS mock: %s", string(output))
			GinkgoWriter.Println(string(output))

			By("Waiting for mock LocalDNS to be available again")
			Eventually(func() bool {
				return isMockLocalDNSAvailable(clientset)
			}, "120s", "5s").Should(BeTrue(), "Mock LocalDNS is not available after re-enabling")
		})

		By("Waiting for LocalDNS checker metrics to report unhealthy status")
		Eventually(func() bool {
			matched, foundCheckers := verifyCheckerResultMetrics(localPort, localDNSCheckerNames, checkerTypeDNS, metricsUnhealthyStatus, localDNSTimeoutErrorCode)
			if !matched {
				GinkgoWriter.Printf("Expected LocalDNS checker to be unhealthy, found: %v\n", foundCheckers)
				return false
			}
			GinkgoWriter.Printf("Found unhealthy LocalDNS checker metric for %v\n", foundCheckers)
			return true
		}, "30s", "5s").Should(BeTrue(), "LocalDNS checker metrics did not report unhealthy status within the timeout period")
	})
})
