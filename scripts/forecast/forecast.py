
import pandas as pd
import datetime
from prometheus_api_client import PrometheusConnect

from kubernetes import client

import urllib3
import logging
from kubernetes.dynamic import DynamicClient


urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
logger = logging.getLogger(__name__)

def get_control_plane_ip():

    k8s_client = client.ApiClient()
    dyn_client = DynamicClient(k8s_client)

    # Ottieni la risorsa Machine
    machine_resource = dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="Machine")

    # Lista tutte le Machines nel namespace
    machines = machine_resource.get()

    for machine in machines.items:
        labels = machine.metadata.labels or {}
        #print("a.", labels['cluster.x-k8s.io/control-plane'])

        # Cerca le macchine di tipo control-plane
        if labels['cluster.x-k8s.io/control-plane'] == "":
            # Machine ha uno status.addresses
            addresses = machine.status.addresses or []
            for address in addresses:
                if address["type"] == "InternalIP":

                    return address["address"]

    return None


def load_data_instance_cpu(prometheus_url, past_time_window):
    """
    This function perform the query to retrieve the data of the CPU load in order to make prediction.
    Query: '100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle"}[RATE_INTERVAL])))'
    """
    logger.info("loading CPU data to make prediction")
    control_plane_ip = get_control_plane_ip()
    cpu_query = '100*(1- (avg(rate(node_cpu_seconds_total{mode="idle", exported_instance!~"'+control_plane_ip+':9100", exported_job="node-exporter"}[1m])) by (exported_instance)))'
    prom_client = PrometheusConnect(url=prometheus_url, disable_ssl=True)
    end_time = datetime.datetime.now()
    start_time = end_time - datetime.timedelta(minutes=past_time_window)
    results = prom_client.custom_query_range(
        query=cpu_query,
        start_time=start_time,
        end_time=end_time,
        step="15s"
    )

    if results:
        logger.info("CPU query has data")

        data = []

        for metric in results:
            instance = metric['metric']['exported_instance']
            for timestamp, value in metric['values']:
                data.append({
                    'timestamp': pd.to_datetime(timestamp, unit='s'),
                    'value': float(value),
                    'instance': instance
                })

        # Crea il DataFrame
        df = pd.DataFrame(data)
        #print(df)

        df["timestamp"] = pd.to_datetime(df["timestamp"], unit="s")

    else:
        df = pd.DataFrame([{ "timestamp": pd.Timestamp.now(),"value": 0.0, "instance": ""}])
        logger.info("CPU query has no data, defaulting to 0")
        return df, True


    return df, False



def basic_prediction(df, prediction_window):
    """
    it takes the last X minutes and average the utilization
    :param df:
    :return:
    """
    time_window = pd.Timedelta(minutes=prediction_window)
    latest_time = df["timestamp"].max()
    window_df = df[df["timestamp"] >= (latest_time - time_window)]
    #print(window_df["value"])

    return window_df["value"].mean()

def evaluate_predicted_cpu(values, min_threshold, max_threshold):
    if all(v > max_threshold for v in values):
        return 1
    elif all(v < min_threshold for v in values):
        return -1
    else:
        return 0
