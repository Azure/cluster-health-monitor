package checker

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"golang.org/x/sync/errgroup"
)

// Runner manages and runs a set of checkers periodically.
type Runner struct {
	config *config.Config
}

// NewRunner constructs a Runner from a config.Config.
func NewRunner(cfg *config.Config) (*Runner, error) {
	return &Runner{
		config: cfg,
	}, nil
}

// Run starts all checkers according to their configured intervals and timeouts.
func (r *Runner) Run(ctx context.Context) error {
	var g errgroup.Group
	for _, chkCfg := range r.config.Checkers {
		cfg := chkCfg // capture range variable
		g.Go(func() error {
			interval := cfg.Interval
			timeout := cfg.Timeout
			chk, err := buildChecker(cfg)
			if err != nil {
				return fmt.Errorf("Failed to build checker %q: %w", cfg.Name, err)
			}
			return r.runChecker(ctx, chk, interval, timeout)
		})
	}
	return g.Wait()
}

func (r *Runner) runChecker(ctx context.Context, chk Checker, interval, timeout time.Duration) error {
	if interval <= 0 {
		// Run only once, with timeout if specified
		runCtx := ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		err := chk.Run(runCtx)
		if cancel != nil {
			cancel()
		}
		return err
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			runCtx := ctx
			var cancel context.CancelFunc
			if timeout > 0 {
				runCtx, cancel = context.WithTimeout(ctx, timeout)
			}
			if err := chk.Run(runCtx); err != nil {
				log.Printf("Checker %q failed: %s", chk.Name(), err)
			}
			if cancel != nil {
				cancel()
			}

		case <-ctx.Done():
			fmt.Println("stopping")
			return nil
		}
	}
}
