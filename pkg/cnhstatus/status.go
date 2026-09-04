// Package cnhstatus provides concurrency-safe writes of check results into a
// CheckNodeHealth status. Multiple checker pods and the controller report into the
// same status.results list, so writes must be an upsert under optimistic concurrency
// rather than a blind read-append-update.
package cnhstatus

import (
	"context"

	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

// UpsertResults writes results into the named CheckNodeHealth status, keyed by result name.
// The CR is refetched on every attempt so a conflicting writer's results are preserved.
func UpsertResults(ctx context.Context, c client.Client, crName string, results ...chmv1alpha1.CheckResult) error {
	if len(results) == 0 {
		return nil
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cnh := &chmv1alpha1.CheckNodeHealth{}
		if err := c.Get(ctx, client.ObjectKey{Name: crName}, cnh); err != nil {
			return err
		}
		for _, result := range results {
			UpsertResult(&cnh.Status, result)
		}
		return c.Status().Update(ctx, cnh)
	})
}

// FillMissingResults records each default only if no result with that name exists yet. The CR is
// refetched on every attempt, so a result the checker pod wrote after the caller's own read is
// still observed and left untouched.
func FillMissingResults(ctx context.Context, c client.Client, crName string, defaults ...chmv1alpha1.CheckResult) ([]chmv1alpha1.CheckResult, error) {
	var added []chmv1alpha1.CheckResult

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		added = nil
		cnh := &chmv1alpha1.CheckNodeHealth{}
		if err := c.Get(ctx, client.ObjectKey{Name: crName}, cnh); err != nil {
			return err
		}
		for _, result := range defaults {
			if HasResult(&cnh.Status, result.Name) {
				continue
			}
			UpsertResult(&cnh.Status, result)
			added = append(added, result)
		}
		if len(added) == 0 {
			return nil
		}
		return c.Status().Update(ctx, cnh)
	})

	return added, err
}

// UpsertResult replaces the result carrying the same name, or appends it when absent.
func UpsertResult(status *chmv1alpha1.CheckNodeHealthStatus, result chmv1alpha1.CheckResult) {
	for i := range status.Results {
		if status.Results[i].Name == result.Name {
			status.Results[i] = result
			return
		}
	}
	status.Results = append(status.Results, result)
}

// HasResult reports whether a result with the given name has been recorded.
func HasResult(status *chmv1alpha1.CheckNodeHealthStatus, name string) bool {
	for i := range status.Results {
		if status.Results[i].Name == name {
			return true
		}
	}
	return false
}
