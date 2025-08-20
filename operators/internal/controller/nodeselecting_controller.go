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
	"os"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubectl/pkg/drain"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
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
	fullName, err := r.resolveFullNodeName(ctx, selectedNode)
	fmt.Print("AAAAA, fullName: ", fullName, "\n")
	if selectedNode == "" {
		nodeSelecting.Status.Phase = v1alpha1.NS_PhaseFailed
		nodeSelecting.Status.Message = "Server failed getting a valid node for the scaling"
		if err := r.Status().Update(ctx, nodeSelecting); err != nil {
			log.Error(err, "Failed to update NodeSelecting resource status to Failed", "name", nodeSelecting.Name)
			return err
		}
		return fmt.Errorf("no node selected, null result on NodeSelecting %s", nodeSelecting.Name)
	}

	if nodeSelecting.Spec.ScalingLabel < 0 {
		// emulate drain operation
		isDrainable, err := r.canDrain(ctx, fullName)
		if err != nil {
			log.Error(err, "Failed to check if the node can be drained", "node", fullName)
			nodeSelecting.Status.Phase = v1alpha1.NS_PhaseFailed
			nodeSelecting.Status.Message = "Failed to check if the node can be drained: " + err.Error()
			if updateErr := r.Status().Update(ctx, nodeSelecting); updateErr != nil {
				log.Error(updateErr, "Failed to update NodeSelecting resource status to Failed", "name", nodeSelecting.Name)
				return updateErr
			}
			return err
		}

		if isDrainable {
			// if the node can be drained, proceed with the drain operation
			err := r.drainNode(ctx, fullName)
			if err != nil {
				log.Error(err, "Failed to drain node", "node", fullName)
				nodeSelecting.Status.Phase = v1alpha1.NS_PhaseFailed
				nodeSelecting.Status.Message = "Failed to drain node: " + err.Error()
				if updateErr := r.Status().Update(ctx, nodeSelecting); updateErr != nil {
					log.Error(updateErr, "Failed to update NodeSelecting resource status to Failed", "name", nodeSelecting.Name)
					return updateErr
				}
				return err
			}
		} else {
			nodeSelecting.Status.Phase = v1alpha1.NS_PhaseFailed
			nodeSelecting.Status.Message = "Draining simulation failed: no other node available for the drain operation"
			if err := r.Status().Update(ctx, nodeSelecting); err != nil {
				log.Error(err, "Failed to update NodeSelecting resource status to Failed", "name", nodeSelecting.Name)
				return err
			}
			return fmt.Errorf("no other node available for the drain operation on NodeSelecting %s", nodeSelecting.Name)
		}
	}
	// if the simulation is successful, create the NodeHandling resource
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

// checks if the pods on the node that has been selected to be shutdown can be drained on another available node
func (r *NodeSelectingReconciler) canDrain(ctx context.Context, selectedNode string) (bool, error) {
	pods, err := r.getMigratablePodsOnNode(ctx, selectedNode)
	if err != nil {
		return false, fmt.Errorf("failed to get migratable pods on node %s: %w", selectedNode, err)
	}

	schedulableNodes, err := r.getAvailableTargetNodes(ctx, selectedNode)
	if err != nil {
		return false, fmt.Errorf("failed to get available target nodes: %w", err)
	}

	nodeAvailable := make(map[string]AvailableResources)
	nodeInfo := make(map[string]corev1.Node)
	for _, node := range schedulableNodes {
		nodeAvailable[node.Name] = AvailableResources{
			CPU:    node.Status.Allocatable[corev1.ResourceCPU].DeepCopy(),
			Memory: node.Status.Allocatable[corev1.ResourceMemory].DeepCopy(),
		}
		nodeInfo[node.Name] = node
	}

	// pods that will be assigned to nodes
	assignedPods := make(map[string][]*corev1.Pod)

	// sort pods by CPU requests in descending order
	sort.Slice(pods, func(i, j int) bool {
		return podRequestsCPU(&pods[i]).Cmp(*podRequestsCPU(&pods[j])) > 0
	})

	for i := range pods {
		pod := &pods[i]
		reqCPU := podRequestsCPU(pod)
		reqMem := podRequestsMemory(pod)

		scheduled := false
		for nodeName, available := range nodeAvailable {
			node := nodeInfo[nodeName]

			if reqCPU.Cmp(available.CPU) > 0 || reqMem.Cmp(available.Memory) > 0 {
				continue
			}

			// Check for nodeAffinity labels
			if !matchesNodeAffinity(pod, &node) {
				continue
			}

			// check for anti-affinity rules (semplified)
			if !satisfiesPodAffinity(ctx, pod, &node, assignedPods[nodeName]) {
				continue
			}

			available.CPU.Sub(*reqCPU)
			available.Memory.Sub(*reqMem)
			nodeAvailable[nodeName] = available
			assignedPods[nodeName] = append(assignedPods[nodeName], pod)
			scheduled = true
			break
		}

		if !scheduled {
			return false, nil
		}
	}

	return true, nil
}

