kubectl delete cluster capi-quickstart --kubeconfig ~/.kube/kind

#setta prima la KUBECONFIG di kind
export KUBECONFIG=~/.kube/kind
kind delete cluster 