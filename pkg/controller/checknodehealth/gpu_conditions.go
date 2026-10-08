package checknodehealth

import (
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/gpu"
)

const (
	// NodeConditionGPUCountHealthy is False when the node exposes a different number of GPUs than its SKU should have.
	NodeConditionGPUCountHealthy corev1.NodeConditionType = nodeConditionPrefix + "GPUCountHealthy"
	// NodeConditionGPUHostBandwidthHealthy is False when host<->device copy bandwidth is below the expected threshold.
	NodeConditionGPUHostBandwidthHealthy corev1.NodeConditionType = nodeConditionPrefix + "GPUHostBandwidthHealthy"
	// NodeConditionGPUPeerBandwidthHealthy is False when device<->device copy bandwidth is below the expected threshold.
	NodeConditionGPUPeerBandwidthHealthy corev1.NodeConditionType = nodeConditionPrefix + "GPUPeerBandwidthHealthy"
	// NodeConditionGPUAllReduceBandwidthHealthy is False when NCCL all-reduce bus bandwidth is below the expected threshold.
	NodeConditionGPUAllReduceBandwidthHealthy corev1.NodeConditionType = nodeConditionPrefix + "GPUAllReduceBandwidthHealthy"
	// NodeConditionGPUCorrectnessHealthy is False when a benchmark reports incorrect GPU computation results.
	NodeConditionGPUCorrectnessHealthy corev1.NodeConditionType = nodeConditionPrefix + "GPUCorrectnessHealthy"
)

// assessFunc reads one check result as passing (True), failing (False) or saying nothing about
// (Unknown) a condition. It only judges the single result; gpuNodeConditions handles missing results
// and combines the verdicts.
type assessFunc func(chmv1alpha1.CheckResult) corev1.ConditionStatus

// gpuCondition maps GPU check results onto a single Node condition.
type gpuCondition struct {
	conditionType corev1.NodeConditionType
	// checkerNames are the checks whose results can pass or fail this condition.
	checkerNames []string
	assess       assessFunc
}

var gpuConditions = []gpuCondition{
	{
		conditionType: NodeConditionGPUCountHealthy,
		checkerNames:  gpuCheckerNames,
		// Every GPU benchmark verifies the GPU count before it measures anything.
		assess: checkedFirst(gpu.ErrorCodeUnexpectedGPUCount),
	},
	{
		conditionType: NodeConditionGPUHostBandwidthHealthy,
		checkerNames:  []string{CheckerGpuHostBandwidth},
		assess:        failsOn(gpu.ErrorCodeLowBandwidth),
	},
	{
		conditionType: NodeConditionGPUPeerBandwidthHealthy,
		checkerNames:  []string{CheckerGpuPeerBandwidth},
		assess:        failsOn(gpu.ErrorCodeLowBandwidth),
	},
	{
		conditionType: NodeConditionGPUAllReduceBandwidthHealthy,
		checkerNames:  []string{CheckerNcclAllReduce},
		assess:        failsOn(gpu.ErrorCodeLowBandwidth),
	},
	{
		conditionType: NodeConditionGPUCorrectnessHealthy,
		checkerNames:  []string{CheckerNcclAllReduce},
		assess:        assessCorrectness,
	},
}

// failsOn is the default rule: a healthy result passes the condition, a result with one of codes fails
// it, and anything else leaves it Unknown.
func failsOn(codes ...string) assessFunc {
	return func(result chmv1alpha1.CheckResult) corev1.ConditionStatus {
		switch {
		case result.Status == chmv1alpha1.CheckStatusHealthy:
			return corev1.ConditionTrue
		case slices.Contains(codes, result.ErrorCode):
			return corev1.ConditionFalse
		default:
			return corev1.ConditionUnknown
		}
	}
}

// checkedFirst is failsOn for a condition every check verifies before anything else, such as the GPU
// count. A failure with any other code therefore passes it, but an Unknown result stays Unknown since
// the check may have stopped mid-verification.
func checkedFirst(codes ...string) assessFunc {
	return func(result chmv1alpha1.CheckResult) corev1.ConditionStatus {
		switch {
		case result.Status == chmv1alpha1.CheckStatusHealthy:
			return corev1.ConditionTrue
		case slices.Contains(codes, result.ErrorCode):
			return corev1.ConditionFalse
		case result.Status == chmv1alpha1.CheckStatusUnhealthy:
			return corev1.ConditionTrue
		default:
			return corev1.ConditionUnknown
		}
	}
}

