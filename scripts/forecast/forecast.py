
import pandas as pd
import datetime
from prometheus_api_client import PrometheusConnect

from utils import *
import numpy as np
import urllib3
import logging

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
logger = logging.getLogger(__name__)



def load_data_instance_cpu(prometheus_url, past_time_window):
    """
    This function perform the query to retrieve the data of the CPU load in order to make prediction.
    Query: '100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle"}[RATE_INTERVAL])))'
    """
    logger.info("loading CPU data to make prediction")

    control_plane_ips = get_control_plane_ip()

    # Aggiungi ":9100" a ogni IP
    ips_with_port = [f"{ip}:9100" for ip in control_plane_ips]

    # Costruisci la regex per l'esclusione
    regex = "|".join(ips_with_port)

    # Costruisci la query PromQL
    cpu_query = (
        '100*(1 - (avg(rate(node_cpu_seconds_total{mode="idle", '
        f'exported_instance!~"{regex}", exported_job="node-exporter"}}[1m])) by (exported_instance)))'
    )
    print(cpu_query)
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

    #print(df)
    return df, False



def basic_prediction(df, prediction_window):
    """
    it takes the last X minutes and average the utilization
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

def LSTM_univariate_CPU(df, model, mean_time_to_boot):
    #X_max = 93.0
    #X_min = 3
    X_max = 91
    X_min = 18
    window_size= 20
    # set column CPU total usage in the dataset
    # | timestamp | value | instance

    #smoothing values
    df['cpu_smooth'] = df['value'].rolling(window=5, min_periods=1).mean()

    # normalize data
    df['cpu_normalized'] = (df['cpu_smooth'] - X_min) / (X_max - X_min)
    # create the dataset for LSTM
    lstm_df = create_realtime_input( df['cpu_normalized'], window_size)

    # make prediction
    y_pred = model.predict(lstm_df)

    # return predicted value
    y_pred_flatten = y_pred.flatten()
    y_pred_denorm = y_pred_flatten * (X_max - X_min) + X_min

    # exclude the first minutes which are needed to boot up a new server
    points_to_not_consider = int(mean_time_to_boot) * 4 # query step is 15s, hence 4 points per minute
    y_pred_valid = y_pred_denorm[points_to_not_consider:]

    return y_pred_valid.mean()


def create_realtime_input(series, window_size=10):
    """
    It extracts the last time window from the serie to make a realtime prediction

    Returns:
        np.array: array with shape (1, window_size, 1) for the LSTM
    """
    recent_window = series[-window_size:]
    X_seq = np.array([[val] for val in recent_window])  # shape: (window_size, 1)
    return X_seq[np.newaxis, ...]  #  (1, window_size, 1)