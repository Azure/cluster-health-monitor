package types

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestNewHealthyResult(t *testing.T) {
	g := NewWithT(t)
	result := NewHealthyResult()
	
	g.Expect(result.Status).To(Equal(StatusHealthy))
	g.Expect(result.ErrorDetail).To(BeNil())
}

func TestNewUnhealthyResult(t *testing.T) {
	g := NewWithT(t)
	result := NewUnhealthyResult("TEST_CODE", "test message")
	
	g.Expect(result.Status).To(Equal(StatusUnhealthy))
	g.Expect(result.ErrorDetail).NotTo(BeNil())
	g.Expect(result.ErrorDetail.Code).To(Equal("TEST_CODE"))
	g.Expect(result.ErrorDetail.Message).To(Equal("test message"))
}

func TestNewUnknownResult(t *testing.T) {
	g := NewWithT(t)
	result := NewUnknownResult("UNKNOWN_CODE", "unknown message")
	
	g.Expect(result.Status).To(Equal(StatusUnknown))
	g.Expect(result.ErrorDetail).NotTo(BeNil())
	g.Expect(result.ErrorDetail.Code).To(Equal("UNKNOWN_CODE"))
	g.Expect(result.ErrorDetail.Message).To(Equal("unknown message"))
}