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
	"time"

	"golang.org/x/exp/rand"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// NodeHandlingReconciler reconciles a NodeHandling object
type NodeHandlingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandlings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandlings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandlings/finalizers,verbs=update

// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments,verbs=list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments/status,verbs=get;update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch;update

func (r *NodeHandlingReconciler) handleInitialPhase(ctx context.Context, nodeHandling *clusterv1alpha1.NodeHandling) error {
	log := log.FromContext(ctx).WithName("handle-initial-phase")

	//check if another NodeHandling is already in progress for the same clusterConfiguration
	nodeHandlingList := &clusterv1alpha1.NodeHandlingList{}
	if err := r.List(ctx, nodeHandlingList); err != nil {
		log.Error(err, "Failed to list NodeSelecting resources")
		return err
	}

	for _, nh := range nodeHandlingList.Items {
		if nh.Spec.ClusterConfigurationName == nodeHandling.Spec.ClusterConfigurationName {

			if nh.Name != nodeHandling.Name && nh.Status.Phase == clusterv1alpha1.NH_PhaseRunning {
				log.Info("Another NodeHandling resource is already in progress for the same clusterConfiguration, waiting for it to complete", "name", nh.Name)
				return nil
			}
		}
	}

	nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseRunning
	if err := r.Status().Update(ctx, nodeHandling); err != nil {
		log.Error(err, "Failed to update NodeHandling status to Running", "name", nodeHandling.Name)
		return err
	}

	return nil
}