// assessCorrectness is failsOn(CorrectnessError) plus an NCCL special case that relies on the order
// its parser checks results in.
func assessCorrectness(result chmv1alpha1.CheckResult) corev1.ConditionStatus {
	// If the NCCL all-reduce check reports low bandwidth, it means correctness has passed, so the condition is True.
	if result.Name == CheckerNcclAllReduce &&
		result.Status == chmv1alpha1.CheckStatusUnhealthy &&
		result.ErrorCode == gpu.ErrorCodeLowBandwidth {
		return corev1.ConditionTrue
	}
	return failsOn(gpu.ErrorCodeCorrectness)(result)
}

// gpuNodeConditions builds the GPU conditions for a node. It returns none if no GPU check reported,
// meaning the GPU checks did not run. Otherwise:
//   - Only the checks that run for the node's SKU, are considered. For example, the peer bandwidth
//     condition on a SKU without NVLink is ignored.
//   - A condition is False if any associated check failed it, True if every check passed it, and Unknown
//     otherwise.
//   - A check with no result counts as Unknown, so an incomplete run never marks a condition healthy.
func gpuNodeConditions(results []chmv1alpha1.CheckResult, applicable []string) []corev1.NodeCondition {
	// The GPU checks did not run on this node, so it has nothing to say about any GPU condition.
	if !slices.ContainsFunc(results, func(r chmv1alpha1.CheckResult) bool { return slices.Contains(gpuCheckerNames, r.Name) }) {
		return nil
	}

	conditions := make([]corev1.NodeCondition, 0, len(gpuConditions))
	for _, c := range gpuConditions {
		checkerNames := slices.DeleteFunc(slices.Clone(c.checkerNames), func(name string) bool {
			return !slices.Contains(applicable, name)
		})
		// The condition does not apply to this SKU, e.g. peer bandwidth on a SKU without NVLink.
		if len(checkerNames) == 0 {
			continue
		}

		byStatus := map[corev1.ConditionStatus][]chmv1alpha1.CheckResult{}
		for _, name := range checkerNames {
			i := slices.IndexFunc(results, func(r chmv1alpha1.CheckResult) bool { return r.Name == name })
			if i < 0 {
				byStatus[corev1.ConditionUnknown] = append(byStatus[corev1.ConditionUnknown], chmv1alpha1.CheckResult{
					Name:      name,
					Status:    chmv1alpha1.CheckStatusUnknown,
					ErrorCode: ErrorCodeCheckNotReported,
					Message:   "no result reported",
				})
				continue
			}
			status := c.assess(results[i])
			byStatus[status] = append(byStatus[status], results[i])
		}

		var condition corev1.NodeCondition
		switch {
		case len(byStatus[corev1.ConditionFalse]) > 0:
			unhealthy := byStatus[corev1.ConditionFalse]
			condition = c.build(corev1.ConditionFalse, unhealthy[0].ErrorCode, unhealthy)
		case len(byStatus[corev1.ConditionUnknown]) > 0:
			condition = c.build(corev1.ConditionUnknown, ReasonCheckUnknown, byStatus[corev1.ConditionUnknown])
		default:
			condition = c.build(corev1.ConditionTrue, ReasonCheckPassed, byStatus[corev1.ConditionTrue])
		}
		conditions = append(conditions, condition)
	}
	return conditions
}

// build creates the condition, with a message naming the checks behind the verdict, e.g.:
//
//	NcclAllReduce [CorrectnessError]: NCCL all-reduce reported 3 out-of-bounds values
func (c gpuCondition) build(status corev1.ConditionStatus, reason string, evidence []chmv1alpha1.CheckResult) corev1.NodeCondition {
	lines := make([]string, 0, len(evidence))
	for _, result := range evidence {
		line := result.Name
		if result.ErrorCode != "" {
			line += fmt.Sprintf(" [%s]", result.ErrorCode)
		}
		detail := result.Message
		if detail == "" {
			detail = string(result.Status)
		}
		lines = append(lines, line+": "+detail)
	}
	return corev1.NodeCondition{
		Type:    c.conditionType,
		Status:  status,
		Reason:  reason,
		Message: strings.Join(lines, "\n"),
	}
}
