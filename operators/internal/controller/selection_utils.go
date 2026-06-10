package controller

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
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

// Generate combinations of assigning pods to candidate nodes.
// With many pods, this becomes exponential (nodes^pods). To avoid memory issues,
// we limit the combinations and use sampling if necessary.
func GenerateCombinations(pods []corev1.Pod, nodes []corev1.Node, maxCombinations int) []Combination {
	if len(nodes) == 0 {
		return nil
	}
	if len(pods) == 0 {
		return []Combination{{}}
	}

	numPods := len(pods)
	numNodes := len(nodes)
	total := pow(numNodes, numPods) // combinazioni totali = numNodes^numPods

	// Limit combinations to avoid memory exhaustion
	if total > maxCombinations {
		klog.V(2).Infof("Too many combinations (%d > %d), sampling subset for pods=%d nodes=%d", total, maxCombinations, numPods, numNodes)
		return sampleCombinations(pods, nodes, maxCombinations)
	}

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

// sampleCombinations randomly samples a subset of combinations instead of generating all
func sampleCombinations(pods []corev1.Pod, nodes []corev1.Node, maxSamples int) []Combination {
	numPods := len(pods)
	numNodes := len(nodes)
	combinations := make([]Combination, 0, maxSamples)

	// Sample random combinations
	for i := 0; i < maxSamples; i++ {
		comb := make(Combination, numPods)
		for p := 0; p < numPods; p++ {
			randIdx, _ := rand.Int(rand.Reader, big.NewInt(int64(numNodes)))
			nodeIndex := int(randIdx.Int64())
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

// ---- CHECK TOPOLOGY SPREAD CONSTRAINTS  -----
func CheckTopologySpreadConstraints(ctx context.Context, r client.Client, combinations []Combination) []Combination {
	validCombinations := make([]Combination, 0)

	// 1. Recuperiamo l'elenco completo dei nodi del cluster
	var allNodes corev1.NodeList
	if err := r.List(ctx, &allNodes); err != nil {
		klog.V(2).ErrorS(err, "Failed to list nodes for topology spread check")
		return combinations // In caso di errore, restituiamo l'input per non bloccare l'intero ciclo
	}

	// 2. Recuperiamo l'elenco completo dei pod del cluster
	var allPods corev1.PodList
	if err := r.List(ctx, &allPods); err != nil {
		klog.V(2).ErrorS(err, "Failed to list pods for topology spread check")
		return combinations
	}

	// 3. Analizziamo ogni singola combinazione candidata
	for _, comb := range combinations {
		isValidCombination := true

		// Per ogni combinazione, cicliamo sui pod che stiamo ricollocando
		for _, assignment := range comb {
			pod := assignment.Pod

			// Se il pod non definisce vincoli di distribuzione topologica, saltiamo
			if len(pod.Spec.TopologySpreadConstraints) == 0 {
				continue
			}

			// Verifichiamo ogni vincolo presente nel pod
			for _, constraint := range pod.Spec.TopologySpreadConstraints {
				// Ci interessano solo i vincoli rigidi (Hard Constraints)
				if constraint.WhenUnsatisfiable == corev1.DoNotSchedule {

					// Calcoliamo lo skew REALE simulando lo scenario di questa combinazione
					skew := calculateRealSkew(comb, allNodes.Items, allPods.Items, constraint)

					// Se lo skew risultante è maggiore del massimo consentito, la combinazione viene scartata
					if skew > int(constraint.MaxSkew) {
						isValidCombination = false
						break // Esci dal ciclo dei vincoli di questo pod
					}
				}
			}
			if !isValidCombination {
				break // Esci dal ciclo dei pod, questa combinazione è già invalida
			}
		}

		// Se la combinazione ha superato i controlli di tutti i pod, la aggiungiamo a quelle valide
		if isValidCombination {
			validCombinations = append(validCombinations, comb)
		}
	}

	return validCombinations
}

func calculateRealSkew(comb Combination, allNodes []corev1.Node, allPods []corev1.Pod, constraint corev1.TopologySpreadConstraint) int {
	topoKey := constraint.TopologyKey
	counts := make(map[string]int)

	// Mappe per tracciare la movimentazione dei pod in questa specifica combinazione
	movedPodsNewNode := make(map[string]string) // Pod.Name -> Nome del nodo di destinazione
	movedPodsOldNode := make(map[string]string) // Pod.Name -> Nome del nodo di provenienza attuale

	for _, assignment := range comb {
		podName := assignment.Pod.Name
		movedPodsNewNode[podName] = assignment.Node.Name
		movedPodsOldNode[podName] = assignment.Pod.Spec.NodeName
	}

	// Mappa di appoggio per risalire alla zona/dominio partendo dal nome di un nodo
	nodeToZone := make(map[string]string)
	for _, node := range allNodes {
		if zoneVal, ok := node.Labels[topoKey]; ok {
			counts[zoneVal] = 0 // Inizializziamo a 0 tutti i domini fisicamente esistenti nel cluster
			nodeToZone[node.Name] = zoneVal
		}
	}

	// STEP 1: Conteggio dei Pod statici (quelli non influenzati da questa combinazione)
	for _, pod := range allPods {
		// Ignoriamo i pod che sono già terminati
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}

		// Se questo pod fa parte di quelli che stiamo spostando, lo saltiamo adesso.
		// Verrà conteggiato nel prossimo step all'interno del suo nuovo nodo.
		if _, isMoving := movedPodsOldNode[pod.Name]; isMoving {
			continue
		}

		// Verifichiamo se le label del pod corrispondono al LabelSelector del vincolo
		if constraint.LabelSelector != nil {
			selector, err := metav1.LabelSelectorAsSelector(constraint.LabelSelector)
			if err != nil || !selector.Matches(labels.Set(pod.Labels)) {
				continue // Se non corrisponde, non impatta questa metrica topologica
			}
		}

		// Incrementiamo il contatore del dominio in cui risiede il pod statico
		if zoneVal, ok := nodeToZone[pod.Spec.NodeName]; ok {
			counts[zoneVal]++
		}
	}

	// STEP 2: Conteggio dei Pod in movimento (aggiunti virtualmente ai nodi di destinazione)
	for _, assignment := range comb {
		if constraint.LabelSelector != nil {
			selector, err := metav1.LabelSelectorAsSelector(constraint.LabelSelector)
			if err != nil || !selector.Matches(labels.Set(assignment.Pod.Labels)) {
				continue
			}
		}

		targetNodeName := assignment.Node.Name
		if zoneVal, ok := nodeToZone[targetNodeName]; ok {
			counts[zoneVal]++
		}
	}

	// Se nessun dominio ha registrato pod corrispondenti al selettore, lo skew è zero
	if len(counts) == 0 {
		return 0
	}

	// STEP 3: Calcolo effettivo dello Skew (Max Pods - Min Pods tra i vari domini)
	maxCount := 0
	minCount := math.MaxInt32
	for _, count := range counts {
		if count > maxCount {
			maxCount = count
		}
		if count < minCount {
			minCount = count
		}
	}

	return maxCount - minCount
}

// ---- SOFT CONSTRAINTS (PREFERENCES) -----

type TOPSISCriteria struct {
	Node                          corev1.Node
	PowerCycle                    int     // cost, to minimize
	EnergyProfile                 float64 // benefit, to maximize in scale down (higher values represent higher consumption, so worse nodes)
	PreferredNodeAffinity         int     // cost, to minimize (higher values represent more preferred node affinity rules, so better nodes)
	PrefererredInterPodAffinity   int     // cost, to minimize (higher values represent more preferred pod affinity rules, so better nodes)
	PreferredInterPodAntiAffinity int     // cost, to minimize (higher values represent more preferred pod anti-affinity rules, so better nodes)
	NumberOfRunningPods           int     // cost, to minimize
	TopologySpreadScore           int     // cost, to minimize (higher values represent better distribution of pods across topology domains, therefore the node must be less preferred for scale down)
}

type TOPSISCriteriaScaleUp struct {
	Node          corev1.Node
	PowerCycle    int     // cost, to minimize
	EnergyProfile float64 // cost, to minimize in scale up (lower values represent higher efficiency, so better nodes)
}

type RankedNode struct {
	RelativeCloseness float64
	Node              corev1.Node
}

type RankedMachineDeployment struct {
	RelativeCloseness float64
	Node              corev1.Node
}

type AHPweights struct {
	Profile                       string
	PowerCycle                    float64
	EnergyProfile                 float64
	PreferredNodeAffinity         float64
	PrefererredInterPodAffinity   float64
	PreferredInterPodAntiAffinity float64
	NumberOfRunningPods           float64
	TopologySpread                float64
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
	TopologySpread           string `yaml:"TopologySpread"`
}

func ApplySoftConstraintsScaleUp(ctx context.Context, nodes corev1.NodeList, k8sClient client.Client, nodeSelecting clusterv1alpha1.NodeSelecting) ([]corev1.Node, error) {
	klog.FromContext(ctx).WithName("Apply-TOPSIS-scale-up")

	var criteriaList []TOPSISCriteriaScaleUp

	for _, d := range nodes.Items {
		if d.Status.Phase != corev1.NodeRunning {

			// get power cycle count
			powerCycleStr, ok := d.Annotations[DREEM_POWER_CYCLE_ANNOTATION]
			if !ok {
				klog.V(2).Info("Power cycle annotation not found for Node", "node", d.Name)
				continue
			}
			powerCycle, err := strconv.Atoi(powerCycleStr)
			if err != nil {
				klog.V(2).ErrorS(err, "Failed to convert power cycle annotation to int for Node", "node", d.Name)
				continue
			}

			// get the energy profile for each node
			energyProfileStr, ok := d.Annotations[DREEM_ENERGY_EFFICIENCY_ANNOTATION]
			if !ok {
				klog.V(2).Info("Energy efficiency annotation not found for Node", "node", d.Name)
				continue
			}
			energyProfile, err := strconv.ParseFloat(energyProfileStr, 64)
			if err != nil {
				klog.V(2).ErrorS(err, "Failed to convert energy efficiency annotation to float for Node", "node", d.Name)
				continue
			}
			criteria := TOPSISCriteriaScaleUp{
				Node:          d,
				PowerCycle:    powerCycle,
				EnergyProfile: energyProfile,
			}
			criteriaList = append(criteriaList, criteria)

		} else { // if the node is already running, we skip it for scale up evaluation
			continue
		}
	}

	// Check if we have any valid criteria
	if len(criteriaList) == 0 {
		klog.V(2).Info("No valid criteria collected for TOPSIS scale-up evaluation, all MachineDeployments had errors or are already running")
		return []corev1.Node{}, nil
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

func ApplySoftConstraints(validScheduling []Assignment, nodes []corev1.Node, ctx context.Context, client client.Client, nodeSelecting clusterv1alpha1.NodeSelecting) ([]corev1.Node, string, error) {
	klog.FromContext(ctx).WithName("Apply-TOPSIS-scale-down")
	klog.V(2).Info("Applying soft constraints with TOPSIS")
	// Fill the structure with criteria values
	var criteriaList []TOPSISCriteria
	var message = "Values for TOPSIS scale down evaluation: | "
	for _, node := range nodes {

		// get number of running pods
		numPods, err := GetNumberOfRunningPods(node, ctx, client)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get number of running pods on node", "node", node.Name)
			continue
		}

		// get power cycle count
		powerCycle, err := GetPowerCycle(node, ctx, client)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get power cycle count for node", "node", node.Name)
			continue
		}

		// get the energy profile for each node
		energyProfile, err := GetEnergyProfile(node, ctx, client)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get energy profile for node", "node", node.Name)
			continue
		}

		// compute preferred node affinity
		preferredNodeAffinity := GetPreferredNodeAffinity(node, validScheduling)

		// compute preferred inter-pod affinity
		preferredInterPodAffinity, err := GetPreferredInterPodAffinity(node, validScheduling, client, ctx)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get preferred inter-pod affinity for node", "node", node.Name)
			continue
		}

		preferredInterPodAntiAffinity, err := GetPreferredInterPodAntiAffinity(node, validScheduling, client, ctx)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get preferred inter-pod anti-affinity for node", "node", node.Name)
			continue
		}

		// get topology spread score
		topologySpreadScore, err := GetTopologySpreadScore(ctx, client, validScheduling, node)
		if err != nil {
			klog.V(2).ErrorS(err, "Failed to get topology spread score for node", "node", node.Name)
			continue
		}
		message += fmt.Sprintf("Node %s: Power Cycle: %d, Energy Profile: %.2f, Preferred Node Affinity: %d, Preferred Inter-Pod Affinity: %d, Preferred Inter-Pod Anti-Affinity: %d, Number of Running Pods: %d, Topology Spread Score: %d | ", node.Name, powerCycle, energyProfile, preferredNodeAffinity, preferredInterPodAffinity, preferredInterPodAntiAffinity, numPods, topologySpreadScore)
		klog.V(3).Info("NODE: ", node.Name, " POWER CYCLE:", powerCycle, " ENERGY PROFILE:", energyProfile, " PREF NODE AFFINITY:", preferredNodeAffinity, " PREF INTER-POD AFFINITY:", preferredInterPodAffinity, " PREF INTER-POD ANTI-AFFINITY:", preferredInterPodAntiAffinity, " NUM PODS:", numPods, " TOPOLOGY SPREAD SCORE:", topologySpreadScore)

		criteria := TOPSISCriteria{
			Node:                          node,
			PowerCycle:                    powerCycle,
			EnergyProfile:                 energyProfile,
			PreferredNodeAffinity:         preferredNodeAffinity,
			PrefererredInterPodAffinity:   preferredInterPodAffinity,
			PreferredInterPodAntiAffinity: preferredInterPodAntiAffinity,
			TopologySpreadScore:           topologySpreadScore,
			NumberOfRunningPods:           numPods,
		}
		criteriaList = append(criteriaList, criteria)
	}

	// Check if we have any valid criteria
	if len(criteriaList) == 0 {
		klog.V(2).Info("No valid criteria collected for TOPSIS evaluation, all nodes had errors during data collection")
		return []corev1.Node{}, "", nil
	}

	// load weights
	weights, err := LoadAHPweights(client, ctx)
	if err != nil {
		klog.V(2).ErrorS(err, "Failed to load AHP weights")
		return nil, "", err
	}
	// apply TOPSIS
	rankedNodes, err := ApplyTOPSIS(criteriaList, weights, nodeSelecting, client, ctx)
	if err != nil {
		klog.V(2).ErrorS(err, "Failed to apply TOPSIS")
		return nil, "", err
	}

	// extract ordered node list
	nodes = make([]corev1.Node, 0, len(rankedNodes))
	for _, crit := range rankedNodes {
		nodes = append(nodes, crit.Node)
	}

	return nodes, message, nil
}

