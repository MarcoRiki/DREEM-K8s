
import pandas as pd
import datetime
from prometheus_api_client import PrometheusConnect
import torch
import torch.nn as nn
from utils import *
import numpy as np
import urllib3
import logging

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
logger = logging.getLogger(__name__)


def load_data_instance_cpu(prometheus_url, past_time_window, cluster_namespace, external_cluster_name):
    """
    This function perform the query to retrieve the data of the CPU load in order to make prediction.
    Query: '100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle"}[RATE_INTERVAL])))'
    Returns resampled data at 5-minute intervals for PyTorch LSTM model.
    """
    logger.info("loading CPU data to make prediction")

    control_plane_ips = get_control_plane_ip(cluster_namespace, external_cluster_name)

    # Costruisci la query PromQL
    if control_plane_ips and len(control_plane_ips) > 0:
        # Aggiungi ":9100" a ogni IP
        ips_with_port = [f"{ip}:9100" for ip in control_plane_ips]
        # Costruisci la regex per l'esclusione
        regex = "|".join(ips_with_port)
        cpu_query = (
            '100*(1 - (avg(rate(node_cpu_seconds_total{mode="idle", '
            f'exported_instance!~"{regex}", exported_job="node-exporter"}}[5m])) by (exported_instance)))'
        )
    else:
        # Se non ci sono control plane IPs, query senza esclusioni
        logger.warning("No control plane IPs found, querying all instances")
        cpu_query = (
            '100*(1 - (avg(rate(node_cpu_seconds_total{mode="idle", '
            'exported_job="node-exporter"}[2m])) by (exported_instance)))'
        )
    
    logger.info(f"Constructed PromQL query: {cpu_query}")
    
    prom_client = PrometheusConnect(url=prometheus_url, disable_ssl=True)
    end_time = datetime.datetime.now()
    start_time = end_time - datetime.timedelta(minutes=past_time_window)
    results = prom_client.custom_query_range(
        query=cpu_query,
        start_time=start_time,
        end_time=end_time,
        step="15s"
    )

    if not results:
        logger.info("CPU query has no data, defaulting to empty DataFrame")
        return pd.DataFrame(), True

    logger.info("CPU query has data")

    data = []

    for metric in results:
        instance = metric['metric']['exported_instance']
        for timestamp, value in metric['values']:
            data.append({
                'ds': pd.to_datetime(timestamp, unit='s'),
                'cpu_util_percent': float(value),
                'instance': instance
            })

    # Crea il DataFrame
    df = pd.DataFrame(data)
    df = df.sort_values(['instance', 'ds'])

    # Resample a 5 minuti e interpola per ogni istanza
    dfs_resampled = []
    for instance, df_instance in df.groupby('instance'):
        df_instance = df_instance.set_index('ds').sort_index()
        df_resampled = df_instance[['cpu_util_percent']].resample('5min').mean()
        df_resampled = df_resampled.interpolate(method='linear', limit_direction='both')
        df_resampled['instance'] = instance
        df_resampled = df_resampled.reset_index()
        dfs_resampled.append(df_resampled)

    df_final = pd.concat(dfs_resampled, ignore_index=True)
    logger.info(f"Resampled data to 5min intervals: {len(df_final)} rows for {len(dfs_resampled)} instances")

    return df_final, False



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
    if any(v > max_threshold for v in values): # if only one is above the threshold, it scales up
        return 1
    elif all(v < min_threshold for v in values): # all the node must be below the threshold to scale-down
        return -1
    else:
        return 0

    
def LSTM_torch_forecast_CPU(df_instance, model, mean_time_to_boot):
    """
    Fa il forecast per una singola istanza usando il modello PyTorch LSTM.
    
    Args:
        df_instance: DataFrame con colonne 'ds' e 'cpu_util_percent' per una singola istanza
        model: Modello PyTorch già caricato
        mean_time_to_boot: Tempo medio di boot in minuti
    
    Returns:
        Media del forecast escludendo il tempo di boot
    """
    # Valori di normalizzazione dal training (dal notebook)
    cpu_mean = 37.661774760007766
    cpu_std = 15.296503574604664
    
    # Estrai la serie CPU
    cpu_series = df_instance['cpu_util_percent'].values
    
    # Verifica che ci siano abbastanza dati
    if len(cpu_series) < INPUT_WINDOW:
        logger.warning(f"Not enough data for forecast: {len(cpu_series)} < {INPUT_WINDOW}. Returning last value.")
        return None
    
    device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
    model.eval()

    # Prendi gli ultimi INPUT_WINDOW punti
    x = torch.tensor(cpu_series[-INPUT_WINDOW:], dtype=torch.float32)
    # Normalizza
    x = ((x - cpu_mean) / cpu_std).unsqueeze(0).unsqueeze(-1).to(device)

    # Fai la predizione
    with torch.no_grad():
        pred = model(x).cpu().numpy()[0]

    # Denormalizza
    pred_denorm = pred * cpu_std + cpu_mean
    
    # Escludi i primi minuti necessari per il boot
    points_to_skip = int(mean_time_to_boot / 5)
    pred_valid = pred_denorm[points_to_skip:] if points_to_skip < len(pred_denorm) else pred_denorm
    logger.info(f"skipping first {points_to_skip} points due to boot time ({mean_time_to_boot} min), valid predictions: {len(pred_valid)}")
    logger.info(f"Predicted values (denormalized): {pred_denorm}")
    logger.info(f"Predicted values after excluding boot time: {pred_valid}")
    # Ritorna l'90° percentile del forecast
    return float(np.percentile(pred_valid, 90))
    

def LSTM_univariate_CPU(df, model, mean_time_to_boot):
    #best_model-3.keras
    #X_max = 93.0
    #X_min = 3
    #window_size= 20
    
    #model_12.keras
    # X_max = 91
    # X_min = 18
    #window_size= 20
    
     #model_0.keras - version 89
    # X_max = 96
    # X_min = 0
    # window_size= 25
    
     #model_0-2.keras - models63
    X_max = 95
    X_min = 0
    window_size= 15
    
    
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
    points_to_not_consider = int(mean_time_to_boot) * 4 # query step is 30s, hence 2 points per minute
    y_pred_valid = y_pred_denorm[points_to_not_consider:]
    #print("input values:", df['cpu_smooth'])
   # print("new values:", y_pred_valid)
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