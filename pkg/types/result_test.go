package types

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestResultStatus(t *testing.T) {
	g := NewWithT(t)

	// Test that all status constants are defined correctly
	g.Expect(StatusHealthy).To(Equal(Status("healthy")))
	g.Expect(StatusUnhealthy).To(Equal(Status("unhealthy")))
	g.Expect(StatusUnknown).To(Equal(Status("unknown")))
}

func TestResult(t *testing.T) {
	g := NewWithT(t)

	// Test healthy result
	healthyResult := Result{
		Status: StatusHealthy,
	}
	g.Expect(healthyResult.Status).To(Equal(StatusHealthy))
	g.Expect(healthyResult.ErrorDetail).To(BeNil())

	// Test unhealthy result with error detail
	errorDetail := &ErrorDetail{
		Code:    "TEST_ERROR",
		Message: "Test error message",
	}
	unhealthyResult := Result{
		Status:      StatusUnhealthy,
		ErrorDetail: errorDetail,
	}
	g.Expect(unhealthyResult.Status).To(Equal(StatusUnhealthy))
	g.Expect(unhealthyResult.ErrorDetail).To(Equal(errorDetail))
	g.Expect(unhealthyResult.ErrorDetail.Code).To(Equal("TEST_ERROR"))
	g.Expect(unhealthyResult.ErrorDetail.Message).To(Equal("Test error message"))
}