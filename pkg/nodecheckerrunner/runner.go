package nodecheckerrunner

import (
	"context"
	"fmt"
	"time"

	"github.com/avast/retry-go/v4"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	chmclient "sigs.k8s.io/controller-runtime/pkg/client"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/checker/podnetwork"
)

const (
	// maxRetryAttempts is the maximum number of retry attempts for each checker
	maxRetryAttempts = 3

	// retryDelay is the delay between retry attempts
	retryDelay = 3 * time.Second
)

// NodeChecker represents a health checker that can be run on a node
type NodeChecker interface {
	Name() string
	Run(ctx context.Context) (*checker.Result, error)
}

// Runner executes node health checkers and updates CheckNodeHealth CR
type Runner struct {
	chmClient chmclient.Client
	nodeName  string
	crName    string
	checkers  []NodeChecker
}

// NewRunner creates a new Runner instance
func NewRunner(clientset kubernetes.Interface, chmClient chmclient.Client, nodeName, crName string) *Runner {
	return &Runner{
		chmClient: chmClient,
		nodeName:  nodeName,
		crName:    crName,
		checkers:  initializeCheckers(clientset, nodeName),
	}
}

// Run executes all node health checkers and updates the CheckNodeHealth CR
func (r *Runner) Run(ctx context.Context) error {
	klog.InfoS("Initialized checkers", "count", len(r.checkers))

	// Run all checkers
	if err := r.runCheckers(ctx); err != nil {
		return fmt.Errorf("failed to run checkers: %w", err)
	}

	klog.InfoS("All checkers completed successfully", "cr", r.crName, "checkers", len(r.checkers))
	return nil
}

// initializeCheckers creates and returns a list of all checkers to run
func initializeCheckers(clientset kubernetes.Interface, nodeName string) []NodeChecker {
	checkers := []NodeChecker{}
	checkers = append(checkers, podnetwork.NewPodNetworkChecker(clientset, nodeName))
	return checkers
}

// runCheckers runs all checkers sequentially and updates the CR once with all results
func (r *Runner) runCheckers(ctx context.Context) error {
	results := make(map[string]*checker.Result)

	// Run all checkers and collect results
	for _, chk := range r.checkers {
		klog.InfoS("Running checker", "checker", chk.Name())

		var result *checker.Result

		// Retry with configured attempts and delay
		err := retry.Do(
			func() error {
				var runErr error
				result, runErr = chk.Run(ctx)
				return runErr
			},
			retry.Attempts(maxRetryAttempts),
			retry.Delay(retryDelay),
			retry.OnRetry(func(n uint, err error) {
				klog.InfoS("Checker attempt failed", "checker", chk.Name(), "attempt", n+1, "error", err)
			}),
		)

		if err != nil {
			klog.ErrorS(err, "Checker failed after retries", "checker", chk.Name())
			// Record as Unknown and continue with other checkers
			result = checker.Unknown(fmt.Sprintf("Checker failed after %d attempts: %v", maxRetryAttempts, err))
		}

		klog.InfoS("Checker completed", "checker", chk.Name(), "status", result.Status, "message", result.Detail.Message)
		results[chk.Name()] = result
	}

	// Update CheckNodeHealth CR with all results at once
	if err := r.updateCheckNodeHealthStatus(ctx, results); err != nil {
		klog.ErrorS(err, "Failed to update CheckNodeHealth status")
		return fmt.Errorf("failed to update CR status: %w", err)
	}

	klog.InfoS("Successfully updated CheckNodeHealth status", "cr", r.crName)
	return nil
}

// updateCheckNodeHealthStatus updates the CheckNodeHealth CR with all checker results
func (r *Runner) updateCheckNodeHealthStatus(ctx context.Context, results map[string]*checker.Result) error {
	// Get the CheckNodeHealth CR
	cnh := &chmv1alpha1.CheckNodeHealth{}
	if err := r.chmClient.Get(ctx, chmclient.ObjectKey{Name: r.crName}, cnh); err != nil {
		return fmt.Errorf("failed to get CheckNodeHealth CR: %w", err)
	}

	// Convert all checker results to CheckResults
	for checkerName, result := range results {
		checkResult := chmv1alpha1.CheckResult{
			Name:      checkerName,
			Status:    convertStatus(result.Status),
			Message:   result.Detail.Message,
			ErrorCode: result.Detail.Code,
		}
		cnh.Status.Results = append(cnh.Status.Results, checkResult)
	}

	// Update the status once with all results
	if err := r.chmClient.Status().Update(ctx, cnh); err != nil {
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
