package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"golang.org/x/sync/errgroup"
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
				if _, err := chkSch.Checker.Run(runCtx); err != nil {
					// TODO: handle the error of the checker and emit corresponding metrics
					log.Printf("Checker %q failed: %s.", chkSch.Checker.Name(), err)
				}
				// TODO: handle the result of the checker and emit corresponding metrics
			}()

		case <-ctx.Done():
			log.Println("Scheduler stopping.")
			return ctx.Err()
		}
	}
}
