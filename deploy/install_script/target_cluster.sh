# bin/sh

if [ -z "$TARGET_KUBECONFIG" ]; then
  echo "Error: TARGET_KUBECONFIG variable is not set"
  exit 1
fi

echo "⚙️ Installing monitoring stack on the target cluster..."
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
  --set prometheus-node-exporter.service.type=NodePort --kubeconfig $TARGET_KUBECONFIG

sleep 60

echo "⚙️ Labeling monitoring namespace on the target cluster..."
kubectl label namespace monitoring pod-security.kubernetes.io/enforce=privileged --overwrite --kubeconfig $TARGET_KUBECONFIG
kubectl label namespace monitoring pod-security.kubernetes.io/audit=privileged --overwrite --kubeconfig $TARGET_KUBECONFIG
kubectl label namespace monitoring pod-security.kubernetes.io/warn=privileged --overwrite --kubeconfig $TARGET_KUBECONFIG