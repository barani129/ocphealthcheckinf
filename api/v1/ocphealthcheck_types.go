/*
Copyright 2025 baranitharan.chittharanjan@spark.co.nz.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

const (
	EventSource                 = "OcpHealthCheck"
	EventReasonIssuerReconciler = "OcpHealthCheckReconciler"
)

// OcpHealthCheckSpec defines the desired state of OcpHealthCheck.
type OcpHealthCheckSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Set suspend to true to disable controller run
	// +optional
	Suspend *bool `json:"suspend"`

	// Cluster name to be used. The controller tries to retrieve the API name from a configmap for Openshift clusters
	// If configmap is not found (in case of generic k8s deployments) or subdomain is not set, this field will be used for notifications
	Cluster *string `json:"cluster,omitempty"`

	// Suspends email alerts if set to true, target email (.spec.email) will not be notified
	// +optional
	SuspendEmailAlert *bool `json:"suspendEmailAlert,omitempty"`

	// Target user's email for container status notification
	// +optional
	Email string `json:"email,omitempty"`

	// SMTP Relay host for sending the email
	// +optional
	RelayHost string `json:"relayHost,omitempty"`

	// the frequency of checks to be done, if not set, defaults to 2 minutes
	// +optional
	CheckInterval *int64 `json:"checkInterval,omitempty"`

	// Identifies if the openshift cluster is being used as hub for other clusters
	// Runs extra healthchecks about managed clusters
	// +optional
	HubCluster *bool `json:"hubCluster,omitempty"`
}

// OcpHealthCheckStatus defines the observed state of OcpHealthCheck.
type OcpHealthCheckStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// list of status conditions to indicate the status of managed cluster
	// known conditions are 'Ready'.
	// +optional
	Conditions []OcpHealthCheckCondition `json:"conditions,omitempty"`

	// Indicates if the target is free of failing resources
	// +optional
	Healthy bool `json:"healthy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// OcpHealthCheck is the Schema for the ocphealthchecks API.
// +kubebuilder:printcolumn:name="CreatedAt",type="string",JSONPath=".metadata.creationTimestamp",description="object creation timestamp(in cluster's timezone)"
// +kubebuilder:printcolumn:name="Reconciled",type="string",JSONPath=".status.conditions[].status",description="if set to true, all resource informers are reconciled successfully"
// +kubebuilder:printcolumn:name="Healthy",type="string",JSONPath=".status.healthy",description="if set to true, all monitored resources in the target cluster are running fine"
type OcpHealthCheck struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OcpHealthCheckSpec   `json:"spec,omitempty"`
	Status OcpHealthCheckStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OcpHealthCheckList contains a list of OcpHealthCheck.
type OcpHealthCheckList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OcpHealthCheck `json:"items"`
}

type OcpHealthCheckCondition struct {
	// Type of the condition, known values are 'Ready'.
	Type OcpHealthCheckConditionType `json:"type"`

	// Status of the condition, one of ('True', 'False', 'Unknown')
	Status ConditionStatus `json:"status"`

	// LastTransitionTime is the timestamp of the last update to the status
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`

	// Reason is the machine readable explanation for object's condition
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message is the human readable explanation for object's condition
	Message string `json:"message"`
}

// ManagedConditionType represents a managed cluster condition value.
type OcpHealthCheckConditionType string

const (
	// ContainerScanConditionReady represents the fact that a given managed cluster condition
	// is in reachable from the ACM/source cluster.
	// If the `status` of this condition is `False`, managed cluster is unreachable
	OcpHealthCheckConditionReady OcpHealthCheckConditionType = "Ready"
)

// ConditionStatus represents a condition's status.
// +kubebuilder:validation:Enum=True;False;Unknown
type ConditionStatus string

// These are valid condition statuses. "ConditionTrue" means a resource is in
// the condition; "ConditionFalse" means a resource is not in the condition;
// "ConditionUnknown" means kubernetes can't decide if a resource is in the
// condition or not. In the future, we could add other intermediate
// conditions, e.g. ConditionDegraded.
const (
	// ConditionTrue represents the fact that a given condition is true
	ConditionTrue ConditionStatus = "True"

	// ConditionFalse represents the fact that a given condition is false
	ConditionFalse ConditionStatus = "False"

	// ConditionUnknown represents the fact that a given condition is unknown
	ConditionUnknown ConditionStatus = "Unknown"
)

func init() {
	SchemeBuilder.Register(&OcpHealthCheck{}, &OcpHealthCheckList{})
}
