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
	"encoding/json"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
	clusterv1alpha1 "github.com/MarcoRiki/DREEM-K8s/api/v1alpha1"
)

type Response struct {
	SelectedNode string `json:"selectedNode"`
}

func getNodeSelectionServerURL(ctx context.Context, r *NodeSelectingReconciler) (string, error) {
	//log := log.FromContext(ctx)

	// use if you want to test the controller locally
	return "http://localhost:8000/", nil

	// // get the address of the control plane node of the cluster
	// nodes := &corev1.NodeList{}
	// if err := r.Client.List(ctx, nodes); err != nil {
	// 	log.Error(err, "unable to list nodes")
	// 	return "", err
	// }

	// var controlPlaneIP string
	// for _, node := range nodes.Items {
	// 	if _, exists := node.Labels["node-role.kubernetes.io/control-plane"]; exists {
	// 		for _, addr := range node.Status.Addresses {
	// 			if addr.Type == corev1.NodeInternalIP {
	// 				controlPlaneIP = addr.Address
	// 				break
	// 			}
	// 		}
	// 		if controlPlaneIP != "" {
	// 			break
	// 		}
	// 	}
	// }

	// if controlPlaneIP == "" {
	// 	log.Error(fmt.Errorf("control-plane node not found"), "unable to determine control-plane IP")
	// 	return "", fmt.Errorf("unable to determine control-plane IP")
	// }

	// service := &corev1.Service{}
	// if err := r.Client.Get(ctx, client.ObjectKey{Namespace: "dreem", Name: "selection-service"}, service); err != nil {
	// 	log.Error(err, "unable to fetch service")
	// 	return "", err
	// }

	// var nodePort int32
	// for _, port := range service.Spec.Ports {
	// 	if port.NodePort != 0 {
	// 		nodePort = port.NodePort
	// 		break
	// 	}
	// }

	// if nodePort == 0 {
	// 	log.Error(fmt.Errorf("nodePort not found"), "service does not expose a NodePort")
	// 	return "", fmt.Errorf("service does not expose a NodePort")
	// }

	// log.Info("Control plane IP and NodePort found", "controlPlaneIP", controlPlaneIP, "nodePort", nodePort)
	// return fmt.Sprintf("http://%s:%d/", controlPlaneIP, nodePort), nil

}

// get the name of the chosen node (to shut down or scale up) from the server
// the server is a simple HTTP server that returns a JSON object with the name of the node
func getNodeLabel(ctx context.Context, scalingLabel int32, URL string) (string, error) {
	log := log.FromContext(ctx)
	url := URL
	if scalingLabel > 0 {
		url = url + "nodes/scaleUp"
	} else if scalingLabel < 0 {
		url = url + "nodes/scaleDown"
	}

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

func createNodeHandlingCRD(ctx context.Context, r *NodeSelectingReconciler, selectedNode string, NodeSelectingName string, ClusterConfigurationName string, scalingLabel int32) error {
	log := log.FromContext(ctx)

	log.Info("Creating the NodeHandling CRD")

	// create unique identifier for the NodeHandling CRD
	crdNameBytes := make([]byte, 8)
	if _, err := rand.Read(crdNameBytes); err != nil {
		log.Error(err, "unable to generate random name for NodeHandling CRD")
		return err
	}
	crdName := "node-handling-" + fmt.Sprintf("%x", crdNameBytes)
	// create the NodeHandling CRD
	var nodeHandling = &clusterv1alpha1.NodeHandling{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crdName,
			Namespace: "dreem",
		},
		Spec: clusterv1alpha1.NodeHandlingSpec{
			ClusterConfigurationName: ClusterConfigurationName,
			NodeSelectingName:        NodeSelectingName,
			SelectedNode:             selectedNode,
			ScalingLabel:             scalingLabel,
		},
	}

	if err := r.Client.Create(ctx, nodeHandling); err != nil {
		log.Error(err, "unable to create NodeHandling CRD")
		return err
	}
	log.Info("NodeHandling CRD created", "name", nodeHandling.Name)

	return nil
}

