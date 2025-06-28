
kubectl create ns dreem

helm install kind-prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace \
  --set prometheus.service.nodePort=30000 \
  --set prometheus.service.type=NodePort \
  --set grafana.service.nodePort=31000 \
  --set grafana.service.type=NodePort \
  --set alertmanager.service.nodePort=32000 \
  --set alertmanager.service.type=NodePort \
  --set prometheus-node-exporter.service.nodePort=32001 \
  --set prometheus-node-exporter.service.type=NodePort


# kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml

# echo "⚙️ patch metrics-server to run local..."
# kubectl patch deployment metrics-server -n kube-system \
#   --type=json \
#   -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-insecure-tls"}]'



kubectl apply -f ../../configmaps -n dreem

#kubectl create ns monitoring --kubeconfig ~/.kube/capi 

helm install kind-prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --create-namespace \
  --set prometheus.service.nodePort=30000 \
  --set prometheus.service.type=NodePort \
  --set grafana.service.nodePort=31000 \
  --set grafana.service.type=NodePort \
  --set alertmanager.service.nodePort=32000 \
  --set alertmanager.service.type=NodePort \
  --set prometheus-node-exporter.service.nodePort=32001 \
  --set prometheus-node-exporter.service.type=NodePort --kubeconfig ~/.kube/capi 

  helm upgrade kind-prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --reuse-values \
  --set-file prometheus.prometheusSpec.additionalScrapeConfigs=scrapefederate.yaml 