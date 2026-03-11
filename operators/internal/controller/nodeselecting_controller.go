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
	"math/big"
	"strings"

	"github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	clusterapi "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselectings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselectings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselectings/finalizers,verbs=update

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch

// NodeSelectingReconciler reconciles a NodeSelecting object
type NodeSelectingReconciler struct {
	client.Client
	ExternalClient client.Client
	ExternalConfig *rest.Config
	Scheme         *runtime.Scheme
}

func (r *NodeSelectingReconciler) handleInitialPhase(ctx context.Context, nodeSelecting *clusterv1alpha1.NodeSelecting) error {
	klog.FromContext(ctx).WithName("handle-initial-phase")

	// check if another NodeSelecting resource is already in progress for the same clusterConfiguration
	nodeSelectingList := &clusterv1alpha1.NodeSelectingList{}
	if err := r.List(ctx, nodeSelectingList); err != nil {
		klog.V(2).ErrorS(err, "Failed to list NodeSelecting resources")
		return err
	}

	for _, ns := range nodeSelectingList.Items {
		if ns.Spec.ClusterConfigurationName == nodeSelecting.Spec.ClusterConfigurationName {
			if ns.Name != nodeSelecting.Name && ns.Status.Phase == v1alpha1.NS_PhaseRunning {
				klog.V(2).Info("Another NodeSelecting resource is already in progress for the same clusterConfiguration, waiting for it to complete", "name", ns.Name)
				return nil
			}

		}
	}
	// If no other NodeSelecting is in progress, proceed with the current one
	nodeSelecting.Status.Phase = v1alpha1.NS_PhaseRunning
	if err := r.Status().Update(ctx, nodeSelecting); err != nil {
		klog.V(2).ErrorS(err, "Failed to update NodeSelecting resource status to Running", "name", nodeSelecting.Name)
	}

	return nil

}

func (r *NodeSelectingReconciler) handleRunningPhase(ctx context.Context, nodeSelecting *clusterv1alpha1.NodeSelecting) error {
	klog.FromContext(ctx).WithName("handle-running-phase")

	selectedNode := ""
	selectedMD := ""

	if nodeSelecting.Spec.ScalingLabel < 0 {
		klog.V(2).Info("Trying to scale down infrastructure")
		err, MD, dNode := r.selectNodeScaleDown(ctx, nodeSelecting)
		klog.V(2).Info("selectNodeScaleDown returned", "error", err, "selectedMD", MD, "selectedNode", dNode)
		if err != nil {
			return err
		}
		selectedMD = MD
		selectedNode = dNode

	} else if nodeSelecting.Spec.ScalingLabel > 0 {
		klog.V(2).Info("Trying to scale up infrastructure")
		err, MD := r.selectNodeScaleUp(ctx, nodeSelecting, r.Client)
		if err != nil {
			return err
		}
		klog.V(3).Info("Selected MD:", MD, " for scale up")
		selectedMD = MD
		selectedNode = MD
	}

	if selectedMD == "" && selectedNode == "" {
		klog.V(2).Info("No node can be shutdown at the moment")
		nodeSelecting.Status.Phase = v1alpha1.NS_PhaseFailed
		nodeSelecting.Status.Message = "No node can be shutdown at the moment"

	} else if selectedMD != "" && selectedNode != "" {
		klog.V(2).Info("Node and MD selected, creating NodeHandling", "selectedNode", selectedNode, "selectedMD", selectedMD)
		// if the simulation is successful, create the NodeHandling resource
		errNH := r.CreateNodeHandling(ctx, nodeSelecting, selectedNode, selectedMD)
		if errNH != nil {
			klog.V(2).ErrorS(errNH, "Failed to create NodeHandling resource in NodeSelecting", "name", nodeSelecting.Name)
			return errNH
		}

		klog.V(2).Info("Node selected", "node", selectedNode)
		nodeSelecting.Status.SelectedMachineDeployment = selectedMD
		nodeSelecting.Status.SelectedNode = selectedNode

		nodeSelecting.Status.Phase = v1alpha1.NS_PhaseCompleted
	}
	klog.V(2).Info("Updating NodeSelecting status", "phase", nodeSelecting.Status.Phase)
	if err := r.Status().Update(ctx, nodeSelecting); err != nil {
		klog.V(2).ErrorS(err, "Failed to update NodeSelecting resource status to Completed", "name", nodeSelecting.Name)
		return err
	}
	klog.V(2).Info("handleRunningPhase completed successfully")
	return nil

}

