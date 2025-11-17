// Package e2e contains end-to-end tests for the cluster health monitor.
package e2e

import (
	"os/exec"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	"github.com/Azure/cluster-health-monitor/pkg/config"
)

const (
	checkerTypeDNS = string(config.CheckTypeDNS)

	localDNSTimeoutErrorCode = dnscheck.ErrCodeLocalDNSTimeout
	serviceTimeoutErrorCode  = dnscheck.ErrCodeServiceTimeout
)

var (
	// Expected DNS checkers.
	// Note that these checkers must match with the configmap in manifests/overlays/test.
	coreDNSCheckerNames  = []string{"TestInternalCoreDNS", "TestExternalCoreDNS"}
	localDNSCheckerNames = []string{"TestInternalLocalDNS", "TestExternalLocalDNS"}
	dnsCheckerNames      = append(coreDNSCheckerNames, localDNSCheckerNames...)
)

var _ = Describe("DNS checker metrics", Ordered, ContinueOnFailure, func() {
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

	It("should report healthy status for CoreDNS and LocalDNS checkers", func() {
		By("Verifying LocalDNS is properly configured in the pod via the DNS patch")
		pod, err := getClusterHealthMonitorPod(clientset)
		Expect(err).NotTo(HaveOccurred(), "Failed to get cluster health monitor pod")
		cmd := exec.Command("kubectl", "get", "pod", "-n", kubesystem, pod.Name, "-o", "jsonpath={.spec.dnsConfig}")
		output, err := run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to get pod DNS config")
		GinkgoWriter.Printf("Pod DNS config: %s\n", string(output))
		Expect(string(output)).To(ContainSubstring("169.254.10.11"), "LocalDNS IP not found in pod DNS config")

		By("Waiting for LocalDNS mock to be available")
		Eventually(func() bool {
			return isMockLocalDNSAvailable(clientset)
		}, "60s", "5s").Should(BeTrue(), "Mock LocalDNS is not available")

		By("Waiting for DNS checker metrics to report healthy status")
		time0Metrics, err := getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			timeNMetrics, err := getMetrics(localPort)
			Expect(err).NotTo(HaveOccurred())

			allIncreased, increasedCheckers, err := verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics,
				checkerResultMetricName, dnsCheckerNames, checkerTypeDNS, metricsHealthyStatus, metricsHealthyErrorCode,
			)
			Expect(err).NotTo(HaveOccurred())

			if !allIncreased {
				GinkgoWriter.Printf("Expected increase in healthy results for checkers: %v, Actual: %v\n", dnsCheckerNames, increasedCheckers)
				return false
			}
			GinkgoWriter.Printf("Found increase in healthy results for checkers %v\n", increasedCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Not all checkers reported increase in healthy results within the timeout period")
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

		By("Waiting for DNS checker metrics to report unhealthy status")
		time0Metrics, err := getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			timeNMetrics, err := getMetrics(localPort)
			Expect(err).NotTo(HaveOccurred())

			allIncreased, increasedCheckers, err := verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics,
				checkerResultMetricName, coreDNSCheckerNames, checkerTypeDNS, metricsUnhealthyStatus, serviceTimeoutErrorCode,
			)
			Expect(err).NotTo(HaveOccurred())

			if !allIncreased {
				GinkgoWriter.Printf("Expected increase in unhealthy results for checkers: %v, Actual: %v\n", coreDNSCheckerNames, increasedCheckers)
				return false
			}
			GinkgoWriter.Printf("Found increase in unhealthy results for checkers %v\n", increasedCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "DNS checker metrics did not report unhealthy status within the timeout period")
	})

	It("should report unhealthy status with timeout for LocalDNS checkers when LocalDNS is unreachable", func() {
		By("Disabling LocalDNS mock")
		err := disableMockLocalDNS(clientset)
		Expect(err).NotTo(HaveOccurred(), "Failed to disable LocalDNS mock")

		DeferCleanup(func() {
			By("Re-enabling LocalDNS mock")
			err := enableMockLocalDNS(clientset)
			Expect(err).NotTo(HaveOccurred(), "Failed to re-enable LocalDNS mock")

			By("Waiting for mock LocalDNS to be available again")
			Eventually(func() bool {
				return isMockLocalDNSAvailable(clientset)
			}, "120s", "5s").Should(BeTrue(), "Mock LocalDNS is not available after re-enabling")
		})

		By("Waiting for LocalDNS checker metrics to report unhealthy status")
		time0Metrics, err := getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			timeNMetrics, err := getMetrics(localPort)
			Expect(err).NotTo(HaveOccurred())

			allIncreased, increasedCheckers, err := verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics,
				checkerResultMetricName, localDNSCheckerNames, checkerTypeDNS, metricsUnhealthyStatus, localDNSTimeoutErrorCode,
			)
			Expect(err).NotTo(HaveOccurred())

			if !allIncreased {
				GinkgoWriter.Printf("Expected increase in unhealthy results for LocalDNS checkers: %v, Actual: %v\n", localDNSCheckerNames, increasedCheckers)
				return false
			}
			GinkgoWriter.Printf("Found increase in unhealthy results for LocalDNS checkers %v\n", increasedCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "LocalDNS checker metrics did not report unhealthy status within the timeout period")
	})
})

