package podstartup

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
)

func TestPodStartupCheckerRunReturnsResult(t *testing.T) {
	g := NewWithT(t)

	c, err := Build(&config.CheckerConfig{
		Name:             "test-pod-checker",
		Type:             config.CheckTypePodStartup,
		PodStartupConfig: &config.PodStartupConfig{},
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(c).NotTo(BeNil())

	ctx := context.Background()
	result, err := c.Run(ctx)
	g.Expect(err).To(BeNil())

	// Since PodStartupChecker is not implemented, it should return unhealthy status
	g.Expect(result.Status).To(Equal(checker.StatusUnhealthy))
	g.Expect(result.ErrorDetail).NotTo(BeNil())
	g.Expect(result.ErrorDetail.Code).To(Equal("NOT_IMPLEMENTED"))
	g.Expect(result.ErrorDetail.Message).To(Equal("PodStartupChecker not implemented yet"))
}
