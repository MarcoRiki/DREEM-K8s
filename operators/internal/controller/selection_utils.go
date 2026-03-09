package controller

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
	clusterapi "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// generateCombinations returns all possible node assignments:
// for each pod, choose one of the candidate nodes.
// Example: 3 pods, 2 nodes → 8 configurations.
type Assignment struct {
	Pod  corev1.Pod
	Node corev1.Node
}

type Combination []Assignment

// Generate all combinations of assigning pods to candidate nodes.
func GenerateCombinations(pods []corev1.Pod, nodes []corev1.Node) []Combination {
	if len(nodes) == 0 {
		return nil
	}
	if len(pods) == 0 {
		return []Combination{{}}
	}

	numPods := len(pods)
	numNodes := len(nodes)
	total := pow(numNodes, numPods) // combinazioni totali = numNodes^numPods

	combinations := make([]Combination, 0, total)

	// generiamo tutte le combinazioni usando base numNodes
	for i := 0; i < total; i++ {
		tmp := i
		comb := make(Combination, numPods)

		for p := 0; p < numPods; p++ {
			nodeIndex := tmp % numNodes
			tmp = tmp / numNodes

			comb[p] = Assignment{
				Pod:  pods[p],
				Node: nodes[nodeIndex],
			}
		}

		combinations = append(combinations, comb)
	}

	return combinations
}

// integer power
func pow(a, b int) int {
	res := 1
	for i := 0; i < b; i++ {
		res *= a
	}
	return res
}

// ----- RESOURCE CHECKING -----

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

func CheckResources(combinations []Combination) []Combination {
	if combinations == nil {
		return nil
	}
	if len(combinations) == 0 {
		return []Combination{{}}
	}
	validCombinations := make([]Combination, 0)

	for _, comb := range combinations {
		// mappa per tenere traccia delle risorse disponibili su ogni nodo
		nodeAvailable := make(map[string]AvailableResources)

		// inizializza le risorse disponibili per ogni nodo
		for _, assignment := range comb {
			if _, exists := nodeAvailable[assignment.Node.Name]; !exists {
				nodeAvailable[assignment.Node.Name] = AvailableResources{
					CPU:    assignment.Node.Status.Allocatable[corev1.ResourceCPU].DeepCopy(),
					Memory: assignment.Node.Status.Allocatable[corev1.ResourceMemory].DeepCopy(),
				}
			}
		}

		valid := true

		// verifica se le risorse richieste dai pod possono essere soddisfatte
		for _, assignment := range comb {
			reqCPU := podRequestsCPU(&assignment.Pod)
			reqMem := podRequestsMemory(&assignment.Pod)

			available := nodeAvailable[assignment.Node.Name]

			if reqCPU.Cmp(available.CPU) > 0 || reqMem.Cmp(available.Memory) > 0 {
				valid = false
				break
			}

			// aggiorna le risorse disponibili
			available.CPU.Sub(*reqCPU)
			available.Memory.Sub(*reqMem)
			nodeAvailable[assignment.Node.Name] = available
		}

		if valid {
			validCombinations = append(validCombinations, comb)
		}
	}

	return validCombinations
}

// --- NODE AFFINITY CHECKING ---

