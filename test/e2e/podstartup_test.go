package e2e

import (
	"github.com/Azure/cluster-health-monitor/pkg/checker/podstartup"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	rbacv1 "k8s.io/api/rbac/v1"
)

const (
	checkerTypePodStartup                   = "PodStartup"
	clusterHealthMonitorSynthPodManagerRole = "cluster-health-monitor-synth-pod-manager"

	podCreationErrorCode = podstartup.ErrCodePodCreationError
)

var (
	// Note that podStartupCheckerName must match with the configmap in manifests/overlays/test.
	podStartupCheckerNames = []string{"TestPodStartup"}

	// These labels are required on nodes for the synthetic pods created by the pod startup checker to meet node affinity requirements
	// and be scheduled. These are specified in the synthetic pod spec used by the podstartup checker.
	requiredNodeLabelsForSchedulingSyntheticPods = map[string]string{
		"kubernetes.azure.com/cluster": "",
		"kubernetes.azure.com/mode":    "system",
	}
)

var _ = Describe("Pod startup checker", Ordered, ContinueOnFailure, func() {
	var (
		session   *gexec.Session
		localPort int
	)

	BeforeEach(func() {
		// Typically, an AKS cluster will already have the required labels on at least one node. In some cases like AKS automatic, adding
		// labels to individual nodes is not allowed (it is recommended to add them to the node pool instead). Thus, we do not try to add
		// them if they are already present. However, KIND clusters will not have these labels by default, so we add them here to ensure the
		// synthetic pods created by the pod startup checker can be scheduled.
		By("Ensuring required node labels exist for scheduling synthetic pods created by pod startup checker")
		ensureLabelsExistOnAtLeastOneNode(clientset, requiredNodeLabelsForSchedulingSyntheticPods)
		session, localPort = setupMetricsPortforwarding(clientset)
	})

	AfterEach(func() {
		safeSessionKill(session)
	})

	It("should report healthy status for pod startup checker", func() {
		By("Waiting for pod startup checker metrics to report healthy status")
		time0Metrics, err := getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			timeNMetrics, err := getMetrics(localPort)
			Expect(err).NotTo(HaveOccurred())

			allIncreased, increasedCheckers, err := verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics,
				checkerResultMetricName, podStartupCheckerNames, checkerTypePodStartup, metricsHealthyStatus, metricsHealthyErrorCode,
			)
			Expect(err).NotTo(HaveOccurred())

			if !allIncreased {
				GinkgoWriter.Printf("Expected increase in healthy results for pod startup checkers: %v, Actual: %v\n", podStartupCheckerNames, increasedCheckers)
				return false
			}
			GinkgoWriter.Printf("Found increase in healthy results for pod startup checkers %v\n", increasedCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Pod startup checker metrics did not report healthy status within the timeout period")
	})

	It("should report unhealthy status when pods cannot be scheduled", func() {
		By("Removing pod creation permissions from cluster-health-monitor to prevent pod scheduling")

		restrictedRules := []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"events"},
				Verbs:     []string{"get", "list"},
			},
		}
		originalRules, err := replaceRolePermissions(clientset, kubesystem, clusterHealthMonitorSynthPodManagerRole, restrictedRules)
		Expect(err).NotTo(HaveOccurred())

		DeferCleanup(func() {
			By("Restoring pod creation permissions to cluster-health-monitor")
			_, err := replaceRolePermissions(clientset, kubesystem, clusterHealthMonitorSynthPodManagerRole, originalRules)
			Expect(err).NotTo(HaveOccurred())
		})

		By("Waiting for pod startup checker to report unhealthy status")
		time0Metrics, err := getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			timeNMetrics, err := getMetrics(localPort)
			Expect(err).NotTo(HaveOccurred())

			allIncreased, increasedCheckers, err := verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics,
				checkerResultMetricName, podStartupCheckerNames, checkerTypePodStartup, metricsUnhealthyStatus, podCreationErrorCode,
			)
			Expect(err).NotTo(HaveOccurred())

			if !allIncreased {
				GinkgoWriter.Printf("Expected increase in unhealthy results for pod startup checkers: %v, Actual: %v\n", podStartupCheckerNames, increasedCheckers)
				return false
			}
			GinkgoWriter.Printf("Found increase in unhealthy results for pod startup checkers %v\n", increasedCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Pod startup checker did not report unhealthy status within the timeout period")

		By("Restoring pod creation permissions to cluster-health-monitor")
		_, err = replaceRolePermissions(clientset, kubesystem, clusterHealthMonitorSynthPodManagerRole, originalRules)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for pod startup checker to report healthy status after restoring permissions")
		time0Metrics, err = getMetrics(localPort)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			timeNMetrics, err := getMetrics(localPort)
			Expect(err).NotTo(HaveOccurred())

			allIncreased, increasedCheckers, err := verifyCheckerResultMetricsValueIncreased(time0Metrics, timeNMetrics,
				checkerResultMetricName, podStartupCheckerNames, checkerTypePodStartup, metricsHealthyStatus, metricsHealthyErrorCode,
			)
			Expect(err).NotTo(HaveOccurred())

			if !allIncreased {
				GinkgoWriter.Printf("Expected increase in healthy results for pod startup checkers after restoration: %v, Actual: %v\n", podStartupCheckerNames, increasedCheckers)
				return false
			}
			GinkgoWriter.Printf("Found increase in healthy results for pod startup checkers %v\n", increasedCheckers)
			return true
		}, "60s", "5s").Should(BeTrue(), "Pod startup checker did not return to healthy status after adding back label within the timeout period")
	})
})