func GetPowerCycle(node corev1.Node, ctx context.Context, k8sClient client.Client) (int, error) {
	fetchedNode := &corev1.Node{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: node.Name}, fetchedNode); err != nil {
		return 0, err
	}

	// add annotation only if not present
	if _, ok := fetchedNode.Annotations[DREEM_POWER_CYCLE_ANNOTATION]; !ok {
		if fetchedNode.Annotations == nil {
			fetchedNode.Annotations = make(map[string]string)
		}
		fetchedNode.Annotations[DREEM_POWER_CYCLE_ANNOTATION] = "0"
		if err := k8sClient.Update(ctx, fetchedNode); err != nil {
			return 0, err
		}
	}

	powerCycleStr, ok := fetchedNode.Annotations[DREEM_POWER_CYCLE_ANNOTATION]
	if !ok {
		return 0, nil
	}

	powerCycleCount, err := strconv.Atoi(powerCycleStr)
	if err != nil {
		return 0, err
	}

	return powerCycleCount, nil

}

func GetEnergyProfile(nodeManagedCluster corev1.Node, ctx context.Context, k8sClient client.Client) (float64, error) {
	// retrieve the energy efficiency from the Node annotation
	node := &corev1.Node{}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeManagedCluster.Name}, node)
	if err != nil {
		return 0, err
	}

	energyProfileStr, ok := node.Annotations[DREEM_ENERGY_EFFICIENCY_ANNOTATION]
	if !ok {
		return 0, fmt.Errorf("energy efficiency annotation not found for Node %s", nodeManagedCluster.Name)
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
		TopologySpread:                parse(parsed.TopologySpread),
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

	// Check if criteriaList is empty
	if len(criteriaList) == 0 {
		klog.V(2).Info("No nodes available for TOPSIS evaluation")
		return []RankedNode{}, nil
	}

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
	// nodes := []string{}
	// name := "scaleDown_" + time.Now().Format("20060102_150405")
	// for _, crit := range criteriaList {
	// 	nodes = append(nodes, crit.Node.Name)
	// }
	// saveMatrixToJSON(name+".json", weightedMatrix, nodes, nodeSelecting, managementClusterClient, ctx)

	return sortedRankedNodes, nil
}

