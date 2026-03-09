import random
import string
import time
import argparse
from kubernetes.client.rest import ApiException
from forecast import load_data_instance_cpu, basic_prediction, evaluate_predicted_cpu, LSTM_torch_forecast_CPU
import logging
import utils
from kubernetes import client
from kubernetes.dynamic import DynamicClient
import torch
import torch.nn as nn

from utils import *


class LSTMForecast(nn.Module):
    def __init__(self, hidden=256, layers=3, horizon=30):
        super().__init__()
        self.lstm = nn.LSTM(
            input_size=1,
            hidden_size=hidden,
            num_layers=layers,
            batch_first=True
        )
        self.fc = nn.Linear(hidden, horizon)

    def forward(self, x):
        out, _ = self.lstm(x)
        out = out[:, -1, :]
        return self.fc(out)

logger = logging.getLogger(__name__)

def forecast(prometheus_url, past_time_window, future_time_window, min_threshold, max_threshold, model, mean_time_to_boot):
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

    elif model == "LSTM":

        logger.info("LSTM CPU prediction is started")
        df_cpu, err = load_data_instance_cpu(prometheus_url, INPUT_TIME)
        if err:
            logger.error("error in loading data for forecast")
            return 0
        
        # Carica il modello PyTorch
        try:
            lstm_model = torch.load("lstm_cpu_model_full.pt", weights_only=False, map_location=torch.device('cpu'))
            lstm_model.eval()
            logger.info("PyTorch LSTM model loaded successfully")
        except Exception as e:
            logger.error(f"Error loading PyTorch model: {e}")
            return 0
        
        predicted_cpu_per_instance = []
        grouped = df_cpu.groupby('instance')
        
        # Itera su ogni istanza e fa il forecast
        for instance, group_df in grouped:
            logger.info(f"Processing forecast for instance: {instance}")
            
            try:
                pred_cpu = LSTM_torch_forecast_CPU(group_df, lstm_model, mean_time_to_boot)
                if pred_cpu is None:
                    return None
                pred_cpu = max(pred_cpu, 0)  # Assicura che sia non negativo
                predicted_cpu_per_instance.append(float(pred_cpu))
                logger.info(f"CPU predicted mean load: {pred_cpu:.2f}% for node {instance}")
            except Exception as e:
                logger.error(f"Error forecasting for instance {instance}: {e}")
                # In caso di errore, usa l'ultimo valore disponibile
                if len(group_df) > 0:
                    last_value = group_df['cpu_util_percent'].iloc[-1]
                    predicted_cpu_per_instance.append(float(last_value))
                    logger.warning(f"Using last known value {last_value:.2f}% for node {instance}")
        
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
        if phase not in ["Finished", "Failed"]:
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
    min_threshold = 65
    max_threshold= 45
    forecast_period_in_minutes= 30
    model = "LSTM"
    mean_time_to_boot = 1
    enabled = True

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
        mean_time_to_boot = int(cm.get("Mean_time_to_boot", mean_time_to_boot))
        enabled = cm.get("Enabled", str(enabled)) == "true"
    except client.exceptions.ApiException as e:
        logging.error(f"Error during the reading of the ConfigMap: {e}")
        return None

    return  past_time_window,future_time_window, min_threshold, max_threshold, forecast_period_in_minutes, model, mean_time_to_boot, enabled

def read_cluster_configuration_cm():
    """
       The function reads the clusterConfiguration-parameters ConfigMap and updates the default values
       """
    # set some default values
    min_nodes=1
    max_nodes=5

    # update the CM if values are present
    v1 = client.CoreV1Api()
    try:
        config_map = v1.read_namespaced_config_map(name="cluster-configuration-parameters", namespace="dreem")
        cm = config_map.data
        min_nodes = int(cm.get("minNodes", min_nodes))
        max_nodes = int(cm.get("MaxNodes", max_nodes))


    except client.exceptions.ApiException as e:
        logging.error(f"Error during the reading of the ConfigMap: {e}")
        return None

    return min_nodes, max_nodes

def get_prometheus_url():
    local_conf= utils.load_configuration()

    if local_conf:
        return "http://localhost:9090/"
    else:
        return "http://kind-prometheus-kube-prome-prometheus.monitoring.svc.cluster.local:9090"



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
    parser = argparse.ArgumentParser(description='Forecast script for DREEM-K8s')
    parser.add_argument('--start-after', type=int, default=0, 
                        help='Minutes to wait before starting the first forecast (default: 0)')
    args = parser.parse_args()
    
    logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
    utils.load_configuration()

    past_time_window, future_time_window, min_threshold, max_threshold, forecast_period_in_minutes, model, mean_time_to_boot, enabled = read_forecast_cm()
    prometheus_url = get_prometheus_url()

    min_nodes, max_nodes = read_cluster_configuration_cm()

    # Sleep iniziale prima del primo forecast
    if args.start_after > 0:
        logger.info(f"Waiting {args.start_after} minutes before starting first forecast")
        time.sleep(args.start_after * 60)

    while True:
        
        if not enabled:
            logger.info("forecast is disabled, skipping forecast and scaling")
            time.sleep(forecast_period_in_minutes * 60)
            continue
        
        active_worker = get_active_worker()
        scaling_label = forecast(prometheus_url, past_time_window, future_time_window, min_threshold, max_threshold, model, mean_time_to_boot)
        print("is enabled?", enabled)
        
        # create the CRD only if the cluster configuration (aka number of nodes) has to be updated
        if scaling_label != 0 and scaling_label is not None:
            required_worker= active_worker + scaling_label
            if required_worker >= int(min_nodes) and required_worker < int(max_nodes):
                create_cluster_configuration(required_worker, min_nodes, max_nodes)
            else:
                logger.info("reached infrastructure constaints, scaling not possible")
        logger.info(f"forecast finished, next forecast in {forecast_period_in_minutes} minutes")
        time.sleep(forecast_period_in_minutes * 60)


if __name__ == "__main__":
    main()