func CheckNodeAffinity(combo []Combination) []Combination {
	validCombinations := make([]Combination, 0)

	for _, comb := range combo {
		valid := true
		for _, assignment := range comb {
			if !matchesNodeAffinity(&assignment.Pod, &assignment.Node) {
				valid = false
				break
			}
		}
		if valid {
			validCombinations = append(validCombinations, comb)
		}
	}

	return validCombinations
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

// ----- TAINTS CHECKING -----

func CheckTaints(combo []Combination) []Combination {
	validCombinations := make([]Combination, 0)

	for _, comb := range combo {
		valid := true
		for _, assignment := range comb {
			if !toleratesTaints(&assignment.Pod, &assignment.Node) {
				valid = false
				break
			}
		}
		if valid {
			validCombinations = append(validCombinations, comb)
		}
	}

	return validCombinations

}
func toleratesTaints(pod *corev1.Pod, node *corev1.Node) bool {
	for _, taint := range node.Spec.Taints {

		taintTolerated := false

		for _, tol := range pod.Spec.Tolerations {

			if tol.Effect != "" && tol.Effect != taint.Effect {
				continue
			}

			if tol.Key != taint.Key {
				continue
			}

			if tol.Operator == corev1.TolerationOpExists {
				taintTolerated = true
				break
			}

			if tol.Operator == corev1.TolerationOpEqual || tol.Operator == "" {
				if tol.Value == taint.Value {
					taintTolerated = true
					break
				}
			}
		}

		if !taintTolerated {
			return false
		}
	}

	return true
}

// ----- POD AFFINITY/ANTI-AFFINITY CHECKING -----

// Filtra le combinazioni valide rispetto all'inter-pod affinity
func CheckInterPodAffinity(ctx context.Context, r client.Client, combinations []Combination) []Combination {
	validCombinations := make([]Combination, 0)

	for _, comb := range combinations {

		// raggruppiamo i nuovi pod per nodo
		podsByNode := map[string][]corev1.Pod{}
		for _, asg := range comb {
			podsByNode[asg.Node.Name] = append(podsByNode[asg.Node.Name], asg.Pod)
		}

		valid := true

		// controlliamo nodo per nodo
		for nodeName, newPods := range podsByNode {

			// 1) recuperiamo i pod già sul nodo
			existing := &corev1.PodList{}
			err := r.List(ctx, existing, client.MatchingFields{"spec.nodeName": nodeName})
			if err != nil {
				// errore → scarta la combinazione
				valid = false
				break
			}

			existingPods := filterSystemPods(existing.Items)

			// 2) controlliamo affinity/anti-affinity:
			//    nuovi <-> esistenti     e     nuovi <-> nuovi

			if !checkAffinityOnNode(existingPods, newPods) {
				valid = false
				break
			}
		}

		if valid {
			validCombinations = append(validCombinations, comb)
		}
	}

	return validCombinations
}

func filterSystemPods(pods []corev1.Pod) []corev1.Pod {
	out := []corev1.Pod{}
	for _, p := range pods {
		if strings.HasPrefix(p.Namespace, "kube-") || p.Namespace == "kube-system" {
			continue
		}
		if p.Labels["k8s-app"] == "kube-proxy" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func checkAffinityOnNode(existing []corev1.Pod, newPods []corev1.Pod) bool {

	// nuovi vs esistenti
	for _, np := range newPods {
		for _, ep := range existing {
			if !checkPodAffinityPair(&np, &ep) {
				return false
			}
			if !checkPodAffinityPair(&ep, &np) { // anti-affinity è bidirezionale
				return false
			}
		}
	}

	// nuovi vs nuovi
	for i := 0; i < len(newPods); i++ {
		for j := i + 1; j < len(newPods); j++ {
			if !checkPodAffinityPair(&newPods[i], &newPods[j]) {
				return false
			}
			if !checkPodAffinityPair(&newPods[j], &newPods[i]) {
				return false
			}
		}
	}

	return true
}

func checkPodAffinityPair(a, b *corev1.Pod) bool {
	aff := a.Spec.Affinity
	if aff == nil {
		return true
	}

	// --- PodAffinity rules ---
	if aff.PodAffinity != nil {
		for _, term := range aff.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			if matchPodAffinityTerm(b, &term) {
				// ok, almeno un termine è soddisfatto
				return true
			} else {
				// un required non soddisfatto = fallisce subito
				return false
			}
		}
	}

	// --- PodAntiAffinity rules ---
	if aff.PodAntiAffinity != nil {
		for _, term := range aff.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			if matchPodAffinityTerm(b, &term) {
				// anti-affinity violata
				return false
			}
		}
	}

	return true
}

func matchPodAffinityTerm(pod *corev1.Pod, term *corev1.PodAffinityTerm) bool {
	// match sul namespace (ignoriamo namespaceSelector per semplicità)
	if len(term.Namespaces) > 0 {
		found := false
		for _, ns := range term.Namespaces {
			if ns == pod.Namespace {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// match labelSelector
	selector, err := metav1.LabelSelectorAsSelector(term.LabelSelector)
	if err != nil {
		return false
	}

	return selector.Matches(labels.Set(pod.Labels))
}

// ----- PV/VOLUME CHECKING -----

func checkVolumes(combo []Combination) []Combination {
	return nil
}

// ---- SOFT CONSTRAINTS (PREFERENCES) -----

type TOPSISCriteria struct {
	Node                          corev1.Node
	PowerCycle                    int     // cost, to minimize
	EnergyProfile                 float64 // benefit, to maximize in scale down (higher values represent higher consumption, so worse nodes)
	PreferredNodeAffinity         int     // benefit, to maximize
	PrefererredInterPodAffinity   int     // benefit, to maximize
	PreferredInterPodAntiAffinity int     // benefit, to maximize
	NumberOfRunningPods           int     // cost, to minimize
}

type TOPSISCriteriaScaleUp struct {
	MachineDeployment clusterapi.MachineDeployment
	PowerCycle        int     // cost, to minimize
	EnergyProfile     float64 // cost, to minimize in scale up (lower values represent higher efficiency, so better nodes)
}

type RankedNode struct {
	RelativeCloseness float64
	Node              corev1.Node
}

type RankedMachineDeployment struct {
	RelativeCloseness float64
	MachineDeployment clusterapi.MachineDeployment
}

type AHPweights struct {
	Profile                       string
	PowerCycle                    float64
	EnergyProfile                 float64
	PreferredNodeAffinity         float64
	PrefererredInterPodAffinity   float64
	PreferredInterPodAntiAffinity float64
	NumberOfRunningPods           float64
}

type AHPweightsScaleUp struct {
	PowerCycle    float64
	EnergyProfile float64
}

type ahpProfileCM struct {
	PreferredNodeAffinity    string `yaml:"PreferredNodeAffinity"`
	PreferredPodAffinity     string `yaml:"PreferredPodAffinity"`
	PreferredPodAntiAffinity string `yaml:"PreferredPodAntiAffinity"`
	EnergyProfile            string `yaml:"EnergyProfile"`
	PowerCycles              string `yaml:"PowerCycles"`
	NumberOfRunningPods      string `yaml:"NumberOfRunningPods"`
}

func ApplySoftConstraintsScaleUp(ctx context.Context, md []clusterapi.MachineDeployment, k8sClient client.Client, nodeSelecting clusterv1alpha1.NodeSelecting) ([]clusterapi.MachineDeployment, error) {
	klog.FromContext(ctx).WithName("Apply-TOPSIS-scale-up")

	var criteriaList []TOPSISCriteriaScaleUp

	for _, d := range md {
		if d.Spec.Replicas == nil || *d.Spec.Replicas == 0 { // eligible for scale up

			// get power cycle count
			powerCycleStr, ok := d.Annotations[DREEM_POWER_CYCLE_ANNOTATION]
			if !ok {
				klog.V(2).Info("Power cycle annotation not found for MachineDeployment", "md", d.Name)
				continue
			}
			powerCycle, err := strconv.Atoi(powerCycleStr)
			if err != nil {
				klog.V(2).ErrorS(err, "Failed to convert power cycle annotation to int for MachineDeployment", "md", d.Name)
				continue
			}

			// get the energy profile for each node
			energyProfileStr, ok := d.Annotations[DREEM_ENERGY_EFFICIENCY_ANNOTATION]
			if !ok {
				klog.V(2).Info("Energy efficiency annotation not found for MachineDeployment", "md", d.Name)
				continue
			}
			energyProfile, err := strconv.ParseFloat(energyProfileStr, 64)
			if err != nil {
				klog.V(2).ErrorS(err, "Failed to convert energy efficiency annotation to float for MachineDeployment", "md", d.Name)
				continue
			}
			criteria := TOPSISCriteriaScaleUp{
				MachineDeployment: d,
				PowerCycle:        powerCycle,
				EnergyProfile:     energyProfile,
			}
			criteriaList = append(criteriaList, criteria)

		} else { // if there is a replica, the server is already up
			continue
		}
	}

	// load weights
	weights, err := LoadAHPweightsScaleUp(ctx, k8sClient)
	if err != nil {
		klog.V(2).ErrorS(err, "Failed to load AHP weights for scale up")
		return nil, err
	}

	// apply TOPSIS for scale up
	rankedNodes, err := ApplyTOPSISScaleUp(criteriaList, weights, nodeSelecting, k8sClient, ctx)
	if err != nil {
		klog.V(2).ErrorS(err, "Failed to apply TOPSIS for scale up")
		return nil, err
	}

	return rankedNodes, nil
}

func ApplySoftConstraints(validScheduling []Assignment, nodes []corev1.Node, ctx context.Context, managedClusterClient client.Client, managementClusterClient client.Client, nodeSelecting clusterv1alpha1.NodeSelecting) ([]corev1.Node, error) {
	klog.FromContext(ctx).WithName("Apply-TOPSIS-scale-down")
	klog.V(2).Info("Applying soft constraints with TOPSIS")
	// Fill the structure with criteria values
	var criteriaList []TOPSISCriteria
	for _, node := range nodes {

		// get number of running pods
		numPods, err := GetNumberOfRunningPods(node, ctx, managedClusterClient)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get number of running pods on node", "node", node.Name)
			continue
		}

		// get power cycle count
		powerCycle, err := GetPowerCycle(node, ctx, managementClusterClient)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get power cycle count for node", "node", node.Name)
			continue
		}

		// get the energy profile for each node
		energyProfile, err := GetEnergyProfile(node, ctx, managementClusterClient)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get energy profile for node", "node", node.Name)
			continue
		}

		// compute preferred node affinity
		preferredNodeAffinity := GetPreferredNodeAffinity(node, validScheduling)

		// compute preferred inter-pod affinity
		preferredInterPodAffinity, err := GetPreferredInterPodAffinity(node, validScheduling, managedClusterClient, ctx)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get preferred inter-pod affinity for node", "node", node.Name)
			continue
		}

		preferredInterPodAntiAffinity, err := GetPreferredInterPodAntiAffinity(node, validScheduling, managedClusterClient, ctx)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get preferred inter-pod anti-affinity for node", "node", node.Name)
			continue
		}

		klog.V(3).Info("NODE:", node.Name, " POWER CYCLE:", powerCycle, " ENERGY PROFILE:", energyProfile, " PREF NODE AFFINITY:", preferredNodeAffinity, " PREF INTER-POD AFFINITY:", preferredInterPodAffinity, " PREF INTER-POD ANTI-AFFINITY:", preferredInterPodAntiAffinity, " NUM PODS:", numPods)

		criteria := TOPSISCriteria{
			Node:                          node,
			PowerCycle:                    powerCycle,
			EnergyProfile:                 energyProfile,
			PreferredNodeAffinity:         preferredNodeAffinity,
			PrefererredInterPodAffinity:   preferredInterPodAffinity,
			PreferredInterPodAntiAffinity: preferredInterPodAntiAffinity,
			NumberOfRunningPods:           numPods,
		}
		criteriaList = append(criteriaList, criteria)
	}

	// load weights
	weights, err := LoadAHPweights(managementClusterClient, ctx)
	if err != nil {
		klog.V(2).ErrorS(err, "Failed to load AHP weights")
		return nil, err
	}
	// apply TOPSIS
	rankedNodes, err := ApplyTOPSIS(criteriaList, weights, nodeSelecting, managementClusterClient, ctx)
	if err != nil {
		klog.V(2).ErrorS(err, "Failed to apply TOPSIS")
		return nil, err
	}

	// extract ordered node list
	nodes = make([]corev1.Node, 0, len(rankedNodes))
	for _, crit := range rankedNodes {
		nodes = append(nodes, crit.Node)
	}

	return nodes, nil
}

