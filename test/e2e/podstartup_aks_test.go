package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega/gexec"
)

var (
	// Note that podStartupWithNAPAndCSICheckerName must match with the configmap in manifests/overlays/test-aks.
	podStartupWithNAPAndCSICheckerNames = []string{"TestPodStartupWithNAPAndCSI"}
)

var _ = Describe("Pod startup checker with NAP and CSI", Ordered, ContinueOnFailure, func() {
	var (
		session   *gexec.Session
		localPort int
	)

	BeforeEach(func() {
		By("Ensuring required node labels exist for scheduling synthetic pods created by pod startup checker")
		ensureLabelsExistOnAtLeastOneNode(clientset, requiredNodeLabelsForSchedulingSyntheticPods)
		session, localPort = setupMetricsPortforwarding(clientset)
	})

	AfterEach(func() {
		safeSessionKill(session)
	})

	It("should report healthy status for pod startup checker with NAP and CSI", func() {
		By("Waiting for pod startup checker with NAP and CSI metrics to report healthy status")
		waitForCheckerResultsMetricsValueIncrease(localPort,
			checkerResultMetricName, podStartupWithNAPAndCSICheckerNames, checkerTypePodStartup, metricsHealthyStatus, metricsHealthyErrorCode,
			13*time.Minute, 10*time.Second,
			"Pod startup checker with NAP and CSI metrics did not report healthy status within the timeout period")
	})
})
