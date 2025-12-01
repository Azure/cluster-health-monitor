package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega/gexec"
)

const (
	checkerTypeAzurePolicy = "AzurePolicy"
)

var (
	// Note that azurePolicyCheckerName must match with the configmap in manifests/overlays/test-aks.
	azurePolicyCheckerNames = []string{"TestAzurePolicy"}
)

var _ = Describe("Azure Policy checker", Ordered, ContinueOnFailure, func() {
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

	It("should report healthy status for Azure Policy checker", func() {
		By("Waiting for Azure Policy checker metrics to report healthy status")
		waitForCheckerResultsMetricsValueIncrease(localPort,
			checkerResultMetricName, azurePolicyCheckerNames, checkerTypeAzurePolicy, metricsHealthyStatus, metricsHealthyErrorCode,
			30*time.Second, 5*time.Second,
			"Azure Policy checker metrics did not report healthy status within the timeout period")
	})
})
