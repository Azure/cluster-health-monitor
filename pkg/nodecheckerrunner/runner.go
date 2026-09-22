package nodecheckerrunner

import (
	"context"
	"fmt"
	"time"

	"github.com/avast/retry-go/v4"
	"k8s.io/client-go/kubernetes"
	k8sretry "k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	chmclient "sigs.k8s.io/controller-runtime/pkg/client"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/gpu"
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/podnetwork"
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

// GPUOptions carries information the GPU checkers need from the controller.
type GPUOptions struct {
	SKU string
}

// Options configures which checkers run and what they are told about the node.
type Options struct {
	NodeName string
	CRName   string
	// GPU is nil on nodes without GPUs.
	GPU *GPUOptions
}

// Runner executes node health checkers and updates CheckNodeHealth CR
type Runner struct {
	chmClient chmclient.Client
	nodeName  string
	crName    string
	checkers  []NodeChecker
}

// NewRunner creates a new Runner instance
func NewRunner(clientset kubernetes.Interface, chmClient chmclient.Client, opts Options) *Runner {
	r := &Runner{
		chmClient: chmClient,
		nodeName:  opts.NodeName,
		crName:    opts.CRName,
	}
	r.initializeCheckers(clientset, opts)
	return r
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

// initializeCheckers creates all the checkers to run.
func (r *Runner) initializeCheckers(clientset kubernetes.Interface, opts Options) {
	r.checkers = []NodeChecker{podnetwork.NewPodNetworkChecker(clientset, opts.NodeName)}

	// The GPU checkers only work in the GPU image, which is what the controller uses when it populates these options.
	if opts.GPU != nil {
		for _, c := range gpu.NewCheckers(gpu.Config{SKU: opts.GPU.SKU}) {
			r.checkers = append(r.checkers, c)
		}
	}
}

// runCheckers runs each checker sequentially and records its result as soon as it finishes. This way a pod
// that is killed part way through still reports everything it managed to measure.
func (r *Runner) runCheckers(ctx context.Context) error {
	var unrecorded []string
	for _, chk := range r.checkers {
		result := r.runChecker(ctx, chk)
		if err := r.recordResult(ctx, chk.Name(), result); err != nil {
			// The controller reports anything we never record as Unknown, so losing one result is
			// not a reason to abandon the checks that have not run yet.
			klog.ErrorS(err, "Failed to record checker result", "checker", chk.Name(), "cr", r.crName)
			unrecorded = append(unrecorded, chk.Name())
			continue
		}
		klog.InfoS("Recorded checker result", "checker", chk.Name(), "cr", r.crName)
	}

	if len(unrecorded) > 0 {
		return fmt.Errorf("failed to record results for %v", unrecorded)
	}
	return nil
}

// runChecker runs one checker, retrying while it returns an error.
func (r *Runner) runChecker(ctx context.Context, chk NodeChecker) *checker.Result {
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
	return result
}

// recordResult writes one checker's result to the CR. Retrys on conflict.
func (r *Runner) recordResult(ctx context.Context, checkerName string, result *checker.Result) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		cnh := &chmv1alpha1.CheckNodeHealth{}
		if err := r.chmClient.Get(ctx, chmclient.ObjectKey{Name: r.crName}, cnh); err != nil {
			return fmt.Errorf("failed to get CheckNodeHealth CR: %w", err)
		}

		upsertResult(&cnh.Status.Results, chmv1alpha1.CheckResult{
			Name:      checkerName,
			Status:    convertStatus(result.Status),
			Message:   result.Detail.Message,
			ErrorCode: result.Detail.Code,
		})

		if err := r.chmClient.Status().Update(ctx, cnh); err != nil {
			return fmt.Errorf("failed to update status: %w", err)
		}
		return nil
	})
}

// upsertResult upserts a result based off name.
func upsertResult(results *[]chmv1alpha1.CheckResult, result chmv1alpha1.CheckResult) {
	for i := range *results {
		if (*results)[i].Name == result.Name {
			(*results)[i] = result
			return
		}
	}
	*results = append(*results, result)
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