// APPLY TOPSIS METHOD FOR SCALE UP (simplified model)
func ApplyTOPSISScaleUp(criteriaList []TOPSISCriteriaScaleUp, weights AHPweightsScaleUp, nodeSelecting clusterv1alpha1.NodeSelecting, managementClusterClient client.Client, ctx context.Context) ([]corev1.Node, error) {
	// Implementation for scale up TOPSIS application

	// Check if criteriaList is empty
	if len(criteriaList) == 0 {
		klog.V(2).Info("No node available for TOPSIS evaluation")
		return []corev1.Node{}, nil
	}

	evalMatrix := MakeEvaluationMatrixScaleUp(criteriaList)
	normalizedMatrix := NormalizeMatrix(evalMatrix)
	weightedMatrix := WeightMatrixScaleUp(normalizedMatrix, weights)
	idealSolution, negativeIdealSolution := CalculateIdealSolutions(weightedMatrix)
	separationFromIdeal, separationFromNegativeIdeal := CalculateSeparationMeasures(weightedMatrix, idealSolution, negativeIdealSolution)
	relativeCloseness := CalculateRelativeCloseness(separationFromIdeal, separationFromNegativeIdeal)

	// name with timestamp
	// name := "scaleUp_" + time.Now().Format("20060102_150405")
	// nodes := []string{}
	// for _, crit := range criteriaList {
	// 	nodes = append(nodes, crit.MachineDeployment.Name)
	// }
	// saveMatrixToJSON(name+".json", weightedMatrix, nodes, nodeSelecting, managementClusterClient, ctx)

	// Associate relative closeness to MachineDeployment
	rankedNodes := make([]RankedMachineDeployment, len(criteriaList))
	for i, crit := range criteriaList {
		rankedNodes[i] = RankedMachineDeployment{
			RelativeCloseness: relativeCloseness[i],
			Node:              crit.Node,
		}
	}

	// Rank the alternatives based on their relative closeness: the higher, the better
	sortedRankedNodes := SortMachineDeploymentsByCloseness(rankedNodes)

	// Extract ordered Node list
	orderedNodes := make([]corev1.Node, 0, len(sortedRankedNodes))
	for _, rankedNode := range sortedRankedNodes {
		orderedNodes = append(orderedNodes, rankedNode.Node)
	}

	return orderedNodes, nil

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
			"TopologySpreadScore":           float64(crit.TopologySpreadScore),
		}
	}
	return evalMatrix
}

