package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	"github.com/Azure/cluster-health-monitor/pkg/checker/podstartup"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/metrics"
	"github.com/Azure/cluster-health-monitor/pkg/scheduler"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var (
	configPath string

	cmd = &cobra.Command{
		Use:   "checker --config file",
		Short: "Cluster Health Monitor Checker",
		Run:   run,
	}
)

func init() {
	cmd.PersistentFlags().StringVar(&configPath, "config", "", "Path of the configuration file")
	cmd.MarkFlagRequired("config")
}

func run(cmd *cobra.Command, args []string) {
	if configPath == "" {
		cmd.Usage()
		return
	}

	ctx := cmd.Context()

	// run the metrics server
	m, err := metrics.NewServer(9800)
	if err != nil {
		klog.Fatalf("Failed to create metrics server:", err)
	}
	go func() {
		if err := m.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			klog.Fatalf("Metrics server error: %s.", err)
		}
	}()

	// run the health checkers
	cfg, err := config.ParseFromFile(configPath)
	if err != nil {
		klog.Fatalf("Failed to parse config: %s", err)
	}
	cs, err := buildCheckerSchedule(cfg)
	if err != nil {
		klog.Fatalf("Failed to build checker schedule: %s", err)

	}
	err = scheduler.NewScheduler(cs).Start(ctx)
	if err != nil && errors.Is(err, context.Canceled) {
		klog.Fatalf("Metrics server error: %s.", err)
	}
}

func buildCheckerSchedule(cfg *config.Config) ([]scheduler.CheckerSchedule, error) {
	var schedules []scheduler.CheckerSchedule
	for _, chkCfg := range cfg.Checkers {
		chk, err := checker.Build(&chkCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to build checker %q: %w", chkCfg.Name, err)
		}
		schedules = append(schedules, scheduler.CheckerSchedule{
			Interval: chkCfg.Interval,
			Timeout:  chkCfg.Timeout,
			Checker:  chk,
		})
	}
	return schedules, nil
}

func main() {
	registerCheckers()

	// Wait for interrupt signal to gracefully shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cmd.ExecuteContext(ctx)
}

func registerCheckers() {
	dnscheck.Register()
	podstartup.Register()
}
