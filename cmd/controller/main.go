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
	"github.com/Azure/cluster-health-monitor/pkg/checker/azurepolicy"
	"github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	"github.com/Azure/cluster-health-monitor/pkg/checker/metricsserver"
	"github.com/Azure/cluster-health-monitor/pkg/checker/podstartup"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/controller"
	"k8s.io/client-go/dynamic"
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
	workers := flag.Int("workers", 2, "Number of concurrent workers")
	flag.Parse()
	defer klog.Flush()

	klog.InfoS("Started CheckHealthMonitor Controller")
	registerCheckers()

	// Wait for interrupt signal to gracefully shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

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

	// Create dynamic client for CRD operations
	dynamicClient, err := dynamic.NewForConfig(k8sConfig)
	if err != nil {
		logErrorAndExit(err, "Failed to create dynamic client")
	}

	// Build checker registry
	checkerRegistry, err := buildCheckerRegistry(cfg, kubeClient)
	if err != nil {
		logErrorAndExit(err, "Failed to build checker registry")
	}
	klog.InfoS("Built checker registry", "numCheckers", len(checkerRegistry))

	// Start CheckHealthMonitor CRD controller
	chmController := controller.NewCheckHealthMonitorController(kubeClient, dynamicClient, checkerRegistry)
	if err := chmController.Run(ctx, *workers); err != nil {
		logErrorAndExit(err, "CheckHealthMonitor controller error")
	}

	klog.InfoS("Stopped CheckHealthMonitor Controller")
}

func buildCheckerRegistry(cfg *config.Config, kubeClient kubernetes.Interface) (map[string]checker.Checker, error) {
	registry := make(map[string]checker.Checker)
	for _, chkCfg := range cfg.Checkers {
		chk, err := checker.Build(&chkCfg, kubeClient)
		if errors.Is(err, checker.ErrSkipChecker) {
			klog.ErrorS(err, "Skipped checker", "name", chkCfg.Name)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to build checker %q: %w", chkCfg.Name, err)
		}
		registry[chkCfg.Name] = chk
	}
	return registry, nil
}

func registerCheckers() {
	dnscheck.Register()
	podstartup.Register()
	apiserver.Register()
	metricsserver.Register()
	azurepolicy.Register()
}
