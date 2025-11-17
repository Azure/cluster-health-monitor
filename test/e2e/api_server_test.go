package e2e

import (
	"context"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/checker/apiserver"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	checkerTypeAPIServer     = string(config.CheckTypeAPIServer)
	apiServerObjectNamespace = kubesystem
	apiServerCreateErrorCode = apiserver.ErrCodeAPIServerCreateError
)

var (
	// Note that apiServerCheckerNames must match with the configmap in manifests/overlays/test.
	apiServerCheckerNames = []string{"TestAPIServer"}
)

var _ = Describe("API server checker", Ordered, ContinueOnFailure, func() {
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

	It("should report healthy status for API server checker", func() {
		By("Waiting for API server checker metrics to report healthy status")
		waitForCheckerResultsMetricsValueIncrease(localPort,
			checkerResultMetricName, apiServerCheckerNames, checkerTypeAPIServer, metricsHealthyStatus, metricsHealthyErrorCode,
			60*time.Second, 5*time.Second,
			"API server checker metrics did not report healthy status within the timeout period")
	})

	It("should report unhealthy status when configmap creation fails", func() {
		By("Creating a resource quota to limit configmaps in the object namespace to prevent creation")
		quota := &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-api-server-quota",
				Namespace: apiServerObjectNamespace,
			},
			Spec: corev1.ResourceQuotaSpec{
				Hard: corev1.ResourceList{
					"count/configmaps": resource.MustParse("0"),
				},
			},
		}
		_, err := clientset.CoreV1().ResourceQuotas(apiServerObjectNamespace).Create(context.TODO(), quota, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred(), "Failed to create resource quota")

		DeferCleanup(func() {
			By("Removing the resource quota")
			err := clientset.CoreV1().ResourceQuotas(apiServerObjectNamespace).Delete(context.TODO(), quota.Name, metav1.DeleteOptions{})
			Expect(err).NotTo(HaveOccurred(), "Failed to delete resource quota")
		})

		By("Waiting for API server checker to report unhealthy status")
		waitForCheckerResultsMetricsValueIncrease(localPort,
			checkerResultMetricName, apiServerCheckerNames, checkerTypeAPIServer, metricsUnhealthyStatus, apiServerCreateErrorCode,
			60*time.Second, 5*time.Second,
			"API server checker did not report unhealthy status within the timeout period")
	})
})
