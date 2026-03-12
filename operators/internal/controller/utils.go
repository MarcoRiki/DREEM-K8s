package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	DREEM_CLUSTER_CONFIGURATION_CM_NAME = "cluster-configuration-parameters"
	DREEM_WEIGHTS_SCALE_DOWN_CM_NAME    = "selection-weights-scale-down"
	DREEM_WEIGHTS_SCALE_UP_CM_NAME      = "selection-weights-scale-up"
	DREEM_POWER_CYCLE_ANNOTATION        = "dreemk8s.io/power-cycle-count"
	DREEM_ENERGY_EFFICIENCY_ANNOTATION  = "dreemk8s.io/consumption-profile"
	CAPI_MACHINE_DEPLOYMENT_LABEL       = "cluster.x-k8s.io/deployment-name"
)

// number of worker nodes in the ClusterAPI managed cluster
func getNumberOfWorkerNodes(ctx context.Context, k8sClient client.Client) (int32, error) {

	machineDeploymentList := &clusterv1.MachineDeploymentList{}
	if err := k8sClient.List(ctx, machineDeploymentList); err != nil {
		return 0, err
	}

	var numberOfWorkerNodes int32
	for _, md := range machineDeploymentList.Items {

		//numberOfWorkerNodes += md.Status.ReadyReplicas
		numberOfWorkerNodes += md.Status.ReadyReplicas
	}
	return numberOfWorkerNodes, nil
}

// useful for logs during tests
func getDesiredNodes(ctx context.Context, k8sClient client.Client) (int32, error) {

	machineDeploymentList := &clusterv1.MachineDeploymentList{}
	if err := k8sClient.List(ctx, machineDeploymentList); err != nil {
		return 0, err
	}

	var numberOfWorkerNodes int32
	for _, md := range machineDeploymentList.Items {

		//numberOfWorkerNodes += md.Status.ReadyReplicas
		numberOfWorkerNodes += *&md.Status.UpdatedReplicas
	}
	return numberOfWorkerNodes, nil
}

// useful for logs during tests
func getReadyNodes(ctx context.Context, k8sClient client.Client) (int32, error) {

	machineDeploymentList := &clusterv1.MachineDeploymentList{}
	if err := k8sClient.List(ctx, machineDeploymentList); err != nil {
		return 0, err
	}

	var numberOfWorkerNodes int32
	for _, md := range machineDeploymentList.Items {

		//numberOfWorkerNodes += md.Status.ReadyReplicas
		numberOfWorkerNodes += *&md.Status.ReadyReplicas
	}
	return numberOfWorkerNodes, nil
}

func saveMatrixToJSON(filePath string, matrix []map[string]float64, nodes []string, nodeSelecting clusterv1alpha1.NodeSelecting, managementClusterClient client.Client, ctx context.Context) error {

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("errore apertura file: %w", err)
	}
	defer file.Close()

	data := make(map[string]map[string]float64)
	for i, nodeName := range nodes {
		data[nodeName] = matrix[i]
	}

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		return fmt.Errorf("errore scrittura JSON: %w", err)
	}
	// nodeSelecting.Status.NodesRanking = encodeToString(data)
	// err = managementClusterClient.Status().Update(ctx, &nodeSelecting)
	// if err != nil {
	// 	return fmt.Errorf("errore aggiornamento NodeSelecting con ranking: %w", err)
	// }

	return nil
}

func encodeToString(data map[string]map[string]float64) string {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	return string(jsonData)
}
