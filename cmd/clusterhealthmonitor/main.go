package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/spf13/cobra"
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
	cmd.PersistentFlags().StringVar(&configPath, "config", "config.yaml", "Path of the configuration file")
}

func run(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()
	cfg, err := config.ParsefromYAML(configPath)
	if err != nil {
		log.Fatalf("Failed to parse config: %v", err)
	}
	if err := checker.Run(ctx, cfg); err != nil {
		log.Fatalf("Failed to run checkers: %v", err)
	}
	log.Println("Starting Cluster Health Monitor Checker with config:", configPath)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cmd.ExecuteContext(ctx)
}