// NodeSelectingReconciler reconciles a NodeSelecting object
type NodeSelectingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselectings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselectings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.dreemk8s,resources=nodeselectings/finalizers,verbs=update

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch

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

	// retrieve the address of the http server for the node selection
	URL, err := getNodeSelectionServerURL(ctx, r)
	if err != nil {
		log.Error(err, "unable to get the node selection server URL")
		return ctrl.Result{}, err
	}

	var nodeSelecting = &clusterv1alpha1.NodeSelecting{}
	if err := r.Client.Get(ctx, req.NamespacedName, nodeSelecting); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "unable to fetch NodeSelecting")
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("NodeSelecting CRD found", "name", nodeSelecting.Name)

	if nodeSelecting.Status.Phase == "" {

		// get clusterconfiguration resource from the name in the spec
		clusterConfiguration := &clusterv1alpha1.ClusterConfiguration{}
		if err := r.Client.Get(ctx, client.ObjectKey{
			Name:      nodeSelecting.Spec.ClusterConfigurationName,
			Namespace: nodeSelecting.Namespace,
		}, clusterConfiguration); err != nil {
			log.Error(err, "unable to find ClusterConfiguration parent resource for NodeSelecting")
			nodeSelecting.Status.Phase = clusterv1alpha1.NS_PhaseFailed
			if err := r.Status().Update(ctx, nodeSelecting); err != nil {
				log.Error(err, "unable to update the NodeSelecting status")
			}
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		fmt.Println("ClusterConfiguration found", "name", clusterConfiguration.Name)

		// var clusterConfigurationList clusterv1alpha1.ClusterConfigurationList
		// if err := r.Client.List(ctx, &clusterConfigurationList); err != nil {
		// 	log.Error(err, "unable to list ClusterConfiguration resources")
		// 	return ctrl.Result{}, err
		// }

		// // find the clusterconfiguration with the name in the spec
		// for _, cc := range clusterConfigurationList.Items {
		// 	if cc.Name == nodeSelecting.Spec.ClusterConfigurationName {
		// 		clusterConfiguration = &cc
		// 		break
		// 	}
		// }
		// if clusterConfiguration == nil {
		// 	log.Info("Unable to find the ClusterConfiguration parent resource", "name", nodeSelecting.Spec.ClusterConfigurationName)
		// 	nodeSelecting.Status.Phase = clusterv1alpha1.NS_PhaseFailed
		// 	if err := r.Status().Update(ctx, nodeSelecting); err != nil {
		// 		log.Error(err, "unable to update the NodeSelecting status")
		// 	}
		// 	return ctrl.Result{}, fmt.Errorf("unable to find the ClusterConfiguration parent resource")
		// }

		//check if exists another NodeSelecting with the same clusterconfiguration
		nodeSelectingList := &clusterv1alpha1.NodeSelectingList{}
		if err := r.Client.List(ctx, nodeSelectingList); err != nil {
			log.Error(err, "unable to list NodeSelecting resources")
			return ctrl.Result{}, err
		}
		for _, ns := range nodeSelectingList.Items {
			if ns.Name != nodeSelecting.Name && ns.Spec.ClusterConfigurationName == nodeSelecting.Spec.ClusterConfigurationName {
				log.Info("Another NodeSelecting resource already exists with the same ClusterConfiguration", "name", ns.Name)
				nodeSelecting.Status.Phase = clusterv1alpha1.NS_PhaseFailed
				if err := r.Status().Update(ctx, nodeSelecting); err != nil {
					log.Error(err, "unable to update the NodeSelecting status")
				}
				return ctrl.Result{}, fmt.Errorf("another NodeSelecting resource already exists with the same ClusterConfiguration")
			}
		}
		log.Info("No other NodeSelecting resource found with the same ClusterConfiguration")

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
		nodeLabel, err := getNodeLabel(ctx, nodeSelecting.Spec.ScalingLabel, URL)
		if err != nil {
			log.Error(err, "unable to get the node label")
			nodeSelecting.Status.Phase = clusterv1alpha1.NS_PhaseFailed
			if err := r.Status().Update(ctx, nodeSelecting); err != nil {
				log.Error(err, "unable to update the NodeSelecting status")
			}

			return ctrl.Result{}, err
		}
		log.Info("Node label received", "nodeLabel", nodeLabel)

		if nodeLabel == "null" {
			log.Error(fmt.Errorf("node label is null"), "unable to get the node label")
			nodeSelecting.Status.Phase = clusterv1alpha1.NS_PhaseFailed
			if err := r.Status().Update(ctx, nodeSelecting); err != nil {
				log.Error(err, "unable to update the NodeSelecting status")
			}
			return ctrl.Result{}, fmt.Errorf("node label is null")
		}

		errorNodeHandling := createNodeHandlingCRD(ctx, r, nodeLabel, nodeSelecting.Name, nodeSelecting.Spec.ClusterConfigurationName, nodeSelecting.Spec.ScalingLabel)
		if errorNodeHandling == nil {
			nodeSelecting.Status.SelectedNode = nodeLabel
			nodeSelecting.Status.Phase = clusterv1alpha1.NS_PhaseComplete

		} else {
			log.Error(errorNodeHandling, "createNodeHandlingCRD function failed")
			nodeSelecting.Status.Phase = clusterv1alpha1.NS_PhaseFailed
		}

		if err := r.Status().Update(ctx, nodeSelecting); err != nil {
			log.Error(err, "unable to update the NodeSelecting status")
			return ctrl.Result{}, err
		}
	}

	if nodeSelecting.Status.Phase == clusterv1alpha1.NS_PhaseComplete || nodeSelecting.Status.Phase == clusterv1alpha1.NS_PhaseFailed {
		//set annotation in clusterconfiguration to tirgger the controller
		clusterConf := &v1alpha1.ClusterConfiguration{}
		_ = r.Get(ctx, client.ObjectKey{Namespace: "dreem", Name: nodeSelecting.Spec.ClusterConfigurationName}, clusterConf)

		patch := client.MergeFrom(clusterConf.DeepCopy())
		if clusterConf.Annotations == nil {
			clusterConf.Annotations = map[string]string{}
		}
		clusterConf.Annotations["cluster.dreemk8s/clusterconfiguration_trigger"] = "NodeSelecting"
		_ = r.Patch(ctx, clusterConf, patch)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeSelectingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1alpha1.NodeSelecting{}).
		Named("nodeselecting").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		Complete(r)
}
