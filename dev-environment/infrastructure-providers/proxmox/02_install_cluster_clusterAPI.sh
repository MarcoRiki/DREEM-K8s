kubectl apply -f cluster.yaml

echo "⏳ Wait for cluster to be ready..."
kubectl wait --for=condition=Ready --timeout=10m cluster/dreem-mmiracapillo-cluster

clusterctl get kubeconfig dreem-mmiracapillo-cluster > ~/.kube/capi

# kubectl \
#   apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.26.1/manifests/calico.yaml --kubeconfig ~/.kube/capi

kubectl get nodes --kubeconfig ~/.kube/capi


