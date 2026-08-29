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

	// CheckSuite selects the controller-owned set of checks and execution environment.
	// It defaults to Core for backward compatibility.
	// +optional
	// +kubebuilder:validation:Enum=Core;GPU
	// +kubebuilder:default=Core
	CheckSuite CheckSuite `json:"checkSuite,omitempty"`

	// FailureAction controls how a failed check affects the node's health condition.
	// It defaults to MarkNodeUnhealthy to preserve existing behavior.
	// +optional
	// +kubebuilder:validation:Enum=ReportOnly;MarkNodeUnhealthy
	// +kubebuilder:default=MarkNodeUnhealthy
	FailureAction CheckFailureAction `json:"failureAction,omitempty"`

	// DisruptionPolicy controls whether the check must finish or may yield to other
	// workloads. It defaults to RunToCompletion.
	// +optional
	// +kubebuilder:validation:Enum=RunToCompletion;YieldToWorkloads
	// +kubebuilder:default=RunToCompletion
	DisruptionPolicy CheckDisruptionPolicy `json:"disruptionPolicy,omitempty"`
}

// CheckSuite selects a controller-owned set of checks and execution environment.
type CheckSuite string

const (
	// CheckSuiteCore runs the built-in node health checks.
	CheckSuiteCore CheckSuite = "Core"
	// CheckSuiteGPU reserves GPU resources and configures the pod for GPU checks.
	CheckSuiteGPU CheckSuite = "GPU"
)

// CheckFailureAction controls how a failed check affects the node's health condition.
type CheckFailureAction string

const (
	// CheckFailureActionReportOnly records the failure without marking the node unhealthy.
	CheckFailureActionReportOnly CheckFailureAction = "ReportOnly"
	// CheckFailureActionMarkNodeUnhealthy marks the node unhealthy when the check fails.
	CheckFailureActionMarkNodeUnhealthy CheckFailureAction = "MarkNodeUnhealthy"
)

// CheckDisruptionPolicy controls whether a check may yield to other workloads.
type CheckDisruptionPolicy string

const (
	// CheckDisruptionPolicyRunToCompletion runs the check without voluntary interruption.
	CheckDisruptionPolicyRunToCompletion CheckDisruptionPolicy = "RunToCompletion"
	// CheckDisruptionPolicyYieldToWorkloads allows higher-priority workloads to displace the check.
	CheckDisruptionPolicyYieldToWorkloads CheckDisruptionPolicy = "YieldToWorkloads"
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

	// SubResults contains results for the component checks performed by this check.
	// +optional
	// +listType=map
	// +listMapKey=name
	SubResults []SubCheckResult `json:"subResults,omitempty"`
}

// SubCheckResult describes one component of a check result.
type SubCheckResult struct {
	// Name uniquely identifies the component within its parent result.
	// +required
	// +kubebuilder:validation:Pattern=`^[A-Z][a-zA-Z0-9]*$`
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Status is the health status of this component.
	// +required
	// +kubebuilder:validation:Enum=Healthy;Unhealthy;Unknown
	Status CheckStatus `json:"status"`

	// Message provides additional details about the component result.
	// +optional
	// +kubebuilder:validation:MaxLength=32768
	Message string `json:"message,omitempty"`

	// ErrorCode is the specific error code if the status is not Healthy.
	// +optional
	// +kubebuilder:validation:Pattern=`^[A-Z][a-zA-Z0-9]*$`
	// +kubebuilder:validation:MaxLength=253
	ErrorCode string `json:"errorCode,omitempty"`

	// Observations contains measured values emitted by the check.
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