func matchesNodeAffinity(pod *corev1.Pod, node *corev1.Node) bool {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil {
		return true
	}

	required := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil {
		return true
	}

	nodeLabels := node.Labels

	// Ogni NodeSelectorTerm è un OR
	for _, term := range required.NodeSelectorTerms {
		if matchNodeSelectorTerm(term, nodeLabels) {
			return true
		}
	}

	return false
}

func matchNodeSelectorTerm(term corev1.NodeSelectorTerm, labels map[string]string) bool {
	//  matchExpressions
	for _, expr := range term.MatchExpressions {
		value, exists := labels[expr.Key]

		switch expr.Operator {
		case corev1.NodeSelectorOpIn:
			if !exists || !contains(expr.Values, value) {
				return false
			}
		case corev1.NodeSelectorOpNotIn:
			if exists && contains(expr.Values, value) {
				return false
			}
		case corev1.NodeSelectorOpExists:
			if !exists {
				return false
			}
		case corev1.NodeSelectorOpDoesNotExist:
			if exists {
				return false
			}
		default:
			return false
		}
	}

	return true
}

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func satisfiesPodAffinity(ctx context.Context, pod *corev1.Pod, node *corev1.Node, assigned []*corev1.Pod) bool {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAntiAffinity == nil {
		return true
	}

	for _, term := range pod.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		selector, err := metav1.LabelSelectorAsSelector(term.LabelSelector)
		if err != nil {
			return false
		}

		for _, otherPod := range assigned {

			if otherPod.Namespace != pod.Namespace {
				continue
			}

			if selector.Matches(labels.Set(otherPod.Labels)) {
				for _, topologyKey := range []string{term.TopologyKey} {
					if node.Labels[topologyKey] == node.Labels[topologyKey] {
						return false
					}
				}
			}
		}
	}

	return true
}

// drainNode drains the node by evicting all pods that can be migrated to other nodes
func (r *NodeSelectingReconciler) drainNode(ctx context.Context, nodeName string) error {
	// get the capi kubernetes config
	externalclient, cfg, err := getExternalClient()
	if err != nil {
		return fmt.Errorf("failed to get external client: %w", err)
	}

	node := &corev1.Node{}
	err = externalclient.Get(ctx, client.ObjectKey{Name: nodeName, Namespace: ""}, node)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("node %s not found", nodeName)
		}
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	// Crea il client client-go
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	// Create a drain helper
	helper := &drain.Helper{
		Ctx:                 ctx,
		Client:              clientset,
		Force:               true,
		IgnoreAllDaemonSets: true,
		DeleteEmptyDirData:  true,
		GracePeriodSeconds:  -1,
		Timeout:             0,
		Out:                 os.Stdout,
		ErrOut:              os.Stderr,
	}

	// Cordon the node: Disable scheduling on the node
	if err := drain.RunCordonOrUncordon(helper, node, true); err != nil {
		return fmt.Errorf("failed to cordon node: %w", err)
	}

	// Drain the node
	if err := drain.RunNodeDrain(helper, nodeName); err != nil {
		return fmt.Errorf("failed to drain node: %w", err)
	}

	return nil
}

