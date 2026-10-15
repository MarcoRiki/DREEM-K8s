package controller

import (
	"context"
	"testing"

	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func clusterParametersCM(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: DREEM_CLUSTER_CONFIGURATION_CM_NAME, Namespace: "dreem"},
		Data:       data,
	}
}

func fakeClientWithPodIndex(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithObjects(objs...).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).Build()
}

func TestLoadSelectionSettings(t *testing.T) {
	cases := []struct {
		name    string
		data    map[string]string
		want    selectionSettings
		wantErr bool
	}{
		{name: "keys missing", data: map[string]string{"selectionProfile": "ENERGY"},
			want: selectionSettings{Policy: selectionPolicyTOPSIS, HardChecks: true}},
		{name: "keys empty", data: map[string]string{"selectionPolicy": "", "hardChecks": ""},
			want: selectionSettings{Policy: selectionPolicyTOPSIS, HardChecks: true}},
		{name: "random in any case", data: map[string]string{"selectionPolicy": " random "},
			want: selectionSettings{Policy: selectionPolicyRandom, HardChecks: true}},
		{name: "hard checks off", data: map[string]string{"hardChecks": "False"},
			want: selectionSettings{Policy: selectionPolicyTOPSIS, HardChecks: false}},
		{name: "hard checks on", data: map[string]string{"hardChecks": "TRUE"},
			want: selectionSettings{Policy: selectionPolicyTOPSIS, HardChecks: true}},
		{name: "unknown policy", data: map[string]string{"selectionPolicy": "ROUND_ROBIN"}, wantErr: true},
		{name: "unknown hard checks", data: map[string]string{"hardChecks": "no"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithObjects(clusterParametersCM(tc.data)).Build()
			got, err := loadSelectionSettings(context.Background(), c)
			if (err != nil) != tc.wantErr {
				t.Fatalf("expected error %v, got %v", tc.wantErr, err)
			}
			if got != tc.want {
				t.Fatalf("expected %+v, got %+v", tc.want, got)
			}
		})
	}
}

func TestPlanWithoutHardChecksLeavesOutPodsThatFitNowhere(t *testing.T) {
	// 1 CPU is free on the destination: the 500m pod fits, the 2-CPU pod fits nowhere
	drained := planTestNode("node-d", "big")
	dest := planTestNode("node-1", "big")
	fits := planTestPod("fits", "fits", "node-d", "500m", nil)
	tooBig := planTestPod("too-big", "too-big", "node-d", "2", nil)
	c := fakeClientWithPodIndex(&drained, &dest, fits.DeepCopy(), tooBig.DeepCopy(),
		planTestPod("filler", "filler", "node-1", "9", nil))
	ctx := context.Background()
	pods := []corev1.Pod{*fits, *tooBig}

	if plan, err := buildMigrationPlan(ctx, c, drained, []corev1.Node{dest}, pods, true); err != nil || plan != nil {
		t.Fatalf("hard checks on: expected no plan, got %v, %v", plan, err)
	}

	plan, err := buildMigrationPlan(ctx, c, drained, []corev1.Node{dest}, pods, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].Pod.Name != "fits" || plan[0].Node.Name != "node-1" {
		t.Fatalf("hard checks off: expected only fits on node-1, got %d assignments", len(plan))
	}

	// the last worker: nowhere to go, yet the node can still be selected
	plan, err = buildMigrationPlan(ctx, c, drained, nil, pods, false)
	if err != nil || plan == nil || len(plan) != 0 {
		t.Fatalf("hard checks off, no candidates: expected an empty plan, got %v, %v", plan, err)
	}
}

