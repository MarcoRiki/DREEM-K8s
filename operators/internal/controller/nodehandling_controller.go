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
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeHandlingReconciler reconciles a NodeHandling object
type NodeHandlingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func scaleUPCluster(ctx context.Context, r *NodeHandlingReconciler, requiredNodes int32) error {
	log := log.FromContext(ctx)

	// Ottieni il cluster
	clusterList := &v1beta1.ClusterList{}
	if err := r.Client.List(ctx, clusterList); err != nil {
		log.Error(err, "Error listing clusters")
		return err
	}
	if len(clusterList.Items) == 0 {
		return fmt.Errorf("no clusters found")
	}
	clusterName := clusterList.Items[0].Name
	clusterNamespace := clusterList.Items[0].Namespace
	log.Info("Found cluster", "name", clusterName)

	cluster := &v1beta1.Cluster{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: clusterName, Namespace: clusterNamespace}, cluster); err != nil {
		log.Error(err, "Error getting Cluster")
		return err
	}

	// // Check for existing scale-in-progress annotation
	// if cluster.Annotations != nil && cluster.Annotations["dreemk8s/scale-in-progress"] == "true" {
	// 	log.Info("Scale-up already in progress, skipping")
	// 	return nil
	// }

	// // Set annotation to signal scale-up in progress
	// if cluster.Annotations == nil {
	// 	cluster.Annotations = make(map[string]string)
	// }
	// cluster.Annotations["dreemk8s/scale-in-progress"] = "true"

	// Update cluster with annotation
	if err := r.Client.Update(ctx, cluster); err != nil {
		log.Error(err, "Failed to update cluster with scale-in-progress annotation")
		return err
	}

	var newMdName string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Ricarica SEMPRE una nuova copia del cluster all'interno del retry
		machineDeploymentList := &v1beta1.MachineDeploymentList{}
		if err := r.Client.List(ctx, machineDeploymentList, &client.ListOptions{Namespace: clusterNamespace}); err != nil {
			log.Error(err, "Error listing MachineDeployments")
		}

		// Calcola un nuovo nome disponibile
		existingNames := make(map[string]bool)
		prefix := strings.Split(machineDeploymentList.Items[0].Name, "-")[0]

		for _, md := range machineDeploymentList.Items {
			existingNames[md.Name] = true
		}

		for i := 0; ; i++ {
			candidate := fmt.Sprintf("%s-%d", prefix, i)
			if !existingNames[candidate] {
				newMdName = candidate
				break
			}
		}
		log.Info("Creating new MachineDeployment", "name", newMdName)

		// Aggiunge il nuovo MachineDeployment
		basicMd := &v1beta1.MachineDeployment{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "cluster.x-k8s.io/v1beta1",
				Kind:       "MachineDeployment",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      newMdName,
				Namespace: "default",
			},
			Spec: v1beta1.MachineDeploymentSpec{
				ClusterName: "dreem-mmiracapillo-cluster",
				Replicas:    pointerTo(int32(1)),
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{}, // Non può essere null in Go, mappa vuota è valida
				},
				Template: v1beta1.MachineTemplateSpec{
					Spec: v1beta1.MachineSpec{
						ClusterName: "dreem-mmiracapillo-cluster",
						Version:     pointerTo("v1.32.0"),
						Bootstrap: v1beta1.Bootstrap{
							ConfigRef: &corev1.ObjectReference{
								APIVersion: "bootstrap.cluster.x-k8s.io/v1alpha3",
								Kind:       "TalosConfigTemplate",
								Name:       "talosconfig-workers",
							},
						},
						InfrastructureRef: corev1.ObjectReference{
							APIVersion: "infrastructure.cluster.x-k8s.io/v1alpha1",
							Kind:       "ProxmoxMachineTemplate",
							Name:       "worker-template",
						},
					},
				},
			},
		}

		// check if the number of MachineDeployments has not already been reached
		if len(machineDeploymentList.Items) >= int(requiredNodes) {
			log.Info("new MachineDeployments already up, skipping creation", "requiredNodes", requiredNodes)
			return nil
		} else {
			// crea il nuovo MachineDeployment
			if err := r.Client.Create(ctx, basicMd); err != nil {
				log.Error(err, "Error creating MachineDeployment")
			}

		}

		return nil
	})

	if err != nil {
		return err
	}

	// Attendi che il nuovo MachineDeployment venga effettivamente creato
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	pollInterval := 10 * time.Second
	//expectedSubstring := fmt.Sprintf("%s-%s", clusterName, newMdName)

	err = wait.PollUntilContextCancel(waitCtx, pollInterval, true, func(ctx context.Context) (bool, error) {
		mdList := &v1beta1.MachineDeploymentList{}
		err := r.Client.List(ctx, mdList, &client.ListOptions{Namespace: clusterNamespace})
		if err != nil {
			log.Error(err, "Error listing MachineDeployments")
			return false, err
		}

		for _, md := range mdList.Items {
			if strings.Contains(md.Name, newMdName) {
				if md.Status.Replicas == 1 {
					log.Info("MachineDeployment created successfully", "name", md.Name)

					// Rimuovi l'annotazione di scale-in-progress
					err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
						cluster := &v1beta1.Cluster{}
						if err := r.Client.Get(ctx, client.ObjectKey{Name: clusterName, Namespace: clusterNamespace}, cluster); err != nil {
							return err
						}

						// if cluster.Annotations != nil {
						// 	delete(cluster.Annotations, "dreemk8s/scale-in-progress")
						// }

						return r.Client.Update(ctx, cluster)
					})
					if err != nil {
						log.Error(err, "Failed to remove scale-in-progress annotation")

					}

					return true, nil
				}

			}
		}

		log.Info("Waiting for MachineDeployment with substring", "substring", newMdName)
		return false, nil
	})
	if err != nil {
		log.Error(err, "Timed out waiting for MachineDeployment creation")
		return err
	}

	return nil
}
func pointerTo[T any](v T) *T {
	return &v
}

