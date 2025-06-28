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

// NodeHandlingSpec defines the desired state of NodeHandling.
type NodeHandlingSpec struct {

	// Name of the associated ClusterConfiguration Resource
	ClusterConfigurationName string `json:"clusterConfigurationName"`

	// Name of the associated NodeSelecting Resource
	NodeSelectingName string `json:"nodeSelectingName"`

	// Worker selected for the scaling process
	SelectedNode string `json:"selectedNode"`

	// MachineDeployment selected for the scaling process
	SelectedMachineDeployment string `json:"selectedMachineDeployment"`

	// Number of node to add to the cluster (it can be positive, scale up, or negative, scale down)
	ScalingLabel int32 `json:"scalingLabel"`
}

// NodeHandlingStatus defines the observed state of NodeHandling.
type NodeHandlingStatus struct {

	// Execution phase of the resource
	Phase NodeHandlingPhase `json:"phase"`

	// Information message for Failed phase
	Message string `json:"message"`
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
