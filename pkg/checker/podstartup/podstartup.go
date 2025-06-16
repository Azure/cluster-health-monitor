package podstartup

import (
	"context"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

type PodStartupChecker struct {
	name      string
	namespace string
	podName   string
}

func BuildPodStartupChecker(name string, config *config.PodStartupConfig) (*PodStartupChecker, error) {
	return &PodStartupChecker{
		name: name,
	}, nil
}

func (c *PodStartupChecker) Name() string {
	return c.name
}

func (c *PodStartupChecker) Run(ctx context.Context) types.Result {
	return types.NewUnknownResult("NOT_IMPLEMENTED", "PodStartupChecker not implemented yet")
}
