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
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// ClusterConfigurationReconciler reconciles a ClusterConfiguration object
type ClusterConfigurationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

var max_retries = 5
var retryInterval = 10 * time.Second // seconds
var timeout = 10 * time.Second
var pollingInterval = 5 * time.Second // seconds

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=clusterconfigurations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=clusterconfigurations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=clusterconfigurations/finalizers,verbs=update

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselecting,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselecting/status,verbs=get;update;patch

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandling,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandling/status,verbs=get;update;patch

// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments,verbs=list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments/status,verbs=get;update

// Handle the initial phase
func (r *ClusterConfigurationReconciler) handleInitialPhase(ctx context.Context, clusterConfig *clusterv1alpha1.ClusterConfiguration) error {
	log := log.FromContext(ctx).WithName("handle-intial-phase")

	clusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseStable
	activeNodes, err := getNumberOfWorkerNodes(ctx, r.Client)
	if err != nil {
		log.Error(err, "Failed to get the number of MachineDeployments")
		return err
	}
	clusterConfig.Status.ActiveNodes = activeNodes
	if err := r.Status().Update(ctx, clusterConfig); err != nil {
		log.Error(err, "Failed to update ClusterConfiguration status to Stable phase")
		return err
	}
	log.Info("ClusterConfiguration status updated to Stable phase", "name", clusterConfig.Name, "namespace", clusterConfig.Namespace)
	return nil
}

// Handle the stable phase
func (r *ClusterConfigurationReconciler) handleStablePhase(ctx context.Context, clusterConfiguration *clusterv1alpha1.ClusterConfiguration) error {
	var clusterConfig = &clusterv1alpha1.ClusterConfiguration{}
	r.Get(ctx, client.ObjectKeyFromObject(clusterConfiguration), clusterConfig)
	log := log.FromContext(ctx).WithName("handle-stable-phase")

	// check  if the number of active nodes is different from the expected number
	updatedNumberOfWorker, err := getNumberOfWorkerNodes(ctx, r.Client)
	if err != nil {
		return err
	}
	if updatedNumberOfWorker != clusterConfig.Status.ActiveNodes {
		clusterConfig.Status.ActiveNodes = updatedNumberOfWorker
	}
	var numberOfWorkerToAdd = int32(0)
	// Check if the number of active nodes is diffent from the required number
	if clusterConfig.Status.ActiveNodes != clusterConfig.Spec.RequiredNodes {
		log.Info("Number of active nodes is different from the required number, scaling is needed for ClusterConfiguration",
			"name", clusterConfig.Name, "activeNodes", clusterConfig.Status.ActiveNodes, "requiredNodes", clusterConfig.Spec.RequiredNodes)

		if clusterConfig.Status.ActiveNodes < clusterConfig.Spec.RequiredNodes {
			// scale up the cluster
			maxNumberOfWorker := min(clusterConfig.Spec.MaxNodes, clusterConfig.Spec.RequiredNodes)
			numberOfWorkerToAdd = maxNumberOfWorker - clusterConfig.Status.ActiveNodes

			if numberOfWorkerToAdd != 0 {
				log.Info("scaling up the cluster",
					"name", clusterConfig.Name, "activeNodes", clusterConfig.Status.ActiveNodes,
					"requiredNodes", clusterConfig.Spec.RequiredNodes, "numberOfWorkerToAdd", numberOfWorkerToAdd)
				// create a NodeSelecting resource to handle the scaling up

				if err := r.CreateNodeSelecting(ctx, r.Client, numberOfWorkerToAdd, *clusterConfig); err != nil {
					log.Error(err, "Failed to create NodeHandling resource for scaling down")
					return err
				}
			} else {
				log.Info("scaling up not needed, maximum number of workers reached",
					"name", clusterConfig.Name, "activeNodes", clusterConfig.Status.ActiveNodes,
					"requiredNodes", clusterConfig.Spec.RequiredNodes, "numberOfWorkerToAdd", numberOfWorkerToAdd)
				clusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseCompleted
				if err := r.Status().Update(ctx, clusterConfig); err != nil {
					log.Error(err, "Failed to update ClusterConfiguration status to Completed phase")
					return err
				}
			}

		} else if clusterConfig.Status.ActiveNodes > clusterConfig.Spec.RequiredNodes {
			// scale down the cluster
			minNumberOfWorker := max(clusterConfig.Spec.RequiredNodes, clusterConfig.Spec.MinNodes)
			numberOfWorkerToAdd = minNumberOfWorker - clusterConfig.Status.ActiveNodes

			if numberOfWorkerToAdd != 0 {
				log.Info("scaling down the cluster",
					"name", clusterConfig.Name, "activeNodes", clusterConfig.Status.ActiveNodes,
					"requiredNodes", clusterConfig.Spec.RequiredNodes, "numberOfWorkerToAdd", numberOfWorkerToAdd)

				// Create a NodeHandling resource to handle the scaling down
				if err := r.CreateNodeSelecting(ctx, r.Client, numberOfWorkerToAdd, *clusterConfig); err != nil {
					log.Error(err, "Failed to create NodeHandling resource for scaling down")
					return err
				}
			} else {
				log.Info("scaling down not needed, minimum number of workers reached",
					"name", clusterConfig.Name, "activeNodes", clusterConfig.Status.ActiveNodes,
					"requiredNodes", clusterConfig.Spec.RequiredNodes, "numberOfWorkerToAdd", numberOfWorkerToAdd)
				clusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseCompleted
				if err := r.Status().Update(ctx, clusterConfig); err != nil {
					log.Error(err, "Failed to update ClusterConfiguration status to Completed phase")
					return err
				}
			}
		}
		clusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseSelecting
		if err := r.Status().Update(ctx, clusterConfig); err != nil {
			log.Error(err, "Failed to update ClusterConfiguration status to Selecting phase")
			return err
		}

	} else {
		log.Info("Numer of active nodes is equal to the required number, no action needed for ClusterConfiguration",
			"name", clusterConfig.Name, "activeNodes", clusterConfig.Status.ActiveNodes, "requiredNodes", clusterConfig.Spec.RequiredNodes)
		clusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseCompleted
		if err := r.Status().Update(ctx, clusterConfig); err != nil {
			log.Error(err, "Failed to update ClusterConfiguration status to Completed phase")
			return err
		}
	}

	return nil
}

