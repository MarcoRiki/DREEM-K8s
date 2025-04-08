import os
import time
from kubernetes import client, config
from kubernetes.client.rest import ApiException
from forecast import load_data_cpu, make_future_prediction_cpu
import logging



def forecast(prometheus_url, rate_interval, time_windows_forecast, time_window_prediction, query_step_in_seconds, min_threshold, max_threshold):
    """
    The function returns the number of nodes that must be turned on (positive number) or off (negative number)
    """

    df_cpu = load_data_cpu(prometheus_url, rate_interval, time_windows_forecast, query_step_in_seconds)
    pred_cpu = make_future_prediction_cpu(df_cpu, time_window_prediction, query_step_in_seconds)
    print(pred_cpu)

    if pred_cpu > max_threshold:
        print("return 1")
        return 1
    else:
        if pred_cpu < min_threshold:
            print("return -1")
            return -1
        else:
            print("return 0")
            return 0


def update_crd(scaling_label):
    """
    The function takes the scaling label (aka the number of nodes to start up or shut down) and update the ClusterConfiguration CRD
    """

    api = client.CustomObjectsApi()

    group = "cluster.dreemk8s"
    version = "v1aplha1"
    plural = "clusterconfigurations"
    namespace = "default"
    name= "clusterconfiguration-sample"

    try:
        cluster_config = api.get_namespaced_custom_object(
            group=group,
            version=version,
            namespace=namespace,
            plural=plural,
            name=name
        )

        cluster_config["spec"]["scalingLabel"] = scaling_label

        api.patch_namespaced_custom_object(
            group=group,
            version=version,
            namespace=namespace,
            plural=plural,
            name=name,
            body=cluster_config
        )

    except ApiException as e:
        logging.error(f"Error during the CRD update: {e}")

def read_cm():
    """
    The function reads the forecast-parameters ConfigMap and updates the default values
    """
    # set some default values
    prometheus_url = 'https://prometheus.crownlabs.polito.it/'
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
        config_map = v1.read_namespaced_config_map(name="forecast-parameters", namespace="default")
        cm=config_map.data
        prometheus_url = cm.get("Prometheus_URL", prometheus_url)
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


    return prometheus_url, rate_interval, time_window_forecast_in_minutes,time_window_prediction_in_minutes, query_step_in_seconds, min_threshold, max_threshold, forecast_period_in_minutes

def load_configuration():
    """
    The function loads the Kubernetes configuration
    """
    try:
        # Controlla se siamo in un cluster Kubernetes
        if os.path.exists("/var/run/secrets/kubernetes.io/serviceaccount/token"):
            config.load_incluster_config()
        else:
            config.load_kube_config()
    except Exception as e:
        logging.error(f"Error during configuration loading: {e}")
        raise

def main():
    logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
    load_configuration()

    prometheus_url, rate_interval, time_windows_forecast, time_window_prediction, query_step_in_seconds, min_threshold, max_threshold, forecast_period_in_minutes = read_cm()

    while True:
        logging.info("Forecast started")
        scaling_label = forecast(prometheus_url, rate_interval, time_windows_forecast, time_window_prediction, query_step_in_seconds, min_threshold, max_threshold)
        # update the CRD only if the cluster configuration (aka number of nodes has to be updated)
        if scaling_label != 0:
            update_crd(scaling_label)
        time.sleep(forecast_period_in_minutes * 60)


if __name__ == "__main__":
    main()