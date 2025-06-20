package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	"github.com/Azure/cluster-health-monitor/pkg/checker/podstartup"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/metrics"
	"github.com/Azure/cluster-health-monitor/pkg/scheduler"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

const (
	defaultConfigPath = "/etc/cluster-health-monitor/config.yaml"
)

func init() {
	klog.InitFlags(nil)
}

func main() {
	configPath := flag.String("config", defaultConfigPath, "Path to the configuration file")
	flag.Parse()
	defer klog.Flush()

	registerCheckers()

	ctx := context.Background()

	// Wait for interrupt signal to gracefully shutdown.
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Run the prometheus metrics server.
	m, err := metrics.NewServer(9800)
	if err != nil {
		log.Fatalf("Failed to create metrics server:%s.", err)
	}
	go func() {
		if err := m.Run(ctx); err != nil {
			log.Fatalf("Metrics server error: %v.", err)
		}
	}()

	// Load checkers from config.
	configBytes, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("Failed to read config file: %v", err)
	}

	checkers, err := checker.BuildCheckersFromConfig(configBytes)
	if err != nil {
		log.Fatalf("Failed to build checkers: %v", err)
	}

	if len(checkers) == 0 {
		log.Printf("Warning: No checkers were loaded from the config file.")
	} else {
		log.Printf("Loaded %d checkers from config", len(checkers))
	}

	// TODO: refactor config parsing to only parse once for building checkers and for intervals/timeouts.
	var parsedConfig config.Config
	if err := yaml.Unmarshal(configBytes, &parsedConfig); err != nil {
		log.Fatalf("Failed to parse config for intervals and timeouts: %v", err)
	}

	intervalTimeoutMap := make(map[string]struct {
		interval time.Duration
		timeout  time.Duration
	})

	for _, cfg := range parsedConfig.Checkers {
		intervalTimeoutMap[cfg.Name] = struct {
			interval time.Duration
			timeout  time.Duration
		}{
			interval: cfg.Interval,
			timeout:  cfg.Timeout,
		}
	}

	// Create checker schedules from checkers.
	var checkerSchedules []scheduler.CheckerSchedule
	for _, chk := range checkers {
		// Get interval and timeout from config for each checker.
		itConfig, ok := intervalTimeoutMap[chk.Name()]
		if !ok {
			log.Fatalf("Failed to find interval/timeout config for checker %q", chk.Name())
		}

		checkerSchedules = append(checkerSchedules, scheduler.CheckerSchedule{
			Checker:  chk,
			Interval: itConfig.interval,
			Timeout:  itConfig.timeout,
		})
		log.Printf("Scheduled checker %q with interval %s and timeout %s",
			chk.Name(), itConfig.interval, itConfig.timeout)
	}

	// Run the scheduler.
	sched := scheduler.NewScheduler(checkerSchedules)
	go func() {
		if err := sched.Start(ctx); err != nil {
			log.Fatalf("Scheduler error: %v", err)
		}
	}()

	log.Printf("Cluster Health Monitor started, using config from %s", *configPath)
	<-ctx.Done()
}

func registerCheckers() {
	dnscheck.Register()
	podstartup.Register()
}