// Handle the selecting phase
func (r *ClusterConfigurationReconciler) handleSelectingPhase(ctx context.Context, clusterConfiguration *clusterv1alpha1.ClusterConfiguration) (bool, error) {
	var clusterConfig = &clusterv1alpha1.ClusterConfiguration{}
	r.Get(ctx, client.ObjectKeyFromObject(clusterConfiguration), clusterConfig)
	log := log.FromContext(ctx).WithName("handle-selecting-phase")
	// Implement the logic for handling the selecting phase

	// Check every pollingInterval if there is a NodeSelecting resource associated with the ClusterConfiguration
	nodeSelectingList := &clusterv1alpha1.NodeSelectingList{}

	if err := r.List(ctx, nodeSelectingList, client.InNamespace(clusterConfig.Namespace)); err != nil {
		log.Error(err, "Failed to list NodeSelecting resources", "namespace", clusterConfig.Namespace)
		return false, err
	}

	filteredList := &v1alpha1.NodeSelectingList{}
	for _, item := range nodeSelectingList.Items {
		if item.Spec.ClusterConfigurationName == clusterConfig.Name {
			filteredList.Items = append(filteredList.Items, item)
		}
	}

	if len(filteredList.Items) == 1 {
		log.Info("NodeSelecting resource found for ClusterConfiguration, proceeding with selection",
			"name", clusterConfig.Name, "nodeSelectingName", filteredList.Items[0].Name)

		if filteredList.Items[0].Status.Phase == clusterv1alpha1.NS_PhaseCompleted {
			log.Info("NodeSelecting resource completed successfully, proceeding to Switching phase",
				"name", clusterConfig.Name, "nodeSelectingName", filteredList.Items[0].Name)

			// Update the ClusterConfiguration status to Switching phase
			clusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseSwitching
			if err := r.Status().Update(ctx, clusterConfig); err != nil {
				log.Error(err, "Failed to update ClusterConfiguration status to Switching phase")
				return false, err
			}
		} else {
			if filteredList.Items[0].Status.Phase == clusterv1alpha1.NS_PhaseFailed {
				clusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseFailed
				clusterConfig.Status.Message = "Failed because of failed NodeSelecting found"
				if err := r.Status().Update(ctx, clusterConfig); err != nil {
					log.Error(err, "Failed to update ClusterConfiguration status to Failed phase")
					return false, err
				}
			}
			log.Info("NodeSelecting resource is still in progress, waiting for completion",
				"name", clusterConfig.Name, "nodeSelectingName", filteredList.Items[0].Name,
				"phase", filteredList.Items[0].Status.Phase)
			return false, nil // Wait for the next polling interval
		}

		return true, nil
	} else if len(filteredList.Items) > 1 {
		log.Error(nil, "Multiple NodeSelecting resources found for ClusterConfiguration, this should not happen",
			"name", clusterConfig.Name, "nodeSelectingCount", len(filteredList.Items))
		clusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseFailed
		clusterConfig.Status.Message = "Failed, multiple NodeSelecting found"
		if err := r.Status().Update(ctx, clusterConfig); err != nil {
			log.Error(err, "Failed to update ClusterConfiguration status to Failed phase")
			return false, err
		}
	}
	log.Info("No NodeSelecting resource found for ClusterConfiguration, waiting for selection", "name", clusterConfig.Name)
	return false, nil

}

