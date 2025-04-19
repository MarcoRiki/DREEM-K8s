/*
Copyright 2025.

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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type NodeHandlingPhase string

const (
	NH_PhaseRunning   NodeHandlingPhase = "Running"
	NH_PhaseCompleted NodeHandlingPhase = "Completed"
	NH_PhaseFailed    NodeHandlingPhase = "Failed"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// NodeHandlingSpec defines the desired state of NodeHandling.
type NodeHandlingSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	ClusterConfigurationName string `json:"clusterConfigurationName"`
	NodeSelectingName        string `json:"nodeSelectingName"`
	SelectedNode             string `json:"selectedNode"`
	ScalingLabel             int32  `json:"scalingLabel"`
}

// NodeHandlingStatus defines the observed state of NodeHandling.
type NodeHandlingStatus struct {
	Phase NodeHandlingPhase `json:"phase"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NodeHandling is the Schema for the nodehandlings API.
type NodeHandling struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeHandlingSpec   `json:"spec,omitempty"`
	Status NodeHandlingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeHandlingList contains a list of NodeHandling.
type NodeHandlingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeHandling `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeHandling{}, &NodeHandlingList{})
}
