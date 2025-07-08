package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/checker/apiserver"
	"github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	"github.com/Azure/cluster-health-monitor/pkg/checker/podstartup"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/metrics"
	"github.com/Azure/cluster-health-monitor/pkg/scheduler"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	defaultConfigPath = "/etc/cluster-health-monitor/config.yaml"
)

func init() {
	klog.InitFlags(nil)
}

// logErrorAndExit logs an error message and exits the program with exit code 1.
func logErrorAndExit(err error, message string) {
	klog.ErrorS(err, message)
	klog.FlushAndExit(klog.ExitFlushTimeout, 1)
}

func main() {
	configPath := flag.String("config", defaultConfigPath, "Path to the configuration file")
	flag.Parse()
	defer klog.Flush()

	klog.InfoS("Started Cluster Health Monitor")
	registerCheckers()

	// Wait for interrupt signal to gracefully shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Run the prometheus metrics server.
	m, err := metrics.NewServer(9800)
	if err != nil {
		logErrorAndExit(err, "Failed to create metrics server")
	}
	go func() {
		if err := m.Run(ctx); err != nil {
			logErrorAndExit(err, "Metrics server error")
		}
	}()

	// Parse the configuration file.
	cfg, err := config.ParseFromFile(*configPath)
	if err != nil {
		logErrorAndExit(err, "Failed to parse config")
	}
	klog.InfoS("Parsed configuration file",
		"path", *configPath,
		"numCheckers", len(cfg.Checkers))

	// Create Kubernetes client.
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		logErrorAndExit(err, "Failed to get in-cluster config")
	}
	kubeClient, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		logErrorAndExit(err, "Failed to create Kubernetes client")
	}

	// Build the checker schedule from the configuration.
	cs, err := buildCheckerSchedule(cfg, kubeClient)
	if err != nil {
		logErrorAndExit(err, "Failed to build checker schedule")
	}
	klog.InfoS("Built checker schedule", "numSchedules", len(cs))

	// Run the scheduler.
	sched := scheduler.NewScheduler(cs)
	go func() {
		if err := sched.Start(ctx); err != nil {
			logErrorAndExit(err, "Scheduler error")
		}
	}()
	klog.InfoS("Scheduler started")

	<-ctx.Done()
	klog.InfoS("Stopped Cluster Health Monitor due to context cancel")
}

func buildCheckerSchedule(cfg *config.Config, kubeClient kubernetes.Interface) ([]scheduler.CheckerSchedule, error) {
	var schedules []scheduler.CheckerSchedule
	for _, chkCfg := range cfg.Checkers {
		chk, err := checker.Build(&chkCfg, kubeClient)
		if errors.Is(err, checker.ErrSkipChecker) {
			klog.ErrorS(err, "Skipped checker", "name", chkCfg.Name)
			continue
		}
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

func registerCheckers() {
	dnscheck.Register()
	podstartup.Register()
	apiserver.Register()
}