func scaleUPClusterDocker(ctx context.Context, r *NodeHandlingReconciler) error {
	log := log.FromContext(ctx)

	// Ottieni il cluster
	clusterList := &v1beta1.ClusterList{}
	if err := r.Client.List(ctx, clusterList); err != nil {
		log.Error(err, "Error listing clusters")
		return err
	}
	if len(clusterList.Items) == 0 {
		return fmt.Errorf("no clusters found")
	}
	clusterName := clusterList.Items[0].Name
	clusterNamespace := clusterList.Items[0].Namespace
	log.Info("Found cluster", "name", clusterName)

	cluster := &v1beta1.Cluster{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: clusterName, Namespace: clusterNamespace}, cluster); err != nil {
		log.Error(err, "Error getting Cluster")
		return err
	}

	// Check for existing scale-in-progress annotation
	if cluster.Annotations != nil && cluster.Annotations["dreemk8s/scale-in-progress"] == "true" {
		log.Info("Scale-up already in progress, skipping")
		return nil
	}

	// Set annotation to signal scale-up in progress
	if cluster.Annotations == nil {
		cluster.Annotations = make(map[string]string)
	}
	cluster.Annotations["dreemk8s/scale-in-progress"] = "true"

	// Update cluster with annotation
	if err := r.Client.Update(ctx, cluster); err != nil {
		log.Error(err, "Failed to update cluster with scale-in-progress annotation")
		return err
	}

	var newMdName string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Ricarica SEMPRE una nuova copia del cluster all'interno del retry
		cluster := &v1beta1.Cluster{}
		if err := r.Client.Get(ctx, client.ObjectKey{Name: clusterName, Namespace: clusterNamespace}, cluster); err != nil {
			log.Error(err, "Failed to get latest version of Cluster")
			return err
		}

		// Calcola un nuovo nome disponibile
		existingNames := make(map[string]bool)
		prefix := strings.Split(cluster.Spec.Topology.Workers.MachineDeployments[0].Name, "-")[0]

		for _, md := range cluster.Spec.Topology.Workers.MachineDeployments {
			existingNames[md.Name] = true
		}

		for i := 0; ; i++ {
			candidate := fmt.Sprintf("%s-%d", prefix, i)
			if !existingNames[candidate] {
				newMdName = candidate
				break
			}
		}

		// Aggiunge il nuovo MachineDeployment
		cluster.Spec.Topology.Workers.MachineDeployments = append(
			cluster.Spec.Topology.Workers.MachineDeployments,
			v1beta1.MachineDeploymentTopology{
				Name:     newMdName,
				Class:    "default-worker",
				Replicas: pointer.Int32(1),
			},
		)

		// Prova ad aggiornare
		if err := r.Client.Update(ctx, cluster); err != nil {
			log.Info("Conflict during cluster update, retrying...", "err", err)
			return err // verrà ritentato automaticamente
		}

		return nil
	})

	if err != nil {
		return err
	}

	// Attendi che il nuovo MachineDeployment venga effettivamente creato
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	pollInterval := 10 * time.Second
	expectedSubstring := fmt.Sprintf("%s-%s-", clusterName, newMdName)

	err = wait.PollUntilContextCancel(waitCtx, pollInterval, true, func(ctx context.Context) (bool, error) {
		mdList := &v1beta1.MachineDeploymentList{}
		err := r.Client.List(ctx, mdList, &client.ListOptions{Namespace: clusterNamespace})
		if err != nil {
			log.Error(err, "Error listing MachineDeployments")
			return false, err
		}

		for _, md := range mdList.Items {
			if strings.Contains(md.Name, expectedSubstring) {
				if md.Status.ReadyReplicas == 1 {
					log.Info("MachineDeployment created successfully", "name", md.Name)

					// Rimuovi l'annotazione di scale-in-progress
					err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
						cluster := &v1beta1.Cluster{}
						if err := r.Client.Get(ctx, client.ObjectKey{Name: clusterName, Namespace: clusterNamespace}, cluster); err != nil {
							return err
						}

						if cluster.Annotations != nil {
							delete(cluster.Annotations, "dreemk8s/scale-in-progress")
						}

						return r.Client.Update(ctx, cluster)
					})
					if err != nil {
						log.Error(err, "Failed to remove scale-in-progress annotation")

					}

					return true, nil
				}

			}
		}

		log.Info("Waiting for MachineDeployment with substring", "substring", expectedSubstring)
		return false, nil
	})
	if err != nil {
		log.Error(err, "Timed out waiting for MachineDeployment creation")
		return err
	}

	return nil
}

