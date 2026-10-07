package checknodehealth

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/gpu"
)

func TestGPUNodeConditions(t *testing.T) {
	t.Parallel()

	healthy := func(name string) chmv1alpha1.CheckResult {
		return chmv1alpha1.CheckResult{Name: name, Status: chmv1alpha1.CheckStatusHealthy, Message: "ok"}
	}
	withCode := func(name string, status chmv1alpha1.CheckStatus, code string) chmv1alpha1.CheckResult {
		return chmv1alpha1.CheckResult{Name: name, Status: status, ErrorCode: code, Message: "detail"}
	}
	lines := func(l ...string) string { return strings.Join(l, "\n") }

	const (
		ncclOK = "NcclAllReduce: ok"
		hostOK = "GpuHostBandwidth: ok"
		peerOK = "GpuPeerBandwidth: ok"
	)
	// The A10 has no NVLink, so peer bandwidth does not run on it.
	a10Checks := []string{CheckerNcclAllReduce, CheckerGpuHostBandwidth}

	type want struct {
		status  corev1.ConditionStatus
		reason  string
		message string
	}

	tests := []struct {
		name    string
		results []chmv1alpha1.CheckResult
		// applicable are the checks that run for the node's SKU. Defaults to every GPU check.
		applicable []string
		// want is exactly the set of GPU conditions expected, keyed by type.
		want map[corev1.NodeConditionType]want
	}{
		{
			name:    "no gpu results publish no gpu conditions",
			results: []chmv1alpha1.CheckResult{healthy("PodStartup"), healthy("PodNetwork")},
			want:    map[corev1.NodeConditionType]want{},
		},
		{
			name: "all gpu checks passing mark every condition healthy",
			results: []chmv1alpha1.CheckResult{
				healthy("PodNetwork"), healthy(CheckerNcclAllReduce), healthy(CheckerGpuHostBandwidth), healthy(CheckerGpuPeerBandwidth),
			},
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy:              {corev1.ConditionTrue, ReasonCheckPassed, lines(ncclOK, hostOK, peerOK)},
				NodeConditionGPUHostBandwidthHealthy:      {corev1.ConditionTrue, ReasonCheckPassed, hostOK},
				NodeConditionGPUPeerBandwidthHealthy:      {corev1.ConditionTrue, ReasonCheckPassed, peerOK},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, ncclOK},
				NodeConditionGPUCorrectnessHealthy:        {corev1.ConditionTrue, ReasonCheckPassed, ncclOK},
			},
		},
		{
			name: "low host bandwidth fails only host bandwidth",
			results: []chmv1alpha1.CheckResult{
				healthy(CheckerNcclAllReduce),
				withCode(CheckerGpuHostBandwidth, chmv1alpha1.CheckStatusUnhealthy, gpu.ErrorCodeLowBandwidth),
				healthy(CheckerGpuPeerBandwidth),
			},
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy: {corev1.ConditionTrue, ReasonCheckPassed,
					lines(ncclOK, "GpuHostBandwidth [BandwidthBelowThreshold]: detail", peerOK)},
				NodeConditionGPUHostBandwidthHealthy: {corev1.ConditionFalse, gpu.ErrorCodeLowBandwidth,
					"GpuHostBandwidth [BandwidthBelowThreshold]: detail"},
				NodeConditionGPUPeerBandwidthHealthy:      {corev1.ConditionTrue, ReasonCheckPassed, peerOK},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, ncclOK},
				NodeConditionGPUCorrectnessHealthy:        {corev1.ConditionTrue, ReasonCheckPassed, ncclOK},
			},
		},
		{
			name: "low peer bandwidth fails only peer bandwidth",
			results: []chmv1alpha1.CheckResult{
				healthy(CheckerNcclAllReduce),
				healthy(CheckerGpuHostBandwidth),
				withCode(CheckerGpuPeerBandwidth, chmv1alpha1.CheckStatusUnhealthy, gpu.ErrorCodeLowBandwidth),
			},
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy: {corev1.ConditionTrue, ReasonCheckPassed,
					lines(ncclOK, hostOK, "GpuPeerBandwidth [BandwidthBelowThreshold]: detail")},
				NodeConditionGPUHostBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, hostOK},
				NodeConditionGPUPeerBandwidthHealthy: {corev1.ConditionFalse, gpu.ErrorCodeLowBandwidth,
					"GpuPeerBandwidth [BandwidthBelowThreshold]: detail"},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, ncclOK},
				NodeConditionGPUCorrectnessHealthy:        {corev1.ConditionTrue, ReasonCheckPassed, ncclOK},
			},
		},
		{
			// Correctness is not tied to low bandwidth, so NCCL failing that way leaves it unknown.
			name: "low nccl bus bandwidth fails all-reduce bandwidth and leaves correctness unknown",
			results: []chmv1alpha1.CheckResult{
				withCode(CheckerNcclAllReduce, chmv1alpha1.CheckStatusUnhealthy, gpu.ErrorCodeLowBandwidth),
				healthy(CheckerGpuHostBandwidth),
				healthy(CheckerGpuPeerBandwidth),
			},
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy: {corev1.ConditionTrue, ReasonCheckPassed,
					lines("NcclAllReduce [BandwidthBelowThreshold]: detail", hostOK, peerOK)},
				NodeConditionGPUHostBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, hostOK},
				NodeConditionGPUPeerBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, peerOK},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionFalse, gpu.ErrorCodeLowBandwidth,
					"NcclAllReduce [BandwidthBelowThreshold]: detail"},
				NodeConditionGPUCorrectnessHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"NcclAllReduce [BandwidthBelowThreshold]: detail"},
			},
		},
		{
			name: "nccl correctness error fails correctness and leaves all-reduce bandwidth unknown",
			results: []chmv1alpha1.CheckResult{
				withCode(CheckerNcclAllReduce, chmv1alpha1.CheckStatusUnhealthy, gpu.ErrorCodeCorrectness),
				healthy(CheckerGpuHostBandwidth),
				healthy(CheckerGpuPeerBandwidth),
			},
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy: {corev1.ConditionTrue, ReasonCheckPassed,
					lines("NcclAllReduce [CorrectnessError]: detail", hostOK, peerOK)},
				NodeConditionGPUHostBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, hostOK},
				NodeConditionGPUPeerBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, peerOK},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"NcclAllReduce [CorrectnessError]: detail"},
				NodeConditionGPUCorrectnessHealthy: {corev1.ConditionFalse, gpu.ErrorCodeCorrectness,
					"NcclAllReduce [CorrectnessError]: detail"},
			},
		},
		{
			// The count mismatch stops every benchmark before it measures anything.
			name: "gpu count mismatch fails count and leaves the rest unknown",
			results: []chmv1alpha1.CheckResult{
				withCode(CheckerNcclAllReduce, chmv1alpha1.CheckStatusUnhealthy, gpu.ErrorCodeUnexpectedGPUCount),
				withCode(CheckerGpuHostBandwidth, chmv1alpha1.CheckStatusUnhealthy, gpu.ErrorCodeUnexpectedGPUCount),
				withCode(CheckerGpuPeerBandwidth, chmv1alpha1.CheckStatusUnhealthy, gpu.ErrorCodeUnexpectedGPUCount),
			},
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy: {corev1.ConditionFalse, gpu.ErrorCodeUnexpectedGPUCount, lines(
					"NcclAllReduce [UnexpectedGPUCount]: detail",
					"GpuHostBandwidth [UnexpectedGPUCount]: detail",
					"GpuPeerBandwidth [UnexpectedGPUCount]: detail")},
				NodeConditionGPUHostBandwidthHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"GpuHostBandwidth [UnexpectedGPUCount]: detail"},
				NodeConditionGPUPeerBandwidthHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"GpuPeerBandwidth [UnexpectedGPUCount]: detail"},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"NcclAllReduce [UnexpectedGPUCount]: detail"},
				NodeConditionGPUCorrectnessHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"NcclAllReduce [UnexpectedGPUCount]: detail"},
			},
		},
		{
			// Every condition NCCL covers needs it to pass, so its tool failure leaves them unknown.
			name: "a tool failure leaves conditions its check covers unknown",
			results: []chmv1alpha1.CheckResult{
				withCode(CheckerNcclAllReduce, chmv1alpha1.CheckStatusUnknown, gpu.ErrorCodeToolFailed),
				healthy(CheckerGpuHostBandwidth),
				healthy(CheckerGpuPeerBandwidth),
			},
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy:              {corev1.ConditionUnknown, ReasonCheckUnknown, "NcclAllReduce [ToolFailed]: detail"},
				NodeConditionGPUHostBandwidthHealthy:      {corev1.ConditionTrue, ReasonCheckPassed, hostOK},
				NodeConditionGPUPeerBandwidthHealthy:      {corev1.ConditionTrue, ReasonCheckPassed, peerOK},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown, "NcclAllReduce [ToolFailed]: detail"},
				NodeConditionGPUCorrectnessHealthy:        {corev1.ConditionUnknown, ReasonCheckUnknown, "NcclAllReduce [ToolFailed]: detail"},
			},
		},
		{
			// A failure is reported even if another check that covers the condition did not finish.
			name: "a failure wins over an unknown result",
			results: []chmv1alpha1.CheckResult{
				withCode(CheckerNcclAllReduce, chmv1alpha1.CheckStatusUnknown, gpu.ErrorCodeToolFailed),
				withCode(CheckerGpuHostBandwidth, chmv1alpha1.CheckStatusUnhealthy, gpu.ErrorCodeUnexpectedGPUCount),
				healthy(CheckerGpuPeerBandwidth),
			},
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy: {corev1.ConditionFalse, gpu.ErrorCodeUnexpectedGPUCount,
					"GpuHostBandwidth [UnexpectedGPUCount]: detail"},
				NodeConditionGPUHostBandwidthHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"GpuHostBandwidth [UnexpectedGPUCount]: detail"},
				NodeConditionGPUPeerBandwidthHealthy:      {corev1.ConditionTrue, ReasonCheckPassed, peerOK},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown, "NcclAllReduce [ToolFailed]: detail"},
				NodeConditionGPUCorrectnessHealthy:        {corev1.ConditionUnknown, ReasonCheckUnknown, "NcclAllReduce [ToolFailed]: detail"},
			},
		},
		{
			// A check with no result at all is treated like one that never finished.
			name:    "a check missing from the results leaves conditions it covers unknown",
			results: []chmv1alpha1.CheckResult{healthy(CheckerNcclAllReduce), healthy(CheckerGpuHostBandwidth)},
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"GpuPeerBandwidth [CheckNotReported]: no result reported"},
				NodeConditionGPUHostBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, hostOK},
				NodeConditionGPUPeerBandwidthHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"GpuPeerBandwidth [CheckNotReported]: no result reported"},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, ncclOK},
				NodeConditionGPUCorrectnessHealthy:        {corev1.ConditionTrue, ReasonCheckPassed, ncclOK},
			},
		},
		{
			name:       "a sku without a check leaves its condition off",
			results:    []chmv1alpha1.CheckResult{healthy(CheckerNcclAllReduce), healthy(CheckerGpuHostBandwidth)},
			applicable: a10Checks,
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy:              {corev1.ConditionTrue, ReasonCheckPassed, lines(ncclOK, hostOK)},
				NodeConditionGPUHostBandwidthHealthy:      {corev1.ConditionTrue, ReasonCheckPassed, hostOK},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, ncclOK},
				NodeConditionGPUCorrectnessHealthy:        {corev1.ConditionTrue, ReasonCheckPassed, ncclOK},
			},
		},
		{
			// Only the checks that run for the SKU count, so a stray result cannot add or fail a condition.
			name: "a result from a check the sku does not run is ignored",
			results: []chmv1alpha1.CheckResult{
				healthy(CheckerNcclAllReduce),
				healthy(CheckerGpuHostBandwidth),
				withCode(CheckerGpuPeerBandwidth, chmv1alpha1.CheckStatusUnhealthy, gpu.ErrorCodeUnexpectedGPUCount),
			},
			applicable: a10Checks,
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy:              {corev1.ConditionTrue, ReasonCheckPassed, lines(ncclOK, hostOK)},
				NodeConditionGPUHostBandwidthHealthy:      {corev1.ConditionTrue, ReasonCheckPassed, hostOK},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, ncclOK},
				NodeConditionGPUCorrectnessHealthy:        {corev1.ConditionTrue, ReasonCheckPassed, ncclOK},
			},
		},
		{
			name: "unreported gpu checks leave every condition unknown",
			results: []chmv1alpha1.CheckResult{
				withCode(CheckerNcclAllReduce, chmv1alpha1.CheckStatusUnknown, ErrorCodeCheckNotReported),
				withCode(CheckerGpuHostBandwidth, chmv1alpha1.CheckStatusUnknown, gpu.ErrorCodeUnknownSKU),
				withCode(CheckerGpuPeerBandwidth, chmv1alpha1.CheckStatusUnknown, gpu.ErrorCodeUnknownSKU),
			},
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown, lines(
					"NcclAllReduce [CheckNotReported]: detail",
					"GpuHostBandwidth [UnknownSKU]: detail",
					"GpuPeerBandwidth [UnknownSKU]: detail")},
				NodeConditionGPUHostBandwidthHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"GpuHostBandwidth [UnknownSKU]: detail"},
				NodeConditionGPUPeerBandwidthHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"GpuPeerBandwidth [UnknownSKU]: detail"},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"NcclAllReduce [CheckNotReported]: detail"},
				NodeConditionGPUCorrectnessHealthy: {corev1.ConditionUnknown, ReasonCheckUnknown,
					"NcclAllReduce [CheckNotReported]: detail"},
			},
		},
		{
			name: "a result without a message falls back to its status",
			results: []chmv1alpha1.CheckResult{
				{Name: CheckerNcclAllReduce, Status: chmv1alpha1.CheckStatusHealthy},
				{Name: CheckerGpuHostBandwidth, Status: chmv1alpha1.CheckStatusHealthy},
			},
			applicable: a10Checks,
			want: map[corev1.NodeConditionType]want{
				NodeConditionGPUCountHealthy: {corev1.ConditionTrue, ReasonCheckPassed,
					"NcclAllReduce: Healthy\nGpuHostBandwidth: Healthy"},
				NodeConditionGPUHostBandwidthHealthy:      {corev1.ConditionTrue, ReasonCheckPassed, "GpuHostBandwidth: Healthy"},
				NodeConditionGPUAllReduceBandwidthHealthy: {corev1.ConditionTrue, ReasonCheckPassed, "NcclAllReduce: Healthy"},
				NodeConditionGPUCorrectnessHealthy:        {corev1.ConditionTrue, ReasonCheckPassed, "NcclAllReduce: Healthy"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			applicable := tt.applicable
			if applicable == nil {
				applicable = gpuCheckerNames
			}

			got := map[corev1.NodeConditionType]want{}
			for _, c := range gpuNodeConditions(tt.results, applicable) {
				if _, dup := got[c.Type]; dup {
					t.Errorf("condition %s published more than once", c.Type)
				}
				got[c.Type] = want{c.Status, c.Reason, c.Message}
			}

			if len(got) != len(tt.want) {
				t.Errorf("got %d conditions %v, want %d %v", len(got), got, len(tt.want), tt.want)
			}
			for conditionType, w := range tt.want {
				g, ok := got[conditionType]
				if !ok {
					t.Errorf("missing condition %s", conditionType)
					continue
				}
				if g != w {
					t.Errorf("%s = %+v, want %+v", conditionType, g, w)
				}
			}
		})
	}
}

