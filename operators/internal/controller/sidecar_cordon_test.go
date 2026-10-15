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

func requests(cpu, mem string) corev1.ResourceRequirements {
	list := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)}
	if mem != "" {
		list[corev1.ResourceMemory] = resource.MustParse(mem)
	}
	return corev1.ResourceRequirements{Requests: list}
}

// istioPod is shaped like a muBench replica with Istio injected: istio-init (a regular
// init container), istio-proxy (a native sidecar) and the app container.
func istioPod(name, node, appCPU string) *corev1.Pod {
	always := corev1.ContainerRestartPolicyAlways
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: map[string]string{"app": name}},
		Spec: corev1.PodSpec{
			NodeName: node,
			InitContainers: []corev1.Container{
				{Name: "istio-init", Resources: requests("100m", "128Mi")},
				{Name: "istio-proxy", RestartPolicy: &always, Resources: requests("100m", "128Mi")},
			},
			Containers: []corev1.Container{{Name: "app", Resources: requests(appCPU, "")}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestPodRequestsCountsNativeSidecar(t *testing.T) {
	got := podRequests(istioPod("s5-0", "", "2"))
	if got.CPU.MilliValue() != 2100 {
		t.Fatalf("CPU: expected 2100m (app 2 + istio-proxy 100m), got %dm", got.CPU.MilliValue())
	}
	if want := resource.MustParse("128Mi"); got.Memory.Cmp(want) != 0 {
		t.Fatalf("memory: expected 128Mi (istio-proxy), got %s", got.Memory.String())
	}
}

func TestPodRequestsInitContainerRunsNextToEarlierSidecars(t *testing.T) {
	// a regular init container of 3 CPU runs while the 500m sidecar declared before it is
	// already up: 3.5 CPU, more than the running phase (1 + 0.5)
	always := corev1.ContainerRestartPolicyAlways
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{
			{Name: "sidecar", RestartPolicy: &always, Resources: requests("500m", "")},
			{Name: "migrate", Resources: requests("3", "")},
		},
		Containers: []corev1.Container{{Name: "app", Resources: requests("1", "")}},
	}}
	got := podRequests(pod)
	if got := got.CPU.MilliValue(); got != 3500 {
		t.Fatalf("expected 3500m, got %dm", got)
	}
}

func TestPodRequestsWithoutInitContainersIsTheSum(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Name: "a", Resources: requests("1500m", "")},
		{Name: "b", Resources: requests("250m", "")},
	}}}
	got := podRequests(pod)
	if got := got.CPU.MilliValue(); got != 1750 {
		t.Fatalf("expected 1750m, got %dm", got)
	}
}

func TestPlanRejectsDrainThatOnlyFitsWithoutSidecars(t *testing.T) {
	// The destination runs four 1.9-CPU replicas: 7.6 CPU without the sidecars, 8.0 with
	// them, so 2.0 CPU is really free. The evicted replica needs 2.1 with its sidecar.
	// Counting the sidecars, the plan must be refused: the scheduler could not place it.
	drained := planTestNode("node-d", "small")
	dest := planTestNode("node-1", "big")
	moving := istioPod("s5-0", "node-d", "2")
	objs := []client.Object{&drained, &dest, moving.DeepCopy()}
	for _, name := range []string{"r0", "r1", "r2", "r3"} {
		objs = append(objs, istioPod(name, "node-1", "1900m"))
	}
	c := fake.NewClientBuilder().WithObjects(objs...).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		}).Build()

	plan, err := BuildMigrationPlan(context.Background(), c, drained, []corev1.Node{dest}, []corev1.Pod{*moving})
	if err != nil {
		t.Fatal(err)
	}
	if plan != nil {
		t.Fatalf("expected no feasible plan (2.1 CPU needed, 2.0 free), got %v", plan)
	}
}

func TestScaleDownCandidatesLeavesOutCordonedNodes(t *testing.T) {
	ready := corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}
	notReady := corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}
	nodes := []corev1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "cp", Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}}, Status: ready},
		{ObjectMeta: metav1.ObjectMeta{Name: "worker-ready"}, Status: ready},
		{ObjectMeta: metav1.ObjectMeta{Name: "worker-cordoned"}, Spec: corev1.NodeSpec{Unschedulable: true}, Status: ready},
		{ObjectMeta: metav1.ObjectMeta{Name: "worker-off"}, Status: notReady},
	}
	got := scaleDownCandidates(nodes)
	if len(got) != 1 || got[0].Name != "worker-ready" {
		names := make([]string, 0, len(got))
		for _, n := range got {
			names = append(names, n.Name)
		}
		t.Fatalf("expected only worker-ready, got %v", names)
	}
}
