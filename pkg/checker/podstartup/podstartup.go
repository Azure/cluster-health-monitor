package podstartup

import (
	"context"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
)

type PodStartupChecker struct {
	name      string
	namespace string
	podName   string
}

func Build(cfg *config.CheckerConfig) (*PodStartupChecker, error) {
	return &PodStartupChecker{
		name: cfg.Name,
	}, nil
}

func (c *PodStartupChecker) Name() string {
	return c.name
}

func (c *PodStartupChecker) Run(ctx context.Context) (checker.Result, error) {
	return checker.Result{
		Status: checker.StatusUnhealthy,
		ErrorDetail: &checker.ErrorDetail{
			Code:    "NOT_IMPLEMENTED",
			Message: "PodStartupChecker not implemented yet",
		},
	}, nil
}
