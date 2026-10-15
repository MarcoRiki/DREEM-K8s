package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func planTestNode(name, size string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"size": size, "kubernetes.io/hostname": name}},
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("10"),
			corev1.ResourceMemory: resource.MustParse("32Gi"),
		}},
	}
}

func planTestPod(name, app, node, cpu string, affinity *corev1.Affinity) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: map[string]string{"app": app}},
		Spec: corev1.PodSpec{
			NodeName: node,
			Affinity: affinity,
			Containers: []corev1.Container{{Name: app, Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(cpu),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			}}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// the anchor must run on a big node, the probe prefers its anchor's node (weight 100)
var planTestAnchorAffinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
	RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
		MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "size", Operator: corev1.NodeSelectorOpIn, Values: []string{"big"}}},
	}}},
}}

var planTestProbeAffinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
	PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
		Weight: 100,
		PodAffinityTerm: corev1.PodAffinityTerm{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "aa"}},
			TopologyKey:   "kubernetes.io/hostname",
		},
	}},
}}

// drains a big node running an anchor and its probe; fillers set the CPU already used
// on the other nodes. Returns the plan and the pod affinity lost.
func runDrainScenario(t *testing.T, fillers map[string]string) (Combination, int) {
	t.Helper()
	drained := planTestNode("big-d", "big")
	candidates := []corev1.Node{planTestNode("big-1", "big"), planTestNode("big-2", "big"), planTestNode("small-1", "small")}
	anchor := planTestPod("aa-0", "aa", "big-d", "500m", planTestAnchorAffinity)
	probe := planTestPod("pa-0", "pa", "big-d", "1000m", planTestProbeAffinity)

	objs := []client.Object{&drained, anchor.DeepCopy(), probe.DeepCopy()}
	for i := range candidates {
		objs = append(objs, &candidates[i])
	}
	for node, cpu := range fillers {
		objs = append(objs, planTestPod("filler-"+node, "filler", node, cpu, nil))
	}
	c := fake.NewClientBuilder().WithObjects(objs...).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).Build()

	ctx := context.Background()
	plan, err := BuildMigrationPlan(ctx, c, drained, candidates, []corev1.Pod{*anchor, *probe})
	if err != nil || plan == nil {
		t.Fatalf("no plan: %v", err)
	}
	for _, a := range plan {
		alt := "-"
		if a.AltNode != nil {
			alt = a.AltNode.Name
		}
		t.Logf("%s -> %s, other order -> %s", a.Pod.Name, a.Node.Name, alt)
	}
	lost, err := GetPreferredInterPodAffinity(drained, plan, c, ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pod affinity lost: %d", lost)
	return plan, lost
}

func TestPlanSplitsPairWhenProbeFirstPicksSmallNode(t *testing.T) {
	// big nodes nearly full, small node empty: placed first, the probe picks the small
	// node, where its anchor cannot follow; placed after the anchor, it joins it
	plan, lost := runDrainScenario(t, map[string]string{"big-1": "7", "big-2": "7"})
	if lost != 50 {
		t.Fatalf("expected 50 (kept in one order, broken in the other), got %d", lost)
	}
	for _, a := range plan {
		if a.Pod.Name == "pa-0" && (a.AltNode == nil || a.AltNode.Name != "small-1") {
			t.Fatalf("probe placed first should land on small-1, got %v", a.AltNode)
		}
	}
}

func TestPlanKeepsPairWhenProbeFirstPicksBigNode(t *testing.T) {
	// small node nearly full: placed first, the probe picks a big node and the anchor
	// follows it (the probe's preference counts for the anchor too)
	_, lost := runDrainScenario(t, map[string]string{"small-1": "9"})
	if lost != 0 {
		t.Fatalf("expected 0 (kept in both orders), got %d", lost)
	}
}

func TestPlanNodeWithoutPairsLosesNothing(t *testing.T) {
	drained := planTestNode("big-d", "big")
	candidates := []corev1.Node{planTestNode("big-1", "big")}
	filler := planTestPod("filler-d", "filler", "big-d", "1", nil)
	c := fake.NewClientBuilder().WithObjects(&drained, &candidates[0], filler.DeepCopy()).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).Build()
	ctx := context.Background()
	plan, err := BuildMigrationPlan(ctx, c, drained, candidates, []corev1.Pod{*filler})
	if err != nil || len(plan) != 1 {
		t.Fatalf("expected a one-pod plan, got %v, %v", plan, err)
	}
	if lost, _ := GetPreferredInterPodAffinity(drained, plan, c, ctx); lost != 0 {
		t.Fatalf("expected 0, got %d", lost)
	}
}
