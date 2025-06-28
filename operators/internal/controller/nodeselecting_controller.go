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

package controller

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
)

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselectings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselectings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselectings/finalizers,verbs=update

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch

type Response struct {
	SelectedNode      string `json:"selectedNode"`
	MachineDeployment string `json:"machineDeployment"`
}

// NodeSelectingReconciler reconciles a NodeSelecting object
type NodeSelectingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *NodeSelectingReconciler) handleInitialPhase(ctx context.Context, nodeSelecting *clusterv1alpha1.NodeSelecting) error {
	log := log.FromContext(ctx).WithName("handle-initial-phase")

	// check if another NodeSelecting resource is already in progress for the same clusterConfiguration
	nodeSelectingList := &clusterv1alpha1.NodeSelectingList{}
	if err := r.List(ctx, nodeSelectingList); err != nil {
		log.Error(err, "Failed to list NodeSelecting resources")
		return err
	}

	for _, ns := range nodeSelectingList.Items {
		if ns.Spec.ClusterConfigurationName == nodeSelecting.Spec.ClusterConfigurationName {
			if ns.Name != nodeSelecting.Name && ns.Status.Phase == v1alpha1.NS_PhaseRunning {
				log.Info("Another NodeSelecting resource is already in progress for the same clusterConfiguration, waiting for it to complete", "name", ns.Name)
				return nil
			}

		}
	}
	// If no other NodeSelecting is in progress, proceed with the current one
	nodeSelecting.Status.Phase = v1alpha1.NS_PhaseRunning
	if err := r.Status().Update(ctx, nodeSelecting); err != nil {
		log.Error(err, "Failed to update NodeSelecting resource status to Running", "name", nodeSelecting.Name)
	}

	return nil

}

func (r *NodeSelectingReconciler) handleRunningPhase(ctx context.Context, nodeSelecting *clusterv1alpha1.NodeSelecting) error {
	log := log.FromContext(ctx).WithName("handle-running-phase")
	nodeSelectServer := getNodeSelectionURL()
	response, err := getSelectedNode(ctx, nodeSelectServer, nodeSelecting.Spec.ScalingLabel)
	if err != nil {
		log.Error(err, "Failed to get response from Node Selection Service", "nodeSelectServer", nodeSelectServer)
		return err
	}
	selectedNode := response.SelectedNode
	selectedMD := response.MachineDeployment

	if selectedNode == "" {
		nodeSelecting.Status.Phase = v1alpha1.NS_PhaseFailed
		nodeSelecting.Status.Message = "Server failed getting a valid node for the scaling"
		if err := r.Status().Update(ctx, nodeSelecting); err != nil {
			log.Error(err, "Failed to update NodeSelecting resource status to Failed", "name", nodeSelecting.Name)
			return err
		}
		return fmt.Errorf("no node selected, null result on NodeSelecting %s", nodeSelecting.Name)
	}

	errNH := r.CreateNodeHandling(ctx, nodeSelecting, selectedNode, selectedMD)
	if errNH != nil {
		log.Error(err, "Failed to create NodeHandling resource in NodeSelecting", "name", nodeSelecting.Name)
		return err
	}

	nodeSelecting.Status.SelectedNode = selectedNode
	nodeSelecting.Status.SelectedMachineDeployment = selectedMD
	nodeSelecting.Status.Phase = v1alpha1.NS_PhaseCompleted
	if err := r.Status().Update(ctx, nodeSelecting); err != nil {
		log.Error(err, "Failed to update NodeSelecting resource status to Completed", "name", nodeSelecting.Name)
		return err
	}

	return nil
}

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *NodeSelectingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	nodeSelecting := &clusterv1alpha1.NodeSelecting{}
	if err := r.Get(ctx, req.NamespacedName, nodeSelecting); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("NodeSelecting resource not found, ignoring since object must be deleted")
		}
	}
	log.Info("Reconciling NodeSelecting resource", "name", nodeSelecting.Name)

	switch nodeSelecting.Status.Phase {
	case "":
		if err := r.handleInitialPhase(ctx, nodeSelecting); err != nil {
			log.Error(err, "Failed to handle Initial phase for NodeSelecting resource", "name", nodeSelecting.Name)
			return ctrl.Result{}, err
		}
		break
	case v1alpha1.NS_PhaseRunning:
		if err := r.handleRunningPhase(ctx, nodeSelecting); err != nil {
			log.Error(err, "Failed to handle Running phase for NodeSelecting resource", "name", nodeSelecting.Name)
			return ctrl.Result{}, err
		}
		break
	case v1alpha1.NS_PhaseFailed:
		log.Info("NodeSelecting resource is in Failed phase, no further action needed", "name", nodeSelecting.Name)

		break
	case v1alpha1.NS_PhaseCompleted:
		log.Info("NodeSelecting resource is in Completed phase, no further action needed", "name", nodeSelecting.Name)
		break
	default:
		log.Info("Unknown phase, handling as Failed", "name", nodeSelecting.Name)
		nodeSelecting.Status.Phase = v1alpha1.NS_PhaseFailed
		if err := r.Status().Update(ctx, nodeSelecting); err != nil {
			log.Error(err, "Failed to update NodeSelecting resource status to Failed", "name", nodeSelecting.Name)
			return ctrl.Result{}, err
		}
		break
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeSelectingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1alpha1.NodeSelecting{}).
		Named("nodeselecting").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		Complete(r)
}

