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

package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
)

// NodeHandlingReconciler reconciles a NodeHandling object
type NodeHandlingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func scaleUPCluster(kubeconfig string) {

}

func scaleDownCluster(ctx context.Context, dockerEnv string, debugEnv string, seletedNode string) bool {
	log := log.FromContext(ctx)

	var cfg *rest.Config
	var err error
	var clusterClient client.Client

	if dockerEnv == "true" { // GESTISCI IL CLUSTER IN DOCKER DEBUG
		home, err := os.UserHomeDir()
		if err != nil {
			log.Error(err, "Error getting user home directory")
			return false
		}

		kubeconfigPath := filepath.Join(home, ".kube", "kind")
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			log.Error(err, "Error creating kubeconfig")
			return false
		}
		// the maanged Cluster does not have the CAPI scheme registered, so we need to register it manually to handle its resources
		scheme := runtime.NewScheme()
		if err := v1beta1.AddToScheme(scheme); err != nil {
			log.Error(err, "Unable to register capi scheme")
			return false
		}
		clusterClient, err = client.New(cfg, client.Options{Scheme: scheme})

	} else {
		cfg, err = rest.InClusterConfig()
		if err != nil {
			log.Error(err, "Error creating in-cluster config")
			return false
		}

		clusterClient, err = client.New(cfg, client.Options{})
		if err != nil {
			log.Error(err, "Error creating cluster client")
			return false
		}
	}

	// get the cluster name
	clusterList := &v1beta1.ClusterList{}
	err = clusterClient.List(ctx, clusterList, &client.ListOptions{})
	if err != nil {
		log.Error(err, "Error listing clusters")
		return false
	}
	cluster := clusterList.Items[0]
	log.Info("Found cluster", "name", cluster.Name)

	//get the name of the machineDeployment to remove
	namespace := "default"
	var machineDeploymentName string

	mdList := &v1beta1.MachineDeploymentList{}
	err = clusterClient.List(ctx, mdList, &client.ListOptions{Namespace: ""})
	if err != nil {
		log.Error(err, "Error listing MachineDeployments")
		return false
	}

	for _, md := range mdList.Items {
		if strings.Contains(seletedNode, md.Name) {
			log.Info("Found MachineDeployment", "name", md.Name)
			machineDeploymentName = md.Name
			break
		}
	}

	md := &v1beta1.MachineDeployment{}
	err = clusterClient.Get(ctx, client.ObjectKey{Name: machineDeploymentName, Namespace: namespace}, md)
	if err != nil {
		log.Error(err, "Error getting MachineDeployment")
		return false
	}

	// get the topology of the cluster
	fmt.Println("----MachineDepName:", machineDeploymentName)
	newDeployment := []v1beta1.MachineDeploymentTopology{}
	for _, md := range cluster.Spec.Topology.Workers.MachineDeployments {
		fmt.Println("--MachineDeployment:", md.Name)

		if !strings.Contains(machineDeploymentName, md.Name) {
			newDeployment = append(newDeployment, md)
		}
	}
	cluster.Spec.Topology.Workers.MachineDeployments = newDeployment

	// update the cluster
	err = clusterClient.Update(ctx, &cluster)
	if err != nil {
		log.Error(err, "Error updating cluster")
		return false
	}
	return true
}

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandlings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandlings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandlings/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the NodeHandling object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *NodeHandlingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var nodeHandling clusterv1alpha1.NodeHandling
	if err := r.Get(ctx, req.NamespacedName, &nodeHandling); err != nil {
		log.Error(err, "unable to fetch NodeHandling")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("NodeHandling CRD found", "name", nodeHandling.Name)

	if nodeHandling.Status.Phase == "" {
		log.Info("NodeHandling CRD setting the initial status")
		nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseRunning
		if err := r.Status().Update(ctx, &nodeHandling); err != nil {
			log.Error(err, "unable to update NodeHandling status")
			return ctrl.Result{}, err
		}

		// set the owner reference to the NodeSelecting CRD
		var nodeSelectingList clusterv1alpha1.NodeSelectingList
		if err := r.Client.List(ctx, &nodeSelectingList); err != nil {
			log.Error(err, "unable to list NodeSelecting resources")
			return ctrl.Result{}, err
		}

		// find the NodeSelecting with the name in the spec
		var nodeSelecting *clusterv1alpha1.NodeSelecting
		for _, ns := range nodeSelectingList.Items {
			if ns.Name == nodeHandling.Spec.NodeSelectingName {
				nodeSelecting = &ns
				break
			}
		}
		if nodeSelecting == nil {
			log.Info("Unable to find the NodeSelecting parent resource", "name", nodeHandling.Spec.NodeSelectingName)
			nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
			if err := r.Status().Update(ctx, &nodeHandling); err != nil {
				log.Error(err, "unable to update the NodeHandling status")
			}
			return ctrl.Result{}, fmt.Errorf("unable to find the NodeSelecting parent resource")
		}

		if err := ctrl.SetControllerReference(nodeSelecting, &nodeHandling, r.Scheme); err != nil {
			log.Error(err, "unable to set owner reference on NodeHandling")
			return ctrl.Result{}, err
		}

		// Update the NodeHandling resource with the owner reference
		if err := r.Client.Update(ctx, &nodeHandling); err != nil {
			log.Error(err, "unable to update NodeHandling with owner reference")
			return ctrl.Result{}, err
		}

	}

	if nodeHandling.Status.Phase == clusterv1alpha1.NH_PhaseRunning {
		dockerEnv := os.Getenv("DREEM_DOCKER")
		debugEnv := os.Getenv("DREEM_DEBUG")

		if dockerEnv == "true" {
			if debugEnv == "true" {
				log.Info("DREEM_DOCKER and DREEM_DEBUG environment variables are set, using docker environment in debug")
			} else {
				log.Info("DREEM_DOCKER environment variable is set, using docker environment")
			}

		}

		if nodeHandling.Spec.ScalingLabel > 0 {
			log.Info("NodeHandling is asking the cluster to scale UP")
			//scaleUPCluster(ctx, dockerEnv, nodeHandling.Spec.SelectedNode)
		} else if nodeHandling.Spec.ScalingLabel < 0 {
			log.Info("NodeHandling is asking the cluster to scale DOWN")
			if scaleDownCluster(ctx, dockerEnv, debugEnv, nodeHandling.Spec.SelectedNode) {
				log.Info("Cluster scaled down successfully")
				nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseCompleted
				if err := r.Status().Update(ctx, &nodeHandling); err != nil {
					log.Error(err, "unable to update the NodeHandling status")
				}
				return ctrl.Result{}, nil
			} else {
				log.Info("Error scaling down the cluster")
				nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
				if err := r.Status().Update(ctx, &nodeHandling); err != nil {
					log.Error(err, "unable to update the NodeHandling status")
				}
				return ctrl.Result{}, fmt.Errorf("error scaling down the cluster")
			}
		}

	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeHandlingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1alpha1.NodeHandling{}).
		Named("nodehandling").
		Complete(r)
}
