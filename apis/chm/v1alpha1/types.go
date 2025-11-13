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
// +kubebuilder:object:generate=true
type CheckNodeHealth struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CheckNodeHealthSpec   `json:"spec,omitempty"`
	Status CheckNodeHealthStatus `json:"status,omitempty"`
}

// CheckNodeHealthSpec defines the desired state of CheckNodeHealth
// +kubebuilder:object:generate=true
type CheckNodeHealthSpec struct {
	// NodeRef references the node to check
	// +required
	NodeRef NodeReference `json:"nodeRef"`
}

// NodeReference contains a reference to a node
// +kubebuilder:object:generate=true
type NodeReference struct {
	// Name is the name of the node
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// CheckNodeHealthStatus defines the observed state of CheckNodeHealth
// +kubebuilder:object:generate=true
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

// CheckerType represents the category of health checker
type CheckerType string

const (
	// CheckerTypeAPIServer represents API server health checks
	CheckerTypeAPIServer CheckerType = "APIServer"

	// CheckerTypeDNS represents DNS resolution health checks
	CheckerTypeDNS CheckerType = "DNS"

	// CheckerTypeMetricsServer represents metrics server health checks
	CheckerTypeMetricsServer CheckerType = "MetricsServer"

	// CheckerTypePodStartup represents pod startup health checks
	CheckerTypePodStartup CheckerType = "PodStartup"

	// CheckerTypeAzurePolicy represents Azure policy health checks
	CheckerTypeAzurePolicy CheckerType = "AzurePolicy"
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
}

// +kubebuilder:object:root=true

// CheckNodeHealthList contains a list of CheckNodeHealth resources
type CheckNodeHealthList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CheckNodeHealth `json:"items"`
}