func getNodeSelectionURL() string {

	return "http://localhost:8000/"
	//return "http://selection-service.dreem.svc.cluster.local/"
}

func getSelectedNode(ctx context.Context, nodeSelectServer string, scalingLabel int32) (Response, error) {
	var response Response
	response.MachineDeployment = ""
	response.SelectedNode = ""
	apiServer := nodeSelectServer
	if scalingLabel > 0 {
		apiServer += "nodes/scaleUp"
	} else {
		apiServer += "nodes/scaleDown"
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiServer, nil)
	if err != nil {
		return response, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return response, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return response, errors.New("non-OK HTTP status: " + resp.Status)
	}

	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&response); err != nil {
		return response, err
	}

	return response, nil

}

func (r *NodeSelectingReconciler) CreateNodeHandling(ctx context.Context, nodeSelecting *clusterv1alpha1.NodeSelecting, selectedNode string, selectedMD string) error {
	log := log.FromContext(ctx).WithName("create-node-handling")

	// create unique identifier for the NodeHandling CRD
	crdNameBytes := make([]byte, 8)
	if _, err := rand.Read(crdNameBytes); err != nil {
		log.Error(err, "unable to generate random name for NodeHandling CRD")
		return err
	}
	crdName := "node-handling-" + fmt.Sprintf("%x", crdNameBytes)
	ownerRef := metav1.OwnerReference{
		APIVersion:         nodeSelecting.OwnerReferences[0].APIVersion,
		Kind:               nodeSelecting.OwnerReferences[0].Kind,
		Name:               nodeSelecting.OwnerReferences[0].Name,
		UID:                nodeSelecting.OwnerReferences[0].UID,
		Controller:         pointer.BoolPtr(true),
		BlockOwnerDeletion: pointer.BoolPtr(true),
	}
	// create the NodeHandling CRD
	var nodeHandling = &clusterv1alpha1.NodeHandling{
		ObjectMeta: metav1.ObjectMeta{
			Name:            crdName,
			Namespace:       "dreem",
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Spec: clusterv1alpha1.NodeHandlingSpec{
			ClusterConfigurationName:  nodeSelecting.Spec.ClusterConfigurationName,
			NodeSelectingName:         nodeSelecting.Name,
			SelectedNode:              selectedNode,
			SelectedMachineDeployment: selectedMD,
			ScalingLabel:              nodeSelecting.Spec.ScalingLabel,
		},
	}

	if err := r.Create(ctx, nodeHandling); err != nil {
		log.Error(err, "Failed to create NodeHandling resource", "name", crdName)
		return err
	}

	return nil

}
