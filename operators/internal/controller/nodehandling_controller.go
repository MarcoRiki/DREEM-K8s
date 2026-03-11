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
	"fmt"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

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

// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch;update

func (r *NodeHandlingReconciler) handleInitialPhase(ctx context.Context, nodeHandling *clusterv1alpha1.NodeHandling) error {
	klog.FromContext(ctx).WithName("handle-initial-phase")

	//check if another NodeHandling is already in progress for the same clusterConfiguration
	nodeHandlingList := &clusterv1alpha1.NodeHandlingList{}
	if err := r.List(ctx, nodeHandlingList); err != nil {
		klog.V(2).ErrorS(err, "Failed to list NodeSelecting resources")
		return err
	}

	for _, nh := range nodeHandlingList.Items {
		if nh.Spec.ClusterConfigurationName == nodeHandling.Spec.ClusterConfigurationName {

			if nh.Name != nodeHandling.Name && nh.Status.Phase == clusterv1alpha1.NH_PhaseRunning {
				klog.V(2).Info("Another NodeHandling resource is already in progress for the same clusterConfiguration, waiting for it to complete", "name", nh.Name)
				return nil
			}
		}
	}

	nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseRunning
	if err := r.Status().Update(ctx, nodeHandling); err != nil {
		klog.V(2).ErrorS(err, "Failed to update NodeHandling status to Running", "name", nodeHandling.Name)
		return err
	}

	return nil
}

