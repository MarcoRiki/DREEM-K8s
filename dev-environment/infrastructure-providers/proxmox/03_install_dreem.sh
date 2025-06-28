
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


kubectl apply -f ../../configmaps -n dreem


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

kubectl label namespace monitoring pod-security.kubernetes.io/enforce=privileged --overwrite --kubeconfig ~/.kube/capi
kubectl label namespace monitoring pod-security.kubernetes.io/audit=privileged --overwrite --kubeconfig ~/.kube/capi
kubectl label namespace monitoring pod-security.kubernetes.io/warn=privileged --overwrite --kubeconfig ~/.kube/capi

  helm upgrade kind-prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --reuse-values \
  --set-file prometheus.prometheusSpec.additionalScrapeConfigs=../docker/scrapefederate.yaml 