func GetPowerCycle(nodeManagedCluster corev1.Node, ctx context.Context, managementClient client.Client) (int, error) {
	// retrieve the power cycle count from MachineDeployment annotations

	// retrieve the machine whose NodeName matches the node in the managed cluster
	machineClusterAPI := &clusterapi.Machine{}
	machineList := &clusterapi.MachineList{}

	err := managementClient.List(ctx, machineList)
	if err != nil {
		return 0, err
	}

	if len(machineList.Items) == 0 {
		return 0, fmt.Errorf("no Machine found for Node %s", nodeManagedCluster.Name)
	}

	// find the machine with the matching NodeName
	for i, machine := range machineList.Items {
		if *machine.Spec.ProviderID == nodeManagedCluster.Spec.ProviderID {
			machineClusterAPI = &machineList.Items[i]
			break
		}
	}

	if machineClusterAPI == nil {
		return 0, fmt.Errorf(
			"no Machine found matching Node %s",
			nodeManagedCluster.Name,
		)
	}

	machineDeploymentName, ok := machineClusterAPI.Labels[CAPI_MACHINE_DEPLOYMENT_LABEL]
	if !ok {
		return 0, fmt.Errorf("machine %s does not have MachineDeployment label", machineClusterAPI.Name)
	}

	machineDeployment := &clusterapi.MachineDeployment{}
	err = managementClient.Get(ctx, client.ObjectKey{Name: machineDeploymentName, Namespace: "default"}, machineDeployment)
	if err != nil {
		return 0, err
	}

	// add annotation only if not present
	if _, ok := machineDeployment.Annotations[DREEM_POWER_CYCLE_ANNOTATION]; !ok {
		if machineDeployment.Annotations == nil {
			machineDeployment.Annotations = make(map[string]string)
		}
		machineDeployment.Annotations[DREEM_POWER_CYCLE_ANNOTATION] = "0"
		err = managementClient.Update(ctx, machineDeployment)
		if err != nil {
			return 0, err
		}
	}

	powerCycleStr, ok := machineDeployment.Annotations[DREEM_POWER_CYCLE_ANNOTATION]
	if !ok {
		return 0, nil
	}

	powerCycleCount, err := strconv.Atoi(powerCycleStr)
	if err != nil {
		return 0, err
	}

	return powerCycleCount, nil

}