func scaleDownCluster(ctx context.Context, selectedNode string, r *NodeHandlingReconciler) error {
	log := log.FromContext(ctx)

	// Get the cluster name
	clusterList := &v1beta1.ClusterList{}
	if err := r.Client.List(ctx, clusterList); err != nil {
		log.Error(err, "Error listing clusters")
		return err
	}
	if len(clusterList.Items) == 0 {
		return fmt.Errorf("no clusters found")
	}
	clusterName := clusterList.Items[0].Name
	clusterNamespace := clusterList.Items[0].Namespace
	log.Info("Found cluster", "name", clusterName)

	cluster := &v1beta1.Cluster{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: clusterName, Namespace: clusterNamespace}, cluster); err != nil {
		log.Error(err, "Error getting Cluster")
		return err
	}

	// Find MachineDeployment
	mdList := &v1beta1.MachineDeploymentList{}
	if err := r.Client.List(ctx, mdList); err != nil {
		log.Error(err, "Error listing MachineDeployments")
		return err
	}

	var machineDeploymentName string
	for _, md := range mdList.Items {
		if strings.Contains(selectedNode, md.Name) {
			log.Info("Found MachineDeployment", "name", md.Name)
			machineDeploymentName = md.Name
			break
		}
	}
	if machineDeploymentName == "" {
		return fmt.Errorf("no MachineDeployment matched with selected node")
	}

	// Riprova in caso di conflict
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cluster := &v1beta1.Cluster{}
		if err := r.Client.Get(ctx, client.ObjectKey{Name: clusterName, Namespace: clusterNamespace}, cluster); err != nil {
			log.Error(err, "Error getting Cluster")
			return err
		}

		// Aggiorna Topology rimuovendo il MachineDeployment
		mdToRemove := v1beta1.MachineDeployment{}
		if err := r.Client.Get(ctx, client.ObjectKey{Name: machineDeploymentName, Namespace: clusterNamespace}, &mdToRemove); err != nil {
			log.Error(err, "Error getting MachineDeployment")
			return err
		}

		if err := r.Client.Delete(ctx, &mdToRemove); err != nil {
			log.Error(err, "Error deleting MachineDeployment")
			return err
		}

		return nil
	})
	if err != nil {
		return err
	}

	// Attendi che il MachineDeployment venga effettivamente rimosso
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	pollInterval := 10 * time.Second
	err = wait.PollUntilContextCancel(waitCtx, pollInterval, true, func(ctx context.Context) (bool, error) {
		md := &v1beta1.MachineDeployment{}
		err := r.Client.Get(ctx, client.ObjectKey{Name: machineDeploymentName, Namespace: clusterNamespace}, md)
		if err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("MachineDeployment successfully removed", "name", machineDeploymentName)

				// Rimuovi l'annotazione di scale-in-progress
				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					cluster := &v1beta1.Cluster{}
					if err := r.Client.Get(ctx, client.ObjectKey{Name: clusterName, Namespace: clusterNamespace}, cluster); err != nil {
						return err
					}

					if cluster.Annotations != nil {
						delete(cluster.Annotations, "dreemk8s/scale-in-progress")
					}

					return r.Client.Update(ctx, cluster)
				})
				return true, nil
			}
			return false, err
		}
		log.Info("Waiting for MachineDeployment to be removed", "name", machineDeploymentName)
		return false, nil
	})
	if err != nil {
		log.Error(err, "Timed out waiting for MachineDeployment removal")
		return err
	}

	return nil
}

