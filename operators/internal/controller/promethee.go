package controller

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Node selection of Menouer et al., "Reactive Autoscaling of Kubernetes Nodes"
// (FRAME@HPDC 2024, Sec. 3.1 and 3.3), the reactive phase of their TPDS 2026 paper:
// PROMETHEE compares every candidate with every other one on each criterion with the
// usual preference function (1 if strictly better, 0 otherwise), all the weights are 1,
// and the node with the highest net outranking flow is switched on or off.

// prometheeCriterion is one column of the PROMETHEE evaluation table.
type prometheeCriterion struct {
	Name     string
	Maximize bool
	Value    func(ctx context.Context, c client.Client, node corev1.Node) (float64, error)
}

// scaleUpCriteria: among the powered-off nodes, the one that maximizes the allocated CPUs,
// memory and storage and minimizes the energy consumption.
var scaleUpCriteria = []prometheeCriterion{
	{Name: "cpu", Maximize: true, Value: capacityCPU},
	{Name: "memoryGiB", Maximize: true, Value: capacityMemoryGiB},
	{Name: "storageGiB", Maximize: true, Value: capacityStorageGiB},
	{Name: "energy", Maximize: false, Value: energyConsumption},
}

// scaleDownCriteria: among the active nodes, the one that minimizes the allocated CPUs,
// memory and storage and the number of Kubernetes objects, and maximizes the energy
// consumption. The paper's TPDS version calls it efficiency in one sentence, but its
// algorithm and the FRAME paper maximize the consumption: the node that draws the most
// power is switched off.
var scaleDownCriteria = []prometheeCriterion{
	{Name: "cpu", Maximize: false, Value: capacityCPU},
	{Name: "memoryGiB", Maximize: false, Value: capacityMemoryGiB},
	{Name: "storageGiB", Maximize: false, Value: capacityStorageGiB},
	{Name: "pods", Maximize: false, Value: runningPods},
	{Name: "energy", Maximize: true, Value: energyConsumption},
}

// The allocated resources of a node are its capacity, as in the paper's example, where
// the load of a node is the CPUs used over its 8 CPUs. Status.Capacity stays on the Node
// object while the machine is off, so the powered-off candidates have it too; a resource
// the node does not report counts as zero. Memory and storage are compared in whole GiB,
// as the paper states them: the few KiB the kernel can report differently on identical
// machines would otherwise count as a full preference.
func capacityCPU(_ context.Context, _ client.Client, node corev1.Node) (float64, error) {
	return float64(node.Status.Capacity.Cpu().MilliValue()) / 1000, nil
}

func capacityMemoryGiB(_ context.Context, _ client.Client, node corev1.Node) (float64, error) {
	return math.Round(float64(node.Status.Capacity.Memory().Value()) / (1 << 30)), nil
}

func capacityStorageGiB(_ context.Context, _ client.Client, node corev1.Node) (float64, error) {
	return math.Round(float64(node.Status.Capacity.StorageEphemeral().Value()) / (1 << 30)), nil
}

// energyConsumption is the energy figure of the node, which the paper does not specify:
// the dreemk8s.io/consumption-profile annotation that DREEM ranks the nodes with, higher
// for a node that draws more power.
func energyConsumption(_ context.Context, _ client.Client, node corev1.Node) (float64, error) {
	value, ok := node.Annotations[DREEM_ENERGY_EFFICIENCY_ANNOTATION]
	if !ok {
		return 0, fmt.Errorf("annotation %s not found on node %s", DREEM_ENERGY_EFFICIENCY_ANNOTATION, node.Name)
	}
	return strconv.ParseFloat(strings.TrimSpace(value), 64)
}

// runningPods is the number of Kubernetes objects running on the node: its pods in phase
// Running, whatever their namespace or owner.
func runningPods(ctx context.Context, c client.Client, node corev1.Node) (float64, error) {
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods, client.MatchingFields{"spec.nodeName": node.Name}); err != nil {
		return 0, err
	}
	running := 0
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			running++
		}
	}
	return float64(running), nil
}