func GetEnergyProfile(nodeManagedCluster corev1.Node, ctx context.Context, managementClient client.Client) (float64, error) {
	// retrieve the energy efficiency from MachineDeployment annotation
	machineClusterAPI := &clusterapi.Machine{}
	machineList := &clusterapi.MachineList{}

	err := managementClient.List(ctx, machineList)
	if err != nil {
		return 0, err
	}

	if len(machineList.Items) == 0 {
		return 0, fmt.Errorf("no Machine found for Node %s", nodeManagedCluster.Name)
	}

	// find the machine with the matching NodeName
	for i, machine := range machineList.Items {
		if *machine.Spec.ProviderID == nodeManagedCluster.Spec.ProviderID {
			machineClusterAPI = &machineList.Items[i]
			break
		}
	}

	if machineClusterAPI == nil {
		return 0, fmt.Errorf(
			"no Machine found matching Node %s",
			nodeManagedCluster.Name,
		)
	}
	machineDeploymentName, ok := machineClusterAPI.Labels[CAPI_MACHINE_DEPLOYMENT_LABEL]
	if !ok {
		return 0, nil
	}
	machineDeployment := &clusterapi.MachineDeployment{}
	err = managementClient.Get(ctx, client.ObjectKey{Name: machineDeploymentName, Namespace: "default"}, machineDeployment)
	if err != nil {
		return 0, err
	}

	// return error if annotation not present
	energyProfileStr, ok := machineDeployment.Annotations[DREEM_ENERGY_EFFICIENCY_ANNOTATION]
	if !ok {
		return 0, fmt.Errorf("energy efficiency annotation not found for MachineDeployment %s", machineDeploymentName)
	}

	energyProfile, err := strconv.ParseFloat(energyProfileStr, 64)
	if err != nil {
		return 0, err
	}

	return energyProfile, nil
}

func GetPreferredNodeAffinity(node corev1.Node, validSchedulingConfig []Assignment) int {

	// reproduce the logic to count preferred node affinity weights used by kube-scheduler
	// for each pod checks if it has preferred node affinity to the node
	// if they match, sum the weight

	totalWeight := 0
	for _, assignment := range validSchedulingConfig {
		pod := assignment.Pod
		if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil {
			continue
		}

		preferred := pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
		for _, term := range preferred {
			if matchNodeSelectorTerm(term.Preference, node.Labels) {
				totalWeight += int(term.Weight)
			}
		}
	}
	return totalWeight
}