// Handle the switching phase
func (r *ClusterConfigurationReconciler) handleSwitchingPhase(ctx context.Context, clusterConfiguration *clusterv1alpha1.ClusterConfiguration) (bool, error) {
	var clusterConfig = &clusterv1alpha1.ClusterConfiguration{}
	r.Get(ctx, client.ObjectKeyFromObject(clusterConfiguration), clusterConfig)

	log := log.FromContext(ctx).WithName("handle-switching-phase")
	// Implement the logic for handling the switching phase

	// check if there is a NodeHandling resource associated with the ClusterConfiguration
	nodeHandlingList := &clusterv1alpha1.NodeHandlingList{}
	if err := r.List(ctx, nodeHandlingList, client.InNamespace(clusterConfig.Namespace)); err != nil {
		log.Error(err, "Failed to list NodeSelecting resources", "namespace", clusterConfig.Namespace)
		return false, err
	}

	filteredList := &v1alpha1.NodeHandlingList{}
	for _, item := range nodeHandlingList.Items {
		if item.Spec.ClusterConfigurationName == clusterConfig.Name {
			filteredList.Items = append(filteredList.Items, item)
		}
	}
	if len(filteredList.Items) == 1 {
		log.Info("NodeHandling resource found for ClusterConfiguration, proceeding with handling",
			"name", clusterConfig.Name, "nodeHandlingName", filteredList.Items[0].Name)
		if filteredList.Items[0].Status.Phase == clusterv1alpha1.NH_PhaseCompleted {
			log.Info("NodeHandling resource completed successfully, proceeding to Completed phase",
				"name", clusterConfig.Name, "nodeHandlingName", filteredList.Items[0].Name)
			// Update the ClusterConfiguration status to Completed phase
			clusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseCompleted
			if err := r.Status().Update(ctx, clusterConfig); err != nil {
				log.Error(err, "Failed to update ClusterConfiguration status to Completed phase")
				return false, err
			}
		} else {
			if filteredList.Items[0].Status.Phase == clusterv1alpha1.NH_PhaseFailed {
				clusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseFailed
				clusterConfig.Status.Message = "Failed because of failed NodeHandling found"
				if err := r.Status().Update(ctx, clusterConfig); err != nil {
					log.Error(err, "Failed to update ClusterConfiguration status to Failed phase")
					return false, err
				}
			}
			return false, nil // Wait for the next polling interval
		}
		return true, nil
	} else if len(nodeHandlingList.Items) > 1 {
		log.Error(nil, "Multiple NodeHandling resources found for ClusterConfiguration, this should not happen",
			"name", clusterConfig.Name, "nodeHandlingCount", len(nodeHandlingList.Items))
		clusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseFailed
		clusterConfig.Status.Message = "Failed, multiple NodeHandling found"
		if err := r.Status().Update(ctx, clusterConfig); err != nil {
			log.Error(err, "Failed to update ClusterConfiguration status to Failed phase")
			return false, err
		}
	}
	log.Info("No NodeHandling resource found for ClusterConfiguration, waiting for handling", "name", clusterConfig.Name)
	// No NodeHandling resource found, wait for the next polling interval
	return false, nil
}

