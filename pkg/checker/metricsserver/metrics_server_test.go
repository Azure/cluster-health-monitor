package metricsserver

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

func TestMetricsServerChecker_Run(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		client      *metricsfake.Clientset
		validateRes func(g *WithT, res *types.Result, err error)
	}{
		{
			name: "healthy result - metrics server API is available",
			client: func() *metricsfake.Clientset {
				return metricsfake.NewSimpleClientset()
			}(),
			validateRes: func(g *WithT, res *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(res).ToNot(BeNil())
				g.Expect(res.Status).To(Equal(types.StatusHealthy))
			},
		},
		{
			name: "unhealthy result - metrics server API fails",
			client: func() *metricsfake.Clientset {
				client := metricsfake.NewSimpleClientset()
				// Add a reactor to make the List call fail
				client.PrependReactor("list", "nodes", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, fmt.Errorf("metrics server unavailable")
				})
				return client
			}(),
			validateRes: func(g *WithT, res *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(res).ToNot(BeNil())
				g.Expect(res.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(res.Detail.Code).To(Equal(errCodeMetricsServerUnavailable))
				g.Expect(res.Detail.Message).To(ContainSubstring("metrics server API unavailable"))
			},
		},
		{
			name: "unhealthy result - metrics server API times out",
			client: func() *metricsfake.Clientset {
				client := metricsfake.NewSimpleClientset()
				// Add a reactor that returns a timeout error
				client.PrependReactor("list", "nodes", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, context.DeadlineExceeded
				})
				return client
			}(),
			validateRes: func(g *WithT, res *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(res).ToNot(BeNil())
				g.Expect(res.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(res.Detail.Code).To(Equal(errCodeMetricsServerTimeout))
				g.Expect(res.Detail.Message).To(ContainSubstring("timed out while calling metrics server API"))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			checker := &MetricsServerChecker{
				name:          "test-metrics-server",
				config:        &config.MetricsServerConfig{},
				timeout:       1 * time.Second,
				kubeClient:    k8sfake.NewSimpleClientset(),
				metricsClient: tc.client,
			}

			res, err := checker.Run(context.Background())
			tc.validateRes(g, res, err)
		})
	}
}
