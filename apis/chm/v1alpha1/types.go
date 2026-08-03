package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cnh
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.nodeRef.name`
// +kubebuilder:printcolumn:name="Healthy",type=string,JSONPath=`.status.conditions[?(@.type=="Healthy")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CheckNodeHealth is a one-time health check resource for a specific node.
// When created, the controller runs health checks on the target node and updates
// the status with results. The resource is not modified after completion.
type CheckNodeHealth struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CheckNodeHealthSpec   `json:"spec,omitempty"`
	Status CheckNodeHealthStatus `json:"status,omitempty"`
}

// CheckNodeHealthSpec defines the desired state of CheckNodeHealth
type CheckNodeHealthSpec struct {
	// NodeRef references the node to check
	// +required
	NodeRef NodeReference `json:"nodeRef"`

	// Checks optionally requests additional, on-demand checks to run on the node,
	// beyond the controller's built-in checks. Each entry runs as its own pod on the
	// target node and produces a CheckResult with the same Name in the status.
	//
	// This is the extension point for intrusive checks (e.g. GPU diagnostics): the
	// requester supplies the image and parameters; the controller owns the pod's
	// security/scheduling shape via Profile.
	// +optional
	// +listType=map
	// +listMapKey=name
	Checks []NodeCheckSpec `json:"checks,omitempty"`
}

