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
	"strings"

	"github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	//clusterapi "sigs.k8s.io/cluster-api/api/v1beta1"

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
	MaxNumberOfConfigurations int
	Scheme                    *runtime.Scheme
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
	msg := ""

	if nodeSelecting.Spec.ScalingLabel < 0 {
		klog.V(2).Info("Trying to scale down infrastructure")
		err, message, node := r.selectNodeScaleDown(ctx, nodeSelecting)
		if err != nil {
			return err
		}
		selectedNode = node
		msg = message

	} else if nodeSelecting.Spec.ScalingLabel > 0 {
		klog.V(2).Info("Trying to scale up infrastructure")
		err, message, node := r.selectNodeScaleUp(ctx, nodeSelecting, r.Client)
		if err != nil {
			return err
		}
		selectedNode = node
		msg = message
	}

	if selectedNode == "" {
		klog.V(2).Info("No node can be shutdown at the moment")
		nodeSelecting.Status.Phase = v1alpha1.NS_PhaseFailed
		nodeSelecting.Status.Message = "No node can be shutdown at the moment"

	} else {
		klog.V(2).Info("Node selected, creating NodeHandling", "selectedNode", selectedNode)
		// if the simulation is successful, create the NodeHandling resource
		errNH := r.CreateNodeHandling(ctx, nodeSelecting, selectedNode)
		if errNH != nil {
			klog.V(2).ErrorS(errNH, "Failed to create NodeHandling resource in NodeSelecting", "name", nodeSelecting.Name)
			return errNH
		}

		klog.V(2).Info("Node selected", "node", selectedNode)
		nodeSelecting.Status.SelectedNode = selectedNode
		nodeSelecting.Status.Message = msg
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

func (r *NodeSelectingReconciler) selectNodeScaleUp(ctx context.Context, nodeSelecting *clusterv1alpha1.NodeSelecting, k8sClient client.Client) (error, string, string) {
	klog.FromContext(ctx).WithName("can-scale-up")

	selectedNode := ""

	// get the list of available nodes in the cluster.
	// Keep only worker nodes that are NotReady, because they represent powered-off nodes.
	nodeList := &corev1.NodeList{}
	if err := r.Client.List(ctx, nodeList); err != nil {
		klog.V(2).ErrorS(err, "failed to list nodes")
		return err, "", ""

	}
	nodeListFiltered := corev1.NodeList{}
	for _, node := range nodeList.Items {
		if isControlPlaneNode(node) {
			continue
		}
		if isNodeReady(node) {
			continue
		}

		nodeListFiltered.Items = append(nodeListFiltered.Items, node)
	}

	rankedNodes, err := ApplySoftConstraintsScaleUp(ctx, nodeListFiltered, k8sClient, *nodeSelecting)

	if err != nil {
		klog.V(2).ErrorS(err, "Failed to apply soft constraints for node selection in scale up")
		return err, "", ""
	}

	if len(rankedNodes) == 0 {
		klog.V(2).Info("No machine deployments available after applying soft constraints")
		return nil, "", ""
	}

	selectedNode = rankedNodes[0].Name
	klog.V(3).Info("SELECTED node:", selectedNode)

	return nil, "", selectedNode
}

// scaleDownCandidates returns the worker nodes that can take part in a scale-down: the
// ones that are Ready and schedulable. They are both the nodes that may be switched off
// and the destinations the evicted pods are planned onto.
//
// A cordoned node is still Ready, so the Ready check alone let it through: a node that
// a previous NodeHandling is draining, or that an administrator has cordoned, would be
// offered as a destination for pods the scheduler can never put there, and could be
// selected for shutdown a second time.
func scaleDownCandidates(nodes []corev1.Node) []corev1.Node {
	candidates := make([]corev1.Node, 0, len(nodes))
	for _, node := range nodes {
		if isControlPlaneNode(node) || !isNodeReady(node) {
			continue
		}
		if node.Spec.Unschedulable {
			klog.V(3).Info("Node is cordoned, left out of the scale-down", "node", node.Name)
			continue
		}
		candidates = append(candidates, node)
	}
	return candidates
}

func isControlPlaneNode(node corev1.Node) bool {
	if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; ok {
		return true
	}
	_, ok := node.Labels["node-role.kubernetes.io/master"]
	return ok
}

func isNodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *NodeSelectingReconciler) selectNodeScaleDown(ctx context.Context, nodeSelecting *clusterv1alpha1.NodeSelecting) (error, string, string) {
	klog.FromContext(ctx).WithName("can-scale-down")

	selectedNode := ""

	// get the list of available nodes in the cluster.
	// Keep only worker nodes that are Ready and schedulable, because they represent
	// powered-on nodes the scheduler can use.
	nodeList := &corev1.NodeList{}
	if err := r.Client.List(ctx, nodeList); err != nil {
		klog.V(2).ErrorS(err, "failed to list nodes")
		return err, "", ""

	}
	nodeList = &corev1.NodeList{Items: scaleDownCandidates(nodeList.Items)}
	for _, node := range nodeList.Items {
		klog.V(3).Info("Node candidate for scale down:", "node", node.Name)
	}

	// For every candidate node build the single plan that would evacuate it, and keep
	// that plan alongside the node. Each node is then scored against its own plan, so
	// the soft criteria answer "what does the application lose if THIS node goes away".
	validNodeToShutdown := make([]corev1.Node, 0) // nodes that survive the hard constraints
	plans := make(map[string]Combination, len(nodeList.Items))

	for _, node := range nodeList.Items {

		klog.V(2).Info("Getting migratable pods for node", "node", node.Name)
		podToScheduleList, err := r.getMigratablePodsOnNode(ctx, node.Name)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get migratable pods on node", "node", node.Name)
			continue
		}
		klog.V(2).Info("Got migratable pods", "node", node.Name, "count", len(podToScheduleList))

		// take the candidate destinations, excluding the node being drained
		candidateNodes := make([]corev1.Node, 0, len(nodeList.Items))
		for _, n := range nodeList.Items {
			if n.Name != node.Name {
				candidateNodes = append(candidateNodes, n)
			}
		}
		if len(candidateNodes) == 0 {
			continue
		}

		plan, err := BuildMigrationPlan(ctx, r.Client, node, candidateNodes, podToScheduleList)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to build a migration plan for node", "node", node.Name)
			continue
		}
		if plan == nil {
			klog.V(3).Info("No feasible migration plan, node cannot be drained", "node", node.Name)
			continue
		}

		klog.V(3).Info("Migration plan built", "node", node.Name, "podsToMove", len(plan))
		plans[node.Name] = plan
		validNodeToShutdown = append(validNodeToShutdown, node)
	}

	if len(validNodeToShutdown) == 0 {
		klog.V(3).Info("No node can be shutdown after checking hard constraints")
		return nil, "", selectedNode
	}

	klog.V(2).Info("Calling ApplySoftConstraints ", " numPlans ", len(plans), " numNodes ", len(validNodeToShutdown))
	rankedNodes, message, err := ApplySoftConstraints(plans, validNodeToShutdown, ctx, r.Client, *nodeSelecting)
	klog.V(2).Info("ApplySoftConstraints returned ", " hasError ", err != nil, " numRankedNodes ", len(rankedNodes))
	if err != nil {
		klog.V(2).ErrorS(err, "Failed to apply soft constraints for node selection")
		return err, "", ""
	}

	if len(rankedNodes) == 0 {
		klog.V(2).Info("No nodes available after applying soft constraints")
		return nil, "", selectedNode
	}

	selectedNode = rankedNodes[0].Name

	klog.V(2).Info("Scale down selection completed", "selectedNode", selectedNode)
	return nil, message, selectedNode
}

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *NodeSelectingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.FromContext(ctx).WithName("nodeselecting-reconciler")

	nodeSelecting := &clusterv1alpha1.NodeSelecting{}
	if err := r.Get(ctx, req.NamespacedName, nodeSelecting); err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(1).Info("NodeSelecting resource not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
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

func (r *NodeSelectingReconciler) CreateNodeHandling(ctx context.Context, nodeSelecting *clusterv1alpha1.NodeSelecting, selectedNode string) error {
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
			ClusterConfigurationName: nodeSelecting.Spec.ClusterConfigurationName,
			NodeSelectingName:        nodeSelecting.Name,
			SelectedNode:             selectedNode,
			ScalingLabel:             nodeSelecting.Spec.ScalingLabel,
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
	err := r.Client.List(ctx, &allPods, client.MatchingFields{"spec.nodeName": nodeName})
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