func GetPreferredInterPodAffinity(node corev1.Node, validSchedulingConfig []Assignment, managedClusterClient client.Client, ctx context.Context) (int, error) {

	// reproduce the logic to count preferred node affinity weights used by kube-scheduler
	// for each pod checks if it has preferred pod affinity to the pods on the selected node
	// if they match, sum the weight

	// as first, get the pods scheduled on the seelected node
	podsOnNode := corev1.PodList{}
	err := managedClusterClient.List(ctx, &podsOnNode, client.MatchingFields{"spec.nodeName": node.Name})
	if err != nil {
		return 0, err
	}

	// check the affinity between the existing pods and the pods to schedule
	totalWeight := 0
	for _, assignment := range validSchedulingConfig {
		pod := assignment.Pod
		if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAffinity == nil {
			continue
		}

		preferred := pod.Spec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution
		for _, term := range preferred {
			selector, err := metav1.LabelSelectorAsSelector(term.PodAffinityTerm.LabelSelector)
			if err != nil {
				continue
			}

			// check against existing pods on the node
			for _, existingPod := range podsOnNode.Items {
				// check namespace
				if len(term.PodAffinityTerm.Namespaces) > 0 {
					found := false
					for _, ns := range term.PodAffinityTerm.Namespaces {
						if ns == existingPod.Namespace {
							found = true
							break
						}
					}
					if !found {
						continue
					}
				}
				if selector.Matches(labels.Set(existingPod.Labels)) {
					totalWeight += int(term.Weight)
				}
			}
		}
	}

	return totalWeight, nil
}
func GetPreferredInterPodAntiAffinity(node corev1.Node, validSchedulingConfig []Assignment, managedClusterClient client.Client, ctx context.Context) (int, error) {

	// reproduce the logic to count preferred node affinity weights used by kube-scheduler
	// for each pod checks if it has preferred pod affinity to the pods on the selected node
	// if they match, sum the weight

	// as first, get the pods scheduled on the seelected node
	podsOnNode := corev1.PodList{}
	err := managedClusterClient.List(ctx, &podsOnNode, client.MatchingFields{"spec.nodeName": node.Name})
	if err != nil {
		return 0, err
	}

	// check the affinity between the existing pods and the pods to schedule
	totalWeight := 0
	for _, assignment := range validSchedulingConfig {
		pod := assignment.Pod
		if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAntiAffinity == nil {
			continue
		}

		preferred := pod.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
		for _, term := range preferred {
			selector, err := metav1.LabelSelectorAsSelector(term.PodAffinityTerm.LabelSelector)
			if err != nil {
				continue
			}

			// check against existing pods on the node
			for _, existingPod := range podsOnNode.Items {
				// check namespace
				if len(term.PodAffinityTerm.Namespaces) > 0 {
					found := false
					for _, ns := range term.PodAffinityTerm.Namespaces {
						if ns == existingPod.Namespace {
							found = true
							break
						}
					}
					if !found {
						continue
					}
				}
				if selector.Matches(labels.Set(existingPod.Labels)) {
					totalWeight += int(term.Weight)
				}
			}
		}
	}

	return totalWeight, nil

}

func GetNumberOfRunningPods(node corev1.Node, ctx context.Context, k8sClient client.Client) (int, error) {

	pods := &corev1.PodList{}
	err := k8sClient.List(ctx, pods, client.MatchingFields{"spec.nodeName": node.Name})
	if err != nil {
		return 0, err
	}

	return len(pods.Items), nil
}

