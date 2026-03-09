# check if the TARGET_KUBECONFIG and PROMETHEUS_PORT variables are set, if not exit with an error message
echo "⚙️ Generating federate scrape config for the management cluster..."
if [ -z "$TARGET_KUBECONFIG" ] || [ -z "$PROMETHEUS_PORT" ]; then
  echo "Error: TARGET_KUBECONFIG and PROMETHEUS_PORT variables are not set"
  exit 1
fi

CONTROLPLANE_IP=$(kubectl get nodes -o wide --kubeconfig $TARGET_KUBECONFIG | grep control-plane | awk '{print $6}')

sed "s/{{CONTROLPLANE_IP}}/${CONTROLPLANE_IP}:${PROMETHEUS_PORT}/g" ./install_script/scrapefederate-template.yaml > ./scrapefederate.yaml