// NodeCheckSpec defines a single on-demand check to run on the target node.
type NodeCheckSpec struct {
	// Name uniquely identifies this check within the request and is echoed back on the
	// corresponding CheckResult.Name so results correlate to requests.
	// +required
	// +kubebuilder:validation:Pattern=`^[A-Z][a-zA-Z0-9]*$`
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Profile selects a controller-defined execution profile (pod security context, device
	// requests, host mounts, tolerations, isolation, default image, and default timeout).
	// Requesters cannot craft an arbitrary privileged pod; they may only select a profile
	// the controller supports.
	// +required
	// +kubebuilder:validation:Enum=GPUIntrusive
	Profile NodeCheckProfile `json:"profile"`

	// Image optionally overrides the container image for this check. When empty, the
	// controller-configured default image for the selected Profile is used. Must be a
	// pinned, digest- or tag-referenced image the cluster is allowed to pull.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Image string `json:"image,omitempty"`

	// Args are appended to the check container's command, allowing per-request
	// customization such as test selection or run level.
	// +optional
	Args []string `json:"args,omitempty"`

	// Timeout bounds how long the check may run before it is recorded as a timeout
	// (Status=Unknown, ErrorCode=Timeout). Defaults to the profile's controller-configured
	// timeout.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Preemption selects whether an arriving customer GPU workload may preempt (evict) this
	// check. On-demand/investigative checks typically run to completion (NonPreemptible),
	// while periodic/background checks yield to customer work (Preemptible). Either way the
	// check remains bounded by Timeout.
	// +optional
	// +kubebuilder:validation:Enum=Preemptible;NonPreemptible
	// +kubebuilder:default=NonPreemptible
	Preemption CheckPreemption `json:"preemption,omitempty"`

	// Enforcement controls whether this check's result contributes to the node's overall
	// Healthy verdict. Advisory (default) records the result but excludes it from the
	// rollup. Enforcing lets the result flip Healthy.
	// +optional
	// +kubebuilder:validation:Enum=Advisory;Enforcing
	// +kubebuilder:default=Advisory
	Enforcement CheckEnforcement `json:"enforcement,omitempty"`
}

// NodeCheckProfile is a controller-known execution profile for an on-demand check.
type NodeCheckProfile string

const (
	// NodeCheckProfileGPUIntrusive runs a privileged, all-GPU-requesting pod (tolerating
	// GPU taints, with the host mounts and isolation the intrusive GPU engines need).
	NodeCheckProfileGPUIntrusive NodeCheckProfile = "GPUIntrusive"
)

// CheckEnforcement selects whether a check result affects the overall Healthy verdict.
type CheckEnforcement string

const (
	// CheckEnforcementAdvisory records the result but excludes it from the Healthy rollup.
	CheckEnforcementAdvisory CheckEnforcement = "Advisory"
	// CheckEnforcementEnforcing lets the result contribute to the Healthy rollup.
	CheckEnforcementEnforcing CheckEnforcement = "Enforcing"
)

// CheckPreemption selects whether a customer GPU workload may preempt a check.
type CheckPreemption string

const (
	// CheckPreemptible runs the check at low priority; it yields to customer work.
	CheckPreemptible CheckPreemption = "Preemptible"
	// CheckNonPreemptible runs the check to completion (still Timeout-bounded).
	CheckNonPreemptible CheckPreemption = "NonPreemptible"
)

// NodeReference contains a reference to a node
type NodeReference struct {
	// Name is the name of the node
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// CheckNodeHealthStatus defines the observed state of CheckNodeHealth
type CheckNodeHealthStatus struct {
	// StartedAt is the timestamp when the health checks started
	// +required
	StartedAt *metav1.Time `json:"startedAt"`

	// FinishedAt is the timestamp when the health checks completed
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// Conditions represent the latest available observations of the check's current state
	// +optional
	Conditions []metav1.Condition `json:"conditions"`

	// Results contains the individual check results
	// +optional
	Results []CheckResult `json:"results,omitempty"`
}

// NodeHealthConditionType represents the type of condition
type NodeHealthConditionType string

const (
	// NodeHealthConditionHealthy is the condition type used to report the overall health status of the node
	// The condition's Status field will be True/False/Unknown to indicate the actual health state
	NodeHealthConditionHealthy NodeHealthConditionType = "Healthy"
)

// CheckStatus represents the health status of a check
type CheckStatus string

const (
	// CheckStatusHealthy indicates the check passed
	CheckStatusHealthy CheckStatus = "Healthy"

	// CheckStatusUnhealthy indicates the check failed
	CheckStatusUnhealthy CheckStatus = "Unhealthy"

	// CheckStatusUnknown indicates the check is in an unknown state
	CheckStatusUnknown CheckStatus = "Unknown"
)

// CheckResult represents the result of a single health check
type CheckResult struct {
	// Name is the specific instance name of the health check
	// For example: "PodStartup", "PodNetwork"
	// +required
	// +kubebuilder:validation:Pattern=`^[A-Z][a-zA-Z0-9]*$`
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Status is the health status of this check
	// +required
	// +kubebuilder:validation:Enum=Healthy;Unhealthy;Unknown
	Status CheckStatus `json:"status"`

	// Message provides additional details about the check result
	// +optional
	// +kubebuilder:validation:MaxLength=32768
	Message string `json:"message,omitempty"`

	// ErrorCode is the specific error code if the status is not Healthy
	// +optional
	// +kubebuilder:validation:Pattern=`^[A-Z][a-zA-Z0-9]*$`
	// +kubebuilder:validation:MaxLength=253
	ErrorCode string `json:"errorCode,omitempty"`

	// SubResults optionally breaks this check into its constituent sub-checks (e.g. GPU
	// diagnostic plugins), each with its own status and detail. This preserves granular
	// diagnostics on the CR after the check pod is deleted.
	// +optional
	// +listType=map
	// +listMapKey=name
	SubResults []SubCheckResult `json:"subResults,omitempty"`
}

// SubCheckResult is one granular sub-check within a CheckResult.
type SubCheckResult struct {
	// Name identifies the sub-check within the parent CheckResult.
	// +required
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Status is the health status of this sub-check.
	// +required
	// +kubebuilder:validation:Enum=Healthy;Unhealthy;Unknown
	Status CheckStatus `json:"status"`

	// Message provides human-readable detail about the sub-check result.
	// +optional
	// +kubebuilder:validation:MaxLength=32768
	Message string `json:"message,omitempty"`

	// ErrorCode is a stable, machine-readable code when Status is not Healthy.
	// +optional
	// +kubebuilder:validation:Pattern=`^[A-Z][a-zA-Z0-9]*$`
	// +kubebuilder:validation:MaxLength=253
	ErrorCode string `json:"errorCode,omitempty"`

	// Observations optionally carries measured values the check emitted, e.g.
	// {"bandwidthGBps": "335", "eccErrors": "0"}.
	// +optional
	Observations map[string]string `json:"observations,omitempty"`
}

// +kubebuilder:object:root=true

// CheckNodeHealthList contains a list of CheckNodeHealth resources
type CheckNodeHealthList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CheckNodeHealth `json:"items"`
}