// Handle the failed phase
func (r *ClusterConfigurationReconciler) handleFailedPhase(ctx context.Context, clusterConfiguration *clusterv1alpha1.ClusterConfiguration) error {
	var clusterConfig = &clusterv1alpha1.ClusterConfiguration{}
	r.Get(ctx, client.ObjectKeyFromObject(clusterConfiguration), clusterConfig)
	log := log.FromContext(ctx).WithName("handle-failed-phase")
	// Implement the logic for handling the failed phase
	log.Info("Failed phase for ClusterConfiguration",
		"name", clusterConfig.Name, "phase", clusterConfig.Status.Phase)

	return nil
}

// Handle the completed phase
func (r *ClusterConfigurationReconciler) handleCompletedPhase(ctx context.Context, clusterConfiguration *clusterv1alpha1.ClusterConfiguration) error {
	var clusterConfig = &clusterv1alpha1.ClusterConfiguration{}
	r.Get(ctx, client.ObjectKeyFromObject(clusterConfiguration), clusterConfig)
	log := log.FromContext(ctx).WithName("handle-completed-phase")
	// Implement the logic for handling the completed phase

	updatedNumberOfWorker, err := getNumberOfWorkerNodes(ctx, r.Client)
	if err != nil {
		log.Error(err, "Failed to get the number of MachineDeployments")
		return err
	}
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {

		fresh := &clusterv1alpha1.ClusterConfiguration{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(clusterConfig), fresh); err != nil {
			log.Error(err, "Failed to get fresh ClusterConfiguration")
			return err
		}

		if updatedNumberOfWorker != fresh.Spec.RequiredNodes {
			log.Info("Active nodes differ from required nodes",
				"name", fresh.Name, "activeNodes", updatedNumberOfWorker, "requiredNodes", fresh.Spec.RequiredNodes)
			fresh.Status.Phase = clusterv1alpha1.CC_PhaseFailed
			fresh.Status.Message = "Failed, something wrong happened during the scaling: active nodes differ from required nodes"
		} else {
			log.Info("ClusterConfiguration completed successfully",
				"name", fresh.Name, "activeNodes", updatedNumberOfWorker, "requiredNodes", fresh.Spec.RequiredNodes)
			fresh.Status.ActiveNodes = updatedNumberOfWorker
			fresh.Status.Phase = clusterv1alpha1.CC_PhaseFinished

		}

		// Aggiorno lo status con la versione fresca
		return r.Status().Update(ctx, fresh)
	})
}

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *ClusterConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the ClusterConfiguration instance
	clusterConfig := &clusterv1alpha1.ClusterConfiguration{}
	if err := r.Get(ctx, req.NamespacedName, clusterConfig); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: timeout}, client.IgnoreNotFound(err)
		}
	}
	// Log the reconciliation request
	log.Info("Reconciling ClusterConfiguration", "name", clusterConfig.Name, "namespace", clusterConfig.Namespace)

	// Reconcile from the previous phase
	switch clusterConfig.Status.Phase {
	case "":
		err := r.handleInitialPhase(ctx, clusterConfig)
		if err != nil {
			log.Error(err, "Failed to handle initial phase for ClusterConfiguration", "name", clusterConfig.Name)
			return ctrl.Result{}, err
		}
		break
	case clusterv1alpha1.CC_PhaseStable:
		err := r.handleStablePhase(ctx, clusterConfig)
		if err != nil {
			log.Error(err, "Failed to handle stable phase for ClusterConfiguration", "name", clusterConfig.Name)
			return ctrl.Result{}, err
		}
		break
	case clusterv1alpha1.CC_PhaseSelecting:
		found, err := r.handleSelectingPhase(ctx, clusterConfig)
		if err != nil {
			log.Error(err, "Failed to handle selecting phase for ClusterConfiguration", "name", clusterConfig.Name)
			return ctrl.Result{}, err
		}
		if !found {
			return ctrl.Result{RequeueAfter: pollingInterval}, nil // Wait for the next polling interval
		}
		break
	case clusterv1alpha1.CC_PhaseSwitching:
		found, err := r.handleSwitchingPhase(ctx, clusterConfig)
		if err != nil {
			log.Error(err, "Failed to handle switching phase for ClusterConfiguration", "name", clusterConfig.Name)
			return ctrl.Result{}, err
		}
		if !found {
			return ctrl.Result{RequeueAfter: pollingInterval}, nil // Wait for the next polling interval
		}
		break
	case clusterv1alpha1.CC_PhaseFailed:
		err := r.handleFailedPhase(ctx, clusterConfig)
		if err != nil {
			log.Error(err, "Failed to handle failed phase for ClusterConfiguration", "name", clusterConfig.Name)
			return ctrl.Result{}, err
		}
		break
	case clusterv1alpha1.CC_PhaseCompleted:
		err := r.handleCompletedPhase(ctx, clusterConfig)
		if err != nil {
			log.Error(err, "Failed to handle completed phase for ClusterConfiguration", "name", clusterConfig.Name)
			return ctrl.Result{}, err
		}
		break
	case clusterv1alpha1.CC_PhaseFinished:
		log.Info("ClusterConfiguration is in Finished phase, no further action needed", "name", clusterConfig.Name)
		break
	default:
		log.Info("Unknown phase for ClusterConfiguration, handling as Failed", "name", clusterConfig.Name, "phase", clusterConfig.Status.Phase)
		clusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseFailed
		clusterConfig.Status.Message = "Unknown phase, handle as Failed"
		r.Status().Update(ctx, clusterConfig)
		break
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1alpha1.ClusterConfiguration{}).
		Named("clusterconfiguration").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		Owns(&clusterv1alpha1.NodeHandling{}).
		Owns(&clusterv1alpha1.NodeSelecting{}).
		Complete(r)
}

