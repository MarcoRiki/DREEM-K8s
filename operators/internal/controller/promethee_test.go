package controller

import (
	"context"
	"math"
	"strings"
	"testing"

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

// worker is a worker node with the given capacity (also its allocatable) and energy
// figure; energy "" leaves the annotation out.
func worker(name string, ready bool, cpu, memory, energy string) *corev1.Node {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	resources := corev1.ResourceList{
		corev1.ResourceCPU:              resource.MustParse(cpu),
		corev1.ResourceMemory:           resource.MustParse(memory),
		corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"kubernetes.io/hostname": name}},
		Status: corev1.NodeStatus{
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}},
			Capacity:    resources,
			Allocatable: resources.DeepCopy(),
		},
	}
	if energy != "" {
		node.Annotations = map[string]string{DREEM_ENERGY_EFFICIENCY_ANNOTATION: energy}
	}
	return node
}

func TestPrometheeNetFlows(t *testing.T) {
	// cpu to maximize, energy to minimize. A-B: B less energy. A-C: A more cpu, C less
	// energy. B-C: B more cpu and less energy. Net preferences A -1, B +3, C -2, over n-1 = 2.
	values := [][]float64{{10, 280}, {10, 160}, {8, 200}}
	got := prometheeNetFlows(values, []bool{true, false})
	want := []float64{-0.5, 1.5, -1}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
	if got := prometheeNetFlows([][]float64{{1, 2}}, []bool{true, true}); len(got) != 1 || got[0] != 0 {
		t.Fatalf("a single alternative has net flow 0, got %v", got)
	}
}

func TestLoadHardChecks(t *testing.T) {
	cases := []struct {
		name    string
		data    map[string]string
		want    bool
		wantErr bool
	}{
		{name: "key missing", data: map[string]string{"minNodes": "3"}, want: true},
		{name: "key empty", data: map[string]string{"hardChecks": ""}, want: true},
		{name: "off in any case", data: map[string]string{"hardChecks": " False "}, want: false},
		{name: "on", data: map[string]string{"hardChecks": "TRUE"}, want: true},
		{name: "unknown value", data: map[string]string{"hardChecks": "no"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithObjects(clusterParametersCM(tc.data)).Build()
			got, err := loadHardChecks(context.Background(), c)
			if (err != nil) != tc.wantErr {
				t.Fatalf("expected error %v, got %v", tc.wantErr, err)
			}
			if err == nil && got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func selectUp(t *testing.T, objs ...client.Object) string {
	t.Helper()
	r := &NodeSelectingReconciler{Client: fakeClientWithPodIndex(objs...)}
	err, _, node := r.selectNodeScaleUp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func TestScaleUpPicksTheLowestConsumptionAmongPoweredOffWorkers(t *testing.T) {
	cp := worker("cp", false, "10", "32Gi", "100")
	cp.Labels["node-role.kubernetes.io/control-plane"] = ""
	objs := []client.Object{
		cp,
		worker("w-on", true, "10", "32Gi", "100"),
		worker("w-off-1", false, "10", "32Gi", "200"),
		worker("w-off-2", false, "10", "32Gi", "160"),
		worker("w-off-3", false, "10", "32Gi", "250"),
	}
	// equal capacities: the energy criterion alone decides, no tie
	for i := 0; i < 16; i++ {
		if got := selectUp(t, objs...); got != "w-off-2" {
			t.Fatalf("expected w-off-2, the powered-off worker that draws least, got %q", got)
		}
	}
}

func TestScaleUpPrefersTheLargerNode(t *testing.T) {
	// more CPUs and memory beat lower consumption: 2 criteria against 1
	got := selectUp(t,
		worker("w-big", false, "16", "64Gi", "280"),
		worker("w-small", false, "8", "32Gi", "160"))
	if got != "w-big" {
		t.Fatalf("expected w-big, got %q", got)
	}
}

func TestScaleUpComparesMemoryInWholeGiB(t *testing.T) {
	// 4Ki more memory on w-a is not a preference: the energy decides
	for i := 0; i < 16; i++ {
		got := selectUp(t,
			worker("w-a", false, "10", "33554436Ki", "200"),
			worker("w-b", false, "10", "32Gi", "160"))
		if got != "w-b" {
			t.Fatalf("expected w-b, got %q", got)
		}
	}
}

func TestScaleUpLeavesOutNodesWithoutEnergyFigure(t *testing.T) {
	got := selectUp(t,
		worker("w-unknown", false, "10", "32Gi", ""),
		worker("w-known", false, "10", "32Gi", "280"))
	if got != "w-known" {
		t.Fatalf("expected w-known, got %q", got)
	}
	if got := selectUp(t, worker("w-on", true, "10", "32Gi", "160")); got != "" {
		t.Fatalf("no powered-off worker: expected no selection, got %q", got)
	}
}

func selectDown(t *testing.T, hardChecks bool, objs ...client.Object) (string, string) {
	t.Helper()
	r := &NodeSelectingReconciler{Client: fakeClientWithPodIndex(objs...)}
	err, message, node := r.selectNodeScaleDown(context.Background(), hardChecks)
	if err != nil {
		t.Fatal(err)
	}
	return node, message
}

func TestScaleDownWithAndWithoutHardChecks(t *testing.T) {
	// w-big draws more power and runs fewer pods, w-small has fewer CPUs: PROMETHEE
	// prefers to switch off w-big (2 criteria against 1). Its 5-CPU pod does not fit on
	// w-small (4 CPUs, 1 used), while w-small's two pods fit on w-big.
	objs := []client.Object{
		worker("w-small", true, "4", "16Gi", "250"),
		worker("w-big", true, "8", "16Gi", "280"),
		planTestPod("small-0", "small", "w-small", "500m", nil),
		planTestPod("small-1", "small", "w-small", "500m", nil),
		planTestPod("big-0", "big", "w-big", "5", nil),
	}
	if got, _ := selectDown(t, false, objs...); got != "w-big" {
		t.Fatalf("hard checks off: expected w-big, PROMETHEE's first, got %q", got)
	}
	if got, _ := selectDown(t, true, objs...); got != "w-small" {
		t.Fatalf("hard checks on: expected w-small, the only node that can be drained, got %q", got)
	}
}

func TestScaleDownTieIsDrawnAtRandom(t *testing.T) {
	// fewest pods against highest consumption, equal capacities: w1 (3 pods, 280),
	// w2 (1 pod, 160) and w3 (2 pods, 210) all have net flow 0
	objs := []client.Object{
		worker("w1", true, "10", "32Gi", "280"),
		worker("w2", true, "10", "32Gi", "160"),
		worker("w3", true, "10", "32Gi", "210"),
	}
	for node, pods := range map[string]int{"w1": 3, "w2": 1, "w3": 2} {
		for i := 0; i < pods; i++ {
			objs = append(objs, planTestPod(node+"-"+string(rune('a'+i)), "app", node, "100m", nil))
		}
	}
	counts := map[string]int{}
	for i := 0; i < 64; i++ {
		node, message := selectDown(t, false, objs...)
		if !strings.Contains(message, "tie between") {
			t.Fatalf("expected the tie in the message, got %q", message)
		}
		counts[node]++
	}
	// the odds of one node never being drawn in 64 draws are about 3 * (2/3)^64
	if counts["w1"] == 0 || counts["w2"] == 0 || counts["w3"] == 0 {
		t.Fatalf("expected every tied node to be drawn, got %v", counts)
	}
}