func LoadAHPweights(managementClusterClient client.Client, ctx context.Context) (AHPweights, error) {

	// Read selection profile from cluster-configuration-parameters
	clusterCM := &corev1.ConfigMap{}
	err := managementClusterClient.Get(
		ctx,
		client.ObjectKey{
			Name:      DREEM_CLUSTER_CONFIGURATION_CM_NAME,
			Namespace: "dreem",
		},
		clusterCM,
	)
	if err != nil {
		return AHPweights{}, err
	}

	profile := strings.ToLower(clusterCM.Data["selectionProfile"])
	if profile == "" {
		return AHPweights{}, fmt.Errorf("selectionProfile not set, CM: %v", clusterCM.Data)
	}

	// Read selection-weights-scale-down ConfigMap
	weightsCM := &corev1.ConfigMap{}
	err = managementClusterClient.Get(
		ctx,
		client.ObjectKey{
			Name:      DREEM_WEIGHTS_SCALE_DOWN_CM_NAME,
			Namespace: "dreem",
		},
		weightsCM,
	)
	if err != nil {
		return AHPweights{}, err
	}

	rawProfile, ok := weightsCM.Data[profile]
	if !ok {
		return AHPweights{}, fmt.Errorf("profile %s not found in selection-weights-scale-down", profile)
	}

	// Parse YAML of the profile
	var parsed ahpProfileCM
	if err := yaml.Unmarshal([]byte(rawProfile), &parsed); err != nil {
		return AHPweights{}, err
	}

	// Convert string to float64
	parse := func(v string) float64 {
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	// Return AHPweights struct
	return AHPweights{
		Profile:                       strings.ToUpper(profile),
		PreferredNodeAffinity:         parse(parsed.PreferredNodeAffinity),
		PrefererredInterPodAffinity:   parse(parsed.PreferredPodAffinity),
		PreferredInterPodAntiAffinity: parse(parsed.PreferredPodAntiAffinity),
		EnergyProfile:                 parse(parsed.EnergyProfile),
		PowerCycle:                    parse(parsed.PowerCycles),
		NumberOfRunningPods:           parse(parsed.NumberOfRunningPods),
	}, nil
}

func LoadAHPweightsScaleUp(ctx context.Context, managementClient client.Client) (AHPweightsScaleUp, error) {
	clusterCM := &corev1.ConfigMap{}

	err := managementClient.Get(
		ctx,
		client.ObjectKey{
			Name:      DREEM_CLUSTER_CONFIGURATION_CM_NAME,
			Namespace: "dreem",
		},
		clusterCM,
	)
	if err != nil {
		return AHPweightsScaleUp{}, err
	}

	weightsCM := &corev1.ConfigMap{}
	err = managementClient.Get(
		ctx,
		client.ObjectKey{
			Name:      DREEM_WEIGHTS_SCALE_UP_CM_NAME,
			Namespace: "dreem",
		},
		weightsCM,
	)
	if err != nil {
		return AHPweightsScaleUp{}, err
	}

	powerCycleStr, ok := weightsCM.Data["PowerCycles"]
	if !ok {
		return AHPweightsScaleUp{}, fmt.Errorf("PowerCycles weight not found in selection-weights-scale-up ConfigMap")
	}
	powerCycle, err := strconv.ParseFloat(powerCycleStr, 64)
	if err != nil {
		return AHPweightsScaleUp{}, err
	}

	energyProfileStr, ok := weightsCM.Data["EnergyProfile"]
	if !ok {
		return AHPweightsScaleUp{}, fmt.Errorf("EnergyProfile weight not found in selection-weights-scale-up ConfigMap")
	}
	energyProfile, err := strconv.ParseFloat(energyProfileStr, 64)
	if err != nil {
		return AHPweightsScaleUp{}, err
	}

	return AHPweightsScaleUp{
		PowerCycle:    powerCycle,
		EnergyProfile: energyProfile,
	}, nil
}

// APPLY TOPSIS METHOD
// 1. Create the evaluation matrix
// 2. Normalize the evaluation matrix
// 3. Multiply by weights
// 4. Determine ideal and negative-ideal solutions
// 5. Calculate separation measures
// 6. Calculate relative closeness to ideal solution
// 7. Rank the alternatives
func ApplyTOPSIS(criteriaList []TOPSISCriteria, weights AHPweights, nodeSelecting clusterv1alpha1.NodeSelecting, managementClusterClient client.Client, ctx context.Context) ([]RankedNode, error) {

	// 1. Create the evaluation matrix consisting of criteriaList alternatives and their criteria values.
	evalMatrix := MakeEvaluationMatrix(criteriaList)

	// 2. Normalize the matrix based on the criteria type (benefit or cost).
	normalizedMatrix := NormalizeMatrix(evalMatrix)

	// 3. Multiply the normalized matrix by the weights.
	weightedMatrix := WeightMatrix(normalizedMatrix, weights)

	// 4. Determine the ideal and negative-ideal solutions.
	idealSolution, negativeIdealSolution := CalculateIdealSolutions(weightedMatrix)

	// 5. Calculate the separation measures for each alternative.
	separationFromIdeal, separationFromNegativeIdeal := CalculateSeparationMeasures(weightedMatrix, idealSolution, negativeIdealSolution)

	// 6. Calculate the relative closeness to the ideal solution.
	relativeCloseness := CalculateRelativeCloseness(separationFromIdeal, separationFromNegativeIdeal)

	// 6.5 Associate relative closeness to criteriaList (new matrix)
	rankedNodes := make([]RankedNode, len(criteriaList))
	for i, crit := range criteriaList {
		rankedNodes[i] = RankedNode{
			RelativeCloseness: relativeCloseness[i],
			Node:              crit.Node,
		}
	}

	// 7. Rank the alternatives based on their relative closeness: the higher, the better
	sortedRankedNodes := SortNodesByCloseness(rankedNodes)
	nodes := []string{}
	name := "scaleDown_" + time.Now().Format("20060102_150405")
	for _, crit := range criteriaList {
		nodes = append(nodes, crit.Node.Name)
	}
	saveMatrixToJSON(name+".json", weightedMatrix, nodes, nodeSelecting, managementClusterClient, ctx)

	return sortedRankedNodes, nil
}

// APPLY TOPSIS METHOD FOR SCALE UP (simplified model)
func ApplyTOPSISScaleUp(criteriaList []TOPSISCriteriaScaleUp, weights AHPweightsScaleUp, nodeSelecting clusterv1alpha1.NodeSelecting, managementClusterClient client.Client, ctx context.Context) ([]clusterapi.MachineDeployment, error) {
	// Implementation for scale up TOPSIS application

	evalMatrix := MakeEvaluationMatrixScaleUp(criteriaList)
	normalizedMatrix := NormalizeMatrix(evalMatrix)
	weightedMatrix := WeightMatrixScaleUp(normalizedMatrix, weights)
	idealSolution, negativeIdealSolution := CalculateIdealSolutions(weightedMatrix)
	separationFromIdeal, separationFromNegativeIdeal := CalculateSeparationMeasures(weightedMatrix, idealSolution, negativeIdealSolution)
	relativeCloseness := CalculateRelativeCloseness(separationFromIdeal, separationFromNegativeIdeal)

	// name with timestamp
	name := "scaleUp_" + time.Now().Format("20060102_150405")
	nodes := []string{}
	for _, crit := range criteriaList {
		nodes = append(nodes, crit.MachineDeployment.Name)
	}
	saveMatrixToJSON(name+".json", weightedMatrix, nodes, nodeSelecting, managementClusterClient, ctx)

	// Associate relative closeness to MachineDeployment
	rankedMDs := make([]RankedMachineDeployment, len(criteriaList))
	for i, crit := range criteriaList {
		rankedMDs[i] = RankedMachineDeployment{
			RelativeCloseness: relativeCloseness[i],
			MachineDeployment: crit.MachineDeployment,
		}
	}

	// Rank the alternatives based on their relative closeness: the higher, the better
	sortedRankedMDs := SortMachineDeploymentsByCloseness(rankedMDs)

	// Extract ordered MachineDeployment list
	orderedMDs := make([]clusterapi.MachineDeployment, 0, len(sortedRankedMDs))
	for _, rankedMD := range sortedRankedMDs {
		orderedMDs = append(orderedMDs, rankedMD.MachineDeployment)
	}

	return orderedMDs, nil

}

func MakeEvaluationMatrixScaleUp(criteriaList []TOPSISCriteriaScaleUp) []map[string]float64 {
	evalMatrix := make([]map[string]float64, len(criteriaList))
	for i, crit := range criteriaList {
		evalMatrix[i] = map[string]float64{
			"PowerCycle":    float64(crit.PowerCycle),
			"EnergyProfile": crit.EnergyProfile,
		}
	}
	return evalMatrix
}

func MakeEvaluationMatrix(criteriaList []TOPSISCriteria) []map[string]float64 {
	evalMatrix := make([]map[string]float64, len(criteriaList))
	for i, crit := range criteriaList {
		evalMatrix[i] = map[string]float64{
			"PowerCycle":                    float64(crit.PowerCycle),
			"EnergyProfile":                 crit.EnergyProfile,
			"PreferredNodeAffinity":         float64(crit.PreferredNodeAffinity),
			"PrefererredInterPodAffinity":   float64(crit.PrefererredInterPodAffinity),
			"PreferredInterPodAntiAffinity": float64(crit.PreferredInterPodAntiAffinity),
			"NumberOfRunningPods":           float64(crit.NumberOfRunningPods),
		}
	}
	return evalMatrix
}

func NormalizeMatrix(matrix []map[string]float64) []map[string]float64 {
	numAlternatives := len(matrix)
	// get keys from first row
	keys := make([]string, 0, len(matrix[0]))
	for k := range matrix[0] {
		keys = append(keys, k)
	}

	normalized := make([]map[string]float64, numAlternatives)

	// per ogni criterio calcola denominatore
	denominators := make(map[string]float64)
	for _, key := range keys {
		sumSquares := 0.0
		for k := 0; k < numAlternatives; k++ {
			val := matrix[k][key]
			sumSquares += val * val
		}
		denominators[key] = math.Sqrt(sumSquares)
	}

	// normalizza ogni valore
	for i := 0; i < numAlternatives; i++ {
		normalized[i] = make(map[string]float64)
		for _, key := range keys {
			if denominators[key] != 0 {
				normalized[i][key] = math.Round((matrix[i][key]/denominators[key])*1000) / 1000
			} else {
				normalized[i][key] = 0
			}
		}
	}

	return normalized
}

func WeightMatrixScaleUp(matrix []map[string]float64, weights AHPweightsScaleUp) []map[string]float64 {
	// multiply each criterion value by its corresponding weight
	numAlternatives := len(matrix)
	weighted := make([]map[string]float64, numAlternatives)
	for row := 0; row < numAlternatives; row++ {
		weighted[row] = make(map[string]float64)

		for keys, val := range matrix[row] {
			var weight float64
			switch keys {
			case "PowerCycle":
				weight = weights.PowerCycle
			case "EnergyProfile":
				weight = weights.EnergyProfile
			default:
				weight = 1.0
			}
			weighted[row][keys] = math.Round((val*weight)*1000) / 1000
		}
	}

	return weighted

}

func WeightMatrix(matrix []map[string]float64, weights AHPweights) []map[string]float64 {
	// multiply each criterion value by its corresponding weight
	numAlternatives := len(matrix)
	weighted := make([]map[string]float64, numAlternatives)
	for row := 0; row < numAlternatives; row++ {
		weighted[row] = make(map[string]float64)

		for keys, val := range matrix[row] {
			var weight float64
			switch keys {
			case "PowerCycle":
				weight = weights.PowerCycle
			case "EnergyProfile":
				weight = weights.EnergyProfile
			case "PreferredNodeAffinity":
				weight = weights.PreferredNodeAffinity
			case "PrefererredInterPodAffinity":
				weight = weights.PrefererredInterPodAffinity
			case "PreferredInterPodAntiAffinity":
				weight = weights.PreferredInterPodAntiAffinity
			case "NumberOfRunningPods":
				weight = weights.NumberOfRunningPods
			default:
				weight = 1.0
			}
			weighted[row][keys] = math.Round((val*weight)*1000) / 1000
		}
	}

	return weighted

}

func CalculateIdealSolutions(matrix []map[string]float64) (map[string]float64, map[string]float64) {
	// Type of criteria:
	// PowerCycle: cost
	// EnergyProfile: benefit
	// PreferredNodeAffinity: cost
	// PrefererredInterPodAffinity: cost
	// PreferredInterPodAntiAffinity: cost
	// NumberOfRunningPods: cost

	numCriteria := len(matrix[0])
	ideal := make(map[string]float64, numCriteria)
	negativeIdeal := make(map[string]float64, numCriteria)

	// get keys from first row
	keys := make([]string, 0, len(matrix[0]))
	for k := range matrix[0] {
		keys = append(keys, k)
	}

	for _, key := range keys {
		values := make([]float64, len(matrix))
		for i := 0; i < len(matrix); i++ {
			values[i] = matrix[i][key]
		}

		// determine ideal and negative-ideal based on criteria type
		switch key {
		case "EnergyProfile": // benefit
			ideal[key] = maxFloat64(values)
			negativeIdeal[key] = minFloat64(values)
		default: // cost
			ideal[key] = minFloat64(values)
			negativeIdeal[key] = maxFloat64(values)
		}
	}

	return ideal, negativeIdeal
}

func maxFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	max := values[0]
	for _, v := range values[1:] {
		if v > max {
			max = v
		}
	}
	return max
}

func minFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	min := values[0]
	for _, v := range values[1:] {
		if v < min {
			min = v
		}
	}
	return min
}

func CalculateSeparationMeasures(matrix []map[string]float64, ideal map[string]float64, negativeIdeal map[string]float64) ([]float64, []float64) {
	numAlternatives := len(matrix)

	separationFromIdeal := make([]float64, numAlternatives)
	separationFromNegativeIdeal := make([]float64, numAlternatives)

	for i := 0; i < numAlternatives; i++ {
		sumIdeal := 0.0
		sumNegativeIdeal := 0.0

		for key, val := range matrix[i] {
			diffIdeal := val - ideal[key]
			diffNegativeIdeal := val - negativeIdeal[key]
			sumIdeal += diffIdeal * diffIdeal
			sumNegativeIdeal += diffNegativeIdeal * diffNegativeIdeal
		}

		separationFromIdeal[i] = math.Sqrt(sumIdeal)
		separationFromNegativeIdeal[i] = math.Sqrt(sumNegativeIdeal)
	}
	// round 3 decimals
	for i := 0; i < numAlternatives; i++ {
		separationFromIdeal[i] = math.Round(separationFromIdeal[i]*1000) / 1000
		separationFromNegativeIdeal[i] = math.Round(separationFromNegativeIdeal[i]*1000) / 1000
	}
	return separationFromIdeal, separationFromNegativeIdeal
}

