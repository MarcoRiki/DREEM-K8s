
import random
import string
import time
from kubernetes.client.rest import ApiException
from forecast import load_data_instance_cpu, basic_prediction, evaluate_predicted_cpu
import logging
import utils
from kubernetes import client
from kubernetes.dynamic import DynamicClient


logger = logging.getLogger(__name__)

def forecast(prometheus_url, past_time_window, future_time_window, min_threshold, max_threshold, model):
    """
    The function returns the number of nodes that must be turned on (positive number) or off (negative number)
    """
    logger.info("CPU forecast started")

    result = 0
    if model == "naive":
        logger.info("naive CPU prediction is started")
        df_cpu, err = load_data_instance_cpu(prometheus_url, past_time_window)
        if err:
            logger.error("error in loading data for forecast")
            return 0
        predicted_cpu_per_instance = []
        grouped = df_cpu.groupby('instance')
        for instance, group_df in grouped:
            pred_cpu = basic_prediction(group_df, past_time_window)
            predicted_cpu_per_instance.append(float(pred_cpu))
            logger.info(f"CPU predicted: {pred_cpu}% for the next {future_time_window} minutes for node {instance}")
        #print(predicted_cpu_per_instance)
        result = evaluate_predicted_cpu(predicted_cpu_per_instance, min_threshold, max_threshold)
        logger.info(f"Scaling result:{result}")
    else:
        logger.error(f"model {model} not implemented. Scaling not performed")
    return result



def create_cluster_configuration(requiredNodes, min_nodes, max_nodes):
    """
    it creates a new CluterConfiguration CR in the Kubernetes cluster
    """
    logger.info(f"create a new ClusterConfiguration resource: required Nodes {requiredNodes}, min_nodes {min_nodes} , max_nodes {max_nodes}")
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
    version = "v1alpha1" 
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
        logging.info(f" ClusterConfiguration successfully created: {cr_name}")
    except ApiException as e:
        logging.error(f"error during ClusterConfiguration creation: {e}")

def read_forecast_cm():
    """
    The function reads the forecast-parameters ConfigMap and updates the default values
    """
    # set some default values
    past_time_window = 5
    future_time_window = 60
    min_threshold = 50
    max_threshold= 80
    forecast_period_in_minutes= 30
    model = "naive"

    # update the CM if values are present
    v1 = client.CoreV1Api()
    try:
        config_map = v1.read_namespaced_config_map(name="forecast-parameters", namespace="dreem")
        cm=config_map.data
        past_time_window = int(
            cm.get("Past_time_window", past_time_window))
        future_time_window = int(
            cm.get("Prometheus_time_window_prediction_in_minutes", future_time_window))
        min_threshold = int(cm.get("Thresholds_min", min_threshold))
        max_threshold = int(cm.get("Thresholds_max", max_threshold))
        forecast_period_in_minutes = int(cm.get("Forecast_period_in_minutes", forecast_period_in_minutes))
        model = cm.get("Prediction_model", model)

    except client.exceptions.ApiException as e:
        logging.error(f"Error during the reading of the ConfigMap: {e}")
        return None


    return  past_time_window,future_time_window, min_threshold, max_threshold, forecast_period_in_minutes, model

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
    local_conf= utils.load_configuration()

    if local_conf:
        return "http://localhost:9090/"
    else:
        return "http://kind-prometheus-kube-prome-prometheus.monitoring.svc.cluster.local/"



def get_active_worker():
    """
    The function returns the number of active machine in the managed cluster, without considering the control-plane
    """
    k8s_client = client.ApiClient()
    dyn_client = DynamicClient(k8s_client)

    # get machines
    machine_resource = dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="Machine")
    machines = machine_resource.get()

    active_nodes = 0
    for machine in machines.items:
        labels = machine.metadata.labels or {}

        if labels["cluster.x-k8s.io/control-plane"] != "":
            active_nodes += 1

    return active_nodes
    


def main():
    logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
    utils.load_configuration()

    past_time_window, future_time_window, min_threshold, max_threshold, forecast_period_in_minutes, model = read_forecast_cm()
    prometheus_url = get_prometheus_url()
    #print(prometheus_url)
    min_nodes, max_nodes = read_cluster_configuration_cm()


    while True:
        active_worker = get_active_worker()
        #print("min, max, active " + min_nodes, max_nodes, active_nodes)
        scaling_label = forecast(prometheus_url, past_time_window, future_time_window, min_threshold, max_threshold, model)
       # print("scaling:", scaling_label)

        # create the CRD only if the cluster configuration (aka number of nodes) has to be updated
        if scaling_label != 0:
            required_worker= active_worker + scaling_label
            #print("required:" ,required_nodes)
            create_cluster_configuration(required_worker, min_nodes, max_nodes)
        logger.info(f"forecast finished, next forecast in {forecast_period_in_minutes} minutes")
        time.sleep(forecast_period_in_minutes * 60)


if __name__ == "__main__":
    main()