func (r *NodeSelectingReconciler) selectNodeScaleUp(ctx context.Context, nodeSelecting *clusterv1alpha1.NodeSelecting, k8sClient client.Client) (error, string) {
	klog.FromContext(ctx).WithName("can-scale-up")

	selectedMD := ""

	// get the list of available nodes in the cluster, apart the control plane (excldue node with label node-role.kubernetes.io/control-plane)
	nodeList := &corev1.NodeList{}
	if err := r.ExternalClient.List(ctx, nodeList); err != nil {
		klog.V(2).ErrorS(err, "failed to list nodes")
		return err, ""

	}
	nodeListFiltered := &corev1.NodeList{}
	for _, node := range nodeList.Items {
		if _, isControlPlane := node.Labels["node-role.kubernetes.io/control-plane"]; !isControlPlane {
			nodeListFiltered.Items = append(nodeListFiltered.Items, node)
		}
	}

	mdList := clusterapi.MachineDeploymentList{}
	if err := r.Client.List(ctx, &mdList); err != nil {
		klog.V(2).ErrorS(err, "failed to list MachineDeployments")
		return err, ""
	}

	rankedNodes, err := ApplySoftConstraintsScaleUp(ctx, mdList.Items, k8sClient, *nodeSelecting)

	if err != nil {
		klog.V(2).ErrorS(err, "Failed to apply soft constraints for node selection in scale up")
		return err, ""
	}

	if len(rankedNodes) == 0 {
		klog.V(2).Info("No machine deployments available after applying soft constraints")
		return nil, ""
	}

	selectedMD = rankedNodes[0].Name
	klog.V(3).Info("SELECTED MD:", selectedMD)

	return nil, selectedMD
}

