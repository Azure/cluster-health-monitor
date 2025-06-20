package scheduler

import (
	"context"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"golang.org/x/sync/errgroup"
	"k8s.io/klog/v2"
)

// CheckerSchedule defines the schedule for a health checker
type CheckerSchedule struct {
	// Interval defines how often the checker should run.
	Interval time.Duration
	// Timeout defines how long to wait for the checker to complete before considering it failed.
	Timeout time.Duration
	// Checker is the actual health checker that will be run according to the schedule.
	Checker checker.Checker
}

// NewScheduler creates a new Scheduler instance.
func NewScheduler(chkSchedules []CheckerSchedule) *Scheduler {
	return &Scheduler{
		chkSchedules: chkSchedules,
	}
}

// Scheduler manages and runs a set of checkers periodically.
type Scheduler struct {
	chkSchedules []CheckerSchedule
}

// Start starts all checkers according to their configured intervals and timeouts.
func (r *Scheduler) Start(ctx context.Context) error {
	var g errgroup.Group
	for _, chkSch := range r.chkSchedules {
		g.Go(func() error {
			return r.scheduleChecker(ctx, chkSch)
		})

	}
	return g.Wait()
}

func (r *Scheduler) scheduleChecker(ctx context.Context, chkSch CheckerSchedule) error {
	ticker := time.NewTicker(chkSch.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			func() {
				runCtx, cancel := context.WithTimeout(ctx, chkSch.Timeout)
				defer cancel()
				result, err := chkSch.Checker.Run(runCtx)

				checker.RecordCheckerResult(string(chkSch.Checker.Type()), chkSch.Checker.Name(), result, err)
			}()

		case <-ctx.Done():
			klog.Infoln("Scheduler stopping.")
			return ctx.Err()
		}
	}
}