func scaleDownClusterDocker(ctx context.Context, selectedNode string, r *NodeHandlingReconciler) error {
	log := log.FromContext(ctx)

	// Get the cluster name
	clusterList := &v1beta1.ClusterList{}
	if err := r.Client.List(ctx, clusterList); err != nil {
		log.Error(err, "Error listing clusters")
		return err
	}
	if len(clusterList.Items) == 0 {
		return fmt.Errorf("no clusters found")
	}
	clusterName := clusterList.Items[0].Name
	clusterNamespace := clusterList.Items[0].Namespace
	log.Info("Found cluster", "name", clusterName)

	cluster := &v1beta1.Cluster{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: clusterName, Namespace: clusterNamespace}, cluster); err != nil {
		log.Error(err, "Error getting Cluster")
		return err
	}

	// Check for existing scale-in-progress annotation
	if cluster.Annotations != nil && cluster.Annotations["dreemk8s/scale-in-progress"] == "true" {
		log.Info("Scale-up already in progress, skipping")
		return nil
	}

	// Set annotation to signal scale-up in progress
	if cluster.Annotations == nil {
		cluster.Annotations = make(map[string]string)
	}
	cluster.Annotations["dreemk8s/scale-in-progress"] = "true"

	// Update cluster with annotation
	if err := r.Client.Update(ctx, cluster); err != nil {
		log.Error(err, "Failed to update cluster with scale-in-progress annotation")
		return err
	}

	// Find MachineDeployment
	mdList := &v1beta1.MachineDeploymentList{}
	if err := r.Client.List(ctx, mdList); err != nil {
		log.Error(err, "Error listing MachineDeployments")
		return err
	}

	var machineDeploymentName string
	for _, md := range mdList.Items {
		if strings.Contains(selectedNode, md.Name) {
			log.Info("Found MachineDeployment", "name", md.Name)
			machineDeploymentName = md.Name
			break
		}
	}
	if machineDeploymentName == "" {
		return fmt.Errorf("no MachineDeployment matched with selected node")
	}

	// Riprova in caso di conflict
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cluster := &v1beta1.Cluster{}
		if err := r.Client.Get(ctx, client.ObjectKey{Name: clusterName, Namespace: clusterNamespace}, cluster); err != nil {
			log.Error(err, "Error getting Cluster")
			return err
		}

		// Aggiorna Topology rimuovendo il MachineDeployment
		newDeployment := []v1beta1.MachineDeploymentTopology{}
		for _, md := range cluster.Spec.Topology.Workers.MachineDeployments {
			if !strings.Contains(machineDeploymentName, md.Name) {
				newDeployment = append(newDeployment, md)
			}
		}
		cluster.Spec.Topology.Workers.MachineDeployments = newDeployment

		if err := r.Client.Update(ctx, cluster); err != nil {
			log.Error(err, "Error updating cluster")
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Attendi che il MachineDeployment venga effettivamente rimosso
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	pollInterval := 10 * time.Second
	err = wait.PollUntilContextCancel(waitCtx, pollInterval, true, func(ctx context.Context) (bool, error) {
		md := &v1beta1.MachineDeployment{}
		err := r.Client.Get(ctx, client.ObjectKey{Name: machineDeploymentName, Namespace: clusterNamespace}, md)
		if err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("MachineDeployment successfully removed", "name", machineDeploymentName)

				// Rimuovi l'annotazione di scale-in-progress
				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					cluster := &v1beta1.Cluster{}
					if err := r.Client.Get(ctx, client.ObjectKey{Name: clusterName, Namespace: clusterNamespace}, cluster); err != nil {
						return err
					}

					if cluster.Annotations != nil {
						delete(cluster.Annotations, "dreemk8s/scale-in-progress")
					}

					return r.Client.Update(ctx, cluster)
				})
				return true, nil
			}
			return false, err
		}
		log.Info("Waiting for MachineDeployment to be removed", "name", machineDeploymentName)
		return false, nil
	})
	if err != nil {
		log.Error(err, "Timed out waiting for MachineDeployment removal")
		return err
	}

	return nil
}