// newSelectionScenario has two workers. w-small (4 CPU) runs a 1-CPU pod that fits on
// w-big; w-big (8 CPU) runs a 5-CPU pod that does not fit on w-small. Only w-small can
// be drained, but w-big draws more power, so TOPSIS prefers to switch it off.
func newSelectionScenario(policy, hardChecks string) *NodeSelectingReconciler {
	worker := func(name, cpu, wap string) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{"kubernetes.io/hostname": name},
				Annotations: map[string]string{
					DREEM_ENERGY_EFFICIENCY_ANNOTATION: wap,
					DREEM_POWER_CYCLE_ANNOTATION:       "0",
				},
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(cpu),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
				},
			},
		}
	}
	weights := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: DREEM_WEIGHTS_SCALE_DOWN_CM_NAME, Namespace: "dreem"},
		Data: map[string]string{"energy": `PreferredNodeAffinity: "0.0053"
PreferredPodAffinity: "0.0098"
PreferredPodAntiAffinity: "0.0098"
EnergyProfile: "0.7500"
PowerCycles: "0.1500"
NumberOfRunningPods: "0.0375"
TopologySpread: "0.0375"`},
	}
	c := fakeClientWithPodIndex(
		worker("w-small", "4", "250"),
		worker("w-big", "8", "280"),
		planTestPod("small-0", "small", "w-small", "1", nil),
		planTestPod("big-0", "big", "w-big", "5", nil),
		clusterParametersCM(map[string]string{
			"selectionProfile": "ENERGY",
			"selectionPolicy":  policy,
			"hardChecks":       hardChecks,
		}),
		weights,
	)
	return &NodeSelectingReconciler{Client: c}
}

func selectScaleDown(t *testing.T, r *NodeSelectingReconciler) string {
	t.Helper()
	ctx := context.Background()
	settings, err := loadSelectionSettings(ctx, r.Client)
	if err != nil {
		t.Fatal(err)
	}
	nodeSelecting := &clusterv1alpha1.NodeSelecting{ObjectMeta: metav1.ObjectMeta{Name: "node-selecting-test"}}
	err, _, node := r.selectNodeScaleDown(ctx, nodeSelecting, settings)
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func TestScaleDownTOPSISWithAndWithoutHardChecks(t *testing.T) {
	if got := selectScaleDown(t, newSelectionScenario("", "true")); got != "w-small" {
		t.Fatalf("hard checks on: expected w-small, the only node that can be drained, got %q", got)
	}
	if got := selectScaleDown(t, newSelectionScenario("", "false")); got != "w-big" {
		t.Fatalf("hard checks off: expected w-big, the node with the higher WAP, got %q", got)
	}
}

func TestScaleDownRandomWithAndWithoutHardChecks(t *testing.T) {
	draws := func(hardChecks string) map[string]int {
		r := newSelectionScenario("RANDOM", hardChecks)
		counts := map[string]int{}
		for i := 0; i < 64; i++ {
			counts[selectScaleDown(t, r)]++
		}
		return counts
	}
	if got := draws("true"); got["w-small"] != 64 {
		t.Fatalf("hard checks on: expected w-small in every draw, got %v", got)
	}
	// both nodes are eligible: the odds of one never being drawn in 64 draws are 2^-63
	if got := draws("false"); got["w-small"] == 0 || got["w-big"] == 0 {
		t.Fatalf("hard checks off: expected both nodes to be drawn, got %v", got)
	}
}

func TestScaleUpRandomDrawsOnlyPoweredOffWorkers(t *testing.T) {
	node := func(name string, ready corev1.ConditionStatus, labels map[string]string) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
			Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}},
		}
	}
	c := fake.NewClientBuilder().WithObjects(
		node("cp", corev1.ConditionFalse, map[string]string{"node-role.kubernetes.io/control-plane": ""}),
		node("w-on", corev1.ConditionTrue, nil),
		node("w-off-1", corev1.ConditionFalse, nil),
		node("w-off-2", corev1.ConditionFalse, nil),
	).Build()
	r := &NodeSelectingReconciler{Client: c}
	settings := selectionSettings{Policy: selectionPolicyRandom, HardChecks: true}

	counts := map[string]int{}
	for i := 0; i < 64; i++ {
		err, _, selected := r.selectNodeScaleUp(context.Background(), &clusterv1alpha1.NodeSelecting{}, c, settings)
		if err != nil {
			t.Fatal(err)
		}
		counts[selected]++
	}
	if counts["w-off-1"] == 0 || counts["w-off-2"] == 0 || counts["w-off-1"]+counts["w-off-2"] != 64 {
		t.Fatalf("expected draws among the powered-off workers only, both of them, got %v", counts)
	}
}
