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
	"encoding/json"
	"fmt"
	"net/http"

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
)

type Response struct {
	SelectedNode string `json:"selectedNode"`
}

func getNodeLabel(ctx context.Context, scalingLabel int32) (string, error) {
	log := log.FromContext(ctx)

	if scalingLabel > 0 {
		url := "http://localhost:8000/nodes/scaleUp"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			log.Error(err, "unable to create request")
			return "", err
		}

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			log.Error(err, "unable to send request")
			return "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Error(fmt.Errorf("unexpected status code: %d", resp.StatusCode), "request failed")
			return "", err
		}

		var response Response
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			log.Error(err, "unable to decode response")
			return "", err
		}
		log.Info("Node label received from server", "nodeLabel", response.SelectedNode)
		return response.SelectedNode, nil
	}

	// call the server localhost:8000/nodes/scaleDown
	if scalingLabel < 0 {
		return "", nil
	}
	return "", nil
}

// NodeSelectingReconciler reconciles a NodeSelecting object
type NodeSelectingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselectings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselectings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselectings/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the NodeSelecting object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *NodeSelectingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var nodeSelecting = &clusterv1alpha1.NodeSelecting{}
	if err := r.Client.Get(ctx, req.NamespacedName, nodeSelecting); err != nil {
		log.Error(err, "unable to fetch NodeSelecting")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("NodeSelecting CRD found", "name", nodeSelecting.Name)

	if nodeSelecting.Status.Phase == "" {
		log.Info("NodeSelecting CRD setting the initial status")

		var clusterConfigurationList clusterv1alpha1.ClusterConfigurationList
		if err := r.Client.List(ctx, &clusterConfigurationList); err != nil {
			log.Error(err, "unable to list ClusterConfiguration resources")
			return ctrl.Result{}, err
		}

		if len(clusterConfigurationList.Items) != 1 {
			log.Error(nil, "expected exactly one ClusterConfiguration resource, but found", "count", len(clusterConfigurationList.Items))
			return ctrl.Result{}, nil
		}

		clusterConfiguration := &clusterConfigurationList.Items[0]

		if err := ctrl.SetControllerReference(clusterConfiguration, nodeSelecting, r.Scheme); err != nil {
			log.Error(err, "unable to set owner reference on NodeSelecting")
			return ctrl.Result{}, err
		}
		if err := r.Client.Update(ctx, nodeSelecting); err != nil {
			log.Error(err, "unable to update NodeSelecting with owner reference")
			return ctrl.Result{}, err
		}

		nodeSelecting.Status.Phase = clusterv1alpha1.NS_PhaseRunning
		if err := r.Status().Update(ctx, nodeSelecting); err != nil {
			log.Error(err, "unable to update the NodeSelecting status")
		}
		return ctrl.Result{}, nil
	}

	if nodeSelecting.Status.Phase == clusterv1alpha1.NS_PhaseRunning {
		log.Info("NodeSelecting calls the server to have the node label")

		nodeLabel, err := getNodeLabel(ctx, nodeSelecting.Spec.ScalingLabel)
		if err != nil {
			log.Error(err, "unable to get the node label")
			nodeSelecting.Status.Phase = clusterv1alpha1.NS_PhaseFailed
			if err := r.Status().Update(ctx, nodeSelecting); err != nil {
				log.Error(err, "unable to update the NodeSelecting status")
			}
			return ctrl.Result{}, err
		}
		log.Info("Node label received", "nodeLabel", nodeLabel)

		nodeSelecting.Status.SelectedNode = nodeLabel
		nodeSelecting.Status.Phase = clusterv1alpha1.NS_PhaseComplete

		if err := r.Status().Update(ctx, nodeSelecting); err != nil {
			log.Error(err, "unable to update the NodeSelecting status")
			return ctrl.Result{}, err
		}

	}

	if nodeSelecting.Status.Phase == clusterv1alpha1.NS_PhaseComplete {
		log.Info("Creating the NodeHandling CRD")

		// TODO: logic to create the NodeHandling CRD
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeSelectingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1alpha1.NodeSelecting{}).
		Named("nodeselecting").
		Watches(
			&clusterv1alpha1.NodeSelecting{},
			handler.EnqueueRequestsFromMapFunc(
				func(ctx context.Context, obj client.Object) []reconcile.Request {
					_, ok := obj.(*clusterv1alpha1.NodeSelecting)
					if !ok {
						return nil
					}

					NodeSelectingList := &clusterv1alpha1.NodeSelectingList{}
					err := r.Client.List(ctx, NodeSelectingList)
					if err != nil {
						return nil
					}

					var requests []reconcile.Request
					for _, item := range NodeSelectingList.Items {

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
		Complete(r)
}
