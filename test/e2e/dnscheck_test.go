// Package e2e contains end-to-end tests for the cluster health monitor.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	"github.com/Azure/cluster-health-monitor/pkg/config"
)

const (
	checkerTypeDNS = string(config.CheckTypeDNS)

	localDNSTimeoutErrorCode = dnscheck.ErrCodeLocalDNSTimeout
)

var (
	// Expected DNS checkers.
	// Note that these checkers must match with the configmap in manifests/overlays/test.
	coreDNSCheckerNames  = []string{"TestInternalCoreDNS", "TestExternalCoreDNS"}
	localDNSCheckerNames = []string{"TestInternalLocalDNS", "TestExternalLocalDNS"}
	dnsCheckerNames      = append(coreDNSCheckerNames, localDNSCheckerNames...)
)

// This suite replaces shared CoreDNS and LocalDNS resources, so run it after parallel suites.
var _ = Describe("DNS checker metrics", Serial, Ordered, ContinueOnFailure, func() {
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

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			collectDNSDiagnostics(localPort)
		}
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
		waitForCheckerResultsMetricsValueIncrease(localPort,
			checkerResultMetricName, dnsCheckerNames, checkerTypeDNS, metricsHealthyStatus, metricsHealthyErrorCode,
			60*time.Second, 5*time.Second,
			"Not all checkers reported increase in healthy results within the timeout period")
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
		waitForCheckerResultsMetricsValueIncrease(localPort,
			checkerResultMetricName, coreDNSCheckerNames, checkerTypeDNS, metricsUnhealthyStatus, "",
			60*time.Second, 5*time.Second,
			"DNS checker metrics did not report unhealthy status within the timeout period")
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
		waitForCheckerResultsMetricsValueIncrease(localPort,
			checkerResultMetricName, localDNSCheckerNames, checkerTypeDNS, metricsUnhealthyStatus, localDNSTimeoutErrorCode,
			60*time.Second, 5*time.Second,
			"LocalDNS checker metrics did not report unhealthy status within the timeout period")
	})
})

func collectDNSDiagnostics(localPort int) {
	// Collect before teardown hooks and DeferCleanup restore the failing DNS configuration.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	diagnose := func(name string, args ...string) ([]byte, error) {
		commandCtx, commandCancel := context.WithTimeout(ctx, 10*time.Second)
		defer commandCancel()
		GinkgoWriter.Printf("\nDNS diagnostics: %s %s\n", name, strings.Join(args, " "))
		output, err := run(exec.CommandContext(commandCtx, name, args...))
		GinkgoWriter.Printf("%s\n", output)
		if err != nil {
			GinkgoWriter.Printf("DNS diagnostic command failed: %v (context: %v)\n", err, commandCtx.Err())
		}
		return output, err
	}

	_, _ = diagnose("kubectl", "get", "nodes", "-o", "wide")
	_, _ = diagnose("kubectl", "-n", kubesystem, "get", "pods", "-o", "wide")
	_, _ = diagnose("kubectl", "-n", kubesystem, "get", "daemonset", "mock-localdns", "-o", "yaml")
	_, _ = diagnose("kubectl", "-n", kubesystem, "describe", "pods", "-l", "app=mock-localdns")
	_, _ = diagnose("kubectl", "-n", kubesystem, "get", "events", "--sort-by=.metadata.creationTimestamp")
	for _, container := range []string{"ip-binder", "dnsmasq"} {
		_, _ = diagnose("kubectl", "-n", kubesystem, "logs", "-l", "app=mock-localdns",
			"-c", container, "--prefix", "--timestamps", "--tail=100")
	}
	_, _ = diagnose("kubectl", "-n", kubesystem, "logs", "-l", "app=cluster-health-monitor",
		"--prefix", "--timestamps", "--tail=200")
	if localPort != 0 {
		_, _ = diagnose("curl", "--fail", "--silent", "--show-error", "--max-time", "5",
			fmt.Sprintf("http://127.0.0.1:%d/metrics", localPort))
	}

	if clientset == nil {
		GinkgoWriter.Println("DNS network diagnostics unavailable: Kubernetes client was not initialized")
		return
	}
	podCtx, podCancel := context.WithTimeout(ctx, 10*time.Second)
	defer podCancel()
	pods, err := clientset.CoreV1().Pods(kubesystem).List(podCtx, metav1.ListOptions{
		LabelSelector: "app=cluster-health-monitor",
	})
	if err != nil {
		GinkgoWriter.Printf("DNS network diagnostics: failed to list monitor pods: %v\n", err)
		return
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == "" {
			GinkgoWriter.Printf("DNS network diagnostics: pod %s has not been scheduled\n", pod.Name)
			continue
		}
		// Kind node names are also their Docker container names. These commands are read-only.
		_, _ = diagnose("docker", "exec", pod.Spec.NodeName, "ip", "address", "show")
		_, _ = diagnose("docker", "exec", pod.Spec.NodeName, "ip", "route", "show", "table", "all")
		_, _ = diagnose("docker", "exec", pod.Spec.NodeName, "ss", "-lnutp")
		_, _ = diagnose("docker", "exec", pod.Spec.NodeName, "iptables-save")
		output, err := diagnose("docker", "exec", pod.Spec.NodeName,
			"crictl", "pods", "--name", "^"+pod.Name+"$", "--namespace", kubesystem, "--state", "Ready", "-q")
		if err != nil {
			continue
		}
		for _, sandboxID := range strings.Fields(string(output)) {
			output, err := diagnose("docker", "exec", pod.Spec.NodeName, "crictl", "inspectp", sandboxID)
			if err != nil {
				continue
			}
			var sandbox struct {
				Info struct {
					PID int `json:"pid"`
				} `json:"info"`
			}
			if err := json.Unmarshal(output, &sandbox); err != nil {
				GinkgoWriter.Printf("DNS network diagnostics: failed to decode sandbox: %v\n", err)
				continue
			}
			if sandbox.Info.PID <= 0 {
				GinkgoWriter.Println("DNS network diagnostics: sandbox PID is missing")
				continue
			}
			for _, args := range [][]string{
				{"ip", "address", "show"},
				{"ip", "route", "get", "169.254.10.11"},
				{"cat", fmt.Sprintf("/proc/%d/root/etc/resolv.conf", sandbox.Info.PID)},
			} {
				command := []string{"exec", pod.Spec.NodeName, "nsenter", "-t", strconv.Itoa(sandbox.Info.PID), "-n", "--"}
				_, _ = diagnose("docker", append(command, args...)...)
			}
		}
	}
}

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
							Name:    "dnsmasq",
							Image:   "jpillora/dnsmasq",
							Command: []string{"/usr/sbin/dnsmasq", "--no-daemon"},
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
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Host: "169.254.10.11",
										Port: intstr.FromInt32(53),
									},
								},
								PeriodSeconds: 2,
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
