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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ClusterConfigurationSpec defines the desired state of ClusterConfiguration.
type ClusterConfigurationSpec struct {
	// Important: Run "make" to regenerate code after modifying this file

	RequiredNodes int32 `json:"requiredNodes"`
	MaxNodes      int32 `json:"maxNodes"`
	MinNodes      int32 `json:"minNodes,omitempty"`
}

type ClusterConfigurationPhase string

const (
	CC_PhaseStable    ClusterConfigurationPhase = "Stable"
	CC_PhaseSelecting ClusterConfigurationPhase = "Selecting"
	CC_PhaseSwitching ClusterConfigurationPhase = "Switching"
	CC_PhaseCompleted ClusterConfigurationPhase = "Completed"
	CC_PhaseFailed    ClusterConfigurationPhase = "Failed"
	CC_PhaseAborted   ClusterConfigurationPhase = "Aborted"
)

// ClusterConfigurationStatus defines the observed state of ClusterConfiguration.
type ClusterConfigurationStatus struct {

	// Important: Run "make" to regenerate code after modifying this file

	ActiveNodes int32                     `json:"activeNodes"`
	Phase       ClusterConfigurationPhase `json:"phase"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ClusterConfiguration is the Schema for the clusterconfigurations API.
type ClusterConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterConfigurationSpec   `json:"spec,omitempty"`
	Status ClusterConfigurationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterConfigurationList contains a list of ClusterConfiguration.
type ClusterConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterConfiguration{}, &ClusterConfigurationList{})
}