func (r *NodeSelectingReconciler) selectNodeScaleDown(ctx context.Context, nodeSelecting *clusterv1alpha1.NodeSelecting) (error, string, string) {
	klog.FromContext(ctx).WithName("can-scale-down")

	selectedNode := ""
	selectedMD := ""

	// get the list of available nodes in the cluster, apart the control plane (excldue node with label node-role.kubernetes.io/control-plane)
	nodeList := &corev1.NodeList{}
	if err := r.ExternalClient.List(ctx, nodeList); err != nil {
		klog.V(2).ErrorS(err, "failed to list nodes")
		return err, "", ""

	}
	nodeListFiltered := &corev1.NodeList{}
	for _, node := range nodeList.Items {
		if _, isControlPlane := node.Labels["node-role.kubernetes.io/control-plane"]; !isControlPlane {
			nodeListFiltered.Items = append(nodeListFiltered.Items, node)
		}
	}

	nodeList = nodeListFiltered
	validNodeToShutdown := make([]corev1.Node, 0) // nodes that can be shutdown after hard-constraints check
	valid := make([]Combination, 0)               // valid scheduling configurations
	total_valid := make([]Combination, 0)
	// for each node, check the pods that must be migrated
	for _, node := range nodeList.Items {

		podToScheduleList, err := r.getMigratablePodsOnNode(ctx, node.Name)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get migratable pods on node", "node", node.Name)
			continue
		}
		//fmt.Print("Pod to schedule list for node ", node.Name, ":\n")
		// for _, p := range podToScheduleList {
		// 	fmt.Print(" - ", p.Name, "\n")
		// }

		// take the candidates nodes excluding the current one
		candidateNodes := &corev1.NodeList{}
		for _, n := range nodeList.Items {
			if n.Name != node.Name {
				candidateNodes.Items = append(candidateNodes.Items, n)
			}
		}
		// fmt.Print("Candidates nodes for node ", node.Name, ":\n")
		// for _, cn := range candidateNodes.Items {
		// 	fmt.Print(" - ", cn.Name, "\n")
		// }
		// fmt.Print("\n")

		numNodes := len(candidateNodes.Items)

		if numNodes == 0 {
			continue
		}

		// Generate all scheduling combinations
		combinations := GenerateCombinations(podToScheduleList, candidateNodes.Items)

		// check hard-constraints

		valid = CheckResources(combinations)
		klog.V(3).Info("Valid after resources check:", "count", len(valid), "node", node.Name)
		valid = CheckNodeAffinity(valid)
		klog.V(3).Info("Valid after node affinity check:", "count", len(valid), "node", node.Name)
		valid = CheckTaints(valid)
		klog.V(3).Info("Valid after taints check:", "count", len(valid), "node", node.Name)
		valid = CheckInterPodAffinity(ctx, r.Client, valid)
		klog.V(3).Info("Valid after inter-pod affinity check:", "count", len(valid), "node", node.Name)
		klog.V(3).Infof("Node %s: %d valid scheduling configurations found after hard-constraints check", node.Name, len(valid))
		if len(valid) == 0 {
			klog.V(3).Info("No valid scheduling found for node", "node", node.Name)
			continue
		} else {
			total_valid = append(total_valid, valid...)
		}
		// if at least one valid configuration is found, the node can be shutdown
		klog.V(3).Info("Valid scheduling found for node", "node", node.Name)
		validNodeToShutdown = append(validNodeToShutdown, node)
	}
	// fmt.Printf("Total %d nodes can be shutdown after hard constraints check\n", len(validNodeToShutdown))

	if len(validNodeToShutdown) == 0 {
		klog.V(3).Info("No node can be shutdown after checking hard constraints")
		return nil, selectedMD, selectedNode
	}

	// Check if there's at least one non-empty combination
	hasNonEmptyCombination := false
	for _, comb := range total_valid {
		if len(comb) > 0 {
			hasNonEmptyCombination = true
			break
		}
	}
	if !hasNonEmptyCombination {
		klog.V(3).Info("All combinations are empty, no valid scheduling configuration found")
		return nil, selectedMD, selectedNode
	}

	// select randomly one of the valid nodes to shutdown
	index, _ := rand.Int(rand.Reader, big.NewInt(int64(len(total_valid))))

	// check whether it is an empty combination. if so, choose another one
	for len(total_valid[index.Int64()]) == 0 {
		index, _ = rand.Int(rand.Reader, big.NewInt(int64(len(total_valid))))
	}

	klog.V(2).Info("Calling ApplySoftConstraints", "numAssignments", len(total_valid[index.Int64()]), "numNodes", len(validNodeToShutdown))
	rankedNodes, err := ApplySoftConstraints(total_valid[index.Int64()], validNodeToShutdown, ctx, r.ExternalClient, r.Client, *nodeSelecting)
	klog.V(2).Info("ApplySoftConstraints returned", "hasError", err != nil, "numRankedNodes", len(rankedNodes))
	if err != nil {
		klog.V(2).ErrorS(err, "Failed to apply soft constraints for node selection")
		return err, "", ""
	}

	if len(rankedNodes) == 0 {
		klog.V(2).Info("No nodes available after applying soft constraints")
		return nil, selectedMD, selectedNode
	}

	selectedNode = rankedNodes[0].Name
	selectedMD = getMDfromNode(selectedNode, r.Client, ctx)

	klog.V(2).Info("Scale down selection completed", "selectedNode", selectedNode, "selectedMD", selectedMD)
	return nil, selectedMD, selectedNode
}

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *NodeSelectingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.FromContext(ctx).WithName("nodeselecting-reconciler")

	nodeSelecting := &clusterv1alpha1.NodeSelecting{}
	if err := r.Get(ctx, req.NamespacedName, nodeSelecting); err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(1).Info("NodeSelecting resource not found, ignoring since object must be deleted")
		}
	}
	klog.V(1).Info("Reconciling NodeSelecting resource", "name", nodeSelecting.Name)

	switch nodeSelecting.Status.Phase {
	case "":
		if err := r.handleInitialPhase(ctx, nodeSelecting); err != nil {
			klog.V(1).ErrorS(err, "Failed to handle Initial phase for NodeSelecting resource", "name", nodeSelecting.Name)
			return ctrl.Result{}, err
		}

	case v1alpha1.NS_PhaseRunning:
		err := r.handleRunningPhase(ctx, nodeSelecting)
		if err != nil {
			klog.V(1).ErrorS(err, "Failed to handle Running phase for NodeSelecting resource", "name", nodeSelecting.Name)
			return ctrl.Result{}, err
		}

	case v1alpha1.NS_PhaseFailed:
		klog.V(1).Info("NodeSelecting resource is in Failed phase, no further action needed", "name", nodeSelecting.Name)

	case v1alpha1.NS_PhaseCompleted:
		klog.V(1).Info("NodeSelecting resource is in Completed phase, no further action needed", "name", nodeSelecting.Name)

	default:
		klog.V(1).Info("Unknown phase, handling as Failed", "name", nodeSelecting.Name)
		nodeSelecting.Status.Phase = v1alpha1.NS_PhaseFailed
		if err := r.Status().Update(ctx, nodeSelecting); err != nil {
			klog.V(1).ErrorS(err, "Failed to update NodeSelecting resource status to Failed", "name", nodeSelecting.Name)
			return ctrl.Result{}, err
		}

	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeSelectingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Registra l'indice sul campo spec.nodeName dei Pod
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
		pod := obj.(*corev1.Pod)
		if pod.Spec.NodeName == "" {
			return nil
		}
		return []string{pod.Spec.NodeName}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1alpha1.NodeSelecting{}).
		Complete(r)
}

