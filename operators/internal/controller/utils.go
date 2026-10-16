package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	DREEM_CLUSTER_CONFIGURATION_CM_NAME = "cluster-configuration-parameters"
	DREEM_POWER_CYCLE_ANNOTATION        = "dreemk8s.io/power-cycle-count"
	DREEM_ENERGY_EFFICIENCY_ANNOTATION  = "dreemk8s.io/consumption-profile"
	TEMPLATE_REDFISH_ENDPOINT           = "https://%s/redfish/v1/Systems/%s/Actions/ComputerSystem.Reset"
)

// number of worker nodes in the cluster, excluding control plane nodes (nodes with label node-role.kubernetes.io/control-plane)
func getNumberOfWorkerNodes(ctx context.Context, k8sClient client.Client) (int32, error) {

	nodes := &corev1.NodeList{}
	if err := k8sClient.List(ctx, nodes); err != nil {
		return 0, err
	}

	var numberOfWorkerNodes int32
	for _, node := range nodes.Items {
		// skip control plane nodes
		if _, isControlPlane := node.Labels["node-role.kubernetes.io/control-plane"]; isControlPlane {
			continue
		}

		// count only if node is Ready
		isReady := false
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				isReady = true
				break
			}
		}
		if isReady {
			numberOfWorkerNodes++
		}
	}
	return numberOfWorkerNodes, nil

}
