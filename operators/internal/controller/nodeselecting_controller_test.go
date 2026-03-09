/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MarcoRiki/DREEM-K8s/internal/controller"
	corev1 "k8s.io/api/core/v1"
	clusterapi "sigs.k8s.io/cluster-api/api/v1beta1"
)

var nodeSmallName = "node-small"
var providerID = "provider-test"
var nodeSmall = corev1.Node{
	ObjectMeta: metav1.ObjectMeta{
		Name: nodeSmallName,
	},
	Spec: corev1.NodeSpec{
		ProviderID: *pointer.String(fmt.Sprintf("%s%s", providerID, nodeSmallName)),
	},
	Status: corev1.NodeStatus{
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
		Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
	},
}

var MachineNodeSmall = clusterapi.Machine{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "machine-node-small",
		Namespace: "default",
		Labels: map[string]string{
			"cluster.x-k8s.io/cluster-name":    "cluster-test",
			"cluster.x-k8s.io/deployment-name": "deployment-1",
			"cluster.x-k8s.io/set-name":        nodeSmallName,
		},
	},
	Spec: clusterapi.MachineSpec{
		ProviderID:  pointer.String(fmt.Sprintf("%s%s", providerID, nodeSmallName)),
		ClusterName: "cluster-test",
		InfrastructureRef: corev1.ObjectReference{
			Kind:      "TestMachine",
			Namespace: "default",
			Name:      nodeSmallName,
		},
	},
}

var MachineDeployment1 = clusterapi.MachineDeployment{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "deployment-1",
		Namespace: "default",
		Labels: map[string]string{
			"cluster.x-k8s.io/cluster-name": "cluster-test",
		},
		Annotations: map[string]string{
			controller.DREEM_POWER_CYCLE_ANNOTATION:       "7",
			controller.DREEM_ENERGY_EFFICIENCY_ANNOTATION: "1000",
		},
	},
	Spec: clusterapi.MachineDeploymentSpec{
		ClusterName: "cluster-test",
		Template: clusterapi.MachineTemplateSpec{
			Spec: clusterapi.MachineSpec{
				ClusterName: "cluster-test",
			},
		},
	},
}

var nodeLarge = corev1.Node{
	ObjectMeta: metav1.ObjectMeta{
		Name: "node-large",
	},
	Status: corev1.NodeStatus{
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("8"),
			corev1.ResourceMemory: resource.MustParse("16Gi"),
		},
		Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("8"),
			corev1.ResourceMemory: resource.MustParse("12Gi"),
		},
	},
}

var podBigNginx = corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name: "pod-big-nginx",
	},
	Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:  "nginx",
				Image: "nginx:latest",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			},
		},
	},
}

var podSmallNginx = corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name: "pod-small-nginx",
	},
	Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:  "nginx",
				Image: "nginx:latest",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			},
		},
	},
}

var podMediumNginx = corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name: "pod-medium-nginx",
	},
	Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:  "nginx",
				Image: "nginx:latest",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			},
		},
	},
}

var podAffinity_A = corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name: "pod-affinity-a",
	},
	Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:  "nginx",
				Image: "nginx:latest",
			},
		},
		Affinity: &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{ // affinty towards antarctica-east1 or antarctica-west1, antiaffinity with us-central1
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "topology.kubernetes.io/zone",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"antarctica-east1", "antarctica-west1"},
								},
								{

									Key:      "topology.kubernetes.io/zone",
									Operator: corev1.NodeSelectorOpNotIn,
									Values:   []string{"us-central1"},
								},
							},
						},
					},
				},
			},
		},
	},
}

var podAffinity_B = corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name: "pod-affinity-b",
	},
	Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:  "nginx",
				Image: "nginx:latest",
			},
		},
		Affinity: &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "topology.kubernetes.io/zone",
									Operator: corev1.NodeSelectorOpNotIn,
									Values:   []string{"antarctica-east1"},
								},
							},
						},
					},
				},
			},
		},
	},
}

var nodeAffinityAName = "node-affinity-a"

var nodeAffinity_A = corev1.Node{
	ObjectMeta: metav1.ObjectMeta{
		Name: nodeAffinityAName,
		Labels: map[string]string{
			"topology.kubernetes.io/zone": "antarctica-east1",
		},
	},
	Spec: corev1.NodeSpec{
		ProviderID: *pointer.String(fmt.Sprintf("%s%s", providerID, nodeAffinityAName)),
	},
	Status: corev1.NodeStatus{
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("8Gi"),
		},
		Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("3"),
			corev1.ResourceMemory: resource.MustParse("6Gi"),
		},
	},
}

var MachineNodeAffinityA = clusterapi.Machine{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "machine-node-affinity-a",
		Namespace: "default",
		Labels: map[string]string{
			"cluster.x-k8s.io/cluster-name":    "cluster-test",
			"cluster.x-k8s.io/deployment-name": "deployment-2",
			"cluster.x-k8s.io/set-name":        nodeAffinityAName,
		},
	},
	Spec: clusterapi.MachineSpec{
		ProviderID:  pointer.String(fmt.Sprintf("%s%s", providerID, nodeAffinityAName)),
		ClusterName: "cluster-test",
		InfrastructureRef: corev1.ObjectReference{
			Kind:      "TestMachine",
			Namespace: "default",
			Name:      nodeAffinityAName,
		},
	},
}

