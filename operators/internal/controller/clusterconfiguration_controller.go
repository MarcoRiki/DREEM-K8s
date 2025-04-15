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
	"crypto/rand"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterConfigurationReconciler reconciles a ClusterConfiguration object
type ClusterConfigurationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=clusterconfigurations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=clusterconfigurations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=clusterconfigurations/finalizers,verbs=update

// retrieve the number of nodes K8s is using in the cluster
func (r *ClusterConfigurationReconciler) getClusterNodes(ctx context.Context) (int32, error) {
	var nodeList corev1.NodeList
	if err := r.Client.List(ctx, &nodeList); err != nil {
		log := log.FromContext(ctx)
		log.Error(err, "unable to list nodes in the cluster")
		return 0, err
	}
	return int32(len(nodeList.Items)), nil
}

func createNodeSelectingCRD(ctx context.Context, r *ClusterConfigurationReconciler, label int32, ClusterConfigurationName string) bool {
	log := log.FromContext(ctx)

	// timer of 10 seconds just for testing
	log.Info("Creating the NodeSelecting CRD")
	time.Sleep(10 * time.Second)

	// create unique identifier for the NodeSelecting CRD
	crdNameBytes := make([]byte, 8)
	if _, err := rand.Read(crdNameBytes); err != nil {
		log.Error(err, "unable to generate random name for NodeSelecting CRD")
		return false
	}
	crdName := "node-selecting-" + fmt.Sprintf("%x", crdNameBytes)

	var nodeSelecting = &clusterv1alpha1.NodeSelecting{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crdName,
			Namespace: "dreem",
		},
		Spec: clusterv1alpha1.NodeSelectingSpec{
			ClusterConfigurationName: ClusterConfigurationName,
			ScalingLabel:             label,
		},
	}

	if err := r.Client.Create(ctx, nodeSelecting); err != nil {
		log.Error(err, "unable to create NodeSelecting CRD")
		return false
	}
	log.Info("NodeSelecting CRD created", "name", nodeSelecting.Name)

	return true
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ClusterConfiguration object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *ClusterConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var clusterConfiguration clusterv1alpha1.ClusterConfiguration
	if err := r.Get(ctx, req.NamespacedName, &clusterConfiguration); err != nil {
		log.Error(err, "unable to fetch ClusterConfiguration")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("ClusterConfiguration instance found", "name", clusterConfiguration.Name)

	// set the initial status
	if clusterConfiguration.Status.Phase == "" {
		log.Info("Setting the initial status of ClusterConfiguration", "status", clusterConfiguration.Status)
		activeNodes, err := r.getClusterNodes(ctx)
		if err != nil {
			log.Error(err, "Error retrieving Nodes")
			return ctrl.Result{}, err
		}

		clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseStable
		clusterConfiguration.Status.ActiveNodes = activeNodes

		if err := r.Status().Update(ctx, &clusterConfiguration); err != nil {
			log.Error(err, "unable to update ClusterScaling status")
			return ctrl.Result{}, err
		}

	} else if clusterConfiguration.Status.Phase == clusterv1alpha1.CC_PhaseStable && clusterConfiguration.Status.ActiveNodes != clusterConfiguration.Spec.RequiredNodes {

		var scalingNodes int32 = 0                                                             // number of nodes to add or remove
		if clusterConfiguration.Spec.RequiredNodes > clusterConfiguration.Status.ActiveNodes { // SCALE UP
			maxScalableNodes := min(clusterConfiguration.Spec.RequiredNodes, clusterConfiguration.Spec.MaxNodes)
			nodeToAdd := maxScalableNodes - clusterConfiguration.Status.ActiveNodes // positive value

			if nodeToAdd != 0 {
				log.Info("Scaling up, number of nodes to add", "nodes", nodeToAdd)
				scalingNodes = nodeToAdd

			} else {
				log.Info("Scaling up not possible, reached maximum number of physical nodes")
				clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseCompleted

				if err := r.Status().Update(ctx, &clusterConfiguration); err != nil {
					log.Error(err, "unable to update ClusterScaling status")
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, nil

			}

		} else if clusterConfiguration.Spec.RequiredNodes < clusterConfiguration.Status.ActiveNodes { // SCALE DOWN
			minScalableNodes := max(clusterConfiguration.Spec.RequiredNodes, clusterConfiguration.Spec.MinNodes)
			nodeToRemove := minScalableNodes - clusterConfiguration.Status.ActiveNodes // negative value

			if nodeToRemove != 0 {
				log.Info("Scaling down, number of nodes to remove", "nodes", nodeToRemove)
				scalingNodes = nodeToRemove
			} else {
				log.Info("Scaling down not possible, reached minimum number of physical nodes")
				clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseCompleted
				if err := r.Status().Update(ctx, &clusterConfiguration); err != nil {
					log.Error(err, "unable to update ClusterScaling status")
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, nil
			}

		} else { // NO SCALE
			log.Info("No scaling required, the number of nodes is already correct")
			clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseCompleted

		}

		if scalingNodes != 0 {
			if createNodeSelectingCRD(ctx, r, scalingNodes, clusterConfiguration.Name) {

				clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseSelecting
			} else {
				log.Info("Error creating the NodeSelecting CRD")
				clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseFailed

			}

			if err := r.Status().Update(ctx, &clusterConfiguration); err != nil {
				log.Error(err, "unable to update ClusterScaling status")
				return ctrl.Result{}, err
			}
		} else {
			log.Info("Number of nodes to scale is not valid")
			clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseFailed
			if err := r.Status().Update(ctx, &clusterConfiguration); err != nil {
				log.Error(err, "unable to update ClusterScaling status")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}

	}
	var nodeSelectingChild clusterv1alpha1.NodeSelecting
	if clusterConfiguration.Status.Phase == clusterv1alpha1.CC_PhaseSelecting {
		var nodeSelectingList clusterv1alpha1.NodeSelectingList
		//Check if there is a NodeSelecting resource associated with the ClusterConfiguration
		if err := r.Client.List(ctx, &nodeSelectingList, client.InNamespace(req.Namespace)); err != nil {
			log.Error(err, "unable to list NodeSelecting resources")
			return ctrl.Result{}, err
		}
		//var nodeSelectingChild clusterv1alpha1.NodeSelecting
		for _, nodeSelecting := range nodeSelectingList.Items {
			for _, ownerRef := range nodeSelecting.OwnerReferences {
				if ownerRef.Kind == "ClusterConfiguration" && ownerRef.Name == clusterConfiguration.Name {

					nodeSelectingChild = nodeSelecting
					if nodeSelecting.Status.Phase == clusterv1alpha1.NS_PhaseComplete {
						log.Info("NodeSelecting resource in Completed Phase found", "name", nodeSelecting.Name)

						clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseSwitching

						if err := r.Status().Update(ctx, &clusterConfiguration); err != nil {
							log.Error(err, "unable to update ClusterConfiguration status")
							return ctrl.Result{}, err
						}
						log.Info("ClusterConfiguration status updated to Switching", "name", nodeSelecting.Name)

					} else if nodeSelecting.Status.Phase == clusterv1alpha1.NS_PhaseFailed {
						log.Info("NodeSelecting resource in Failed Phase found", "name", nodeSelecting.Name)
						clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseFailed

						if err := r.Status().Update(ctx, &clusterConfiguration); err != nil {
							log.Error(err, "unable to update ClusterConfiguration status")
							return ctrl.Result{}, err
						}
						log.Info("ClusterConfiguration status updated to Failed due to NodeSelecting Failing", "name", nodeSelecting.Name)

					}

					break
				}
			}
		}

	}

	if clusterConfiguration.Status.Phase == clusterv1alpha1.CC_PhaseSwitching {
		// check if there is a NodeHandling resource associated with the NodeSelecting in a Completed phase
		var nodeHandlingList clusterv1alpha1.NodeHandlingList
		if err := r.Client.List(ctx, &nodeHandlingList, client.InNamespace(req.Namespace)); err != nil {
			log.Error(err, "unable to list NodeHandling resources")
			return ctrl.Result{}, err
		}
		for _, nodeHandling := range nodeHandlingList.Items {
			for _, ownerRef := range nodeHandling.OwnerReferences {
				if ownerRef.Kind == "NodeSelecting" && ownerRef.Name == nodeSelectingChild.Name {

					if nodeHandling.Status.Phase == clusterv1alpha1.NH_PhaseCompleted {
						log.Info("NodeHandling resource in Completed Phase found", "name", nodeHandling.Name)
						clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseCompleted

						if err := r.Status().Update(ctx, &clusterConfiguration); err != nil {
							log.Error(err, "unable to update ClusterConfiguration status")
							return ctrl.Result{}, err
						}
						log.Info("ClusterConfiguration status updated to Completed", "name", nodeHandling.Name)
					} else if nodeHandling.Status.Phase == clusterv1alpha1.NH_PhaseFailed {
						log.Info("NodeHandling resource in Failed Phase found", "name", nodeHandling.Name)
						clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseFailed

						if err := r.Status().Update(ctx, &clusterConfiguration); err != nil {
							log.Error(err, "unable to update ClusterConfiguration status")
							return ctrl.Result{}, err
						}
						log.Info("ClusterConfiguration status updated to Failed due to NodeHandling Failing", "name", nodeHandling.Name)

					}
					break
				}
			}
		}
	}

	// update status and delete the NodeSelecting (and NodeHandling) resource
	if clusterConfiguration.Status.Phase == clusterv1alpha1.CC_PhaseCompleted {
		if clusterConfiguration.Status.ActiveNodes != clusterConfiguration.Spec.RequiredNodes {
			clusterConfiguration.Status.ActiveNodes = clusterConfiguration.Spec.RequiredNodes
			if err := r.Status().Update(ctx, &clusterConfiguration); err != nil {
				log.Error(err, "unable to update ClusterConfiguration status")
				return ctrl.Result{}, err
			}
		}

		/*
			if err := r.Client.Delete(ctx, &nodeSelecting); err != nil {
				log.Error(err, "unable to delete NodeSelecting resource", "name", nodeSelecting.Name)
				return ctrl.Result{}, err
			}*/
	}

	//handle a max retry: delete the NodeSelecting and NodeHandling resources if they are in a Failed phase, reset the ClusterConfiguration status and update a max retry counter
	if clusterConfiguration.Status.Phase == clusterv1alpha1.CC_PhaseFailed {

	}

	//after the scaling operation with the node handling, the numebr of nodes is updated
	//clusterConfiguration.Status.ActiveNodes = clusterConfiguration.Status.RequiredNodes
	//clusterConfiguration.Status.LastUpdate = metav1.Now()

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1alpha1.ClusterConfiguration{}).
		Named("clusterconfiguration").
		Watches(
			&clusterv1alpha1.ClusterConfiguration{},
			handler.EnqueueRequestsFromMapFunc(
				func(ctx context.Context, obj client.Object) []reconcile.Request {
					_, ok := obj.(*clusterv1alpha1.ClusterConfiguration)
					if !ok {
						return nil
					}

					ClusterScalingList := &clusterv1alpha1.ClusterConfigurationList{}
					err := r.Client.List(ctx, ClusterScalingList)
					if err != nil {
						return nil
					}

					var requests []reconcile.Request
					for _, item := range ClusterScalingList.Items {

						requests = append(requests, reconcile.Request{
							NamespacedName: client.ObjectKey{
								Name:      item.Name,
								Namespace: item.Namespace,
							},
						})

					}
					return requests
				}),
			builder.WithPredicates(
				predicate.Funcs{
					CreateFunc: func(event event.CreateEvent) bool {

						return true
					},
					DeleteFunc: func(event event.DeleteEvent) bool {
						return true
					},
					UpdateFunc: func(event event.UpdateEvent) bool {
						return true
					},
					GenericFunc: func(event event.GenericEvent) bool {
						return false
					},
				}),
		).
		Owns(&clusterv1alpha1.NodeSelecting{}).
		Complete(r)
}
