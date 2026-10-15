package controller

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"math/big"
	"sort"
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

// criterionTopologySpread is the key used for the topology-spread criterion in the
// evaluation matrix. It is a named constant because MakeEvaluationMatrix and
// WeightMatrix previously disagreed on its spelling, which silently left the
// criterion at the default weight of 1.0 in every profile.
const criterionTopologySpread = "TopologySpreadScore"

// generateCombinations returns all possible node assignments:
// for each pod, choose one of the candidate nodes.
// Example: 3 pods, 2 nodes → 8 configurations.
type Assignment struct {
	Pod  corev1.Pod
	Node corev1.Node
	// AltNode is where the pod lands when the pods are placed in the opposite order
	// (see BuildMigrationPlan); nil when that order has no feasible placement, in which
	// case Node stands for both orders.
	AltNode *corev1.Node
}

type Combination []Assignment

// Deprecated: GenerateCombinations enumerates nodes^pods assignments, which is
// exponential and needs sampling to stay bounded. The scale-down path now uses
// BuildMigrationPlan, which builds one deterministic plan per candidate node in
// O(pods * candidates). Retained only for the existing unit tests.
//
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
	total, ok := pow(numNodes, numPods) // combinazioni totali = numNodes^numPods

	// Limit combinations to avoid memory exhaustion. !ok means numNodes^numPods
	// overflowed an int, which is by definition more than maxCombinations.
	if !ok || total > maxCombinations {
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

// pow returns a^b, and false if the result overflows an int. Without the guard a
// large pod count silently wrapped: 3 nodes ^ 40 pods wraps negative and panics the
// subsequent make(), while 4 ^ 32 wraps to exactly 0 and yields no combinations at all.
func pow(a, b int) (int, bool) {
	res := 1
	for i := 0; i < b; i++ {
		if a != 0 && res > math.MaxInt/a {
			return 0, false
		}
		res *= a
	}
	return res, true
}

// ----- RESOURCE CHECKING -----

type AvailableResources struct {
	CPU    resource.Quantity
	Memory resource.Quantity
}

// podRequests returns the effective CPU and memory requests of a pod, computed the way
// the kube-scheduler accounts for it. The older podRequestsCPU/podRequestsMemory helpers
// ignore init containers entirely.
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
// minus the requests of the pods already running there. CheckResources seeds its
// budget from bare allocatable instead, so every destination looks empty and plans
// that cannot really run pass the feasibility check.
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

// BuildMigrationPlan produces one feasible plan moving every pod in pods off
// drainedNode onto one of candidates, or nil if no such plan exists.
//
// This replaces enumerating all nodes^pods assignments and filtering them. That
// approach was exponential, overflowed for realistic pod counts, and ended with a
// single surviving plan chosen at random — so the soft criteria were evaluated
// against an arbitrary scenario that usually belonged to a different node. Here each
// candidate node gets its own deterministic plan, built in O(pods * candidates^2).
//
// The plan imitates the kube-scheduler that will place the evicted pods: one pod at a
// time, the candidates filtered as its filter plugins would (resources, taints,
// required node affinity, required pod (anti-)affinity), the pod put on the candidate
// with the highest score of the default score plugins (see schedulerScores), and free
// resources and occupants updated after every pod.
//
// The scheduler places the recreated pods in an order DREEM does not control, and for
// pods that prefer to be near each other the order decides whether they end up
// together. The plan is therefore built in both extreme orders: the returned plan puts
// the pods that others prefer to be near first (the order that keeps affinity), and
// every Assignment.AltNode records the destination when they come last. The affinity
// criteria score both.
func BuildMigrationPlan(ctx context.Context, c client.Client, drainedNode corev1.Node, candidates []corev1.Node, pods []corev1.Pod) (Combination, error) {
	return buildMigrationPlan(ctx, c, drainedNode, candidates, pods, true)
}

// buildMigrationPlan is BuildMigrationPlan with the hard constraints optional, as set by
// the hardChecks key of cluster-configuration-parameters. With hardChecks false the plan
// is never nil and the hard topology spread constraints are not checked: a pod that fits
// on no candidate is left out of the plan instead of vetoing the node. It is the pod the
// scheduler will leave Pending, so the soft criteria must not credit it with a destination.
func buildMigrationPlan(ctx context.Context, c client.Client, drainedNode corev1.Node, candidates []corev1.Node, pods []corev1.Pod, hardChecks bool) (Combination, error) {
	if len(candidates) == 0 && hardChecks {
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

	plan := placePods(orderForPlacement(pods, true), candidates, free, resident, drainedNode.Name, hardChecks)
	if plan == nil {
		return nil, nil
	}
	if alt := placePods(orderForPlacement(pods, false), candidates, free, resident, drainedNode.Name, hardChecks); alt != nil {
		altNode := make(map[string]*corev1.Node, len(alt))
		for i := range alt {
			altNode[podKey(&alt[i].Pod)] = &alt[i].Node
		}
		for i := range plan {
			plan[i].AltNode = altNode[podKey(&plan[i].Pod)]
		}
	}

	// topology spread is a property of the whole plan, so it is checked once at the end
	if hardChecks && len(CheckTopologySpreadConstraints(ctx, c, []Combination{plan})) == 0 {
		klog.V(3).Infof("Plan for draining node %s violates a hard topology spread constraint", drainedNode.Name)
		return nil, nil
	}

	return plan, nil
}

// orderForPlacement returns the pods in the order a plan places them. With
// partnersFirst the pods without preferred pod-affinity terms - the ones others prefer
// to be near - come before the pods that want to follow them; without it, after them.
// Within each group the largest pods come first: they are the hardest to place.
func orderForPlacement(pods []corev1.Pod, partnersFirst bool) []corev1.Pod {
	ordered := make([]corev1.Pod, len(pods))
	copy(ordered, pods)
	sort.SliceStable(ordered, func(i, j int) bool {
		fi := len(preferredPodAffinityTerms(&ordered[i])) > 0
		fj := len(preferredPodAffinityTerms(&ordered[j])) > 0
		if fi != fj {
			return fi != partnersFirst
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
// pod has no admissible candidate; with hardChecks false that pod is left out of the
// plan instead. freeInit and resident are not modified.
func placePods(ordered []corev1.Pod, candidates []corev1.Node, freeInit map[string]AvailableResources, resident map[string][]corev1.Pod, drainedNode string, hardChecks bool) Combination {
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
			if !hardChecks {
				klog.V(3).Infof("No admissible destination for pod %s/%s while draining node %s, left out of the plan (hard checks off)",
					pod.Namespace, pod.Name, drainedNode)
				continue
			}
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

// Deprecated: CheckResources seeds each destination's budget from bare Allocatable and
// never subtracts the pods already running there, so it only verifies that the moving
// pods would fit on an empty node. BuildMigrationPlan supersedes it and accounts for
// resident pods via NodeFreeResources. Retained only for the existing unit tests; do
// not use it to decide whether a migration is feasible.
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

// ApplySoftConstraints ranks the candidate nodes for shutdown. plans maps a node name
// to the migration plan that would evacuate that node, and every criterion for a node
// is computed against that node's own plan.
//
// Previously a single plan was drawn at random from the pooled plans of all candidates
// and used to score every node, so for node X the affinity and topology criteria
// actually described a scenario that evacuated some other node, and the values changed
// from one reconcile to the next for an unchanged cluster.
func ApplySoftConstraints(plans map[string]Combination, nodes []corev1.Node, ctx context.Context, client client.Client, nodeSelecting clusterv1alpha1.NodeSelecting) ([]corev1.Node, string, error) {
	klog.FromContext(ctx).WithName("Apply-TOPSIS-scale-down")
	klog.V(2).Info("Applying soft constraints with TOPSIS")
	// Fill the structure with criteria values
	var criteriaList []TOPSISCriteria
	var message = "Values for TOPSIS scale down evaluation: | "
	for _, node := range nodes {
		validScheduling, ok := plans[node.Name]
		if !ok {
			klog.V(2).Info("No migration plan recorded for node, skipping it", "node", node.Name)
			continue
		}

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
	klog.V(2).Info("AHP weights loaded successfully: ", weights)
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

// ----- AFFINITY HELPERS -----

func podKey(pod *corev1.Pod) string {
	return pod.Namespace + "/" + pod.Name
}

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

// stillTogether reports whether two pods that share the drained node end up in the same
// topology domain after the plan is applied. A pod that is not in the plan (DaemonSet,
// monitoring, static) disappears with the node, so the pair is broken.
func stillTogether(podA, podB *corev1.Pod, destination map[string]*corev1.Node, topologyKey string) bool {
	destA, okA := destination[podKey(podA)]
	destB, okB := destination[podKey(podB)]
	if !okA || !okB {
		return false
	}
	if topologyKey == "" || topologyKey == "kubernetes.io/hostname" {
		return destA.Name == destB.Name
	}
	valA, hasA := destA.Labels[topologyKey]
	valB, hasB := destB.Labels[topologyKey]
	return hasA && hasB && valA == valB
}

func GetPreferredNodeAffinity(node corev1.Node, validSchedulingConfig []Assignment) int {
	// Preferred node affinity LOST by draining this node: for every pod in the plan, the
	// weight it gets here minus the weight it still gets on its destination. A pod moving
	// to another node of its preferred group loses nothing. Destinations depend on the
	// order the scheduler places the pods in, so the loss is the mean over the two orders
	// the plan was built in (Node and AltNode).
	lost := 0
	for i := range validSchedulingConfig {
		a := &validSchedulingConfig[i]
		here := preferredNodeAffinityWeight(&a.Pod, &node)
		lost += nodeAffinityLoss(here, preferredNodeAffinityWeight(&a.Pod, &a.Node))
		lost += nodeAffinityLoss(here, preferredNodeAffinityWeight(&a.Pod, altNodeOf(a)))
	}
	return (lost + 1) / 2
}

func nodeAffinityLoss(here, there int) int {
	if here > there {
		return here - there
	}
	return 0
}

// altNodeOf is where the assignment's pod lands in the opposite placement order, or its
// Node when that order had no feasible placement.
func altNodeOf(a *Assignment) *corev1.Node {
	if a.AltNode != nil {
		return a.AltNode
	}
	return &a.Node
}

func GetPreferredInterPodAffinity(node corev1.Node, validSchedulingConfig []Assignment, managedClusterClient client.Client, ctx context.Context) (int, error) {
	// Preferred inter-pod affinity LOST by draining this node: the weight of every
	// (pod -> preferred pod) pair that is satisfied on this node and is no longer in the
	// same topology domain once the pods are moved away. The scheduler places the evicted
	// pods in an order DREEM does not control, and for a pair evicted together the order
	// decides whether it lands together, so every pair is scored in both orders the plan
	// was built in (Node and AltNode): kept in both costs nothing, broken in both the full
	// weight, broken in one of them half the weight - the loss to expect when either
	// order is equally likely.
	podsOnNode := corev1.PodList{}
	if err := managedClusterClient.List(ctx, &podsOnNode, client.MatchingFields{"spec.nodeName": node.Name}); err != nil {
		return 0, err
	}
	existingPods := filterSystemPods(podsOnNode.Items)

	destination := make(map[string]*corev1.Node, len(validSchedulingConfig))
	altDestination := make(map[string]*corev1.Node, len(validSchedulingConfig))
	for i := range validSchedulingConfig {
		a := &validSchedulingConfig[i]
		destination[podKey(&a.Pod)] = &a.Node
		altDestination[podKey(&a.Pod)] = altNodeOf(a)
	}

	lost := 0 // summed over both orders, halved at the end
	for i := range existingPods {
		for j := range existingPods {
			if i == j {
				continue
			}
			podA, podB := &existingPods[i], &existingPods[j]
			for _, term := range preferredPodAffinityTerms(podA) {
				if !podMatchesAffinityTerm(podA, podB, &term.PodAffinityTerm) {
					continue
				}
				topologyKey := term.PodAffinityTerm.TopologyKey
				if !stillTogether(podA, podB, destination, topologyKey) {
					lost += int(term.Weight)
				}
				if !stillTogether(podA, podB, altDestination, topologyKey) {
					lost += int(term.Weight)
				}
			}
		}
	}
	return (lost + 1) / 2, nil
}

// Funzione ausiliaria per calcolare il peso dell'affinità preferita tra due Pod specifici con log interni
func computePodToPodAffinityWeight(podA, podB *corev1.Pod) int {
	if podA.Spec.Affinity == nil || podA.Spec.Affinity.PodAffinity == nil {
		return 0
	}

	weightSum := 0
	preferred := podA.Spec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution

	for termIdx, term := range preferred {
		selector, err := metav1.LabelSelectorAsSelector(term.PodAffinityTerm.LabelSelector)
		if err != nil {
			klog.V(4).Infof("[DEBUG-AFFINITY-INTERNAL] Pod %s has invalid label selector in term %d: %v", podA.Name, termIdx, err)
			continue
		}

		// Controllo del Namespace
		if len(term.PodAffinityTerm.Namespaces) > 0 {
			nsMatch := false
			for _, ns := range term.PodAffinityTerm.Namespaces {
				if ns == podB.Namespace {
					nsMatch = true
					break
				}
			}
			if !nsMatch {
				klog.V(4).Infof("[DEBUG-AFFINITY-INTERNAL] Namespace mismatch between Pod %s (ns: %s) and target term namespaces: %v", podB.Name, podB.Namespace, term.PodAffinityTerm.Namespaces)
				continue
			}
		}

		// Controllo effettivo del match dei selettori delle label
		if selector.Matches(labels.Set(podB.Labels)) {
			weightSum += int(term.Weight)
		} else {
			klog.V(4).Infof("[DEBUG-AFFINITY-INTERNAL] Pod %s labels %v DID NOT MATCH selector %v of Pod %s", podB.Name, podB.Labels, selector, podA.Name)
		}
	}

	return weightSum
}
func GetPreferredInterPodAntiAffinity(node corev1.Node, validSchedulingConfig []Assignment, managedClusterClient client.Client, ctx context.Context) (int, error) {
	// Preferred anti-affinity VIOLATIONS CREATED by draining this node: for every pod the
	// plan moves, the weight of its anti-affinity terms matched by the pods it will share
	// its destination with (hostname topology). A pod never counts against itself.
	// Cost criterion: fewer violations created = better node to remove.
	incoming := make(map[string][]*corev1.Pod)
	for i := range validSchedulingConfig {
		a := &validSchedulingConfig[i]
		incoming[a.Node.Name] = append(incoming[a.Node.Name], &a.Pod)
	}

	created := 0
	for destName, moving := range incoming {
		residentList := corev1.PodList{}
		if err := managedClusterClient.List(ctx, &residentList, client.MatchingFields{"spec.nodeName": destName}); err != nil {
			return 0, err
		}
		residents := filterSystemPods(residentList.Items)

		neighbours := make([]*corev1.Pod, 0, len(residents)+len(moving))
		for i := range residents {
			neighbours = append(neighbours, &residents[i])
		}
		neighbours = append(neighbours, moving...)

		for _, pod := range moving {
			for _, term := range preferredPodAntiAffinityTerms(pod) {
				for _, other := range neighbours {
					if podKey(other) == podKey(pod) {
						continue
					}
					if podMatchesAffinityTerm(pod, other, &term.PodAffinityTerm) {
						created += int(term.Weight)
					}
				}
			}
		}
	}
	return created, nil
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
	klog.V(3).Info("Ideal Solution for nodeselecting ", nodeSelecting.Name, ": ", idealSolution)
	klog.V(3).Info("Negative Ideal Solution for nodeselecting ", nodeSelecting.Name, ": ", negativeIdealSolution)

	// 5. Calculate the separation measures for each alternative.
	separationFromIdeal, separationFromNegativeIdeal := CalculateSeparationMeasures(weightedMatrix, idealSolution, negativeIdealSolution)

	// 6. Calculate the relative closeness to the ideal solution.
	relativeCloseness := CalculateRelativeCloseness(separationFromIdeal, separationFromNegativeIdeal)
	klog.V(3).Info("Relative Closeness to Ideal Solution for nodeselecting ", nodeSelecting.Name, ": ")
	klog.V(3).Info("Node\tRelative Closeness")
	for i, crit := range criteriaList {
		klog.V(3).Info(crit.Node.Name, "\t", relativeCloseness[i])
	}
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
	idealSolution, negativeIdealSolution := CalculateIdealSolutionsFor(weightedMatrix, benefitCriteriaScaleUp)
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
			criterionTopologySpread:         float64(crit.TopologySpreadScore),
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
			case criterionTopologySpread:
				weight = weights.TopologySpread
			default:
				weight = 1.0
			}
			weighted[row][keys] = math.Round((val*weight)*1000) / 1000
		}
	}

	return weighted

}

// EnergyProfile carries the WAP metric: the power the node draws for the workload
// it runs, so a higher value always means a less efficient node. Its direction in
// TOPSIS therefore depends on what we are ranking for:
//
//   - scale down, we rank candidates to switch OFF, so the least efficient node is
//     the best pick and EnergyProfile is a benefit (maximize);
//   - scale up, we rank candidates to switch ON, so the least efficient node is the
//     worst pick and EnergyProfile is a cost (minimize).
//
// Every other criterion is a cost in both directions.
var (
	benefitCriteriaScaleDown = map[string]bool{"EnergyProfile": true}
	benefitCriteriaScaleUp   = map[string]bool{}
)

// CalculateIdealSolutions determines the ideal and negative-ideal solutions for the
// scale-down ranking. Use CalculateIdealSolutionsFor to rank in the other direction.
func CalculateIdealSolutions(matrix []map[string]float64) (map[string]float64, map[string]float64) {
	return CalculateIdealSolutionsFor(matrix, benefitCriteriaScaleDown)
}

// CalculateIdealSolutionsFor determines the ideal and negative-ideal solutions,
// treating every criterion listed in benefitCriteria as a benefit (to maximize) and
// all the others as costs (to minimize).
func CalculateIdealSolutionsFor(matrix []map[string]float64, benefitCriteria map[string]bool) (map[string]float64, map[string]float64) {

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
		if benefitCriteria[key] {
			ideal[key] = maxFloat64(values)
			negativeIdeal[key] = minFloat64(values)
		} else { // cost
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

// GetTopologySpreadScore measures how much a migration plan degrades topology spread:
// the increase in skew the plan causes, summed over the distinct constraints of the
// pods being moved. Higher is worse, so it is a cost criterion.
//
// Both soft (ScheduleAnyway) and hard (DoNotSchedule) constraints are scored. Hard
// constraints stay a pure admission concern in CheckTopologySpreadConstraints, which
// is unchanged and still rejects plans that would exceed MaxSkew; but among the plans
// that are admissible, one that pushes the skew from 0 up to the limit is still worse
// for the application than one that leaves the distribution untouched, and only the
// score can express that difference.
func GetTopologySpreadScore(ctx context.Context, r client.Client, comb Combination, currentNode corev1.Node) (int, error) {
	if len(comb) == 0 {
		return 0, nil
	}

	var allNodes corev1.NodeList
	if err := r.List(ctx, &allNodes); err != nil {
		return 0, err
	}
	var allPods corev1.PodList
	if err := r.List(ctx, &allPods); err != nil {
		return 0, err
	}

	score := 0
	seen := make(map[string]bool)

	for _, assignment := range comb {
		for _, constraint := range assignment.Pod.Spec.TopologySpreadConstraints {
			// the same constraint shared by several pods of a deployment describes one
			// distribution requirement, so it must only be counted once per plan
			key := constraint.TopologyKey + "|" + metav1.FormatLabelSelector(constraint.LabelSelector)
			if seen[key] {
				continue
			}
			seen[key] = true

			before := calculateRealSkew(Combination{}, allNodes.Items, allPods.Items, constraint)
			after := calculateRealSkew(comb, allNodes.Items, allPods.Items, constraint)
			if degradation := after - before; degradation > 0 {
				score += degradation
			}
		}
	}

	return score, nil
}