var MachineDeployment2 = clusterapi.MachineDeployment{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "deployment-2",
		Namespace: "default",
		Labels: map[string]string{
			"cluster.x-k8s.io/cluster-name": "cluster-test",
		},
		Annotations: map[string]string{
			controller.DREEM_POWER_CYCLE_ANNOTATION:       "10",
			controller.DREEM_ENERGY_EFFICIENCY_ANNOTATION: "1500",
		},
	},
	Spec: clusterapi.MachineDeploymentSpec{
		ClusterName: "cluster-test",
		Template: clusterapi.MachineTemplateSpec{
			Spec: clusterapi.MachineSpec{
				ClusterName: "cluster-test",
			},
		},
	},
}

var nodeAffinityBName = "node-affinity-b"
var nodeAffinity_B = corev1.Node{
	ObjectMeta: metav1.ObjectMeta{
		Name: nodeAffinityBName,
		Labels: map[string]string{
			"topology.kubernetes.io/zone": "us-central1",
		},
	},
	Spec: corev1.NodeSpec{
		ProviderID: *pointer.String(fmt.Sprintf("%s%s", providerID, nodeAffinityBName)),
	},
	Status: corev1.NodeStatus{
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("8Gi"),
		},
		Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("3"),
			corev1.ResourceMemory: resource.MustParse("6Gi"),
		},
	},
}

var MachineNodeAffinityB = clusterapi.Machine{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "machine-node-affinity-b",
		Namespace: "default",
		Labels: map[string]string{
			"cluster.x-k8s.io/cluster-name":    "cluster-test",
			"cluster.x-k8s.io/deployment-name": "deployment-3",
			"cluster.x-k8s.io/set-name":        nodeAffinityBName,
		},
	},
	Spec: clusterapi.MachineSpec{
		ProviderID:  pointer.String(fmt.Sprintf("%s%s", providerID, nodeAffinityBName)),
		ClusterName: "cluster-test",
		InfrastructureRef: corev1.ObjectReference{
			Kind:      "TestMachine",
			Namespace: "default",
			Name:      nodeAffinityBName,
		},
	},
}

var MachineDeployment3 = clusterapi.MachineDeployment{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "deployment-3",
		Namespace: "default",
		Labels: map[string]string{
			"cluster.x-k8s.io/cluster-name": "cluster-test",
		},
	},
	Spec: clusterapi.MachineDeploymentSpec{
		ClusterName: "cluster-test",
		Template: clusterapi.MachineTemplateSpec{
			Spec: clusterapi.MachineSpec{
				ClusterName: "cluster-test",
			},
		},
	},
}

var nodeTainted_A = corev1.Node{
	ObjectMeta: metav1.ObjectMeta{
		Name: "node-tainted-a",
	},
	Spec: corev1.NodeSpec{
		Taints: []corev1.Taint{
			{
				Key:    "key1",
				Value:  "value1",
				Effect: corev1.TaintEffectNoSchedule,
			},
		},
	},
	Status: corev1.NodeStatus{
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("8Gi"),
		},
		Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("3"),
			corev1.ResourceMemory: resource.MustParse("6Gi"),
		},
	},
}

var podBindedToNode1 = corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "pod-binded-to-node-replica-1",
		Namespace: "default",
		Labels: map[string]string{
			"app": "frontend",
		},
	},
	Spec: corev1.PodSpec{
		NodeName: "node-large",
		Containers: []corev1.Container{
			{Name: "nginx", Image: "nginx:latest"},
		},
	},
}

var podWithInterPodAffinity = corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "pod-with-interpod-affinity",
		Namespace: "default",
		Labels: map[string]string{
			"app": "frontend",
		},
	},
	Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			{Name: "nginx", Image: "nginx:latest"},
		},
		Affinity: &corev1.Affinity{
			PodAffinity: &corev1.PodAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
					{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"app": "frontend",
							},
						},
						TopologyKey: "kubernetes.io/hostname",
					},
				},
			},
		},
	},
}

var podWithInterPodAntiAffinity = corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "pod-with-interpod-antiaffinity",
		Namespace: "default",
		Labels: map[string]string{
			"app": "frontend",
		},
	},
	Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			{Name: "nginx", Image: "nginx:latest"},
		},
		Affinity: &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
					{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"app": "frontend",
							},
						},
						TopologyKey: "kubernetes.io/hostname",
					},
				},
			},
		},
	},
}

var podNginx = corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "nginx-test-pod",
		Namespace: "default",
		Labels: map[string]string{
			"app": "frontend",
		},
	},
	Spec: corev1.PodSpec{
		NodeName: "node-large",
		Containers: []corev1.Container{
			{Name: "nginx", Image: "nginx:latest"},
		},
	},
}

var podDatabase = corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "database-test-pod",
		Namespace: "default",
		Labels: map[string]string{
			"app": "database",
		},
	},
	Spec: corev1.PodSpec{
		NodeName: "node-large",
		Containers: []corev1.Container{
			{Name: "mysql", Image: "mysql:latest"},
		},
	},
}