// prometheeNetFlows returns the net outranking flow of every alternative: values has one
// row per alternative and one column per criterion, maximize the direction of each
// criterion. P_j(a,b) is 1 when a is strictly better than b on criterion j and 0
// otherwise, pi(a,b) = sum_j P_j(a,b), phi+(a) and phi-(a) are the sums of pi(a,x) and
// pi(x,a) over the other alternatives divided by n-1, and phi(a) = phi+(a) - phi-(a).
func prometheeNetFlows(values [][]float64, maximize []bool) []float64 {
	n := len(values)
	flows := make([]float64, n)
	if n < 2 {
		return flows
	}
	for a := 0; a < n; a++ {
		for b := a + 1; b < n; b++ {
			for j, higherIsBetter := range maximize {
				d := values[a][j] - values[b][j]
				if !higherIsBetter {
					d = -d
				}
				switch {
				case d > 0: // P_j(a,b) = 1: adds to phi+(a) and to phi-(b)
					flows[a]++
					flows[b]--
				case d < 0:
					flows[b]++
					flows[a]--
				}
			}
		}
	}
	for a := range flows {
		flows[a] /= float64(n - 1)
	}
	return flows
}

// selectWithPROMETHEE ranks the candidates on the criteria and returns the node with the
// highest net flow, and the ranking for the NodeSelecting status. The paper does not say
// how a tie is broken: the node is drawn at random among the tied ones, so that no node
// is favoured by its name. A candidate whose criteria cannot be read is left out; ""
// means that no candidate is left.
func selectWithPROMETHEE(ctx context.Context, c client.Client, candidates []corev1.Node, criteria []prometheeCriterion) (string, string) {
	nodes := make([]corev1.Node, 0, len(candidates))
	values := make([][]float64, 0, len(candidates))
	for _, node := range candidates {
		row := make([]float64, len(criteria))
		valid := true
		for j, criterion := range criteria {
			v, err := criterion.Value(ctx, c, node)
			if err != nil {
				klog.V(2).ErrorS(err, "PROMETHEE criterion not available, node left out", "node", node.Name, "criterion", criterion.Name)
				valid = false
				break
			}
			row[j] = v
		}
		if valid {
			nodes = append(nodes, node)
			values = append(values, row)
		}
	}
	if len(nodes) == 0 {
		return "", ""
	}

	maximize := make([]bool, len(criteria))
	names := make([]string, len(criteria))
	for j, criterion := range criteria {
		maximize[j] = criterion.Maximize
		names[j] = criterion.Name
	}
	flows := prometheeNetFlows(values, maximize)

	var best []int
	for i := range flows {
		switch {
		case len(best) == 0 || flows[i] > flows[best[0]]:
			best = []int{i}
		case flows[i] == flows[best[0]]:
			best = append(best, i)
		}
	}
	selected := nodes[best[rand.IntN(len(best))]].Name

	order := make([]int, len(nodes))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool { return flows[order[x]] > flows[order[y]] })
	ranking := make([]string, 0, len(nodes))
	for _, i := range order {
		ranking = append(ranking, fmt.Sprintf("%s %+.2f %v", nodes[i].Name, flows[i], values[i]))
	}
	message := fmt.Sprintf("PROMETHEE net flow and [%s] | %s", strings.Join(names, " "), strings.Join(ranking, " | "))
	if len(best) > 1 {
		tied := make([]string, len(best))
		for k, i := range best {
			tied[k] = nodes[i].Name
		}
		message += fmt.Sprintf(" | tie between %s, %s drawn at random", strings.Join(tied, ", "), selected)
	}
	klog.V(2).InfoS("PROMETHEE selection", "selectedNode", selected, "ranking", message)
	return selected, message
}
