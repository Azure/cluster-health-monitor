package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=chm
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.nodeRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[?(@.type=="Completed")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CheckHealthMonitor is a resource that tracks health check results for a specific node
type CheckHealthMonitor struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CheckHealthMonitorSpec   `json:"spec,omitempty"`
	Status CheckHealthMonitorStatus `json:"status,omitempty"`
}

// CheckHealthMonitorSpec defines the desired state of CheckHealthMonitor
type CheckHealthMonitorSpec struct {
	// NodeRef references the node to check
	// +required
	NodeRef NodeReference `json:"nodeRef"`
}

// NodeReference contains a reference to a node
type NodeReference struct {
	// Name is the name of the node
	// +required
	Name string `json:"name"`
}

// CheckHealthMonitorStatus defines the observed state of CheckHealthMonitor
type CheckHealthMonitorStatus struct {
	// StartedAt is the timestamp when the health checks started
	StartedAt *metav1.Time `json:"startedAt"`

	// FinishedAt is the timestamp when the health checks completed
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// Conditions represent the latest available observations of the check's state
	Conditions []metav1.Condition `json:"conditions"`

	// Results contains the individual check results
	// +optional
	Results []CheckResult `json:"results,omitempty"`
}

// CheckHealthMonitorConditionType represents the type of condition
type CheckHealthMonitorConditionType string

const (
	// CheckHealthMonitorConditionCompleted indicates all checks have completed
	CheckHealthMonitorConditionCompleted CheckHealthMonitorConditionType = "Completed"

	// CheckHealthMonitorConditionFailed indicates one or more checks failed
	CheckHealthMonitorConditionFailed CheckHealthMonitorConditionType = "Failed"

	// CheckHealthMonitorConditionProgressing indicates checks are in progress
	CheckHealthMonitorConditionProgressing CheckHealthMonitorConditionType = "Progressing"
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
)

// CheckResult represents the result of a single health check
type CheckResult struct {
	// CheckerType is the category/type of health check
	// +required
	// +kubebuilder:validation:Enum=APIServer;DNS;MetricsServer;PodStartup;AzurePolicy
	CheckerType CheckerType `json:"checkerType"`

	// Checker is the specific instance name of the health check
	// For example: "internal-dns", "external-dns", "kubernetes-apiserver"
	// +required
	Checker string `json:"checker"`

	// Status is the health status of this check
	// +required
	Status CheckStatus `json:"status"`

	// Message provides additional details about the check result
	// +optional
	Message string `json:"message,omitempty"`

	// ErrorCode is the specific error code if the check failed
	// +optional
	ErrorCode string `json:"errorCode,omitempty"`

	// CompletedAt is when this specific check completed
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true

// CheckHealthMonitorList contains a list of CheckHealthMonitor resources
type CheckHealthMonitorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CheckHealthMonitor `json:"items"`
}