func (r *NodeHandlingReconciler) handleRunningPhase(ctx context.Context, nodeHandling *clusterv1alpha1.NodeHandling) error {
	log := log.FromContext(ctx).WithName("handle-running-phase")

	if nodeHandling.Spec.ScalingLabel > 0 {
		log.Info("NodeHandling has to scale up", "name", nodeHandling.Name)
		err := r.scaleUp(ctx, nodeHandling.Spec.ScalingLabel)
		if err != nil {
			nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
			nodeHandling.Status.Message = "Failed scaling up cluster"
			if updateErr := r.Status().Update(ctx, nodeHandling); updateErr != nil {
				log.Error(updateErr, "Failed to update NodeHandling status to Failed", "name", nodeHandling.Name)
				return updateErr
			}
			log.Error(err, "Failed to scale up nodes for NodeHandling", "name", nodeHandling.Name)
			return err
		}
	} else if nodeHandling.Spec.ScalingLabel < 0 {
		log.Info("NodeHandling has to scale down", "name", nodeHandling.Name)
		err := r.scaleDown(ctx, nodeHandling.Spec.SelectedNode, nodeHandling.Spec.SelectedMachineDeployment, nodeHandling.Spec.ScalingLabel)

		if err != nil {
			nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
			nodeHandling.Status.Message = "Failed scaling down cluster"
			if updateErr := r.Status().Update(ctx, nodeHandling); updateErr != nil {
				log.Error(updateErr, "Failed to update NodeHandling status to Failed", "name", nodeHandling.Name)
				return updateErr
			}
			log.Error(err, "Failed to scale down nodes for NodeHandling", "name", nodeHandling.Name)
			return err
		}
	} else {
		log.Info("NodeHandling has no scaling action to perform", "name", nodeHandling.Name)
		nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseCompleted
		if err := r.Status().Update(ctx, nodeHandling); err != nil {
			log.Error(err, "Failed to update NodeHandling status to Completed", "name", nodeHandling.Name)
			return err
		}
	}

	nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseCompleted
	if err := r.Status().Update(ctx, nodeHandling); err != nil {
		log.Error(err, "Failed to update NodeHandling status to Completed", "name", nodeHandling.Name)
		return err
	}

	return nil
}

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *NodeHandlingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	nodeHandling := &clusterv1alpha1.NodeHandling{}
	if err := r.Get(ctx, req.NamespacedName, nodeHandling); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "unable to fetch NodeHandling")
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("reconciling resource", "name", nodeHandling.Name)

	switch nodeHandling.Status.Phase {
	case "":
		err := r.handleInitialPhase(ctx, nodeHandling)
		if err != nil {
			log.Error(err, "Failed to handle initial phase for NodeHandling", "name", nodeHandling.Name)
			return ctrl.Result{}, err
		}
		break
	case clusterv1alpha1.NH_PhaseRunning:
		err := r.handleRunningPhase(ctx, nodeHandling)
		if err != nil {
			log.Error(err, "Failed to handle running phase for NodeHandling", "name", nodeHandling.Name)
			return ctrl.Result{}, err
		}
		break
	case clusterv1alpha1.NH_PhaseCompleted:
		log.Info("NodeHandling is in Completed phase, no further actions will be taken")
		break
	case clusterv1alpha1.NH_PhaseFailed:
		log.Info("NodeHandling is in failed phase, no further actions will be taken")
		break

	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeHandlingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1alpha1.NodeHandling{}).
		Named("nodehandling").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		Complete(r)
}
func (r *NodeHandlingReconciler) scaleUp(ctx context.Context, scalingLabel int32) error {
	log := log.FromContext(ctx).WithName("scale-up")

	// get the MachineDeployment and update the replicas
	machineDeploymentList := &clusterv1.MachineDeploymentList{}
	if err := r.List(ctx, machineDeploymentList); err != nil {
		log.Error(err, "Failed to list MachineDeployments")
		return err
	}

	if len(machineDeploymentList.Items) == 0 {
		log.Info("No MachineDeployments found")
		return nil
	}

	// Select a random MachineDeployment from the list and scale it up
	randomIndex := rand.Intn(len(machineDeploymentList.Items))
	machineDeployment := &machineDeploymentList.Items[randomIndex]
	clusterNamespace := machineDeployment.Namespace
	machineDeploymentName := machineDeployment.Name

	var newReplicas int32
	if machineDeployment.Spec.Replicas != nil {
		newReplicas = *machineDeployment.Spec.Replicas + scalingLabel
	} else {
		newReplicas = scalingLabel
	}
	log.Info("Scaling MachineDeployment", "name", machineDeployment.Name,
		"currentReplicas", machineDeployment.Spec.Replicas,
		"desiredReplicas", newReplicas)
	machineDeployment.Spec.Replicas = &newReplicas

	if err := r.Update(ctx, machineDeployment); err != nil {
		log.Error(err, "Failed to update MachineDeployment replicas", "name", machineDeploymentName)
		return err
	}

	log.Info("Waiting for MachineDeployment to have desired number of ReadyReplicas", "desiredReplicas", newReplicas)

	// Wait until ReadyReplicas matches the desired replicas
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	pollInterval := 10 * time.Second
	err := wait.PollUntilContextCancel(waitCtx, pollInterval, true, func(ctx context.Context) (bool, error) {
		md := &clusterv1.MachineDeployment{}
		if err := r.Get(ctx, client.ObjectKey{Name: machineDeploymentName, Namespace: clusterNamespace}, md); err != nil {
			return false, err
		}

		log.Info("Polling MachineDeployment status", "name", machineDeploymentName, "ReadyReplicas", md.Status.ReadyReplicas, "DesiredReplicas", newReplicas)

		if md.Status.ReadyReplicas == newReplicas {
			log.Info("MachineDeployment successfully scaled", "replicas", md.Status.ReadyReplicas)
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		log.Error(err, "Timed out waiting for MachineDeployment to become ready")
		return err
	}

	return nil
}

func (r *NodeHandlingReconciler) scaleDown(ctx context.Context, selectedNode string, selectedMD string, scalingLabel int32) error {
	log := log.FromContext(ctx).WithName("scale-down")

	// get the MachineDeployment of the selected node and update the replicas
	if selectedNode == "" {
		log.Info("No selected node provided for scaling down, cannot proceed")
		return nil
	}
	selectedNodeObj := &clusterv1.Machine{}
	if err := r.Get(ctx, client.ObjectKey{Name: selectedNode, Namespace: "default"}, selectedNodeObj); err != nil {
		log.Error(err, "Failed to get Machine", "name", selectedNode)
		return err
	}

	if selectedNodeObj.Annotations == nil {
		selectedNodeObj.Annotations = make(map[string]string)
	}

	selectedNodeObj.Annotations["cluster.x-k8s.io/delete-machine"] = ""

	if err := r.Update(ctx, selectedNodeObj); err != nil {
		log.Error(err, "Failed to annotate Machine for deletion")
		return err
	}

	machineDeploymentObj := &clusterv1.MachineDeployment{}
	if err := r.Get(ctx, client.ObjectKey{Name: selectedMD, Namespace: "default"}, machineDeploymentObj); err != nil {
		log.Error(err, "Failed to get MachineDeployment", "name", selectedMD)
		return err
	}

	if machineDeploymentObj.Spec.Replicas != nil {
		newReplicas := *machineDeploymentObj.Spec.Replicas + scalingLabel
		if newReplicas < 0 {
			log.Info("Scaling down to zero replicas, setting replicas to zero", "name", machineDeploymentObj.Name)
			newReplicas = 0
		}
		machineDeploymentObj.Spec.Replicas = &newReplicas
		if err := r.Update(ctx, machineDeploymentObj); err != nil {
			log.Error(err, "Failed to update MachineDeployment replicas")
			return err
		}

		log.Info("Waiting for MachineDeployment to scale", "desiredReplicas", newReplicas)

		waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()

		pollInterval := 10 * time.Second
		err := wait.PollUntilContextCancel(waitCtx, pollInterval, true, func(ctx context.Context) (bool, error) {
			md := &clusterv1.MachineDeployment{}
			if err := r.Get(ctx, client.ObjectKey{Name: selectedMD, Namespace: machineDeploymentObj.Namespace}, md); err != nil {
				return false, err
			}

			if md.Status.ReadyReplicas == newReplicas {
				log.Info("MachineDeployment successfully scaled", "replicas", md.Status.ReadyReplicas)
				return true, nil
			}

			log.Info("Waiting for MachineDeployment to reach desired replica count",
				"name", selectedMD,
				"current", machineDeploymentObj.Status.ReadyReplicas,
				"desired", newReplicas,
			)

			return false, nil
		})

		if err != nil {
			log.Error(err, "Timed out waiting for MachineDeployment to scale")
			return err
		}
	} else {
		log.Info("No replicas specified in MachineDeployment, cannot scale down", "name", machineDeploymentObj.Name)
	}

	return nil

}
