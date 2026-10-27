package checker

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/metrics"
	. "github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

type fakeChecker struct{ name string }

func (f *fakeChecker) Name() string             { return f.name }
func (f *fakeChecker) Run(ctx context.Context)  {}
func (f *fakeChecker) Type() config.CheckerType { return config.CheckerType("fake") }

func fakeBuilder(cfg *config.CheckerConfig, kubeClient kubernetes.Interface) (Checker, error) {
	if cfg.Name == "fail" {
		return nil, errors.New("forced error")
	}
	return &fakeChecker{name: cfg.Name}, nil
}

func TestRecordResult(t *testing.T) {
	tests := []struct {
		name      string
		result    *Result
		err       error
		status    string
		errorCode string
	}{
		{
			name:      "healthy",
			result:    Healthy(),
			status:    metrics.HealthyStatus,
			errorCode: metrics.HealthyCode,
		},
		{
			name:      "unhealthy",
			result:    Unhealthy("TestFailure", "test failed"),
			status:    metrics.UnhealthyStatus,
			errorCode: "TestFailure",
		},
		{
			name:      "unknown",
			result:    Unknown("required CRD is not installed"),
			status:    metrics.UnknownStatus,
			errorCode: metrics.UnknownCode,
		},
		{
			name: "unknown with error code",
			result: &Result{
				Status: StatusUnknown,
				Detail: Detail{
					Code:    "KarpenterNodePoolCRDNotFound",
					Message: "required CRD is not installed",
				},
			},
			status:    metrics.UnknownStatus,
			errorCode: "KarpenterNodePoolCRDNotFound",
		},
		{
			name:      "execution error",
			err:       errors.New("request failed"),
			status:    metrics.UnknownStatus,
			errorCode: metrics.UnknownCode,
		},
		{
			name:      "execution error overrides result",
			result:    Healthy(),
			err:       errors.New("request failed"),
			status:    metrics.UnknownStatus,
			errorCode: metrics.UnknownCode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			chk := &fakeChecker{name: t.Name()}
			counter := metrics.CheckerResultCounter.WithLabelValues(string(chk.Type()), chk.Name(), tt.status, tt.errorCode)
			var before, after dto.Metric
			g.Expect(counter.Write(&before)).To(Succeed())

			RecordResult(chk, tt.result, tt.err)

			g.Expect(counter.Write(&after)).To(Succeed())
			g.Expect(after.GetCounter().GetValue()).To(Equal(before.GetCounter().GetValue() + 1))
		})
	}
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