func TestAssessFuncs(t *testing.T) {
	t.Parallel()

	const (
		ownCode   = "OwnCode"
		otherCode = "OtherCode"
	)
	result := func(status chmv1alpha1.CheckStatus, code string) chmv1alpha1.CheckResult {
		return chmv1alpha1.CheckResult{Name: "Check", Status: status, ErrorCode: code}
	}

	tests := []struct {
		name             string
		result           chmv1alpha1.CheckResult
		wantFailsOn      corev1.ConditionStatus
		wantCheckedFirst corev1.ConditionStatus
	}{
		{
			name:             "healthy passes",
			result:           result(chmv1alpha1.CheckStatusHealthy, ""),
			wantFailsOn:      corev1.ConditionTrue,
			wantCheckedFirst: corev1.ConditionTrue,
		},
		{
			name:             "own code fails",
			result:           result(chmv1alpha1.CheckStatusUnhealthy, ownCode),
			wantFailsOn:      corev1.ConditionFalse,
			wantCheckedFirst: corev1.ConditionFalse,
		},
		{
			name:             "failure with another code",
			result:           result(chmv1alpha1.CheckStatusUnhealthy, otherCode),
			wantFailsOn:      corev1.ConditionUnknown,
			wantCheckedFirst: corev1.ConditionTrue,
		},
		{
			name:             "unknown result stays unknown",
			result:           result(chmv1alpha1.CheckStatusUnknown, otherCode),
			wantFailsOn:      corev1.ConditionUnknown,
			wantCheckedFirst: corev1.ConditionUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := failsOn(ownCode)(tt.result); got != tt.wantFailsOn {
				t.Errorf("failsOn = %s, want %s", got, tt.wantFailsOn)
			}
			if got := checkedFirst(ownCode)(tt.result); got != tt.wantCheckedFirst {
				t.Errorf("checkedFirst = %s, want %s", got, tt.wantCheckedFirst)
			}
		})
	}
}

