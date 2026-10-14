package nodecheckerrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	chmclient "sigs.k8s.io/controller-runtime/pkg/client"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
)

// CheckerDurationsAnnotation is the CheckNodeHealth annotation the checker pod records how long each of
// its checks took in, so the controller can time them. Its value is a JSON object of check name to
// duration, such as {"PodNetwork":"3.1s"}.
const CheckerDurationsAnnotation = "clusterhealthmonitor.azure.com/checker-durations"

// recordDuration adds how long a check took to the CR's CheckerDurationsAnnotation.
//
// The checker is the only writer of the annotation, and holds every duration it has recorded, so
// each write replaces the whole value rather than reading and merging the one on the CR.
func (r *Runner) recordDuration(ctx context.Context, checkerName string, duration time.Duration) error {
	if r.checkerDurations == nil {
		r.checkerDurations = map[string]metav1.Duration{}
	}
	r.checkerDurations[checkerName] = metav1.Duration{Duration: duration}

	value, err := json.Marshal(r.checkerDurations)
	if err != nil {
		return fmt.Errorf("failed to encode durations: %w", err)
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{CheckerDurationsAnnotation: string(value)},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to encode durations patch: %w", err)
	}

	cnh := &chmv1alpha1.CheckNodeHealth{ObjectMeta: metav1.ObjectMeta{Name: r.crName}}
	if err := r.chmClient.Patch(ctx, cnh, chmclient.RawPatch(types.MergePatchType, patch)); err != nil {
		return fmt.Errorf("failed to patch durations annotation: %w", err)
	}
	return nil
}

// ParseCheckerDurations returns how long each check took, from a CheckNodeHealth's
// CheckerDurationsAnnotation. It returns nil when the annotation is absent.
func ParseCheckerDurations(annotations map[string]string) (map[string]time.Duration, error) {
	value, ok := annotations[CheckerDurationsAnnotation]
	if !ok {
		return nil, nil
	}

	var encoded map[string]metav1.Duration
	if err := json.Unmarshal([]byte(value), &encoded); err != nil {
		return nil, fmt.Errorf("failed to parse %s annotation: %w", CheckerDurationsAnnotation, err)
	}
	durations := make(map[string]time.Duration, len(encoded))
	for name, duration := range encoded {
		durations[name] = duration.Duration
	}
	return durations, nil
}
