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
	c, err := buildChecker(cfg)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(c).ToNot(BeNil())
	g.Expect(c.Name()).To(Equal("foo"))
}

func TestBuildCheckerUnknownType(t *testing.T) {
	g := NewWithT(t)
	cfg := &config.CheckerConfig{Name: "bar", Type: "unknown"}
	c, err := buildChecker(cfg)
	g.Expect(err).To(HaveOccurred())
	g.Expect(c).To(BeNil())
}

func TestBuildCheckerBuilderError(t *testing.T) {
	g := NewWithT(t)
	testType := config.CheckerType("fakeerr")
	RegisterChecker(testType, fakeBuilder)
	cfg := &config.CheckerConfig{Name: "fail", Type: testType}
	c, err := buildChecker(cfg)
	g.Expect(err).To(HaveOccurred())
	g.Expect(c).To(BeNil())
}

func TestBuildCheckersFromConfig_DuplicateNames(t *testing.T) {
	g := NewWithT(t)
	testType := config.CheckerType("dup")
	RegisterChecker(testType, fakeBuilder)
	yaml := `
checkers:
  - name: foo
    type: dup
  - name: foo
    type: dup
`
	checkers, err := BuildCheckersFromConfig([]byte(yaml))
	g.Expect(err).To(HaveOccurred())
	g.Expect(checkers).To(BeNil())
	g.Expect(err.Error()).To(ContainSubstring("duplicate checker name"))
}

func TestBuildCheckersFromConfig_UnknownType(t *testing.T) {
	g := NewWithT(t)
	yaml := `
checkers:
  - name: foo
    type: notype
`
	checkers, err := BuildCheckersFromConfig([]byte(yaml))
	g.Expect(err).To(HaveOccurred())
	g.Expect(checkers).To(BeNil())
	g.Expect(err.Error()).To(ContainSubstring("unrecognized checker type"))
}

func TestBuildCheckersFromConfig_InvalidYAML(t *testing.T) {
	g := NewWithT(t)
	badYAML := `not: [valid, yaml`
	checkers, err := BuildCheckersFromConfig([]byte(badYAML))
	g.Expect(err).To(HaveOccurred())
	g.Expect(checkers).To(BeNil())
	g.Expect(err.Error()).To(ContainSubstring("failed to unmarshal yaml"))
}

func TestBuildCheckersFromConfig_Valid(t *testing.T) {
	g := NewWithT(t)
	testType := config.CheckerType("oktype")
	RegisterChecker(testType, fakeBuilder)
	yaml := `
checkers:
  - name: foo
    type: oktype
    interval: 10s
    timeout: 5s
  - name: bar
    type: oktype
    interval: 10s
    timeout: 5s
`
	checkers, err := BuildCheckersFromConfig([]byte(yaml))
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(checkers).To(HaveLen(2))
	g.Expect(checkers[0].Name()).To(Equal("foo"))
	g.Expect(checkers[1].Name()).To(Equal("bar"))
}