func (r *NodeHandlingReconciler) handleRunningPhase(ctx context.Context, nodeHandling *clusterv1alpha1.NodeHandling) error {
	klog.FromContext(ctx).WithName("handle-running-phase")

	klog.V(2).Info("Calling Cluster API", "name", nodeHandling.Name)

	if nodeHandling.Spec.ScalingLabel > 0 {
		klog.V(2).Info("NodeHandling has to scale up", "name", nodeHandling.Name)
		err := r.scaleUp(ctx, nodeHandling.Spec.ScalingLabel, nodeHandling.Spec.SelectedMachineDeployment)
		if err != nil {
			nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
			nodeHandling.Status.Message = "Failed scaling up cluster"
			if updateErr := r.Status().Update(ctx, nodeHandling); updateErr != nil {
				klog.V(2).ErrorS(updateErr, "Failed to update NodeHandling status to Failed", "name", nodeHandling.Name)
				return updateErr
			}
			klog.V(2).ErrorS(err, "Failed to scale up nodes for NodeHandling", "name", nodeHandling.Name)
			return err
		}
	} else if nodeHandling.Spec.ScalingLabel < 0 {
		klog.V(2).Info("NodeHandling has to scale down", "name", nodeHandling.Name)
		err := r.scaleDown(ctx, nodeHandling.Spec.SelectedNode, nodeHandling.Spec.SelectedMachineDeployment, nodeHandling.Spec.ScalingLabel)

		if err != nil {
			nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
			nodeHandling.Status.Message = "Failed scaling down cluster"
			if updateErr := r.Status().Update(ctx, nodeHandling); updateErr != nil {
				klog.V(2).ErrorS(updateErr, "Failed to update NodeHandling status to Failed", "name", nodeHandling.Name)
				return updateErr
			}
			klog.V(2).ErrorS(err, "Failed to scale down nodes for NodeHandling", "name", nodeHandling.Name)
			return err
		}
	} else {
		klog.V(2).Info("NodeHandling has no scaling action to perform", "name", nodeHandling.Name)
		nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseCompleted
		if err := r.Status().Update(ctx, nodeHandling); err != nil {
			klog.V(2).ErrorS(err, "Failed to update NodeHandling status to Completed", "name", nodeHandling.Name)
			return err
		}
	}

	nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseCompleted
	if err := r.Status().Update(ctx, nodeHandling); err != nil {
		klog.V(2).ErrorS(err, "Failed to update NodeHandling status to Completed", "name", nodeHandling.Name)
		return err
	}

	return nil
}

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *NodeHandlingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.FromContext(ctx).WithName("nodehandling-reconciler")

	nodeHandling := &clusterv1alpha1.NodeHandling{}
	if err := r.Get(ctx, req.NamespacedName, nodeHandling); err != nil {
		if !apierrors.IsNotFound(err) {
			klog.V(1).ErrorS(err, "unable to fetch NodeHandling")
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	klog.V(1).InfoS("reconciling resource", "name", nodeHandling.Name)

	switch nodeHandling.Status.Phase {
	case "":
		err := r.handleInitialPhase(ctx, nodeHandling)
		if err != nil {
			klog.V(1).ErrorS(err, "Failed to handle initial phase for NodeHandling", "name", nodeHandling.Name)
			return ctrl.Result{}, err
		}
	case clusterv1alpha1.NH_PhaseRunning:
		err := r.handleRunningPhase(ctx, nodeHandling)
		if err != nil {
			klog.V(1).ErrorS(err, "Failed to handle running phase for NodeHandling", "name", nodeHandling.Name)
			return ctrl.Result{}, err
		}
	case clusterv1alpha1.NH_PhaseCompleted:
		klog.V(1).Info("NodeHandling is in Completed phase, no further actions will be taken")
	case clusterv1alpha1.NH_PhaseFailed:
		klog.V(1).Info("NodeHandling is in failed phase, no further actions will be taken")

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
func (r *NodeHandlingReconciler) scaleUp(ctx context.Context, scalingLabel int32, selectedMD_string string) error {
	klog.FromContext(ctx).WithName("scale-up")

	// get the MachineDeployment and update the replicas
	machineDeploymentList := &clusterv1.MachineDeploymentList{}
	if err := r.List(ctx, machineDeploymentList); err != nil {
		klog.V(2).ErrorS(err, "Failed to list MachineDeployments")
		return err
	}

	if len(machineDeploymentList.Items) == 0 {
		klog.V(2).Info("No MachineDeployments found")
		return nil
	}

	machineDeployment := &clusterv1.MachineDeployment{}
	if selectedMD_string != "" {
		// If a specific MachineDeployment is selected, use it
		for _, md := range machineDeploymentList.Items {
			if md.Name == selectedMD_string {
				machineDeployment = &md
				break
			}
		}
	}
	clusterNamespace := machineDeployment.Namespace
	machineDeploymentName := machineDeployment.Name

	var newReplicas int32
	if machineDeployment.Spec.Replicas != nil {
		newReplicas = *machineDeployment.Spec.Replicas + scalingLabel
	} else {
		newReplicas = scalingLabel
	}
	klog.V(2).Info("Scaling MachineDeployment", "name", machineDeployment.Name,
		"currentReplicas", machineDeployment.Spec.Replicas,
		"desiredReplicas", newReplicas)
	machineDeployment.Spec.Replicas = &newReplicas

	if err := r.Update(ctx, machineDeployment); err != nil {
		klog.V(2).ErrorS(err, "Failed to update MachineDeployment replicas", "name", machineDeploymentName)
		return err
	}

	klog.V(2).Info("Waiting for MachineDeployment to have desired number of ReadyReplicas", "desiredReplicas", newReplicas)
	// Wait until ReadyReplicas matches the desired replicas
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	pollInterval := 10 * time.Second
	err := wait.PollUntilContextCancel(waitCtx, pollInterval, true, func(ctx context.Context) (bool, error) {
		md := &clusterv1.MachineDeployment{}
		if err := r.Get(ctx, client.ObjectKey{Name: machineDeploymentName, Namespace: clusterNamespace}, md); err != nil {
			return false, err
		}

		klog.V(2).Info("Polling MachineDeployment status", "name", machineDeploymentName, "ReadyReplicas", md.Status.ReadyReplicas, "DesiredReplicas", newReplicas)

		if md.Status.ReadyReplicas == newReplicas {
			klog.V(1).Info("MachineDeployment successfully scaled", "replicas", md.Status.ReadyReplicas)

			// upadate the cyclecount annotation for the MachineDeployment
			if md.Annotations == nil {
				md.Annotations = make(map[string]string)
			}
			cycleCount := 1
			if val, ok := md.Annotations[DREEM_POWER_CYCLE_ANNOTATION]; ok {
				var err error
				cycleCount, err = strconv.Atoi(val)
				if err != nil {
					klog.V(2).ErrorS(err, "Failed to convert cycle-count annotation to int", "value", val)
					cycleCount = 1
				}
				cycleCount++
			}
			md.Annotations[DREEM_POWER_CYCLE_ANNOTATION] = strconv.Itoa(cycleCount)

			if err := r.Update(ctx, md); err != nil {
				klog.V(2).ErrorS(err, "Failed to update MachineDeployment with new cycle-count annotation", "name", machineDeploymentName)
				return false, err
			}
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		klog.V(2).ErrorS(err, "Timed out waiting for MachineDeployment to become ready")
		return err
	}

	return nil
}

func (r *NodeHandlingReconciler) scaleDown(ctx context.Context, selectedNode string, selectedMD string, scalingLabel int32) error {
	klog.FromContext(ctx).WithName("scale-down")

	// get the MachineDeployment of the selected node and update the replicas
	if selectedNode == "" {
		klog.V(2).Info("No selected node provided for scaling down, cannot proceed")
		return nil
	}

	// Find the Machine by listing all machines and matching by name
	machineList := &clusterv1.MachineList{}
	if err := r.List(ctx, machineList); err != nil {
		klog.V(2).ErrorS(err, "Failed to list Machines")
		return err
	}

	var selectedNodeObj *clusterv1.Machine
	for i, machine := range machineList.Items {
		if machine.Name == selectedNode {
			selectedNodeObj = &machineList.Items[i]
			break
		}
	}

	if selectedNodeObj == nil {
		err := fmt.Errorf("Machine %s not found", selectedNode)
		klog.V(2).ErrorS(err, "Failed to find Machine", "name", selectedNode)
		return err
	}

	if selectedNodeObj.Annotations == nil {
		selectedNodeObj.Annotations = make(map[string]string)
	}

	selectedNodeObj.Annotations["cluster.x-k8s.io/delete-machine"] = ""

	if err := r.Update(ctx, selectedNodeObj); err != nil {
		klog.V(2).ErrorS(err, "Failed to annotate Machine for deletion")
		return err
	}

	// Find the MachineDeployment by listing all machinedeployments and matching by name
	machineDeploymentList := &clusterv1.MachineDeploymentList{}
	if err := r.List(ctx, machineDeploymentList); err != nil {
		klog.V(2).ErrorS(err, "Failed to list MachineDeployments")
		return err
	}

	var machineDeploymentObj *clusterv1.MachineDeployment
	for i, md := range machineDeploymentList.Items {
		if md.Name == selectedMD {
			machineDeploymentObj = &machineDeploymentList.Items[i]
			break
		}
	}

	if machineDeploymentObj == nil {
		err := fmt.Errorf("MachineDeployment %s not found", selectedMD)
		klog.V(2).ErrorS(err, "Failed to find MachineDeployment", "name", selectedMD)
		return err
	}

	if machineDeploymentObj.Spec.Replicas != nil {
		newReplicas := *machineDeploymentObj.Spec.Replicas + scalingLabel
		if newReplicas < 0 {
			klog.V(2).InfoS("Scaling down to zero replicas, setting replicas to zero", "name", machineDeploymentObj.Name)
			newReplicas = 0
		}
		machineDeploymentObj.Spec.Replicas = &newReplicas

		if err := r.Update(ctx, machineDeploymentObj); err != nil {
			klog.V(2).ErrorS(err, "Failed to update MachineDeployment replicas")
			return err
		}

		klog.V(2).Info("Waiting for MachineDeployment to scale", "desiredReplicas", newReplicas)

		waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()

		pollInterval := 10 * time.Second
		err := wait.PollUntilContextCancel(waitCtx, pollInterval, true, func(ctx context.Context) (bool, error) {
			md := &clusterv1.MachineDeployment{}
			if err := r.Get(ctx, client.ObjectKey{Name: selectedMD, Namespace: machineDeploymentObj.Namespace}, md); err != nil {
				return false, err
			}

			if md.Status.ReadyReplicas == newReplicas {
				klog.V(2).Info("MachineDeployment ", selectedMD, " successfully scaled", "replicas", md.Status.ReadyReplicas)
				return true, nil
			}

			klog.V(3).Info("Waiting for MachineDeployment to reach desired replica count",
				"name", selectedMD,
				"current", machineDeploymentObj.Status.ReadyReplicas,
				"desired", newReplicas,
			)

			return false, nil
		})

		if err != nil {
			klog.V(2).ErrorS(err, "Timed out waiting for MachineDeployment to scale")
			return err
		}
	} else {
		klog.V(2).Info("No replicas specified in MachineDeployment, cannot scale down", "name", machineDeploymentObj.Name)
	}

	return nil

}
