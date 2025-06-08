
import random
import string
import time
from kubernetes import client, config
from kubernetes.client.rest import ApiException
from forecast import load_data_cpu, basic_prediciton #, make_future_prediction_cpu, make_future_prediction_cpu_LSTM
import logging
import utils
from kubernetes import client, config
from kubernetes.dynamic import DynamicClient



def forecast(prometheus_url, rate_interval, time_windows_forecast, time_window_prediction, query_step_in_seconds, min_threshold, max_threshold):
    """
    The function returns the number of nodes that must be turned on (positive number) or off (negative number)
    """

    df_cpu = load_data_cpu(prometheus_url, rate_interval, time_windows_forecast, query_step_in_seconds)
    #pred_cpu = make_future_prediction_cpu(df_cpu, time_window_prediction, query_step_in_seconds)
    #pred_cpu = make_future_prediction_cpu_LSTM(df_cpu,query_step_in_seconds, time_windows_forecast, time_window_prediction )
    pred_cpu = basic_prediciton(df_cpu)
    print("CPU predicted:", pred_cpu)

    if pred_cpu > max_threshold:

        return 1
    else:
        if pred_cpu < min_threshold:

            return -1
        else:

            return 0

def create_cluster_configuration(requiredNodes, min_nodes, max_nodes):
    """
    La funzione crea un'istanza della risorsa ClusterConfiguration (CR) nel cluster Kubernetes.
    """

    api = client.CustomObjectsApi()
    # Check for existing ClusterConfiguration CRs
    existing_crs = api.list_namespaced_custom_object(
        group="cluster.dreemk8s",
        version="v1alpha1",
        namespace="dreem",
        plural="clusterconfigurations"
    )

    for cr in existing_crs.get("items", []):
        status = cr.get("status", {})
        phase = status.get("phase", "")
        #print(phase)
        if phase not in ["Completed", "Aborted"]:
            logging.info(f"Existing ClusterConfiguration '{cr['metadata']['name']}' is not completed. Skipping creation.")
            return

    group = "cluster.dreemk8s"
    version = "v1alpha1"  # Assicurati che sia scritto correttamente
    plural = "clusterconfigurations"
    namespace = "dreem"
    random_string = ''.join(random.choices(string.ascii_lowercase + string.digits, k=8))
    cr_name = f"clusterconfiguration-{random_string}"

    body = {
        "apiVersion": f"{group}/{version}",
        "kind": "ClusterConfiguration",
        "metadata": {
            "name": cr_name,
            "namespace": namespace
        },
        "spec": {
            "requiredNodes": int(requiredNodes),
            "maxNodes": int(max_nodes),
            "minNodes": int(min_nodes)
        }
    }

    try:
        api.create_namespaced_custom_object(
            group=group,
            version=version,
            namespace=namespace,
            plural=plural,
            body=body
        )
        logging.info(f"Nuova istanza di ClusterConfiguration creata con successo: {cr_name}")
    except ApiException as e:
        logging.error(f"Errore durante la creazione dell'istanza di ClusterConfiguration: {e}")

def read_forecast_cm():
    """
    The function reads the forecast-parameters ConfigMap and updates the default values
    """
    # set some default values
    rate_interval = "10m"
    time_window_forecast_in_minutes = 60
    time_window_prediction_in_minutes = 60
    query_step_in_seconds= '15s'
    min_threshold = 50
    max_threshold= 80
    forecast_period_in_minutes= 30

    # update the CM if values are present
    v1 = client.CoreV1Api()
    try:
        config_map = v1.read_namespaced_config_map(name="forecast-parameters", namespace="dreem")
        cm=config_map.data
        rate_interval = cm.get("Prometheus_rate_interval", rate_interval)
        time_window_forecast_in_minutes = int(
            cm.get("Prometheus_time_window_forecast_in_minutes", time_window_forecast_in_minutes))
        time_window_prediction_in_minutes = int(
            cm.get("Prometheus_time_window_prediction_in_minutes", time_window_prediction_in_minutes))
        query_step_in_seconds = cm.get("Prometheus_query_step", query_step_in_seconds)
        min_threshold = int(cm.get("Thresholds_min", min_threshold))
        max_threshold = int(cm.get("Thresholds_max", max_threshold))
        forecast_period_in_minutes = int(cm.get("Forecast_period_in_minutes", forecast_period_in_minutes))

    except client.exceptions.ApiException as e:
        logging.error(f"Error during the reading of the ConfigMap: {e}")
        return None


    return  rate_interval, time_window_forecast_in_minutes,time_window_prediction_in_minutes, query_step_in_seconds, min_threshold, max_threshold, forecast_period_in_minutes

