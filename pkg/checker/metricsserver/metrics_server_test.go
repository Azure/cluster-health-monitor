package metricsserver

import (
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	. "github.com/onsi/gomega"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestBuildMetricsServerChecker(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	cfg := &config.CheckerConfig{
		Name:                "test-metrics-server",
		Type:                config.CheckTypeMetricsServer,
		Timeout:             10 * time.Second,
		MetricsServerConfig: &config.MetricsServerConfig{},
	}

	client := k8sfake.NewSimpleClientset()
	checker, err := BuildMetricsServerChecker(cfg, client)

	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(checker).ToNot(BeNil())
	g.Expect(checker.Name()).To(Equal("test-metrics-server"))
	g.Expect(checker.Type()).To(Equal(config.CheckTypeMetricsServer))
}