func NormalizeMatrix(matrix []map[string]float64) []map[string]float64 {
	numAlternatives := len(matrix)
	// Check if matrix is empty
	if numAlternatives == 0 {
		klog.V(2).Info("Empty matrix provided to NormalizeMatrix")
		return []map[string]float64{}
	}
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
			case "TopologySpread":
				weight = weights.TopologySpread
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
	// TopologySpreadScore: cost

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

func SortMachineDeploymentsByCloseness(rankedNodes []RankedMachineDeployment) []RankedMachineDeployment {
	sorted := make([]RankedMachineDeployment, len(rankedNodes))
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

func GetTopologySpreadScore(ctx context.Context, r client.Client, comb Combination, currentNode corev1.Node) (int, error) {
	score := 0

	// 1. Recuperiamo i nodi del cluster
	var allNodes corev1.NodeList
	if err := r.List(ctx, &allNodes); err != nil {
		return 0, err
	}

	// 2. Recuperiamo i pod del cluster
	var allPods corev1.PodList
	if err := r.List(ctx, &allPods); err != nil {
		return 0, err
	}

	// 3. Analizziamo i vincoli dei pod
	for _, assignment := range comb {
		pod := assignment.Pod

		if len(pod.Spec.TopologySpreadConstraints) == 0 {
			continue
		}

		for _, constraint := range pod.Spec.TopologySpreadConstraints {
			if constraint.WhenUnsatisfiable == corev1.ScheduleAnyway {

				// Calcoliamo lo skew reale della combinazione di base
				skew := calculateRealSkew(comb, allNodes.Items, allPods.Items, constraint)

				// === DINAMICITÀ BASATA SUL NODO ===
				// Se il nodo corrente appartiene allo stesso dominio topologico (es. stessa "zone")
				// dell'assegnamento corrente, verifichiamo se l'aggiunta incrementa lo sbilanciamento.
				topoKey := constraint.TopologyKey
				if currentZone, ok := currentNode.Labels[topoKey]; ok {
					if targetZone, exists := assignment.Node.Labels[topoKey]; exists && currentZone == targetZone {
						// Questo nodo fa parte del dominio target: premiamo i nodi che mantengono
						// o migliorano lo skew, penalizziamo quelli in domini già sovraccarichi.
						if skew <= int(constraint.MaxSkew) {
							score += 15 // Bonus maggiore se il nodo aiuta a rispettare il vincolo soft
						} else {
							score += 5 // Bonus minimo o nullo se il nodo si trova in una zona già satura
						}
						continue
					}
				}

				// Comportamento di fallback standard se il nodo è in un altro dominio
				if skew <= int(constraint.MaxSkew) {
					score += 10
				} else {
					penalita := skew - int(constraint.MaxSkew)
					punteggioParziale := 10 - penalita
					if punteggioParziale > 0 {
						score += punteggioParziale
					}
				}
			}
		}
	}

	return score, nil
}
