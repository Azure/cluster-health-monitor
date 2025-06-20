package checker

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
	. "github.com/onsi/gomega"
)

type fakeChecker struct{ name string }

func (f *fakeChecker) Name() string                                   { return f.name }
func (f *fakeChecker) Run(ctx context.Context) (*types.Result, error) { return nil, nil }
func (f *fakeChecker) Type() config.CheckerType                       { return config.CheckerType("fake") }

func fakeBuilder(cfg *config.CheckerConfig) (Checker, error) {
	if cfg.Name == "fail" {
		return nil, errors.New("forced error")
	}
	return &fakeChecker{name: cfg.Name}, nil
}

func TestRegisterCheckerAndBuildChecker(t *testing.T) {
	g := NewWithT(t)
	testType := config.CheckerType("fake")
	RegisterChecker(testType, fakeBuilder)
	cfg := &config.CheckerConfig{Name: "foo", Type: testType}
	c, err := Build(cfg)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(c).ToNot(BeNil())
	g.Expect(c.Name()).To(Equal("foo"))
}

func TestBuildCheckerUnknownType(t *testing.T) {
	g := NewWithT(t)
	cfg := &config.CheckerConfig{Name: "bar", Type: "unknown"}
	c, err := Build(cfg)
	g.Expect(err).To(HaveOccurred())
	g.Expect(c).To(BeNil())
}

func TestBuildCheckerBuilderError(t *testing.T) {
	g := NewWithT(t)
	testType := config.CheckerType("fakeerr")
	RegisterChecker(testType, fakeBuilder)
	cfg := &config.CheckerConfig{Name: "fail", Type: testType}
	c, err := Build(cfg)
	g.Expect(err).To(HaveOccurred())
	g.Expect(c).To(BeNil())
}
