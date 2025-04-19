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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// NodeHandlingReconciler reconciles a NodeHandling object
type NodeHandlingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func scaleUPCluster(ctx context.Context, dockerEnv string, debugEnv string) bool {
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
	idNewNode := len(cluster.Spec.Topology.Workers.MachineDeployments)
	fmt.Println("---- New Node name md-", idNewNode)
	// add a new machineDeployment to the cluster
	newMachineDeployment := v1beta1.MachineDeploymentTopology{
		Name:     fmt.Sprintf("md-%d", idNewNode),
		Class:    "default-worker",
		Replicas: pointer.Int32(1),
	}
	// add the new machineDeployment to the cluster

	cluster.Spec.Topology.Workers.MachineDeployments = append(cluster.Spec.Topology.Workers.MachineDeployments, newMachineDeployment)
	// update the cluster
	err = clusterClient.Update(ctx, &cluster)
	if err != nil {
		log.Error(err, "Error updating cluster")
		return false
	}

	return true
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
	//fmt.Println("----MachineDepName:", machineDeploymentName)
	newDeployment := []v1beta1.MachineDeploymentTopology{}
	for _, md := range cluster.Spec.Topology.Workers.MachineDeployments {
		//fmt.Println("--MachineDeployment:", md.Name)

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

	nodeHandling := &clusterv1alpha1.NodeHandling{}
	if err := r.Get(ctx, req.NamespacedName, nodeHandling); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "unable to fetch NodeHandling")
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("NodeHandling CRD found", "name", nodeHandling.Name)

	if nodeHandling.Status.Phase == "" {
		log.Info("NodeHandling CRD setting the initial status")
		nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseRunning
		if err := r.Status().Update(ctx, nodeHandling); err != nil {
			log.Error(err, "unable to update NodeHandling status")
			return ctrl.Result{}, err
		}
		// get the clusterconfiguration resource from the name in the spec
		clusterConfiguration := &clusterv1alpha1.ClusterConfiguration{}
		if err := r.Client.Get(ctx, client.ObjectKey{
			Name:      nodeHandling.Spec.ClusterConfigurationName,
			Namespace: nodeHandling.Namespace,
		}, clusterConfiguration); err != nil {
			log.Error(err, "unable to find ClusterConfiguration parent resource for NodeSelecting")
			nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
			if err := r.Status().Update(ctx, nodeHandling); err != nil {
				log.Error(err, "unable to update the NodeSelecting status")
			}
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		//fmt.Println("ClusterConfiguration for NH:", "name", clusterConfiguration.Name)

		if err := ctrl.SetControllerReference(clusterConfiguration, nodeHandling, r.Scheme); err != nil {
			log.Error(err, "unable to set owner reference ClusterConfiguration on NodeHandling")
			return ctrl.Result{}, err
		}

		// Update the NodeHandling resource with the owner reference
		if err := r.Client.Update(ctx, nodeHandling); err != nil {
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
			if scaleUPCluster(ctx, dockerEnv, debugEnv) {
				var latest clusterv1alpha1.NodeHandling // CHIEDI STA ROBA
				if err := r.Get(ctx, types.NamespacedName{
					Name:      nodeHandling.Name,
					Namespace: nodeHandling.Namespace,
				}, &latest); err != nil {
					log.Error(err, "unable to re-fetch NodeHandling before status update")
					return ctrl.Result{}, err
				}
				latest.Status.Phase = clusterv1alpha1.NH_PhaseCompleted
				if err := r.Status().Update(ctx, &latest); err != nil {
					log.Error(err, "unable to update the NodeHandling status")
					return ctrl.Result{}, err
				}

				log.Info("Cluster scaled up successfully")
				return ctrl.Result{}, nil
			} else {
				log.Info("Error scaling up the cluster")
				nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
				if err := r.Status().Update(ctx, nodeHandling); err != nil {
					log.Error(err, "unable to update the NodeHandling status")
				}
				return ctrl.Result{}, fmt.Errorf("error scaling up the cluster")
			}
		} else if nodeHandling.Spec.ScalingLabel < 0 {
			log.Info("NodeHandling is asking the cluster to scale DOWN")
			if scaleDownCluster(ctx, dockerEnv, debugEnv, nodeHandling.Spec.SelectedNode) {
				var latest clusterv1alpha1.NodeHandling // CHIEDI STA ROBA
				if err := r.Get(ctx, types.NamespacedName{
					Name:      nodeHandling.Name,
					Namespace: nodeHandling.Namespace,
				}, &latest); err != nil {
					log.Error(err, "unable to re-fetch NodeHandling before status update")
					return ctrl.Result{}, err
				}
				latest.Status.Phase = clusterv1alpha1.NH_PhaseCompleted
				if err := r.Status().Update(ctx, &latest); err != nil {
					log.Error(err, "unable to update the NodeHandling status")
					return ctrl.Result{}, err
				}

				log.Info("Cluster scaled down successfully")
				return ctrl.Result{}, nil
			} else {
				log.Info("Error scaling down the cluster")
				nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
				if err := r.Status().Update(ctx, nodeHandling); err != nil {
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
