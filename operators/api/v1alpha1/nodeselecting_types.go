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

type NodeSelectingPhase string

const (
	NS_PhaseRunning   NodeSelectingPhase = "Running"
	NS_PhaseCompleted NodeSelectingPhase = "Completed"
	NS_PhaseFailed    NodeSelectingPhase = "Failed"
)

// NodeSelectingSpec defines the desired state of NodeSelecting.
type NodeSelectingSpec struct {

	// Name of the associated ClusterConfiguration Resource
	ClusterConfigurationName string `json:"clusterConfigurationName"`

	// Number of node to add to the cluster (it can be positive, scale up, or negative, scale down)
	ScalingLabel int32 `json:"scalingLabel"`
}

// NodeSelectingStatus defines the observed state of NodeSelecting.
type NodeSelectingStatus struct {

	// MachineDeployment selected for the scaling process
	SelectedMachineDeployment string `json:"selectedMachineDeployment"`

	// Worker selected for the scaling process
	SelectedNode string `json:"selectedNode"`

	// Execution phase of the resource
	Phase NodeSelectingPhase `json:"phase"`

	// Information message for Failed phase
	Message string `json:"message"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NodeSelecting is the Schema for the nodeselectings API.
type NodeSelecting struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeSelectingSpec   `json:"spec,omitempty"`
	Status NodeSelectingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeSelectingList contains a list of NodeSelecting.
type NodeSelectingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeSelecting `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeSelecting{}, &NodeSelectingList{})
}