// CreateNodeSelecting creates a NodeSelecting resource to handle the scaling up/down of the cluster
func (r *ClusterConfigurationReconciler) CreateNodeSelecting(ctx context.Context, k8sClient client.Client, numberOfWorkerToAdd int32, clusterConfig clusterv1alpha1.ClusterConfiguration) error {
	log := log.FromContext(ctx).WithName("create-node-selecting")

	crdNameBytes := make([]byte, 8)
	if _, err := rand.Read(crdNameBytes); err != nil {
		log.Error(err, "unable to generate random name for NodeSelecting CRD")
		return err
	}
	crdName := "node-selecting-" + fmt.Sprintf("%x", crdNameBytes)
	ownerRef := metav1.OwnerReference{
		APIVersion:         clusterConfig.APIVersion,
		Kind:               clusterConfig.Kind,
		Name:               clusterConfig.Name,
		UID:                clusterConfig.UID,
		Controller:         pointer.BoolPtr(true), // segnala che è il controller
		BlockOwnerDeletion: pointer.BoolPtr(true),
	}

	var nodeSelecting = &clusterv1alpha1.NodeSelecting{
		ObjectMeta: metav1.ObjectMeta{
			Name:            crdName,
			Namespace:       "dreem",
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Spec: clusterv1alpha1.NodeSelectingSpec{
			ClusterConfigurationName: clusterConfig.Name,
			ScalingLabel:             numberOfWorkerToAdd,
		},
	}

	if err := r.Client.Create(ctx, nodeSelecting); err != nil {
		log.Error(err, "unable to create NodeSelecting CRD")
		return err
	}
	log.Info("NodeSelecting CRD created", "name", nodeSelecting.Name)
	return nil
}