// TestGPUConditionBuild shows the message format: one line per check, as "<check> [<error code>]: <message>".
func TestGPUConditionBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		condition corev1.NodeConditionType
		status    corev1.ConditionStatus
		reason    string
		evidence  []chmv1alpha1.CheckResult
		want      corev1.NodeCondition
	}{
		{
			name:      "passing check",
			condition: NodeConditionGPUAllReduceBandwidthHealthy,
			status:    corev1.ConditionTrue,
			reason:    ReasonCheckPassed,
			evidence: []chmv1alpha1.CheckResult{{
				Name:    CheckerNcclAllReduce,
				Status:  chmv1alpha1.CheckStatusHealthy,
				Message: "bus bandwidth 480.000 GB/s (>= 460.000 GB/s threshold for Standard_ND96isr_H100_v5)",
			}},
			want: corev1.NodeCondition{
				Type:    NodeConditionGPUAllReduceBandwidthHealthy,
				Status:  corev1.ConditionTrue,
				Reason:  ReasonCheckPassed,
				Message: `NcclAllReduce: bus bandwidth 480.000 GB/s (>= 460.000 GB/s threshold for Standard_ND96isr_H100_v5)`,
			},
		},
		{
			name:      "failing check shows its error code",
			condition: NodeConditionGPUCorrectnessHealthy,
			status:    corev1.ConditionFalse,
			reason:    gpu.ErrorCodeCorrectness,
			evidence: []chmv1alpha1.CheckResult{{
				Name:      CheckerNcclAllReduce,
				Status:    chmv1alpha1.CheckStatusUnhealthy,
				ErrorCode: gpu.ErrorCodeCorrectness,
				Message:   "NCCL all-reduce reported 3 out-of-bounds values",
			}},
			want: corev1.NodeCondition{
				Type:    NodeConditionGPUCorrectnessHealthy,
				Status:  corev1.ConditionFalse,
				Reason:  gpu.ErrorCodeCorrectness,
				Message: `NcclAllReduce [CorrectnessError]: NCCL all-reduce reported 3 out-of-bounds values`,
			},
		},
		{
			name:      "every check behind the verdict gets its own line",
			condition: NodeConditionGPUCountHealthy,
			status:    corev1.ConditionFalse,
			reason:    gpu.ErrorCodeUnexpectedGPUCount,
			evidence: []chmv1alpha1.CheckResult{
				{
					Name:      CheckerNcclAllReduce,
					Status:    chmv1alpha1.CheckStatusUnhealthy,
					ErrorCode: gpu.ErrorCodeUnexpectedGPUCount,
					Message:   "found 7 of 8 GPUs expected for Standard_ND96isr_H100_v5",
				},
				{
					Name:      CheckerGpuHostBandwidth,
					Status:    chmv1alpha1.CheckStatusUnhealthy,
					ErrorCode: gpu.ErrorCodeUnexpectedGPUCount,
					Message:   "found 7 of 8 GPUs expected for Standard_ND96isr_H100_v5",
				},
			},
			want: corev1.NodeCondition{
				Type:   NodeConditionGPUCountHealthy,
				Status: corev1.ConditionFalse,
				Reason: gpu.ErrorCodeUnexpectedGPUCount,
				Message: `NcclAllReduce [UnexpectedGPUCount]: found 7 of 8 GPUs expected for Standard_ND96isr_H100_v5
GpuHostBandwidth [UnexpectedGPUCount]: found 7 of 8 GPUs expected for Standard_ND96isr_H100_v5`,
			},
		},
		{
			// The check's own message is used as-is, so a multi-line one continues on unprefixed lines.
			name:      "multi-line check message",
			condition: NodeConditionGPUHostBandwidthHealthy,
			status:    corev1.ConditionFalse,
			reason:    gpu.ErrorCodeLowBandwidth,
			evidence: []chmv1alpha1.CheckResult{{
				Name:      CheckerGpuHostBandwidth,
				Status:    chmv1alpha1.CheckStatusUnhealthy,
				ErrorCode: gpu.ErrorCodeLowBandwidth,
				Message: "host_to_device_memcpy_ce: min 20.000 GB/s at GPU 5 below 48.000 GB/s threshold\n" +
					"device_to_host_memcpy_ce: min 55.000 GB/s at GPU 0 (>= 48.000 GB/s threshold)",
			}},
			want: corev1.NodeCondition{
				Type:   NodeConditionGPUHostBandwidthHealthy,
				Status: corev1.ConditionFalse,
				Reason: gpu.ErrorCodeLowBandwidth,
				Message: `GpuHostBandwidth [BandwidthBelowThreshold]: host_to_device_memcpy_ce: min 20.000 GB/s at GPU 5 below 48.000 GB/s threshold
device_to_host_memcpy_ce: min 55.000 GB/s at GPU 0 (>= 48.000 GB/s threshold)`,
			},
		},
		{
			name:      "check without a message falls back to its status",
			condition: NodeConditionGPUPeerBandwidthHealthy,
			status:    corev1.ConditionTrue,
			reason:    ReasonCheckPassed,
			evidence:  []chmv1alpha1.CheckResult{{Name: CheckerGpuPeerBandwidth, Status: chmv1alpha1.CheckStatusHealthy}},
			want: corev1.NodeCondition{
				Type:    NodeConditionGPUPeerBandwidthHealthy,
				Status:  corev1.ConditionTrue,
				Reason:  ReasonCheckPassed,
				Message: `GpuPeerBandwidth: Healthy`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := gpuCondition{conditionType: tt.condition}.build(tt.status, tt.reason, tt.evidence)
			if got != tt.want {
				t.Errorf("build() =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}