func intPtr(i int) *int { return &i }

// returns the list of all the pods that should be migrated on another node
// it exccludes pods like deamonset, mirrored/static pods, etc.
func (r *NodeSelectingReconciler) getMigratablePodsOnNode(ctx context.Context, nodeName string) ([]corev1.Pod, error) {
	var allPods corev1.PodList
	err := r.List(ctx, &allPods, client.MatchingFields{"spec.nodeName": nodeName})
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

		if pod.Namespace == "kube-system" {
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

// return the list of all the nodes except the one that is being drained
func (r *NodeSelectingReconciler) getAvailableTargetNodes(ctx context.Context, nodeName string) ([]corev1.Node, error) {
	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		return nil, err
	}

	var schedulableNodes []corev1.Node
	for _, node := range nodes.Items {
		if node.Name != nodeName {
			schedulableNodes = append(schedulableNodes, node)
		}
	}

	return schedulableNodes, nil
}

type AvailableResources struct {
	CPU    resource.Quantity
	Memory resource.Quantity
}

func podRequestsCPU(pod *corev1.Pod) *resource.Quantity {
	total := resource.MustParse("0")
	for _, c := range pod.Spec.Containers {
		total.Add(c.Resources.Requests[corev1.ResourceCPU])
	}
	return &total
}
func podRequestsMemory(pod *corev1.Pod) *resource.Quantity {
	total := resource.MustParse("0")
	for _, c := range pod.Spec.Containers {
		total.Add(c.Resources.Requests[corev1.ResourceMemory])
	}
	return &total
}

func getExternalClient() (client.Client, *rest.Config, error) {
	// Carica la config da file kubeconfig
	cfg, err := clientcmd.BuildConfigFromFlags("", "/Users/marco/.kube/capi")
	if err != nil {
		return nil, nil, err
	}

	// Crea client controller-runtime con la config esterna
	cl, err := client.New(cfg, client.Options{})
	if err != nil {
		return nil, nil, err
	}
	return cl, cfg, nil
}

// func getExternalClientFromCAPICluster(mgmtClient client.Client, clusterName, namespace string) (client.Client, *rest.Config, error) {
// 	ctx := context.Background()

// 	// Prendi la risorsa Cluster
// 	capiCluster := &clusterv1.Cluster{}
// 	err := mgmtClient.Get(ctx, client.ObjectKey{Name: clusterName, Namespace: namespace}, capiCluster)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("failed to get CAPI cluster: %w", err)
// 	}

// 	// Il nome del Secret è convenzionalmente <cluster-name>-kubeconfig
// 	secretName := fmt.Sprintf("%s-kubeconfig", capiCluster.Name)
// 	secret := &corev1.Secret{}
// 	err = mgmtClient.Get(ctx, client.ObjectKey{Name: secretName, Namespace: namespace}, secret)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("failed to get kubeconfig secret: %w", err)
// 	}

// 	kubeconfigBytes, ok := secret.Data["value"]
// 	if !ok {
// 		return nil, nil, fmt.Errorf("secret %s missing 'value' key", secretName)
// 	}

// 	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("failed to build rest config: %w", err)
// 	}

// 	workloadClient, err := client.New(restCfg, client.Options{})
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("failed to create workload client: %w", err)
// 	}

// 	return workloadClient, restCfg, nil
// }

func (r *NodeSelectingReconciler) resolveFullNodeName(ctx context.Context, nodename string) (string, error) {
	var nodeList corev1.NodeList
	fmt.Printf("Resolving full node name for: %s\n", nodename)
	externalclient, _, err := getExternalClient()
	if err != nil {
		return "", fmt.Errorf("failed to get external client: %w", err)
	}

	if err := externalclient.List(ctx, &nodeList); err != nil {
		return "", fmt.Errorf("failed to list nodes: %w", err)
	}

	for _, node := range nodeList.Items {
		fmt.Printf("Checking node: %s\n", node.Name)
		if strings.Contains(node.Name, nodename) {
			return node.Name, nil
		}
	}

	return "", fmt.Errorf("no node found starting with: %s", nodename)
}
