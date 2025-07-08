package checker

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

type fakeChecker struct{ name string }

func (f *fakeChecker) Name() string                                   { return f.name }
func (f *fakeChecker) Run(ctx context.Context) (*types.Result, error) { return nil, nil }
func (f *fakeChecker) Type() config.CheckerType                       { return config.CheckerType("fake") }

func fakeBuilder(cfg *config.CheckerConfig, kubeClient kubernetes.Interface) (Checker, error) {
	if cfg.Name == "fail" {
		return nil, errors.New("forced error")
	}
	return &fakeChecker{name: cfg.Name}, nil
}

func TestBuildChecker(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name            string
		config          *config.CheckerConfig
		kubeClient      kubernetes.Interface
		validateChecker func(g *WithT, chk Checker, err error)
	}{
		{
			name: "Register and build valid checker",
			config: func() *config.CheckerConfig {
				testType := config.CheckerType("fake")
				RegisterChecker(testType, fakeBuilder)
				return &config.CheckerConfig{Name: "foo", Type: testType}
			}(),
			kubeClient: k8sfake.NewClientset(),
			validateChecker: func(g *WithT, chk Checker, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(chk).ToNot(BeNil())
				g.Expect(chk.Name()).To(Equal("foo"))
			},
		},
		{
			name:       "Build checker with unknown type",
			config:     &config.CheckerConfig{Name: "bar", Type: "unknown"},
			kubeClient: k8sfake.NewClientset(),
			validateChecker: func(g *WithT, chk Checker, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(chk).To(BeNil())
			},
		},
		{
			name: "Build checker with builder error",
			config: func() *config.CheckerConfig {
				testType := config.CheckerType("fakeerr")
				RegisterChecker(testType, fakeBuilder)
				return &config.CheckerConfig{Name: "fail", Type: testType}
			}(),
			kubeClient: k8sfake.NewClientset(),
			validateChecker: func(g *WithT, chk Checker, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(chk).To(BeNil())
			},
		},
		{
			name: "Build checker with nil Kubernetes client",
			config: func() *config.CheckerConfig {
				testType := config.CheckerType("fake")
				RegisterChecker(testType, fakeBuilder)
				return &config.CheckerConfig{Name: "foo", Type: testType}
			}(),
			kubeClient: nil,
			validateChecker: func(g *WithT, chk Checker, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(chk).To(BeNil())
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			chk, err := Build(tc.config, tc.kubeClient)
			tc.validateChecker(g, chk, err)
		})
	}
}
