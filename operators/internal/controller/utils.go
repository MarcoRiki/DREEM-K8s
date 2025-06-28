package controller

import (
	"context"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// number of worker nodes in the ClusterAPI managed cluster
func getNumberOfWorkerNodes(ctx context.Context, k8sClient client.Client) (int32, error) {

	machineDeploymentList := &clusterv1.MachineDeploymentList{}
	if err := k8sClient.List(ctx, machineDeploymentList); err != nil {
		return 0, err
	}

	var numberOfWorkerNodes int32
	for _, md := range machineDeploymentList.Items {

		numberOfWorkerNodes += md.Status.ReadyReplicas
	}
	return numberOfWorkerNodes, nil
}
