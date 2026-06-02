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
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// NodeHandlingReconciler reconciles a NodeHandling object
type NodeHandlingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandlings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandlings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandlings/finalizers,verbs=update

// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update

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

	if nodeHandling.Spec.ScalingLabel > 0 {
		klog.V(2).Info("NodeHandling has to scale up", "name", nodeHandling.Name)
		err := r.scaleUp(ctx, nodeHandling.Spec.ScalingLabel, nodeHandling.Spec.SelectedNode)
		if err != nil {
			nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
			nodeHandling.Status.Message = "Failed scaling up cluster with selected node " + nodeHandling.Spec.SelectedNode
			if updateErr := r.Status().Update(ctx, nodeHandling); updateErr != nil {
				klog.V(2).ErrorS(updateErr, "Failed to update NodeHandling status to Failed", "name", nodeHandling.Name)
				return updateErr
			}
			klog.V(2).ErrorS(err, "Failed to scale up nodes for NodeHandling", "name", nodeHandling.Name)
			return err
		}
	} else if nodeHandling.Spec.ScalingLabel < 0 {
		klog.V(2).Info("NodeHandling has to scale down", "name", nodeHandling.Name)
		err := r.scaleDown(ctx, nodeHandling.Spec.SelectedNode, nodeHandling.Spec.ScalingLabel)

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
func (r *NodeHandlingReconciler) scaleUp(ctx context.Context, scalingLabel int32, selectedNode_string string) error {
	klog.FromContext(ctx).WithName("scale-up")

	// get the secret to access the BMC of the node and perform the power cycle action through Redfish API
	credentialSecret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Name: "bmc-credentials-" + selectedNode_string, Namespace: "dreem"}, credentialSecret); err != nil {
		klog.V(2).ErrorS(err, "Failed to get BMC credentials secret for node "+selectedNode_string)
		return err
	}

	node_bmc_ip := string(credentialSecret.Data["bmc_address"])

	// perform the power cycle action through Redfish API
	endpoint := fmt.Sprintf(TEMPLATE_REDFISH_ENDPOINT, node_bmc_ip, string(credentialSecret.Data["id"]))
	err := performPowerCycleAction(ctx, string(credentialSecret.Data["username"]), string(credentialSecret.Data["password"]), endpoint, "ON")

	klog.V(2).Info("Waiting for the node " + selectedNode_string + " to become Ready")
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	pollInterval := 10 * time.Second
	err = wait.PollUntilContextCancel(waitCtx, pollInterval, true, func(ctx context.Context) (bool, error) {
		node := &corev1.Node{}
		if err := r.Get(ctx, client.ObjectKey{Name: selectedNode_string}, node); err != nil {
			return false, err
		}

		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				klog.V(2).Info("Node " + selectedNode_string + " is Ready")
				return true, nil
			}
		}

		klog.V(3).Info("Waiting for node " + selectedNode_string + " to become Ready")
		return false, nil
	})
	if err != nil {
		klog.V(2).ErrorS(err, "Timed out waiting for Node to become ready")
		return err
	}

	// update the DREEM_POWER_CYCLE_ANNOTATION annotation of the node to keep track of how many times the node has been power cycled
	node := &corev1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: selectedNode_string}, node); err != nil {
		klog.V(2).ErrorS(err, "Failed to get node "+selectedNode_string+" to update power cycle annotation")
		return err
	}

	powerCycleCount := int32(0)
	if val, ok := node.Annotations[DREEM_POWER_CYCLE_ANNOTATION]; ok {
		fmt.Sscanf(val, "%d", &powerCycleCount)
	}
	powerCycleCount++
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	node.Annotations[DREEM_POWER_CYCLE_ANNOTATION] = fmt.Sprintf("%d", powerCycleCount)

	if err := r.Update(ctx, node); err != nil {
		klog.V(2).ErrorS(err, "Failed to update node "+selectedNode_string+" with new power cycle count annotation")
		return err
	}

	return nil
}

func performPowerCycleAction(ctx context.Context, username string, password string, endpoint string, action string) error {
	// Perform a post request to the Redfish API endpoint with the provided credentials and action

	klog.V(2).InfoS("Performing power cycle action through Redfish API", "endpoint", endpoint, "action", action)
	jsonData := map[string]interface{}{}
	switch action {
	case "ON":
		jsonData = map[string]interface{}{
			"ResetType": "On",
		}
	case "OFF":
		jsonData = map[string]interface{}{
			"ResetType": "GracefulShutdown",
		}
	default:
		return fmt.Errorf("Invalid power cycle action: %s", action)
	}

	jsonValue, err := json.Marshal(jsonData)
	if err != nil {
		return fmt.Errorf("Failed to marshal JSON data for Redfish API request: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewBuffer(jsonValue))
	if err != nil {
		return fmt.Errorf("Failed to create HTTP request for Redfish API: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(username, password)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Failed to perform HTTP request to Redfish API: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("Redfish API returned non-success status code: %d", resp.StatusCode)
	}

	klog.V(2).InfoS("Power cycle action performed successfully through Redfish API", "endpoint", endpoint, "action", action)
	return nil
}

func (r *NodeHandlingReconciler) scaleDown(ctx context.Context, selectedNode_string string, scalingLabel int32) error {
	klog.FromContext(ctx).WithName("scale-down")

	// get the secret to access the BMC of the node and perform the power cycle action through Redfish API
	credentialSecret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Name: "bmc-credentials-" + selectedNode_string, Namespace: "dreem"}, credentialSecret); err != nil {
		klog.V(2).ErrorS(err, "Failed to get BMC credentials secret for node "+selectedNode_string)
		return err
	}

	node_bmc_ip := string(credentialSecret.Data["bmc_address"])
	endpoint := fmt.Sprintf(TEMPLATE_REDFISH_ENDPOINT, node_bmc_ip, string(credentialSecret.Data["id"]))
	// perform the power cycle action through Redfish API
	err := performPowerCycleAction(ctx, string(credentialSecret.Data["username"]), string(credentialSecret.Data["password"]), endpoint, "OFF")

	klog.V(2).Info("Waiting for the node " + selectedNode_string + " to shutdown and become NotReady")
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	pollInterval := 10 * time.Second
	err = wait.PollUntilContextCancel(waitCtx, pollInterval, true, func(ctx context.Context) (bool, error) {
		node := &corev1.Node{}
		if err := r.Get(ctx, client.ObjectKey{Name: selectedNode_string}, node); err != nil {
			return false, err
		}

		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				klog.V(2).Info("Node " + selectedNode_string + " is Off")
				return true, nil
			}
		}

		klog.V(3).Info("Waiting for node " + selectedNode_string + " to become NotReady")
		return false, nil
	})
	if err != nil {
		klog.V(2).ErrorS(err, "Timed out waiting for Node to shutdown")
		return err
	}

	return nil

}
