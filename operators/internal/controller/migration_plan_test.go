package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// the anchor must run on a big node
var planTestAnchorAffinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
	RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
		MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "size", Operator: corev1.NodeSelectorOpIn, Values: []string{"big"}}},
	}}},
}}

// buildPlan drains big-d, which runs pods, towards candidates; objs are the other pods.
func buildPlan(t *testing.T, candidates []corev1.Node, pods []*corev1.Pod, objs ...client.Object) Combination {
	t.Helper()
	drained := planTestNode("big-d", "big")
	objs = append(objs, &drained)
	moving := make([]corev1.Pod, 0, len(pods))
	for _, pod := range pods {
		objs = append(objs, pod.DeepCopy())
		moving = append(moving, *pod)
	}
	for i := range candidates {
		objs = append(objs, &candidates[i])
	}
	plan, err := buildMigrationPlan(context.Background(), fakeClientWithPodIndex(objs...), drained, candidates, moving)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestPlanMovesEveryPodOrRefusesTheNode(t *testing.T) {
	pods := []*corev1.Pod{planTestPod("a-0", "a", "big-d", "1", nil), planTestPod("b-0", "b", "big-d", "2", nil)}

	// 3 CPUs free on big-1: both pods fit
	plan := buildPlan(t, []corev1.Node{planTestNode("big-1", "big")}, pods,
		planTestPod("filler", "filler", "big-1", "7", nil))
	if len(plan) != 2 {
		t.Fatalf("expected both pods placed, got %v", plan)
	}

	// 2 CPUs free for 3 CPUs of pods
	plan = buildPlan(t, []corev1.Node{planTestNode("big-1", "big")}, pods,
		planTestPod("filler", "filler", "big-1", "8", nil))
	if plan != nil {
		t.Fatalf("expected no plan, got %v", plan)
	}
}

func TestPlanFollowsRequiredNodeAffinity(t *testing.T) {
	anchor := []*corev1.Pod{planTestPod("aa-0", "aa", "big-d", "500m", planTestAnchorAffinity)}

	if plan := buildPlan(t, []corev1.Node{planTestNode("small-1", "small")}, anchor); plan != nil {
		t.Fatalf("the anchor cannot go to a small node, got %v", plan)
	}
	plan := buildPlan(t, []corev1.Node{planTestNode("small-1", "small"), planTestNode("big-1", "big")}, anchor)
	if len(plan) != 1 || plan[0].Node.Name != "big-1" {
		t.Fatalf("expected the anchor on big-1, got %v", plan)
	}
}

func TestPlanForANodeWithoutPods(t *testing.T) {
	if plan := buildPlan(t, []corev1.Node{planTestNode("big-1", "big")}, nil); plan == nil || len(plan) != 0 {
		t.Fatalf("a node with nothing to migrate: expected an empty plan, got %v", plan)
	}
	// the last worker has nowhere to send its pods, even when it has none
	if plan := buildPlan(t, nil, nil); plan != nil {
		t.Fatalf("no destination: expected no plan, got %v", plan)
	}
}
