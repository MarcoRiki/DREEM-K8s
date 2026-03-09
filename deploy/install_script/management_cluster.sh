# /bin/sh

if [ -z "$TARGET_KUBECONFIG" ] || [ -z "$MANAGEMENT_KUBECONFIG" ]; then
  echo "Error: TARGET_KUBECONFIG and MANAGEMENT_KUBECONFIG variables are not set"
  exit 1
fi

# use the kubeconfig in TARGET_KUBECONFIG of the management cluster to install the monitoring stack and configmaps for the forecast parameters 
kubectl create ns dreem --kubeconfig $MANAGEMENT_KUBECONFIG

echo "⚙️ Installing monitoring stack on the management cluster..."
helm install kind-prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace \
  --set prometheus.service.nodePort=$PROMETHEUS_PORT \
  --set prometheus.service.type=NodePort \
  --set grafana.service.nodePort=31000 \
  --set grafana.service.type=NodePort \
  --set alertmanager.service.nodePort=32000 \
  --set alertmanager.service.type=NodePort \
  --set prometheus-node-exporter.service.nodePort=32001 \
  --set prometheus-node-exporter.service.type=NodePort \
  --set-file prometheus.prometheusSpec.additionalScrapeConfigs=./scrapefederate.yaml \
  --kubeconfig $MANAGEMENT_KUBECONFIG

sleep 60
cd ../operators/

export KUBECONFIG=$MANAGEMENT_KUBECONFIG
echo "⚙️ Installing dreem operator on the management cluster..."
# apply configmaps for the forecast parameters
kubectl apply -f configmaps -n dreem --kubeconfig $MANAGEMENT_KUBECONFIG
make install
make deploy 