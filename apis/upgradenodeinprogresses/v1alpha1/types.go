package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=unip
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.nodeRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// UpgradeNodeInProgress represents a node that is currently undergoing an upgrade.
// This resource is used to track nodes in the upgrade process.
type UpgradeNodeInProgress struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec UpgradeNodeInProgressSpec `json:"spec,omitempty"`
}

// UpgradeNodeInProgressSpec defines the desired state of UpgradeNodeInProgress
type UpgradeNodeInProgressSpec struct {
	// NodeRef references the node that is being upgraded
	// +required
	NodeRef NodeReference `json:"nodeRef"`
}

// NodeReference contains a reference to a node
type NodeReference struct {
	// Name is the name of the node
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// +kubebuilder:object:root=true

// UpgradeNodeInProgressList contains a list of UpgradeNodeInProgress resources
type UpgradeNodeInProgressList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []UpgradeNodeInProgress `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=hs
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.target.nodeName`
// +kubebuilder:printcolumn:name="Healthy",type=string,JSONPath=`.status.condition[?(@.type=="Healthy")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HealthSignal represents a health signal for a specific target (e.g., a node).
// It captures health status information including conditions and timing.
type HealthSignal struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HealthSignalSpec   `json:"spec,omitempty"`
	Status HealthSignalStatus `json:"status,omitempty"`
}

// HealthSignalSpec defines the desired state of HealthSignal
type HealthSignalSpec struct {
	// Source identifies the checker that created this health signal
	// For example: "ClusterHealthMonitor", "AKSNodeHealthChecker"
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Source string `json:"source"`

	// Type specifies the type of health signal (e.g., NodeHealth)
	// +required
	// +kubebuilder:validation:Enum=NodeHealth
	Type HealthSignalType `json:"type"`

	// Target specifies the target of the health signal
	// +required
	Target HealthSignalTarget `json:"target"`
}

// HealthSignalType represents the type of health signal
type HealthSignalType string

const (
	// HealthSignalTypeNodeHealth indicates this is a node health signal
	HealthSignalTypeNodeHealth HealthSignalType = "NodeHealth"
)

// HealthSignalTarget specifies the target of the health signal
type HealthSignalTarget struct {
	// NodeName is the name of the target node
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:validation:MaxLength=253
	NodeName string `json:"nodeName"`
}

// HealthSignalStatus defines the observed state of HealthSignal
type HealthSignalStatus struct {
	// StartedAt is the timestamp when the health check started
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// FinishedAt is the timestamp when the health check completed
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// Condition represents the latest available observations of the health signal's current state
	// +optional
	Condition []metav1.Condition `json:"condition,omitempty"`
}

// +kubebuilder:object:root=true

// HealthSignalList contains a list of HealthSignal resources
type HealthSignalList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HealthSignal `json:"items"`
}