var podBusibox = corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "busibox-test-pod",
		Namespace: "default",
	},
	Spec: corev1.PodSpec{
		NodeName: "node-large",
		Containers: []corev1.Container{
			{Name: "busibox", Image: "busibox:latest"},
		},
	},
}

var AHPWeightsConfigMap = corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name:      controller.DREEM_WEIGHTS_SCALE_DOWN_CM_NAME,
		Namespace: "dreem",
	},
	Data: map[string]string{
		"neutral": `PreferredNodeAffinity: 1
PreferredPodAffinity: 1
PreferredPodAntiAffinity: 1
EnergyProfile: 1
PowerCycles: 1
NumberOfRunningPods: 1`,
		"energy": `PreferredNodeAffinity: 0.0083
PreferredPodAffinity: 0.0180
PreferredPodAntiAffinity: 0.0180
EnergyProfile: 0.7778
PowerCycles: 0.1111
NumberOfRunningPods: 0.0668`,
	},
}

var configParametersCM = corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name:      controller.DREEM_CLUSTER_CONFIGURATION_CM_NAME,
		Namespace: "dreem",
	},
	Data: map[string]string{
		"minNodes":         "1",
		"maxNodes":         "5",
		"selectionProfile": "NEUTRAL",
	},
}

var sampleMatrix = []map[string]float64{
	{
		"PowerCycle":                    5.0,
		"EnergyProfile":                 1000.0,
		"PreferredNodeAffinity":         55.0,
		"PrefererredInterPodAffinity":   14.0,
		"PreferredInterPodAntiAffinity": 23.0,
		"NumberOfRunningPods":           4.0,
	},
	{
		"PowerCycle":                    10.0,
		"EnergyProfile":                 1500.0,
		"PreferredNodeAffinity":         0.0,
		"PrefererredInterPodAffinity":   5.0,
		"PreferredInterPodAntiAffinity": 89.0,
		"NumberOfRunningPods":           9.0,
	},
}

var normalizedSampleMatrix = []map[string]float64{
	{
		"PowerCycle":                    0.447,
		"EnergyProfile":                 0.555,
		"PreferredNodeAffinity":         1.0,
		"PrefererredInterPodAffinity":   0.942,
		"PreferredInterPodAntiAffinity": 0.250,
		"NumberOfRunningPods":           0.406,
	},
	{
		"PowerCycle":                    0.894,
		"EnergyProfile":                 0.832,
		"PreferredNodeAffinity":         0.0,
		"PrefererredInterPodAffinity":   0.336,
		"PreferredInterPodAntiAffinity": 0.968,
		"NumberOfRunningPods":           0.914,
	},
}

var weightedNormalizedSampleMatrixEnergyProfile = []map[string]float64{
	{
		"PowerCycle":                    0.050,
		"EnergyProfile":                 0.432,
		"PreferredNodeAffinity":         0.008,
		"PrefererredInterPodAffinity":   0.017,
		"PreferredInterPodAntiAffinity": 0.005,
		"NumberOfRunningPods":           0.027,
	},
	{
		"PowerCycle":                    0.099,
		"EnergyProfile":                 0.647,
		"PreferredNodeAffinity":         0,
		"PrefererredInterPodAffinity":   0.006,
		"PreferredInterPodAntiAffinity": 0.017,
		"NumberOfRunningPods":           0.061,
	},
}

var expectedIdealSolution = map[string]float64{
	"PowerCycle":                    0.050,
	"EnergyProfile":                 0.647,
	"PreferredNodeAffinity":         0,
	"PrefererredInterPodAffinity":   0.006,
	"PreferredInterPodAntiAffinity": 0.005,
	"NumberOfRunningPods":           0.027,
}

var expectedNegativeIdealSolution = map[string]float64{
	"PowerCycle":                    0.099,
	"EnergyProfile":                 0.432,
	"PreferredNodeAffinity":         0.008,
	"PrefererredInterPodAffinity":   0.017,
	"PreferredInterPodAntiAffinity": 0.017,
	"NumberOfRunningPods":           0.061,
}

var expectedSeparationIdeal = []float64{0.215, 0.061}
var expectedSeparationNegativeIdeal = []float64{0.061, 0.215}
var expectedRelativeCloseness = []float64{0.221, 0.779}

