# bin/sh

echo "🚀 Deploying Dreem on the management cluster and target cluster..."
echo " "

helm repo update
#checking if the TARGET_KUBECONFIG and MANAGEMENT_KUBECONFIG variables are set, if not exit with an error message
if [ -z "$TARGET_KUBECONFIG" ] || [ -z "$MANAGEMENT_KUBECONFIG" ]; then
  echo "Error: TARGET_KUBECONFIG and MANAGEMENT_KUBECONFIG variables are not set"
  echo "MANAGEMENT_KUBECONFIG should point to the kubeconfig of the cluster where the operators will be deployed"
  echo "TARGET_KUBECONFIG should point to the kubeconfig of the cluster that has to be scaled and monitored by DREEM"
  exit 1
fi

# Export PROMETHEUS_PORT before generating federate config
export PROMETHEUS_PORT=30000

echo "⚙️ Generating federate scrape config for the management cluster..."
echo " "
./install_script/generate_federate_job.sh
echo "⏳ Waiting for federate config generation to complete..."
sleep 2

echo "⚙️ Deploying Dreem components on the management cluster... "
echo " "
./install_script/management_cluster.sh
echo "⏳ Waiting for management cluster components to be deployed..."
sleep 10
echo "⏳ Waiting for Prometheus pods to be ready on management cluster..."
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=prometheus -n monitoring --timeout=300s --kubeconfig $TARGET_KUBECONFIG || echo "Warning: Prometheus pods may still be starting"
echo "⏳ Waiting for dreem operator to be ready..."
kubectl wait --for=condition=ready pod -l control-plane=controller-manager -n dreem-system --timeout=180s --kubeconfig $TARGET_KUBECONFIG || echo "Warning: Dreem operator may still be starting"

echo "⚙️ Deploying required components on the target cluster... "
echo " "
./install_script/target_cluster.sh
echo "⏳ Waiting for target cluster components to be deployed..."
sleep 10
echo "⏳ Waiting for Prometheus pods to be ready on target cluster..."
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=prometheus -n monitoring --timeout=300s --kubeconfig $TARGET_KUBECONFIG || echo "Warning: Prometheus pods may still be starting"

# verification step to check if the monitoring stack, federate-job-config, configmaps, dreem-operator is up and running on the management cluster; monitoring stack in the target cluster, otherwise make error message and exit
echo "⏳ Waiting before verification steps..."
sleep 5

echo "⚙️ Verifying the deployment of DREEM on both clusters... "
echo " "
echo "⚙️ Checking if the monitoring stack is up and running on the management cluster..."
kubectl get pods -n monitoring --kubeconfig $TARGET_KUBECONFIG
if [ $? -ne 0 ]; then
  echo "Error: Monitoring stack is not up and running on the management cluster"
  exit 1
fi
echo "✅ Monitoring stack is up and running on the management cluster"

echo "⏳ Waiting before next check..."
sleep 2

echo "⚙️ Checking if the monitoring stack is up and running on the target cluster..."
kubectl get pods -n monitoring --kubeconfig $TARGET_KUBECONFIG
if [ $? -ne 0 ]; then
  echo "Error: Monitoring stack is not up and running on the target cluster"
  exit 1
fi
echo "✅ Monitoring stack is up and running on the target cluster"

echo "⏳ Waiting before checking federate job..."
sleep 3

echo "⚙️ Checking if the monitoring stack has been updated with the new federate job on the management cluster..."
./install_script/verify_federate.sh

echo "⏳ Waiting before checking configmaps..."
sleep 2

echo "Verifying if the configmaps for the forecast parameters are created on the management cluster..."

echo "ConfigMap: selection-weights-scale-up"
kubectl get configmap selection-weights-scale-up -n dreem --kubeconfig $MANAGEMENT_KUBECONFIG
if [ $? -ne 0 ]; then
  echo "Error: ConfigMap selection-weights-scale-up is not created on the management cluster"
  exit 1
fi
echo "ConfigMap: selection-weights-scale-down"
kubectl get configmap selection-weights-scale-down -n dreem --kubeconfig $MANAGEMENT_KUBECONFIG
if [ $? -ne 0 ]; then
  echo "Error: ConfigMap selection-weights-scale-down is not created on the management cluster"
  exit 1
fi
echo "ConfigMap: forecast-parameters"
kubectl get configmap forecast-parameters -n dreem --kubeconfig $MANAGEMENT_KUBECONFIG
if [ $? -ne 0 ]; then
  echo "Error: ConfigMap forecast-parameters is not created on the management cluster"
  exit 1
fi
echo "ConfigMap: cluster-configuration-parameters"
kubectl get configmap cluster-configuration-parameters -n dreem --kubeconfig $MANAGEMENT_KUBECONFIG
if [ $? -ne 0 ]; then
  echo "Error: ConfigMap cluster-configuration-parameters is not created on the management cluster"
  exit 1
fi

echo "✅ Configmaps available on the management cluster"

echo "⏳ Waiting before checking dreem operator..."
sleep 3

echo "⚙️ Checking if the dreem-operator is up and running on the management cluster..." # check if exists and running
kubectl get pods -n dreem --kubeconfig $MANAGEMENT_KUBECONFIG | grep dreem-operator | grep Running
if [ $? -ne 0 ]; then
  echo "Error: Dreem operator is not up and running on the management cluster"
  exit 1
fi
echo "✅ Dreem operator is up and running on the management cluster"

echo "🎉 DREEM has been successfully deployed on both clusters and is ready to use!"