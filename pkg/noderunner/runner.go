package noderunner

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	chmclient "sigs.k8s.io/controller-runtime/pkg/client"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/checker/podnetwork"
)

// NodeChecker represents a health checker that can be run on a node
type NodeChecker interface {
	Name() string
	Run(ctx context.Context) (*checker.Result, error)
}

// Run executes all node health checkers and updates the CheckNodeHealth CR
func Run(ctx context.Context, clientset kubernetes.Interface, chmClient chmclient.Client, nodeName, crName string) error {
	// Initialize checkers. There are not much checker to initialize, so we do it inline here.
	// We consider refactoring this into configurable way if the number of checkers grows significantly or we want customer customization.
	checkers := initializeCheckers(clientset, nodeName)
	klog.InfoS("Initialized checkers", "count", len(checkers))

	// Run all checkers
	if err := runCheckers(ctx, chmClient, crName, checkers); err != nil {
		return fmt.Errorf("failed to run checkers: %w", err)
	}

	klog.InfoS("All checkers completed successfully", "cr", crName, "checkers", len(checkers))
	return nil
}

// initializeCheckers creates and returns a list of all checkers to run
func initializeCheckers(clientset kubernetes.Interface, nodeName string) []NodeChecker {
	checkers := []NodeChecker{}
	checkers = append(checkers, podnetwork.NewPodNetworkChecker(clientset, nodeName))
	return checkers
}

// runCheckers runs all checkers sequentially and updates the CR after each one
func runCheckers(ctx context.Context, chmClient chmclient.Client, crName string, checkers []NodeChecker) error {
	for _, chk := range checkers {
		klog.InfoS("Running checker", "checker", chk.Name())

		// Run the checker
		result, err := chk.Run(ctx)
		if err != nil {
			klog.ErrorS(err, "Checker returned error", "checker", chk.Name())
			return fmt.Errorf("checker %s failed: %w", chk.Name(), err)
		}

		klog.InfoS("Checker completed", "checker", chk.Name(), "status", result.Status, "message", result.Detail.Message)

		// Update CheckNodeHealth CR with the result
		if err := updateCheckNodeHealthStatus(ctx, chmClient, crName, chk.Name(), result); err != nil {
			klog.ErrorS(err, "Failed to update CheckNodeHealth status", "checker", chk.Name())
			return fmt.Errorf("failed to update CR status for checker %s: %w", chk.Name(), err)
		}

		klog.InfoS("Successfully updated CheckNodeHealth status", "cr", crName, "checker", chk.Name())
	}

	return nil
}

// updateCheckNodeHealthStatus updates the CheckNodeHealth CR with the checker result
func updateCheckNodeHealthStatus(ctx context.Context, chmClient chmclient.Client, crName, checkerName string, result *checker.Result) error {
	// Get the CheckNodeHealth CR
	cnh := &chmv1alpha1.CheckNodeHealth{}
	if err := chmClient.Get(ctx, chmclient.ObjectKey{Name: crName}, cnh); err != nil {
		return fmt.Errorf("failed to get CheckNodeHealth CR: %w", err)
	}

	// Convert checker.Result to CheckResult
	checkResult := chmv1alpha1.CheckResult{
		Name:      checkerName,
		Status:    convertStatus(result.Status),
		Message:   result.Detail.Message,
		ErrorCode: result.Detail.Code,
	}

	// Append to Results
	cnh.Status.Results = append(cnh.Status.Results, checkResult)

	// Update the status
	if err := chmClient.Status().Update(ctx, cnh); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

// convertStatus converts checker.Status to CheckStatus
func convertStatus(status checker.Status) chmv1alpha1.CheckStatus {
	switch status {
	case checker.StatusHealthy:
		return chmv1alpha1.CheckStatusHealthy
	case checker.StatusUnhealthy:
		return chmv1alpha1.CheckStatusUnhealthy
	case checker.StatusUnknown:
		return chmv1alpha1.CheckStatusUnknown
	default:
		return chmv1alpha1.CheckStatusUnknown
	}
}