func (r *NodeSelectingReconciler) CreateNodeHandling(ctx context.Context, nodeSelecting *clusterv1alpha1.NodeSelecting, selectedNode string, selectedMD string) error {
	klog.FromContext(ctx).WithName("create-node-handling")

	crdName := "node-handling-" + nodeSelecting.Spec.ClusterConfigurationName
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
		klog.V(2).ErrorS(err, "Failed to create NodeHandling resource", "name", crdName)
		return err
	}

	return nil

}

// ----- PODS LISTING -----

// returns the list of all the pods that should be migrated on another node
// it exccludes pods like deamonset, mirrored/static pods, etc.
func (r *NodeSelectingReconciler) getMigratablePodsOnNode(ctx context.Context, nodeName string) ([]corev1.Pod, error) {
	var allPods corev1.PodList
	err := r.ExternalClient.List(ctx, &allPods, client.MatchingFields{"spec.nodeName": nodeName})
	if err != nil {
		return nil, err
	}

	migratable := make([]corev1.Pod, 0, len(allPods.Items))
	for _, pod := range allPods.Items {

		if isStaticPod(pod) || isDaemonSetPod(pod) {
			continue
		}

		if _, isMirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; isMirror {
			continue
		}

		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}

		if pod.Namespace == "monitoring" || strings.HasPrefix(pod.Namespace, "kube") {
			continue
		}

		migratable = append(migratable, pod)
	}

	return migratable, nil
}

func isDaemonSetPod(pod corev1.Pod) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

func isStaticPod(pod corev1.Pod) bool {
	_, isMirror := pod.Annotations[corev1.MirrorPodAnnotationKey]
	return isMirror
}