func getAssociatedClusterConfiguration(ctx context.Context, nodeHandling *clusterv1alpha1.NodeHandling, r *NodeHandlingReconciler) (*clusterv1alpha1.ClusterConfiguration, error) {
	log := log.FromContext(ctx)
	clusterConfiguration := &clusterv1alpha1.ClusterConfiguration{}
	if err := r.Client.Get(ctx, client.ObjectKey{
		Name:      nodeHandling.Spec.ClusterConfigurationName,
		Namespace: nodeHandling.Namespace,
	}, clusterConfiguration); err != nil {
		log.Error(err, "unable to find ClusterConfiguration parent resource for NodeHandling")
		return nil, err
	}

	return clusterConfiguration, nil

}

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandlings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandlings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandlings/finalizers,verbs=update

// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments,verbs=list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments/status,verbs=get;update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch;update

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
		clusterConfiguration, err := getAssociatedClusterConfiguration(ctx, nodeHandling, r)
		if err != nil {
			log.Error(err, "unable to find ClusterConfiguration parent resource for NodeHandling")
			nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
			if err := r.Status().Update(ctx, nodeHandling); err != nil {
				log.Error(err, "unable to update the NodeHandling status")
			}
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		// check if another NodeHandling exists with the same ClusterConfiguration
		nodeHandlingList := &clusterv1alpha1.NodeHandlingList{}
		if err := r.Client.List(ctx, nodeHandlingList, client.InNamespace(nodeHandling.Namespace)); err != nil {
			log.Error(err, "unable to list NodeHandling")
			return ctrl.Result{}, err
		}
		for _, nh := range nodeHandlingList.Items {
			if nh.Name != nodeHandling.Name && nh.Spec.ClusterConfigurationName == nodeHandling.Spec.ClusterConfigurationName {
				log.Info("Another NodeHandling CRD already exists for this ClusterConfiguration")
				nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
				if err := r.Status().Update(ctx, nodeHandling); err != nil {
					log.Error(err, "unable to update the NodeHandling status")
				}
				return ctrl.Result{}, fmt.Errorf("another NodeHandling CRD already exists for this ClusterConfiguration")
			}
		}

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

		if nodeHandling.Spec.ScalingLabel > 0 {
			log.Info("NodeHandling is asking the cluster to scale UP")
			clusterconfig, err := getAssociatedClusterConfiguration(ctx, nodeHandling, r)
			if err != nil {
				log.Error(fmt.Errorf("unable to find ClusterConfiguration parent resource for NodeHandling"), "ClusterConfiguration not found")
			}
			errorScaleUp := scaleUPCluster(ctx, r, clusterconfig.Spec.RequiredNodes)
			if errorScaleUp == nil {

				nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseCompleted
				if err := r.Status().Update(ctx, nodeHandling); err != nil {
					log.Error(err, "unable to update the NodeHandling status")
					return ctrl.Result{}, err
				}

				log.Info("Cluster scaled up successfully")
				return ctrl.Result{}, nil
			} else {
				log.Error(errorScaleUp, "ScaleUPCluster function failed")
				nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
				if err := r.Status().Update(ctx, nodeHandling); err != nil {
					log.Error(err, "unable to update the NodeHandling status")
				}
				return ctrl.Result{}, fmt.Errorf("error scaling up the cluster")
			}
		} else if nodeHandling.Spec.ScalingLabel < 0 {
			log.Info("NodeHandling is asking the cluster to scale DOWN")
			errorScaleDown := scaleDownCluster(ctx, nodeHandling.Spec.SelectedNode, r)
			if errorScaleDown == nil {

				nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseCompleted
				if err := r.Status().Update(ctx, nodeHandling); err != nil {
					log.Error(err, "unable to update the NodeHandling status")
					return ctrl.Result{}, err
				}
				log.Info("Cluster scaled down successfully")
				return ctrl.Result{}, nil
			} else {
				log.Error(errorScaleDown, "ScaleDownCluster function failed")
				nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
				if err := r.Status().Update(ctx, nodeHandling); err != nil {
					log.Error(err, "unable to update the NodeHandling status")
				}
				return ctrl.Result{}, fmt.Errorf("error scaling down the cluster")
			}
		}

	}

	if nodeHandling.Status.Phase == clusterv1alpha1.NH_PhaseCompleted || nodeHandling.Status.Phase == clusterv1alpha1.NH_PhaseFailed {
		clusterConfiguration, err := getAssociatedClusterConfiguration(ctx, nodeHandling, r)
		if err != nil {
			log.Error(err, "unable to find ClusterConfiguration parent resource for NodeHandling")
			nodeHandling.Status.Phase = clusterv1alpha1.NH_PhaseFailed
			if err := r.Status().Update(ctx, nodeHandling); err != nil {
				log.Error(err, "unable to update the NodeHandling status")
			}
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		patch := client.MergeFrom(clusterConfiguration.DeepCopy())
		if clusterConfiguration.Annotations == nil {
			clusterConfiguration.Annotations = map[string]string{}
		}
		clusterConfiguration.Annotations["cluster.dreemk8s/clusterconfiguration_trigger"] = "NodeHandling"
		_ = r.Patch(ctx, clusterConfiguration, patch)

		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeHandlingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1alpha1.NodeHandling{}).
		Named("nodehandling").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		Complete(r)
}
