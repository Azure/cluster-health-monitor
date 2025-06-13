package podstartup

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

func TestPodStartupCheckerRunReturnsResult(t *testing.T) {
	g := NewWithT(t)
	
	checker, err := BuildPodStartupChecker("test-pod-checker", &config.PodStartupConfig{})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(checker).NotTo(BeNil())
	
	ctx := context.Background()
	result := checker.Run(ctx)
	
	// Since PodStartupChecker is not implemented, it should return unknown status
	g.Expect(result.Status).To(Equal(types.StatusUnknown))
	g.Expect(result.ErrorDetail).NotTo(BeNil())
	g.Expect(result.ErrorDetail.Code).To(Equal("NOT_IMPLEMENTED"))
	g.Expect(result.ErrorDetail.Message).To(Equal("PodStartupChecker not implemented yet"))
}