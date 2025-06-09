package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// number of worker nodes in the ClusterAPI managed cluster
func getNumberOfMachineDeployments(ctx context.Context, k8sClient client.Client) (int32, error) {

	return 0, nil
}
