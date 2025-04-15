kubectl apply -f capi.yaml

echo "⏳ Wait for cluster to be ready..."
kubectl wait --for=condition=Ready --timeout=10m cluster/capi

kind get kubeconfig --name capi > ~/.kube/config

kubectl \
  apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.26.1/manifests/calico.yaml

kubectl get nodes