// mockLocalDNSDaemonSet returns a DaemonSet struct that represents the mock LocalDNS server
func mockLocalDNSDaemonSet() *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "DaemonSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mock-localdns",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "mock-localdns",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "mock-localdns",
					},
				},
				Spec: corev1.PodSpec{
					HostNetwork: true,
					DNSPolicy:   corev1.DNSDefault,
					// The init container ensures the IP address is added to the node's network interface before the main dnsmasq container starts.
					// This avoids a race condition where dnsmasq tries to bind to the IP but the IP is not yet configured. If this happens, dnsmasq will
					// fail to start.
					InitContainers: []corev1.Container{
						{
							Name:    "ip-binder",
							Image:   "busybox",
							Command: []string{"/bin/sh", "-c"},
							// Attempt to add the IP address to the interface. If the IP is already assigned (possible on restart or rolling updates),
							// tolerate known errors and continue.
							Args: []string{
								`set -eu
echo "Adding IP 169.254.10.11 to eth0..."
if ! output=$(ip addr add 169.254.10.11/32 dev eth0 2>&1); then
  if echo "$output" | grep -q -e "Error: ipv4: Address already assigned" -e "RTNETLINK answers: File exists"; then
    echo "IP already assigned (or file exists), continuing."
  else
    echo "Failed to add IP: $output" >&2
    exit 1
  fi
fi
echo "IP successfully added or already present."`,
							},
							SecurityContext: &corev1.SecurityContext{
								Capabilities: &corev1.Capabilities{
									Add: []corev1.Capability{"NET_ADMIN"},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "dnsmasq",
							Image: "jpillora/dnsmasq",
							// Options doc: https://dnsmasq.org/docs/dnsmasq-man.html.
							Args: []string{
								"--listen-address=169.254.10.11",
								"--bind-interfaces",
								"--address=/kubernetes.default.svc.cluster.local/1.2.3.4",
								"--address=/mcr.microsoft.com/4.3.2.1",
								"--address=/#/",
								"--log-queries",
								"--log-facility=-",
							},
							Lifecycle: &corev1.Lifecycle{
								PreStop: &corev1.LifecycleHandler{
									Exec: &corev1.ExecAction{
										// On pod termination, clean up the IP address to leave the host network in a clean state.
										Command: []string{
											"/bin/sh",
											"-c",
											`set -eu
echo "Removing IP 169.254.10.11 from eth0..."
if ! output=$(ip addr del 169.254.10.11/32 dev eth0 2>&1); then
  if echo "$output" | grep -q "Error: ipv4: Address not found"; then
    echo "IP already removed, continuing."
  else
    echo "Failed to remove IP: $output" >&2
    exit 1
  fi
fi
echo "IP removed or was already gone."`,
										},
									},
								},
							},
							SecurityContext: &corev1.SecurityContext{
								Capabilities: &corev1.Capabilities{
									Add: []corev1.Capability{"NET_ADMIN"},
								},
							},
						},
					},
				},
			},
		},
	}
}
