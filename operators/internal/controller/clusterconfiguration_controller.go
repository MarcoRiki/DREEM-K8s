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
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	capi "sigs.k8s.io/cluster-api/api/v1beta1"
)

// ClusterConfigurationReconciler reconciles a ClusterConfiguration object
type ClusterConfigurationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

var max_retries = 5
var retryInterval = 10 * time.Second // seconds

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=clusterconfigurations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=clusterconfigurations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=clusterconfigurations/finalizers,verbs=update

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselecting,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselecting/status,verbs=get;update;patch

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandling,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodehandling/status,verbs=get;update;patch

// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments,verbs=list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments/status,verbs=get;update

// retrieve the number of MachineDeployment (aka nodes) which are available within the managed cluster
func (r *ClusterConfigurationReconciler) getClusterNodes(ctx context.Context) (int32, error) {
	var nodeList capi.MachineDeploymentList
	if err := r.Client.List(ctx, &nodeList); err != nil {
		log := log.FromContext(ctx)
		log.Error(err, "unable to list machineDeployments in the managed cluster")
		return 0, err
	}
	return int32(len(nodeList.Items)), nil
}

func (r *ClusterConfigurationReconciler) IncrementAnnotation(ctx context.Context, annotationString string) (string, error) {
	log := log.FromContext(ctx)
	log.Info("converting string to int annotation", "annotation", annotationString)

	annotationInt, err := strconv.Atoi(annotationString)
	if err != nil {
		log.Error(err, "failed converting string to int")
		return "0", fmt.Errorf("failed converting string to int: %w", err)
	}
	annotationInt++
	log.Info("incrementing annotation", "annotation", annotationInt)

	annotationString = strconv.Itoa(annotationInt)
	log.Info("converting int to string updated annotation", "annotation", annotationString)
	return annotationString, nil
}

// creates the NodeSelecting resource associated with the ClusterConfiguration
// the NodeSelecting resource is used to select the nodes to be added or removed
func createNodeSelectingCRD(ctx context.Context, r *ClusterConfigurationReconciler, label int32, ClusterConfigurationName string) error {
	log := log.FromContext(ctx)

	log.Info("Creating the NodeSelecting CRD")

	// create unique identifier for the NodeSelecting CRD
	crdNameBytes := make([]byte, 8)
	if _, err := rand.Read(crdNameBytes); err != nil {
		log.Error(err, "unable to generate random name for NodeSelecting CRD")
		return err
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
		return err
	}
	log.Info("NodeSelecting CRD created", "name", nodeSelecting.Name)

	return nil
}

