package controller

import (
	"context"
	"math"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Feasibility check of a scale-down with hardChecks true: the node can be switched off
// only if every pod it runs can be placed on another active node. It is DREEM's check,
// unchanged; Menouer et al.'s algorithm does not make it (hardChecks false).

// Assignment is a pod and the node a migration plan places it on.
type Assignment struct {
	Pod  corev1.Pod
	Node corev1.Node
}

type Combination []Assignment

// ----- RESOURCE CHECKING -----

type AvailableResources struct {
	CPU    resource.Quantity
	Memory resource.Quantity
}

// podRequests returns the effective CPU and memory requests of a pod, computed the way
// the kube-scheduler accounts for it.
//
// Init containers with restartPolicy Always are native sidecars (Istio's istio-proxy is
// one): they keep running next to the app containers, so their requests add to the
// pod's total. Treating them as ordinary init containers - taking only the largest one
// as a floor - counted an Istio pod 100m CPU and 128Mi short, and every resident pod on
// a destination node the same, so migration plans the scheduler cannot place passed the
// feasibility check. The effective request is the larger of
//   - the running phase: app containers plus every sidecar, and
//   - each regular init container plus the sidecars declared before it, which are
//     already running while it runs.
func podRequests(pod *corev1.Pod) AvailableResources {
	cpu := resource.MustParse("0")
	mem := resource.MustParse("0")
	sidecarCPU := resource.MustParse("0")
	sidecarMem := resource.MustParse("0")
	initCPU := resource.MustParse("0")
	initMem := resource.MustParse("0")

	for _, c := range pod.Spec.InitContainers {
		if isNativeSidecar(&c) {
			sidecarCPU.Add(c.Resources.Requests[corev1.ResourceCPU])
			sidecarMem.Add(c.Resources.Requests[corev1.ResourceMemory])
			continue
		}
		stepCPU := c.Resources.Requests[corev1.ResourceCPU].DeepCopy()
		stepCPU.Add(sidecarCPU)
		stepMem := c.Resources.Requests[corev1.ResourceMemory].DeepCopy()
		stepMem.Add(sidecarMem)
		if stepCPU.Cmp(initCPU) > 0 {
			initCPU = stepCPU
		}
		if stepMem.Cmp(initMem) > 0 {
			initMem = stepMem
		}
	}

	for _, c := range pod.Spec.Containers {
		cpu.Add(c.Resources.Requests[corev1.ResourceCPU])
		mem.Add(c.Resources.Requests[corev1.ResourceMemory])
	}
	cpu.Add(sidecarCPU)
	mem.Add(sidecarMem)

	if initCPU.Cmp(cpu) > 0 {
		cpu = initCPU
	}
	if initMem.Cmp(mem) > 0 {
		mem = initMem
	}
	return AvailableResources{CPU: cpu, Memory: mem}
}

// isNativeSidecar reports whether an init container is a native sidecar
// (restartPolicy Always), which keeps running for the whole life of the pod.
func isNativeSidecar(c *corev1.Container) bool {
	return c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways
}

// NodeFreeResources returns what is actually still free on a node: its allocatable
// minus the requests of the pods already running there.
func NodeFreeResources(ctx context.Context, c client.Client, node corev1.Node) (AvailableResources, error) {
	free := AvailableResources{
		CPU:    node.Status.Allocatable[corev1.ResourceCPU].DeepCopy(),
		Memory: node.Status.Allocatable[corev1.ResourceMemory].DeepCopy(),
	}

	pods := &corev1.PodList{}
	if err := c.List(ctx, pods, client.MatchingFields{"spec.nodeName": node.Name}); err != nil {
		return free, err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		req := podRequests(pod)
		free.CPU.Sub(req.CPU)
		free.Memory.Sub(req.Memory)
	}
	return free, nil
}

// buildMigrationPlan produces one feasible plan moving every pod in pods off
// drainedNode onto one of candidates, or nil if no such plan exists. A node with nothing
// to migrate gets an empty plan.
//
// The plan imitates the kube-scheduler that will place the evicted pods: one pod at a
// time, the candidates filtered as its filter plugins would (resources, taints,
// required node affinity, required pod (anti-)affinity), the pod put on the candidate
// with the highest score of the default score plugins (see schedulerScores), and free
// resources and occupants updated after every pod. The hard topology spread constraints
// of the whole plan are checked at the end.
func buildMigrationPlan(ctx context.Context, c client.Client, drainedNode corev1.Node, candidates []corev1.Node, pods []corev1.Pod) (Combination, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	// A node with nothing to migrate can be switched off for free.
	if len(pods) == 0 {
		return Combination{}, nil
	}

	free := make(map[string]AvailableResources, len(candidates))
	resident := make(map[string][]corev1.Pod, len(candidates))
	for i := range candidates {
		f, err := NodeFreeResources(ctx, c, candidates[i])
		if err != nil {
			return nil, err
		}
		free[candidates[i].Name] = f

		podsOn := &corev1.PodList{}
		if err := c.List(ctx, podsOn, client.MatchingFields{"spec.nodeName": candidates[i].Name}); err != nil {
			return nil, err
		}
		resident[candidates[i].Name] = filterSystemPods(podsOn.Items)
	}

	plan := placePods(orderForPlacement(pods), candidates, free, resident, drainedNode.Name)
	if plan == nil {
		return nil, nil
	}

	// topology spread is a property of the whole plan, so it is checked once at the end
	if len(CheckTopologySpreadConstraints(ctx, c, []Combination{plan})) == 0 {
		klog.V(3).Infof("Plan for draining node %s violates a hard topology spread constraint", drainedNode.Name)
		return nil, nil
	}

	return plan, nil
}

// orderForPlacement returns the pods in the order the plan places them: the pods without
// preferred pod-affinity terms - the ones others prefer to be near - come before the
// pods that want to follow them. Within each group the largest pods come first: they are
// the hardest to place.
func orderForPlacement(pods []corev1.Pod) []corev1.Pod {
	ordered := make([]corev1.Pod, len(pods))
	copy(ordered, pods)
	sort.SliceStable(ordered, func(i, j int) bool {
		fi := len(preferredPodAffinityTerms(&ordered[i])) > 0
		fj := len(preferredPodAffinityTerms(&ordered[j])) > 0
		if fi != fj {
			return !fi
		}
		ri, rj := podRequests(&ordered[i]), podRequests(&ordered[j])
		if cmp := ri.CPU.Cmp(rj.CPU); cmp != 0 {
			return cmp > 0
		}
		return ri.Memory.Cmp(rj.Memory) > 0
	})
	return ordered
}

// placePods places the pods in the given order, each on the admissible candidate with
// the highest scheduler score (ties keep the first candidate), and returns nil if some
// pod has no admissible candidate. freeInit and resident are not modified.
func placePods(ordered []corev1.Pod, candidates []corev1.Node, freeInit map[string]AvailableResources, resident map[string][]corev1.Pod, drainedNode string) Combination {
	free := make(map[string]AvailableResources, len(freeInit))
	for name, f := range freeInit {
		free[name] = AvailableResources{CPU: f.CPU.DeepCopy(), Memory: f.Memory.DeepCopy()}
	}
	placed := make(map[string][]corev1.Pod, len(candidates))
	plan := make(Combination, 0, len(ordered))

	for i := range ordered {
		pod := ordered[i]
		req := podRequests(&pod)

		// filter: what the scheduler's filter plugins would reject
		admissible := make([]int, 0, len(candidates))
		for ci := range candidates {
			node := &candidates[ci]
			avail := free[node.Name]
			if req.CPU.Cmp(avail.CPU) > 0 || req.Memory.Cmp(avail.Memory) > 0 {
				continue
			}
			if !toleratesTaints(&pod, node) || !matchesNodeAffinity(&pod, node) {
				continue
			}
			if !checkAffinityOnNode(occupantsOf(node.Name, resident, placed), []corev1.Pod{pod}) {
				continue
			}
			admissible = append(admissible, ci)
		}
		if len(admissible) == 0 {
			klog.V(3).Infof("No admissible destination for pod %s/%s while draining node %s",
				pod.Namespace, pod.Name, drainedNode)
			return nil
		}

		// score: what the scheduler's score plugins would prefer
		scores := schedulerScores(&pod, req, candidates, admissible, free, resident, placed)
		best := 0
		for k := range scores {
			if scores[k] > scores[best] {
				best = k
			}
		}
		target := candidates[admissible[best]]

		avail := free[target.Name]
		cpu, mem := avail.CPU.DeepCopy(), avail.Memory.DeepCopy()
		cpu.Sub(req.CPU)
		mem.Sub(req.Memory)
		free[target.Name] = AvailableResources{CPU: cpu, Memory: mem}
		placed[target.Name] = append(placed[target.Name], pod)
		plan = append(plan, Assignment{Pod: pod, Node: target})
	}
	return plan
}

// occupantsOf returns the pods on a candidate node: the ones already running there
// plus the ones the plan has placed there so far.
func occupantsOf(nodeName string, resident, placed map[string][]corev1.Pod) []corev1.Pod {
	occupants := make([]corev1.Pod, 0, len(resident[nodeName])+len(placed[nodeName]))
	occupants = append(occupants, resident[nodeName]...)
	return append(occupants, placed[nodeName]...)
}

// Weights of the default kube-scheduler score plugins (KubeSchedulerConfiguration v1).
const (
	scoreWeightNodeAffinity       = 2.0
	scoreWeightInterPodAffinity   = 2.0
	scoreWeightLeastAllocated     = 1.0 // NodeResourcesFit, LeastAllocated strategy
	scoreWeightBalancedAllocation = 1.0 // NodeResourcesBalancedAllocation
	maxNodeScore                  = 100.0
)

// schedulerScores returns, for each admissible candidate (indexes into candidates), the
// score the default kube-scheduler profile gives it for pod: every plugin scores all the
// candidates, its scores are normalised to 0-100 across them, and the weighted sum
// decides. Modelled: NodeAffinity (preferred terms, scaled so the best node gets 100),
// InterPodAffinity (preferred affinity and anti-affinity in both directions, min-max
// scaled), NodeResourcesFit with LeastAllocated, and NodeResourcesBalancedAllocation.
// Not modelled: PodTopologySpread, TaintToleration (PreferNoSchedule), ImageLocality.
func schedulerScores(pod *corev1.Pod, req AvailableResources, candidates []corev1.Node, admissible []int,
	free map[string]AvailableResources, resident, placed map[string][]corev1.Pod) []float64 {

	nodeAffinity := make([]float64, len(admissible))
	podAffinity := make([]float64, len(admissible))
	total := make([]float64, len(admissible))
	for k, ci := range admissible {
		node := &candidates[ci]
		nodeAffinity[k] = float64(preferredNodeAffinityWeight(pod, node))
		podAffinity[k] = float64(interPodAffinityScore(pod, node, candidates, resident, placed))
		least, balanced := resourceScores(req, node, free[node.Name])
		total[k] = scoreWeightLeastAllocated*least + scoreWeightBalancedAllocation*balanced
	}
	scaleToMax(nodeAffinity)
	scaleMinMax(podAffinity)
	for k := range total {
		total[k] += scoreWeightNodeAffinity*nodeAffinity[k] + scoreWeightInterPodAffinity*podAffinity[k]
	}
	return total
}

// interPodAffinityScore is the InterPodAffinity score of pod on node before
// normalisation. Like the scheduler it scores both directions: the weights of the pod's
// preferred affinity terms matched by the pods in the node's topology domain, minus its
// preferred anti-affinity terms, plus the affinity (minus the anti-affinity) terms of
// those pods that the pod matches. Domains are built from the candidate nodes.
func interPodAffinityScore(pod *corev1.Pod, node *corev1.Node, candidates []corev1.Node, resident, placed map[string][]corev1.Pod) int {
	affinity, antiAffinity := preferredPodAffinityTerms(pod), preferredPodAntiAffinityTerms(pod)
	score := 0
	for i := range candidates {
		other := &candidates[i]
		occupants := occupantsOf(other.Name, resident, placed)
		for j := range occupants {
			occupant := &occupants[j]
			score += matchedWeight(pod, occupant, affinity, node, other)
			score -= matchedWeight(pod, occupant, antiAffinity, node, other)
			score += matchedWeight(occupant, pod, preferredPodAffinityTerms(occupant), node, other)
			score -= matchedWeight(occupant, pod, preferredPodAntiAffinityTerms(occupant), node, other)
		}
	}
	return score
}

// matchedWeight sums the weights of owner's terms that target satisfies, counting only
// the terms whose topology domain holds both nodes.
func matchedWeight(owner, target *corev1.Pod, terms []corev1.WeightedPodAffinityTerm, a, b *corev1.Node) int {
	total := 0
	for i := range terms {
		term := &terms[i].PodAffinityTerm
		if sameTopologyDomain(a, b, term.TopologyKey) && podMatchesAffinityTerm(owner, target, term) {
			total += int(terms[i].Weight)
		}
	}
	return total
}

// sameTopologyDomain reports whether two nodes share the value of topologyKey.
func sameTopologyDomain(a, b *corev1.Node, topologyKey string) bool {
	if topologyKey == "" || topologyKey == "kubernetes.io/hostname" {
		return a.Name == b.Name
	}
	va, okA := a.Labels[topologyKey]
	vb, okB := b.Labels[topologyKey]
	return okA && okB && va == vb
}

// resourceScores returns the LeastAllocated and BalancedAllocation scores (0-100) of
// placing a pod with requests req on node, given what is still free there.
func resourceScores(req AvailableResources, node *corev1.Node, free AvailableResources) (leastAllocated, balanced float64) {
	cpuAllocatable := float64(node.Status.Allocatable.Cpu().MilliValue())
	memAllocatable := float64(node.Status.Allocatable.Memory().Value())
	if cpuAllocatable <= 0 || memAllocatable <= 0 {
		return 0, 0
	}
	cpuFree := clamp01((float64(free.CPU.MilliValue()) - float64(req.CPU.MilliValue())) / cpuAllocatable)
	memFree := clamp01((float64(free.Memory.Value()) - float64(req.Memory.Value())) / memAllocatable)
	leastAllocated = (cpuFree + memFree) / 2 * maxNodeScore
	// the used fractions differ by |cpuFree - memFree|; their standard deviation is half that
	balanced = (1 - math.Abs(cpuFree-memFree)/2) * maxNodeScore
	return leastAllocated, balanced
}

func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

// scaleToMax scales the scores so the highest becomes maxNodeScore (the scheduler's
// DefaultNormalizeScore); all zero stays zero.
func scaleToMax(scores []float64) {
	highest := maxFloat64(scores)
	if highest <= 0 {
		return
	}
	for i := range scores {
		scores[i] = scores[i] * maxNodeScore / highest
	}
}

// scaleMinMax scales the scores to 0-maxNodeScore between their minimum and maximum
// (InterPodAffinity's normalisation); when they are all equal they all become zero.
func scaleMinMax(scores []float64) {
	lowest, highest := minFloat64(scores), maxFloat64(scores)
	for i := range scores {
		if highest > lowest {
			scores[i] = maxNodeScore * (scores[i] - lowest) / (highest - lowest)
		} else {
			scores[i] = 0
		}
	}
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

// --- NODE AFFINITY CHECKING ---

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
			if podMatchesAffinityTerm(a, b, &term) {
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
			if podMatchesAffinityTerm(b, a, &term) {
				// anti-affinity violata
				return false
			}
		}
	}

	return true
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

// ----- AFFINITY HELPERS -----

// preferredNodeAffinityWeight returns the total weight of the pod's preferred node
// affinity terms that the node satisfies.
func preferredNodeAffinityWeight(pod *corev1.Pod, node *corev1.Node) int {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil {
		return 0
	}
	total := 0
	for _, term := range pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
		if matchNodeSelectorTerm(term.Preference, node.Labels) {
			total += int(term.Weight)
		}
	}
	return total
}

func preferredPodAffinityTerms(pod *corev1.Pod) []corev1.WeightedPodAffinityTerm {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAffinity == nil {
		return nil
	}
	return pod.Spec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution
}

func preferredPodAntiAffinityTerms(pod *corev1.Pod) []corev1.WeightedPodAffinityTerm {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAntiAffinity == nil {
		return nil
	}
	return pod.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
}

// podMatchesAffinityTerm reports whether target is selected by term, which belongs to
// owner. As in Kubernetes, a term without namespaces and without namespaceSelector only
// selects pods in the owner's own namespace. namespaceSelector is approximated as
// "any namespace" because namespace labels are not fetched here.
func podMatchesAffinityTerm(owner, target *corev1.Pod, term *corev1.PodAffinityTerm) bool {
	switch {
	case term.NamespaceSelector != nil:
		// approximation, see above
	case len(term.Namespaces) > 0:
		if !contains(term.Namespaces, target.Namespace) {
			return false
		}
	default:
		if target.Namespace != owner.Namespace {
			return false
		}
	}
	selector, err := metav1.LabelSelectorAsSelector(term.LabelSelector)
	if err != nil {
		return false
	}
	return selector.Matches(labels.Set(target.Labels))
}