var _ = Describe("NodeSelecting Controller", func() {
	Context("Hard Constraints checks", func() {

		ctx := context.Background()
		BeforeEach(func() {
			Eventually(func() error {
				podList := &corev1.PodList{}
				err := k8sClient.List(ctx, podList, client.InNamespace("default"))
				if err != nil {
					return err
				}
				for _, p := range podList.Items {
					err = k8sClient.Delete(ctx, &p, &client.DeleteOptions{
						GracePeriodSeconds: pointer.Int64(0),
					})
					if err != nil {
						return err
					}
				}
				return nil
			}, "20s", "250ms").Should(Succeed())

			Eventually(func() error {
				nodeList := &corev1.NodeList{}
				err := k8sClient.List(ctx, nodeList)
				if err != nil {
					return err
				}
				for _, n := range nodeList.Items {
					err = k8sClient.Delete(ctx, &n, &client.DeleteOptions{
						GracePeriodSeconds: pointer.Int64(0),
					})
					if err != nil {
						return err
					}
				}
				return nil
			}, "20s", "250ms").Should(Succeed())
		})

		// It("should successfully reconcile the resource", func() {
		// 	node := &corev1.Node{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name: "test-node",
		// 		},
		// 	}
		// 	err := k8sClient.Create(ctx, node)

		// 	Expect(err).NotTo(HaveOccurred())
		// 	By("Getting correct number of nodes")
		// 	var nodeList corev1.NodeList
		// 	err = k8sClient.List(ctx, &nodeList)
		// 	Expect(err).NotTo(HaveOccurred())
		// 	numberNodes := len(nodeList.Items)
		// 	Expect(numberNodes).To(Equal(1))

		// 	nodesel := &clusterv1alpha1.NodeSelecting{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name:      "test-nodeselecting",
		// 			Namespace: "default",
		// 		},
		// 		Spec: clusterv1alpha1.NodeSelectingSpec{
		// 			ClusterConfigurationName: "aa",
		// 			ScalingLabel:             -1,
		// 		},
		// 	}
		// 	err = k8sClient.Create(ctx, nodesel)
		// 	Expect(err).NotTo(HaveOccurred())

		// 	By("expecting status phase be in running")
		// 	Eventually(func(g Gomega) {
		// 		f := &clusterv1alpha1.NodeSelecting{}
		// 		err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-nodeselecting", Namespace: "default"}, f)
		// 		g.Expect(err).NotTo(HaveOccurred())
		// 		g.Expect(f.Status.Phase).To(Equal(clusterv1alpha1.NS_PhaseRunning))
		// 	}, "5s", "250ms").Should(Succeed())

		// })

		It("Should return all the combinations - GenerateCombinations", func() {
			By("Creating combination")
			podList := []corev1.Pod{}
			podList = append(podList, podBigNginx)
			podList = append(podList, podSmallNginx)

			nodeList := []corev1.Node{}
			nodeList = append(nodeList, nodeSmall)
			nodeList = append(nodeList, nodeLarge)
			combinations := controller.GenerateCombinations(podList, nodeList)
			fmt.Fprintf(GinkgoWriter, "combinations: %+v\n", combinations)

			Expect(len(combinations)).To(Equal(4))
		})

		It("should not return any config - CheckResources", func() {
			By("Creating combination")
			podList := []corev1.Pod{}
			podList = append(podList, podBigNginx)

			nodeList := []corev1.Node{}
			nodeList = append(nodeList, nodeSmall)
			combinations := controller.GenerateCombinations(podList, nodeList)
			validCombinations := controller.CheckResources(combinations)

			Expect(len(validCombinations)).To(Equal(0))
		})

		It("should return two valid configs - CheckResources", func() {
			By("Creating combination")
			podList := []corev1.Pod{}
			podList = append(podList, podSmallNginx, podBigNginx, podMediumNginx)
			nodeList := []corev1.Node{}
			nodeList = append(nodeList, nodeLarge, nodeSmall)
			combinations := controller.GenerateCombinations(podList, nodeList)
			By("Checking the resources")
			validCombinations := controller.CheckResources(combinations)
			Expect(len(validCombinations)).To(Equal(2))

			shouldBeValid := []controller.Combination{
				{{Pod: podSmallNginx, Node: nodeLarge}, {Pod: podBigNginx, Node: nodeLarge}, {Pod: podMediumNginx, Node: nodeLarge}},
				{{Pod: podSmallNginx, Node: nodeSmall}, {Pod: podBigNginx, Node: nodeLarge}, {Pod: podMediumNginx, Node: nodeLarge}},
			}
			Expect(validCombinations).To(ContainElements(shouldBeValid))
		})

		It("should return one valid config - CheckNodeAffinity", func() {
			By("Creating combination")
			podList := []corev1.Pod{}
			podList = append(podList, podSmallNginx, podMediumNginx)
			nodeList := []corev1.Node{}
			nodeList = append(nodeList, nodeLarge)
			combinations := controller.GenerateCombinations(podList, nodeList)
			validCombinations := controller.CheckNodeAffinity(combinations)
			Expect(len(validCombinations)).To(Equal(1))
			shouldBeValid := []controller.Combination{
				{{Pod: podSmallNginx, Node: nodeLarge}, {Pod: podMediumNginx, Node: nodeLarge}},
			}
			Expect(validCombinations).To(ContainElements(shouldBeValid))

		})

		It("should return two valid config - CheckNodeAffinity", func() {
			By("Creating combination")
			podList := []corev1.Pod{}
			podList = append(podList, podAffinity_A, podAffinity_B)
			nodeList := []corev1.Node{}
			nodeList = append(nodeList, nodeAffinity_A, *nodeAffinity_A.DeepCopy(), nodeLarge)
			combinations := controller.GenerateCombinations(podList, nodeList)
			By("checking affinity")
			validCombinations := controller.CheckNodeAffinity(combinations)
			Expect(len(validCombinations)).To(Equal(2))

			shouldBeValid := []controller.Combination{
				{{Pod: podAffinity_A, Node: nodeAffinity_A}, {Pod: podAffinity_B, Node: nodeLarge}},
				{{Pod: podAffinity_A, Node: *nodeAffinity_A.DeepCopy()}, {Pod: podAffinity_B, Node: nodeLarge}},
			}
			Expect(validCombinations).To(ContainElements(shouldBeValid))

		})

		It("should return one valid config - CheckNodeAffinity AntiAffinity", func() {
			By("Creating combination")
			podList := []corev1.Pod{}
			podList = append(podList, podAffinity_A)
			nodeList := []corev1.Node{}
			nodeList = append(nodeList, nodeAffinity_B)
			combinations := controller.GenerateCombinations(podList, nodeList)
			By("checking affinity")
			validCombinations := controller.CheckNodeAffinity(combinations)
			Expect(len(validCombinations)).To(Equal(0))

		})

		It("should return one valid config - CheckNodeAffinity AntiAffinity", func() {
			By("Creating combination")
			podList := []corev1.Pod{}
			podList = append(podList, podAffinity_A, podAffinity_B)
			nodeList := []corev1.Node{}
			nodeList = append(nodeList, nodeAffinity_A, nodeAffinity_B)
			combinations := controller.GenerateCombinations(podList, nodeList)
			By("checking affinity")
			validCombinations := controller.CheckNodeAffinity(combinations)
			Expect(len(validCombinations)).To(Equal(1))

			shouldBeValid := []controller.Combination{
				{{Pod: podAffinity_A, Node: nodeAffinity_A}, {Pod: podAffinity_B, Node: nodeAffinity_B}},
			}
			Expect(validCombinations).To(ContainElements(shouldBeValid))
		})

		It("should return no valid config - CheckTaints", func() {
			By("Creating combination")
			podList := []corev1.Pod{}
			podList = append(podList, podSmallNginx)
			nodeList := []corev1.Node{}
			nodeList = append(nodeList, nodeTainted_A)

			By("Checking taints")
			combinations := controller.GenerateCombinations(podList, nodeList)
			validCombinations := controller.CheckTaints(combinations)
			Expect(len(validCombinations)).To(Equal(0))

		})

		It("should return one valid config - CheckInterPodAffinity", func() {
			By("Creating nodes and pods in k8s cluster")
			Expect(k8sClient.Create(ctx, nodeLarge.DeepCopy())).To(Succeed())
			Expect(k8sClient.Create(ctx, podBindedToNode1.DeepCopy())).To(Succeed())

			By("Checking pods on nodes are created")
			podsOnNodes := &corev1.PodList{}
			err := k8sClient.List(ctx, podsOnNodes, client.MatchingFields{"spec.nodeName": "node-large"})
			Expect(err).NotTo(HaveOccurred())
			Expect(len(podsOnNodes.Items)).To(Equal(1))

			By("Generating combinations")
			podList := []corev1.Pod{}
			podList = append(podList, podWithInterPodAffinity)
			nodeList := []corev1.Node{}
			nodeList = append(nodeList, nodeLarge)
			combinations := controller.GenerateCombinations(podList, nodeList)

			By("Checking interpod affinity")
			validCombinations := controller.CheckInterPodAffinity(ctx, k8sClient, combinations)
			Expect(len(validCombinations)).To(Equal(1))

			shouldBeValid := []controller.Combination{
				{{Pod: podWithInterPodAffinity, Node: nodeLarge}},
			}
			Expect(validCombinations).To(ContainElements(shouldBeValid))

		})

		It("should return one valid config - CheckInterPodAffinity antiaffinity", func() {
			By("Creating nodes and pods in k8s cluster")
			Expect(k8sClient.Create(ctx, nodeLarge.DeepCopy())).To(Succeed())
			Expect(k8sClient.Create(ctx, podBindedToNode1.DeepCopy())).To(Succeed())

			By("Checking pods on nodes are created")
			podsOnNodes := &corev1.PodList{}
			err := k8sClient.List(ctx, podsOnNodes, client.MatchingFields{"spec.nodeName": "node-large"})
			Expect(err).NotTo(HaveOccurred())
			Expect(len(podsOnNodes.Items)).To(Equal(1))

			By("Generating combinations")
			podList := []corev1.Pod{}
			podList = append(podList, podWithInterPodAntiAffinity)
			nodeList := []corev1.Node{}
			nodeList = append(nodeList, nodeLarge)
			combinations := controller.GenerateCombinations(podList, nodeList)

			By("Checking interpod antiaffinity")
			validCombinations := controller.CheckInterPodAffinity(ctx, k8sClient, combinations)
			Expect(len(validCombinations)).To(Equal(0))

		})

	})
	Context("Soft Constraints checks", func() {
		ctx := context.Background()

		BeforeEach(func() {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
			_ = k8sClient.Create(context.Background(), ns)

			dreemNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dreem"}}
			_ = k8sClient.Create(context.Background(), dreemNs)

			// create CM if not present
			Eventually(func() error {
				cm := &corev1.ConfigMap{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: controller.DREEM_WEIGHTS_SCALE_DOWN_CM_NAME, Namespace: "dreem"}, cm)
				if err != nil {
					if apierrors.IsNotFound(err) {
						return k8sClient.Create(ctx, AHPWeightsConfigMap.DeepCopy())
					}
					return err
				}
				return nil
			}, "10s", "250ms").Should(Succeed())

			Eventually(func() error {
				cm := &corev1.ConfigMap{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: controller.DREEM_CLUSTER_CONFIGURATION_CM_NAME, Namespace: "dreem"}, cm)
				if err != nil {
					if apierrors.IsNotFound(err) {
						return k8sClient.Create(ctx, configParametersCM.DeepCopy())
					}
					return err
				}
				return nil
			}, "10s", "250ms").Should(Succeed())

			Eventually(func() error {
				podList := &corev1.PodList{}
				err := k8sClient.List(ctx, podList, client.InNamespace("default"))
				if err != nil {
					return err
				}
				for _, p := range podList.Items {
					err = k8sClient.Delete(ctx, &p, &client.DeleteOptions{
						GracePeriodSeconds: pointer.Int64(0),
					})
					if err != nil {
						return err
					}
				}
				return nil
			}, "20s", "250ms").Should(Succeed())

			Eventually(func() error {
				nodeList := &corev1.NodeList{}
				err := k8sClient.List(ctx, nodeList)
				if err != nil {
					return err
				}
				for _, n := range nodeList.Items {
					err = k8sClient.Delete(ctx, &n, &client.DeleteOptions{
						GracePeriodSeconds: pointer.Int64(0),
					})
					if err != nil {
						return err
					}
				}
				return nil
			}, "20s", "250ms").Should(Succeed())
		})

		It("should return the correct number of runnings pods on node - getNumberOfRunningPods", func() {
			By("creating the cluster state")
			activeNode := nodeLarge.DeepCopy()
			k8sClient.Create(ctx, activeNode)
			k8sClient.Create(ctx, podNginx.DeepCopy())
			k8sClient.Create(ctx, podDatabase.DeepCopy())
			k8sClient.Create(ctx, podBusibox.DeepCopy())

			By("retrieving the number of running pods on node")
			numPods, err := controller.GetNumberOfRunningPods(*activeNode, ctx, k8sClient)
			Expect(err).NotTo(HaveOccurred())
			Expect(numPods).To(Equal(3))

		})

		It("should retrieve the power cycle correctly - getPowerCycle", func() {

			By("creating nodes in k8s cluster")
			node1 := nodeSmall.DeepCopy()
			node3 := nodeAffinity_B.DeepCopy()
			node2 := nodeAffinity_A.DeepCopy()

			k8sClient.Create(ctx, node1)
			k8sClient.Create(ctx, node3)
			k8sClient.Create(ctx, node2)

			By("creating machine and machinedeployment in k8s cluster")
			machineNode1 := MachineNodeSmall.DeepCopy()
			machineDeployment1 := MachineDeployment1.DeepCopy()
			machineNode3 := MachineNodeAffinityB.DeepCopy()
			machineDeployment3 := MachineDeployment3.DeepCopy()
			machineNode2 := MachineNodeAffinityA.DeepCopy()
			machineDeployment2 := MachineDeployment2.DeepCopy()

			k8sClient.Create(ctx, machineNode2)
			k8sClient.Create(ctx, machineDeployment2)

			k8sClient.Create(ctx, machineNode3)
			k8sClient.Create(ctx, machineDeployment3)

			k8sClient.Create(ctx, machineDeployment1)
			k8sClient.Create(ctx, machineNode1)

			numDeployments := &clusterapi.MachineDeploymentList{}
			err := k8sClient.List(ctx, numDeployments)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() int {
				numDeployments := &clusterapi.MachineDeploymentList{}
				_ = k8sClient.List(ctx, numDeployments)
				return len(numDeployments.Items)
			}, "10s", "250ms").Should(Equal(3))

			numMachines := &clusterapi.MachineList{}
			err = k8sClient.List(ctx, numMachines)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() int {
				numMachines := &clusterapi.MachineList{}
				_ = k8sClient.List(ctx, numMachines)
				return len(numMachines.Items)
			}, "10s", "250ms").Should(Equal(3))

			By("retrieving power cycle from nodes")
			powerCycle1, err := controller.GetPowerCycle(*node1, ctx, k8sClient)
			Expect(err).NotTo(HaveOccurred())
			Expect(powerCycle1).To(Equal(7))

			powerCycle3, err := controller.GetPowerCycle(*node2, ctx, k8sClient)
			Expect(err).NotTo(HaveOccurred())
			Expect(powerCycle3).To(Equal(10))

			By("testing the annotation is correctly created when not present")
			Expect(machineDeployment3.Annotations).NotTo(HaveKey(controller.DREEM_POWER_CYCLE_ANNOTATION))
			powerCycle2, err := controller.GetPowerCycle(*node3, ctx, k8sClient)
			Expect(err).NotTo(HaveOccurred())
			Expect(powerCycle2).To(Equal(0))

			By("testing the annotation is created with value 0")
			Eventually(func() string {
				updatedMachineDeployment3 := &clusterapi.MachineDeployment{}
				_ = k8sClient.Get(ctx, client.ObjectKey{Name: machineDeployment3.Name, Namespace: machineDeployment3.Namespace}, updatedMachineDeployment3)
				return updatedMachineDeployment3.Annotations[controller.DREEM_POWER_CYCLE_ANNOTATION]
			}, "10s", "250ms").Should(Equal("0"))

		})

		It("should retrieve the energy profile correctly - getEnergyProfile", func() {

			By("creating nodes in k8s cluster")
			node1 := nodeSmall.DeepCopy()
			node2 := nodeAffinity_A.DeepCopy()
			node3 := nodeAffinity_B.DeepCopy()

			k8sClient.Create(ctx, node1)
			k8sClient.Create(ctx, node2)
			k8sClient.Create(ctx, node3)

			By("creating machine and machinedeployment in k8s cluster")
			machineNode1 := MachineNodeSmall.DeepCopy()
			machineDeployment1 := MachineDeployment1.DeepCopy()
			machineNode2 := MachineNodeAffinityA.DeepCopy()
			machineDeployment2 := MachineDeployment2.DeepCopy()
			machineNode3 := MachineNodeAffinityB.DeepCopy()
			machineDeployment3 := MachineDeployment3.DeepCopy()

			k8sClient.Create(ctx, machineNode1)
			k8sClient.Create(ctx, machineDeployment1)

			k8sClient.Create(ctx, machineNode2)
			k8sClient.Create(ctx, machineDeployment2)

			k8sClient.Create(ctx, machineNode3)
			k8sClient.Create(ctx, machineDeployment3)

			Eventually(func() int {
				numDeployments := &clusterapi.MachineDeploymentList{}
				_ = k8sClient.List(ctx, numDeployments)
				return len(numDeployments.Items)
			}, "10s", "250ms").Should(Equal(3))

			Eventually(func() int {
				numMachines := &clusterapi.MachineList{}
				_ = k8sClient.List(ctx, numMachines)
				return len(numMachines.Items)
			}, "10s", "250ms").Should(Equal(3))

			By("retrieving energy profile from nodes")
			energyProfile1, err := controller.GetEnergyProfile(*node1, ctx, k8sClient)
			Expect(err).NotTo(HaveOccurred())
			Expect(energyProfile1).To(Equal(1000.0))

			energyProfile2, err := controller.GetEnergyProfile(*node2, ctx, k8sClient)
			Expect(err).NotTo(HaveOccurred())
			Expect(energyProfile2).To(Equal(1500.0))

			By("testing the annotation is correctly created when not present")
			Expect(machineDeployment3.Annotations).NotTo(HaveKey(controller.DREEM_ENERGY_EFFICIENCY_ANNOTATION))
			_, err = controller.GetEnergyProfile(*node3, ctx, k8sClient)
			Expect(err).To(HaveOccurred())

		})

		// It("should retrieve the preferred node affinity correctly - getPreferredNodeAffinity", func() {

		// })

		// It("should retrieve the preferred inter-pod affinity correctly - getPreferredInterPodAffinity", func() {

		// })

		// It("shoudl retrieve the preferred inter-pod anti-affinity correctly - getPreferredInterPodAntiAffinity", func() {

		// })

		It("should successfully load AHP weights", func() {

			By("Loading AHP weights from CM")
			weights, err := controller.LoadAHPweights(k8sClient, ctx)

			Expect(err).NotTo(HaveOccurred())

			Expect(weights.PreferredNodeAffinity).To(Equal(1.0))
			Expect(weights.PrefererredInterPodAffinity).To(Equal(1.0))
			Expect(weights.PreferredInterPodAntiAffinity).To(Equal(1.0))
			Expect(weights.EnergyProfile).To(Equal(1.0))
			Expect(weights.PowerCycle).To(Equal(1.0))
			Expect(weights.NumberOfRunningPods).To(Equal(1.0))

		})

		It("should create the correct TOPSIS matrix - makeEvaluationMatrix", func() {
			criterias := []controller.TOPSISCriteria{}

			row1 := controller.TOPSISCriteria{
				PreferredNodeAffinity:         1.0,
				PrefererredInterPodAffinity:   0.0,
				PreferredInterPodAntiAffinity: 1.0,
				EnergyProfile:                 1000.0,
				PowerCycle:                    5.0,
				NumberOfRunningPods:           3.0,
			}
			row2 := controller.TOPSISCriteria{
				PreferredNodeAffinity:         0.0,
				PrefererredInterPodAffinity:   5.0,
				PreferredInterPodAntiAffinity: 0.0,
				EnergyProfile:                 1500.0,
				PowerCycle:                    10.0,
				NumberOfRunningPods:           1.0,
			}
			criterias = append(criterias, row1, row2)

			By("Creating evaluation matrix")
			matrix := controller.MakeEvaluationMatrix(criterias)

			// float64(crit.PowerCycle),
			// crit.EnergyProfile,
			// float64(crit.PreferredNodeAffinity),
			// float64(crit.PrefererredInterPodAffinity),
			// float64(crit.PreferredInterPodAntiAffinity),
			// float64(crit.NumberOfRunningPods),

			expectedMatrix := []map[string]float64{
				{
					"PowerCycle":                    5.0,
					"EnergyProfile":                 1000.0,
					"PreferredNodeAffinity":         1.0,
					"PrefererredInterPodAffinity":   0.0,
					"PreferredInterPodAntiAffinity": 1.0,
					"NumberOfRunningPods":           3.0,
				},
				{
					"PowerCycle":                    10.0,
					"EnergyProfile":                 1500.0,
					"PreferredNodeAffinity":         0.0,
					"PrefererredInterPodAffinity":   5.0,
					"PreferredInterPodAntiAffinity": 0.0,
					"NumberOfRunningPods":           1.0,
				},
			}

			Expect(matrix).To(Equal(expectedMatrix))

		})

		It("should normalize the matrix - normalizeMatrix", func() {
			By("Creating sample matrix")
			matrix := sampleMatrix

			By("Normalizing the matrix")
			normalizedMatrix := controller.NormalizeMatrix(matrix)
			//normalize the matrix using linear normalization
			// normalized_value = Xij / sqrt(sum over i (Xij^2), where i is the alternative and j is the criterion))
			expectedNormalizedMatrix := normalizedSampleMatrix

			Expect(normalizedMatrix).To(Equal(expectedNormalizedMatrix))

		})

		It("should weight the normalized matrix - weightNormalizedMatrix", func() {
			By("Loading AHP weights from CM")
			weights := controller.AHPweights{ // energy profile focused weights
				PreferredNodeAffinity:         0.0083,
				PrefererredInterPodAffinity:   0.0180,
				PreferredInterPodAntiAffinity: 0.0180,
				EnergyProfile:                 0.7778,
				PowerCycle:                    0.1111,
				NumberOfRunningPods:           0.0668,
			}

			By("Creating sample normalized matrix")
			normalizedMatrix := normalizedSampleMatrix

			By("Weighting the normalized matrix")
			weightedMatrix := controller.WeightMatrix(normalizedMatrix, weights)
			expectedWeightedMatrix := weightedNormalizedSampleMatrixEnergyProfile

			Expect(weightedMatrix).To(Equal(expectedWeightedMatrix))

		})

		It("should determine ideal and negative-ideal solutions - determineIdealSolutions", func() {
			By("Creating sample weighted matrix")
			weightedMatrix := weightedNormalizedSampleMatrixEnergyProfile

			By("Determining ideal and negative-ideal solutions")
			idealSolution, negativeIdealSolution := controller.CalculateIdealSolutions(weightedMatrix) // TO DO: check the results

			expectedIdealSolution := map[string]float64{
				"PowerCycle":                    0.050,
				"EnergyProfile":                 0.647,
				"PreferredNodeAffinity":         0,
				"PrefererredInterPodAffinity":   0.006,
				"PreferredInterPodAntiAffinity": 0.005,
				"NumberOfRunningPods":           0.027,
			}

			expectedNegativeIdealSolution := map[string]float64{
				"PowerCycle":                    0.099,
				"EnergyProfile":                 0.432,
				"PreferredNodeAffinity":         0.008,
				"PrefererredInterPodAffinity":   0.017,
				"PreferredInterPodAntiAffinity": 0.017,
				"NumberOfRunningPods":           0.061,
			}

			Expect(idealSolution).To(Equal(expectedIdealSolution))
			Expect(negativeIdealSolution).To(Equal(expectedNegativeIdealSolution))
		})

		It("should calculate separation measures - calculateSeparationMeasures", func() {
			By("Creating sample weighted matrix")
			weightedMatrix := weightedNormalizedSampleMatrixEnergyProfile

			By("Creating ideal and negative-ideal solutions")
			idealSolution := expectedIdealSolution
			negativeIdealSolution := expectedNegativeIdealSolution

			By("Calculating separation measures")
			separationIdeal, separationNegativeIdeal := controller.CalculateSeparationMeasures(weightedMatrix, idealSolution, negativeIdealSolution)

			Expect(separationIdeal).To(Equal(expectedSeparationIdeal))
			Expect(separationNegativeIdeal).To(Equal(expectedSeparationNegativeIdeal))
		})

		It("should calculate the relative closeness to the ideal solution - calculateRelativeCloseness", func() {
			By("Creating separation measures")
			separationIdeal := expectedSeparationIdeal
			separationNegativeIdeal := expectedSeparationNegativeIdeal

			By("Calculating relative closeness")
			relativeCloseness := controller.CalculateRelativeCloseness(separationIdeal, separationNegativeIdeal)

			Expect(relativeCloseness).To(Equal(expectedRelativeCloseness))
		})

		It("should rank nodes based on relative closeness - rankNodes", func() {
			By("Creating relative closeness values")
			relativeCloseness := expectedRelativeCloseness
			rankNodes := []controller.RankedNode{}
			rankNodes = append(rankNodes, controller.RankedNode{Node: nodeLarge, RelativeCloseness: relativeCloseness[0]})
			rankNodes = append(rankNodes, controller.RankedNode{Node: nodeSmall, RelativeCloseness: relativeCloseness[1]})

			By("Sorting ranked nodes")
			sortedNodes := controller.SortNodesByCloseness(rankNodes)

			Expect(sortedNodes[0].Node.Name).To(Equal(nodeSmall.Name))
			Expect(sortedNodes[1].Node.Name).To(Equal(nodeLarge.Name))
		})

	})
})