def read_cluster_configuration_cm():
    """
       The function reads the clusterConfiguration-parameters ConfigMap and updates the default values
       """
    # set some default values
    min_nodes=1
    max_nodes=10

    # update the CM if values are present
    v1 = client.CoreV1Api()
    try:
        config_map = v1.read_namespaced_config_map(name="cluster-configuration-parameters", namespace="dreem")
        cm = config_map.data
        min_nodes = cm.get("minNodes", min_nodes)
        max_nodes = cm.get("MaxNodes", max_nodes)


    except client.exceptions.ApiException as e:
        logging.error(f"Error during the reading of the ConfigMap: {e}")
        return None

    return min_nodes, max_nodes

def get_prometheus_url():
    service_name= "kind-prometheus-kube-prome-prometheus"
    namespace = "monitoring"
    local_conf= utils.load_configuration()

    if local_conf:
        return "http://localhost:9090/"

    v1 = client.CoreV1Api()

    control_plane_ip = get_control_plane_ip_management_cluster()
    # Ottieni la porta NodePort del servizio
    service = v1.read_namespaced_service(name=service_name, namespace=namespace)
    node_port = None
    for port in service.spec.ports:
        if port.node_port:
            node_port = port.node_port
            break

    if not node_port:
        raise Exception("impossible finding Nodeport port")

    return f"http://{control_plane_ip}:{node_port}/"

def get_control_plane_ip_management_cluster():
    v1 = client.CoreV1Api()


    nodes = v1.list_node()
    control_plane_ip = None
    for node in nodes.items:
        labels = node.metadata.labels or {}
        if "node-role.kubernetes.io/control-plane" in labels or "node-role.kubernetes.io/master" in labels:
            for addr in node.status.addresses:
                if addr.type == "InternalIP":
                    control_plane_ip = addr.address
                    break
        if control_plane_ip:
            break

    if not control_plane_ip:
        raise Exception("Impossibile trovare l'IP del nodo control-plane")



    return control_plane_ip


def get_active_nodes():
    """
    The function returns the number of active machine in the managed cluster, without considering the control-plane
    """
    k8s_client = client.ApiClient()
    dyn_client = DynamicClient(k8s_client)

    # Ottieni risorsa Machine
    machine_resource = dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="Machine")
    machines = machine_resource.get()

    active_nodes = 0
    for machine in machines.items:
        labels = machine.metadata.labels or {}

        # Se NON è control-plane, conta
        if labels["cluster.x-k8s.io/control-plane"] != "":
            active_nodes += 1

    return active_nodes
    


def main():
    logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
    utils.load_configuration()

    rate_interval, time_windows_forecast, time_window_prediction, query_step_in_seconds, min_threshold, max_threshold, forecast_period_in_minutes = read_forecast_cm()
    prometheus_url = get_prometheus_url()
    print(prometheus_url)
    min_nodes, max_nodes = read_cluster_configuration_cm()


    while True:
        active_nodes = get_active_nodes()
        print("min, max, active " + min_nodes, max_nodes, active_nodes)
        logging.info("Forecast started")
        scaling_label = forecast(prometheus_url, rate_interval, time_windows_forecast, time_window_prediction, query_step_in_seconds, min_threshold, max_threshold)
        print("scaling:", scaling_label)
        # create the CRD only if the cluster configuration (aka number of nodes) has to be updated
        if scaling_label != 0:
            required_nodes= active_nodes + scaling_label
            print("required:" ,required_nodes)
            create_cluster_configuration(required_nodes, min_nodes, max_nodes)
        time.sleep(5 * 60)


if __name__ == "__main__":
    main()