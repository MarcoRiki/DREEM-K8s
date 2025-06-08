create the management cluster
install the capi provider
start the cluster on proxmox with the proxmoxConfig (where the labels are writter)

Create the secret containing the kubeconfig of the managed cluster (the one on proxmox):
```bash
kubectl create secret generic dreem-mmiracapillo-cluster-kubeconfig --from-file=value=/Users/marco/.kube/capi  -n kube-system 
```
apply it on the management cluster

then apply the cluster-autoscaler deployment
```bash
export AUTOSCALER_NS=kube-system                                  
export AUTOSCALER_IMAGE=registry.k8s.io/autoscaling/cluster-autoscaler:v1.29.0
envsubst < clusterautoscaler.yaml| kubectl apply -f-
```