// retrieve the ClusterConfiguration resource associated with the request
func getClusterConfiguration(ctx context.Context, r *ClusterConfigurationReconciler, req ctrl.Request) (*clusterv1alpha1.ClusterConfiguration, error) {
	log := log.FromContext(ctx)
	clusterConfiguration := &clusterv1alpha1.ClusterConfiguration{}
	if err := r.Get(ctx, req.NamespacedName, clusterConfiguration); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "unable to fetch ClusterConfiguration")
		}
		return nil, client.IgnoreNotFound(err)
	}
	return clusterConfiguration, nil
}

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *ClusterConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// check if there are clusterConfiguration resources not in a final state
	var clusterConfigurationList clusterv1alpha1.ClusterConfigurationList
	if err := r.Client.List(ctx, &clusterConfigurationList); err != nil {
		log.Error(err, "unable to list ClusterConfiguration resources")
		return ctrl.Result{}, err
	}
	if len(clusterConfigurationList.Items) == 0 {
		log.Info("No ClusterConfiguration resources found")
		return ctrl.Result{}, nil
	}
	// check if the ClusterConfiguration resource is not in a final state and it has an age greater than 1 hour

	for _, clusterConfiguration := range clusterConfigurationList.Items {
		if clusterConfiguration.Status.Phase != clusterv1alpha1.CC_PhaseCompleted && clusterConfiguration.Status.Phase != clusterv1alpha1.CC_PhaseAborted {
			if time.Since(clusterConfiguration.CreationTimestamp.Time) > 1*time.Hour {
				log.Info("ClusterConfiguration resource not yet in a final state", "name", clusterConfiguration.Name)
				clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseAborted
				if err := r.Status().Update(ctx, &clusterConfiguration); err != nil {
					log.Error(err, "unable to update ClusterConfiguration status")
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, nil
			}
		}
	}

	clusterConfiguration, err := getClusterConfiguration(ctx, r, req)
	if err != nil {
		return ctrl.Result{}, err
	}
	log.Info("ClusterConfiguration instance found", "name", clusterConfiguration.Name)

	triggerType := clusterConfiguration.Annotations["cluster.dreemk8s/clusterconfiguration_trigger"]
	if clusterConfiguration.Status.Phase == clusterv1alpha1.CC_PhaseSelecting || clusterConfiguration.Status.Phase == clusterv1alpha1.CC_PhaseSwitching {
		if triggerType != "NodeSelecting" && triggerType != "NodeHandling" {
			return ctrl.Result{}, nil
		}
	}

	switch clusterConfiguration.Status.Phase {
	case "": // initialize the status subresource
		log.Info("Setting the initial status of ClusterConfiguration", "status", clusterConfiguration.Status)
		activeNodes, err := r.getClusterNodes(ctx)
		if err != nil {
			log.Error(err, "Error retrieving Nodes to set the initial status")
			return ctrl.Result{}, err
		}
		clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseStable
		clusterConfiguration.Status.ActiveNodes = activeNodes

		if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
			log.Error(err, "unable to update ClusterScaling status")
			return ctrl.Result{}, err
		}
		break
	case clusterv1alpha1.CC_PhaseStable: // check if a scaling operation is required
		log.Info("ClusterConfiguration in Stable Phase", "name", clusterConfiguration.Name)
		var scalingNodes int32 = 0                                                             // number of nodes to add or remove
		if clusterConfiguration.Spec.RequiredNodes > clusterConfiguration.Status.ActiveNodes { // SCALE UP
			maxScalableNodes := min(clusterConfiguration.Spec.RequiredNodes, clusterConfiguration.Spec.MaxNodes)
			nodeToAdd := maxScalableNodes - clusterConfiguration.Status.ActiveNodes // positive value

			if nodeToAdd != 0 {
				log.Info("Scaling up, number of nodes to add", "nodes", nodeToAdd)
				scalingNodes = nodeToAdd

			} else {
				log.Info("Scaling up not possible, reached maximum number of physical nodes")
				if err != nil {
					return ctrl.Result{}, err
				}
				clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseAborted

				if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
					log.Error(err, "unable to update ClusterScaling status")
					return ctrl.Result{}, err
				}

			}

		} else if clusterConfiguration.Spec.RequiredNodes < clusterConfiguration.Status.ActiveNodes { // SCALE DOWN
			minScalableNodes := max(clusterConfiguration.Spec.RequiredNodes, clusterConfiguration.Spec.MinNodes)
			nodeToRemove := minScalableNodes - clusterConfiguration.Status.ActiveNodes // negative value

			if nodeToRemove != 0 {
				log.Info("Scaling down, number of nodes to remove", "nodes", nodeToRemove)
				scalingNodes = nodeToRemove
			} else {
				log.Info("Scaling down not possible, reached minimum number of physical nodes")

				if err != nil {
					return ctrl.Result{}, err
				}
				clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseAborted
				if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
					log.Error(err, "unable to update ClusterScaling status")
					return ctrl.Result{}, err
				}

			}

		} else { // NO SCALE
			log.Info("No scaling required, the number of nodes is already correct")

			clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseCompleted

			if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
				log.Error(err, "unable to update ClusterScaling status")
				return ctrl.Result{}, err
			}

		}

		if scalingNodes != 0 {
			err := createNodeSelectingCRD(ctx, r, scalingNodes, clusterConfiguration.Name)
			if err != nil {
				log.Error(err, "unable to create NodeSelecting CRD")
				return ctrl.Result{}, err
			}
			clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseSelecting

			if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
				log.Error(err, "unable to update ClusterConfiguration status")
				return ctrl.Result{}, err
			}
		} else if scalingNodes == 0 {
			log.Info("Number of nodes to scale is 0, update the status to Completed")
			clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseCompleted
			if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
				log.Error(err, "unable to update ClusterConfiguration status")
				return ctrl.Result{}, err
			}

		}
		// else {
		// 	log.Info("Number of nodes to scale is not valid")
		// 	clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseFailed
		// 	if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
		// 		log.Error(err, "unable to update ClusterConfiguration status")
		// 		return ctrl.Result{}, err
		// 	}

		// }

		break
	case clusterv1alpha1.CC_PhaseSelecting:
		//Check if there is a NodeSelecting resource associated with the ClusterConfiguration
		var nodeSelectingList clusterv1alpha1.NodeSelectingList
		if err := r.Client.List(ctx, &nodeSelectingList, client.InNamespace(req.Namespace)); err != nil {
			log.Error(err, "unable to list NodeSelecting resources")
			return ctrl.Result{}, err
		}

		for _, nodeSelecting := range nodeSelectingList.Items {
			for _, ownerRef := range nodeSelecting.OwnerReferences {
				if ownerRef.Kind == "ClusterConfiguration" && ownerRef.Name == clusterConfiguration.Name {

					if nodeSelecting.Status.Phase == clusterv1alpha1.NS_PhaseComplete {
						log.Info("NodeSelecting resource in Completed Phase found", "name", nodeSelecting.Name)

						if err != nil {
							return ctrl.Result{}, err
						}
						clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseSwitching

						if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
							log.Error(err, "unable to update ClusterConfiguration status")
							return ctrl.Result{}, err
						}
						log.Info("ClusterConfiguration status updated to Switching", "name", nodeSelecting.Name)

					} else if nodeSelecting.Status.Phase == clusterv1alpha1.NS_PhaseFailed {
						log.Info("NodeSelecting resource in Failed Phase found", "name", nodeSelecting.Name)

						if err != nil {
							return ctrl.Result{}, err
						}
						clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseFailed

						if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
							log.Error(err, "unable to update ClusterConfiguration status")
							return ctrl.Result{}, err
						}
						log.Info("ClusterConfiguration status updated to Failed due to NodeSelecting Failing", "name", nodeSelecting.Name)

					}

					break
				}
			}
		}
		patch := client.MergeFrom(clusterConfiguration.DeepCopy())

		annotations := clusterConfiguration.GetAnnotations()
		triggerType = clusterConfiguration.Annotations["cluster.dreemk8s/clusterconfiguration_trigger"]
		if annotations != nil && triggerType == "NodeSelecting" {
			delete(annotations, "cluster.dreemk8s/clusterconfiguration_trigger")
			clusterConfiguration.SetAnnotations(annotations)
		}

		_ = r.Patch(ctx, clusterConfiguration, patch)
		break
	case clusterv1alpha1.CC_PhaseSwitching:
		// check if there is a NodeHandling resource associated with the NodeSelecting in a Completed phase
		var nodeHandlingList clusterv1alpha1.NodeHandlingList
		if err := r.Client.List(ctx, &nodeHandlingList, client.InNamespace(req.Namespace)); err != nil {
			log.Error(err, "unable to list NodeHandling resources")
			return ctrl.Result{}, err
		}
		for _, nodeHandling := range nodeHandlingList.Items {
			for _, ownerRef := range nodeHandling.OwnerReferences {
				if ownerRef.Kind == "ClusterConfiguration" && ownerRef.Name == clusterConfiguration.Name {

					if nodeHandling.Status.Phase == clusterv1alpha1.NH_PhaseCompleted {
						log.Info("NodeHandling resource in Completed Phase found", "name", nodeHandling.Name)

						if err != nil {
							return ctrl.Result{}, err
						}
						clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseCompleted

						if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
							log.Error(err, "unable to update ClusterConfiguration status")
							return ctrl.Result{}, err
						}
						log.Info("ClusterConfiguration status updated to Completed", "name", nodeHandling.Name)
					} else if nodeHandling.Status.Phase == clusterv1alpha1.NH_PhaseFailed {
						log.Info("NodeHandling resource in Failed Phase found", "name", nodeHandling.Name)

						if err != nil {
							return ctrl.Result{}, err
						}
						clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseFailed

						if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
							log.Error(err, "unable to update ClusterConfiguration status")
							return ctrl.Result{}, err
						}
						log.Info("ClusterConfiguration status updated to Failed due to NodeHandling Failing", "name", nodeHandling.Name)

					}
					break
				}
			}
		}
		patch := client.MergeFrom(clusterConfiguration.DeepCopy())

		annotations := clusterConfiguration.GetAnnotations()
		triggerType = clusterConfiguration.Annotations["cluster.dreemk8s/clusterconfiguration_trigger"]
		if annotations != nil && triggerType == "NodeSelecting" {
			delete(annotations, "cluster.dreemk8s/clusterconfiguration_trigger")
			clusterConfiguration.SetAnnotations(annotations)
		}

		_ = r.Patch(ctx, clusterConfiguration, patch)

		break
	case clusterv1alpha1.CC_PhaseCompleted:
		if clusterConfiguration.Status.ActiveNodes != clusterConfiguration.Spec.RequiredNodes {

			updatedNumberOfNodes, err := r.getClusterNodes(ctx)
			if err != nil {
				log.Error(err, "Error retrieving updated number of Nodes")
			}

			if updatedNumberOfNodes == clusterConfiguration.Spec.RequiredNodes {
				clusterConfiguration.Status.ActiveNodes = updatedNumberOfNodes

				if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
					log.Error(err, "unable to update ClusterConfiguration status")
					return ctrl.Result{}, err
				}
			} else {
				log.Info("number of active nodes differs from the required number of nodes", "activeNodes", updatedNumberOfNodes, "requiredNodes", clusterConfiguration.Spec.RequiredNodes)
				clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseFailed
				if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
					log.Error(err, "unable to update ClusterConfiguration status")
					return ctrl.Result{}, err
				}
			}
		}
		break
	case clusterv1alpha1.CC_PhaseFailed:

		// numNodes, err := r.getClusterNodes(ctx)
		// if err != nil {
		// 	log.Error(err, "Error retrieving updated number of Nodes")
		// 	return ctrl.Result{}, err
		// }
		// if numNodes == clusterConfiguration.Spec.RequiredNodes {
		// 	clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseCompleted
		// 	if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
		// 		log.Error(err, "unable to update ClusterConfiguration status")
		// 		return ctrl.Result{}, err
		// 	}
		// }

		// // set an annotation to keep track of the number of retries
		// if clusterConfiguration.Annotations == nil {
		// 	clusterConfiguration.Annotations = make(map[string]string)
		// }
		// patch := client.MergeFrom(clusterConfiguration.DeepCopy())
		// if _, ok := clusterConfiguration.Annotations["cluster.dreemk8s/retryCount"]; !ok {

		// 	log.Info("Adding retryCount annotation to ClusterConfiguration")
		// 	clusterConfiguration.Annotations["cluster.dreemk8s/retryCount"] = "1"
		// 	clusterConfiguration.Annotations["cluster.dreemk8s/LastRetryTimestamp"] = time.Now().Format(time.RFC3339)
		// 	if err := r.Update(ctx, clusterConfiguration); err != nil {
		// 		log.Error(err, "unable to update ClusterConfiguration with retryCount annotation")
		// 		return ctrl.Result{}, err
		// 	}
		// 	return ctrl.Result{RequeueAfter: retryInterval}, nil

		// } else {

		// 	// // delete the associated NodeSelecting and NodeHandling resources
		// 	// var nodeSelectingList clusterv1alpha1.NodeSelectingList
		// 	// if err := r.Client.List(ctx, &nodeSelectingList, client.InNamespace(req.Namespace)); err != nil {
		// 	// 	log.Error(err, "unable to list NodeSelecting resources")
		// 	// 	return ctrl.Result{}, err
		// 	// }

		// 	// for _, nodeSelecting := range nodeSelectingList.Items {
		// 	// 	for _, ownerRef := range nodeSelecting.OwnerReferences {
		// 	// 		if ownerRef.Kind == "ClusterConfiguration" && ownerRef.Name == clusterConfiguration.Name {
		// 	// 			if err := r.Client.Delete(ctx, &nodeSelecting); err != nil {
		// 	// 				log.Error(err, "unable to delete NodeSelecting resource")
		// 	// 				return ctrl.Result{}, err
		// 	// 			}
		// 	// 			log.Info("NodeSelecting resource deleted", "name", nodeSelecting.Name)
		// 	// 		}
		// 	// 	}
		// 	// }
		// 	// var nodeHandlingList clusterv1alpha1.NodeHandlingList
		// 	// if err := r.Client.List(ctx, &nodeHandlingList, client.InNamespace(req.Namespace)); err != nil {
		// 	// 	log.Error(err, "unable to list NodeHandling resources")
		// 	// 	return ctrl.Result{}, err
		// 	// }
		// 	// for _, nodeHandling := range nodeHandlingList.Items {
		// 	// 	for _, ownerRef := range nodeHandling.OwnerReferences {
		// 	// 		if ownerRef.Kind == "ClusterConfiguration" && ownerRef.Name == clusterConfiguration.Name {
		// 	// 			if err := r.Client.Delete(ctx, &nodeHandling); err != nil {
		// 	// 				log.Error(err, "unable to delete NodeHandling resource")
		// 	// 				return ctrl.Result{}, err
		// 	// 			}
		// 	// 			log.Info("NodeHandling resource deleted", "name", nodeHandling.Name)
		// 	// 		}
		// 	// 	}
		// 	// }

		// 	// increment the retryCount annotation
		// 	numberOfRetry, err := strconv.Atoi(clusterConfiguration.Annotations["cluster.dreemk8s/retryCount"])
		// 	if err != nil {
		// 		log.Error(err, "Error converting retryCount annotation to int")
		// 		return ctrl.Result{RequeueAfter: retryInterval}, err
		// 	}
		// 	if numberOfRetry < max_retries {
		// 		lastRetryTimestamp, err := time.Parse(time.RFC3339, clusterConfiguration.Annotations["cluster.dreemk8s/LastRetryTimestamp"])
		// 		if err != nil {
		// 			log.Error(err, "Error parsing LastRetryTimestamp annotation")
		// 			return ctrl.Result{}, err
		// 		}

		// 		if time.Since(lastRetryTimestamp) > retryInterval {
		// 			// try to perform the scaling operation again
		// 			log.Info("Retrying the scaling operation", "retryCount", numberOfRetry)
		// 			updatedNumberOfRetry, err := r.IncrementAnnotation(ctx, clusterConfiguration.Annotations["cluster.dreemk8s/retryCount"])
		// 			if err != nil {
		// 				log.Error(err, "Error incrementing retryCount annotation")
		// 			}
		// 			clusterConfiguration.Annotations["cluster.dreemk8s/retryCount"] = updatedNumberOfRetry
		// 			clusterConfiguration.Annotations["cluster.dreemk8s/LastRetryTimestamp"] = time.Now().Format(time.RFC3339)

		// 			if err := r.Patch(ctx, clusterConfiguration, patch); err != nil {
		// 				log.Error(err, "unable to patch ClusterConfiguration with updated annotations")
		// 				return ctrl.Result{}, err
		// 			}

		// 			var updatedClusterConfig clusterv1alpha1.ClusterConfiguration
		// 			if err := r.Get(ctx, types.NamespacedName{Name: clusterConfiguration.Name, Namespace: clusterConfiguration.Namespace}, &updatedClusterConfig); err != nil {
		// 				log.Error(err, "unable to fetch updated ClusterConfiguration before status update")
		// 				return ctrl.Result{}, err
		// 			}

		// 			updatedClusterConfig.Status.Phase = clusterv1alpha1.CC_PhaseStable
		// 			if err := r.Status().Update(ctx, &updatedClusterConfig); err != nil {
		// 				log.Error(err, "unable to update ClusterConfiguration status to Stable")
		// 				return ctrl.Result{}, err
		// 			}

		// 		} else {
		// 			return ctrl.Result{RequeueAfter: retryInterval}, nil
		// 		}

		// 	} else {
		// 		// max retries reached, set the status to Aborted
		// 		log.Info("Max retries reached, setting status to Aborted")
		// 		clusterConfiguration.Status.Phase = clusterv1alpha1.CC_PhaseAborted
		// 		if err := r.Status().Update(ctx, clusterConfiguration); err != nil {
		// 			log.Error(err, "unable to update ClusterConfiguration status")
		// 			return ctrl.Result{}, err
		// 		}
		// 		log.Info("ClusterConfiguration status updated to Aborted", "name", clusterConfiguration.Name)
		// 	}

		// }

		break
	case clusterv1alpha1.CC_PhaseAborted:
		log.Info("ClusterConfiguration in Aborted Phase", "name", clusterConfiguration.Name)
	default:
		log.Info("ClusterConfiguration in unknown Phase", "name", clusterConfiguration.Name)
		break
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1alpha1.ClusterConfiguration{}).
		Named("clusterconfiguration").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		WithEventFilter(predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {

				return true
			},
			CreateFunc: func(e event.CreateEvent) bool {
				// Triggera il reconcile per eventi di creazione
				return true
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				// Triggera il reconcile per eventi di eliminazione
				return true
			},
			GenericFunc: func(e event.GenericEvent) bool {
				// Ignora eventi generici
				return false
			},
		}).
		// Watches per NodeSelecting
		Watches(
			&clusterv1alpha1.NodeSelecting{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				ns, ok := obj.(*clusterv1alpha1.NodeSelecting)
				if !ok {
					return nil
				}

				// Triggera il reconcile per la ClusterConfiguration associata
				clusterConfigName := ns.Spec.ClusterConfigurationName
				return []reconcile.Request{
					{
						NamespacedName: types.NamespacedName{
							Name:      clusterConfigName,
							Namespace: ns.Namespace,
						},
					},
				}
			}),
			builder.WithPredicates(predicate.Funcs{
				UpdateFunc: func(e event.UpdateEvent) bool {

					newObj, _ := e.ObjectNew.(*clusterv1alpha1.NodeSelecting)

					// Triggera il reconcile solo se lo stato cambia
					return newObj.Status.Phase == clusterv1alpha1.NS_PhaseComplete || newObj.Status.Phase == clusterv1alpha1.NS_PhaseFailed
				},
				CreateFunc:  func(e event.CreateEvent) bool { return false },
				DeleteFunc:  func(e event.DeleteEvent) bool { return false },
				GenericFunc: func(e event.GenericEvent) bool { return false },
			}),
		).
		// Watches per NodeHandling
		Watches(
			&clusterv1alpha1.NodeHandling{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				nh, ok := obj.(*clusterv1alpha1.NodeHandling)
				if !ok {
					return nil
				}

				// Triggera il reconcile per la ClusterConfiguration associata
				clusterConfigName := nh.Spec.ClusterConfigurationName
				return []reconcile.Request{
					{
						NamespacedName: types.NamespacedName{
							Name:      clusterConfigName,
							Namespace: nh.Namespace,
						},
					},
				}
			}),
			builder.WithPredicates(predicate.Funcs{
				UpdateFunc: func(e event.UpdateEvent) bool {

					newObj, _ := e.ObjectNew.(*clusterv1alpha1.NodeHandling)

					// Triggera il reconcile solo se lo stato cambia
					return newObj.Status.Phase == clusterv1alpha1.NH_PhaseCompleted || newObj.Status.Phase == clusterv1alpha1.NH_PhaseFailed
				},
				CreateFunc:  func(e event.CreateEvent) bool { return false },
				DeleteFunc:  func(e event.DeleteEvent) bool { return false },
				GenericFunc: func(e event.GenericEvent) bool { return false },
			}),
		).
		Owns(&clusterv1alpha1.NodeHandling{}).
		Owns(&clusterv1alpha1.NodeSelecting{}).
		Complete(r)
}
