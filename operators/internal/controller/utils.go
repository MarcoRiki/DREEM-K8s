package controller

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"time"

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

		//numberOfWorkerNodes += md.Status.ReadyReplicas
		numberOfWorkerNodes += md.Status.Replicas
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

func AppendToCSV(filePath string, Replicas int) error {
	// Apri il file in modalità append e lettura
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("errore apertura file: %w", err)
	}
	defer file.Close()

	// Crea un writer CSV
	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Crea il timestamp corrente in formato RFC3339
	timestamp := time.Now().Format(time.RFC3339)

	// Scrivi la nuova riga
	record := []string{timestamp, fmt.Sprintf("%d", Replicas)}
	if err := writer.Write(record); err != nil {
		return fmt.Errorf("errore scrittura record: %w", err)
	}

	return nil
}