func CalculateRelativeCloseness(separationFromIdeal []float64, separationFromNegativeIdeal []float64) []float64 {
	numAlternatives := len(separationFromIdeal)
	relativeCloseness := make([]float64, numAlternatives)

	for i := 0; i < numAlternatives; i++ {
		denominator := separationFromIdeal[i] + separationFromNegativeIdeal[i]
		if denominator != 0 {
			relativeCloseness[i] = separationFromNegativeIdeal[i] / denominator
		} else {
			relativeCloseness[i] = 0
		}
	}

	// round 3 decimals
	for i := 0; i < numAlternatives; i++ {
		relativeCloseness[i] = math.Round(relativeCloseness[i]*1000) / 1000
	}
	return relativeCloseness
}

func SortNodesByCloseness(rankedNodes []RankedNode) []RankedNode {
	sorted := make([]RankedNode, len(rankedNodes))
	copy(sorted, rankedNodes)

	// simple bubble sort
	for i := 0; i < len(sorted)-1; i++ {
		for j := 0; j < len(sorted)-i-1; j++ {
			if sorted[j].RelativeCloseness < sorted[j+1].RelativeCloseness {
				sorted[j], sorted[j+1] = sorted[j+1], sorted[j]
			}
		}
	}

	return sorted
}

func SortMachineDeploymentsByCloseness(rankedMDs []RankedMachineDeployment) []RankedMachineDeployment {
	sorted := make([]RankedMachineDeployment, len(rankedMDs))
	copy(sorted, rankedMDs)

	// simple bubble sort
	for i := 0; i < len(sorted)-1; i++ {
		for j := 0; j < len(sorted)-i-1; j++ {
			if sorted[j].RelativeCloseness < sorted[j+1].RelativeCloseness {
				sorted[j], sorted[j+1] = sorted[j+1], sorted[j]
			}
		}
	}

	return sorted
}

func getMDfromNode(selectedNode string, k8sclient client.Client, ctx context.Context) string {
	// get the machine (clusterapi res) with the name of the node
	associatedMachine := &clusterapi.Machine{}
	k8sclient.Get(ctx, client.ObjectKey{Name: selectedNode, Namespace: "default"}, associatedMachine)
	selectedMD := associatedMachine.Labels[CAPI_MACHINE_DEPLOYMENT_LABEL]
	return selectedMD
}
