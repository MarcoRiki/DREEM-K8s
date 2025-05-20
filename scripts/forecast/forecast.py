
import pandas as pd
import datetime
from prometheus_api_client import PrometheusConnect
#from sklearn.model_selection import train_test_split
from kubernetes import client, config
import numpy as np
import joblib
import urllib3
import logging
from kubernetes.dynamic import DynamicClient
#from tensorflow.keras.models import load_model
#from tensorflow.keras.losses import mse
#from sklearn.preprocessing import MinMaxScaler

import utils

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
logger = logging.getLogger(__name__)

token='i2hJqTcIBa4i7hTRrg5rsCGXEEz3FDmEFqusXqZbGAHkV80tpRWozZG87QaMVmS4E_KJTmgZTba8HZio0h_RUJsD5QFefF4WCJil0EFvRaElJ1q3s9FAzZfHDrJw|1745678673|4AcS9saCuo46pY7T1k3BfobkO5kr7gyVwb69vrvpQP0='

def get_control_plane_ip():
    utils.load_configuration()

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


def load_data_cpu(prometheus_url, rate_interval, time_windows_forecast, query_step):
    """
    This function perform the query to retrieve the data of the CPU load in order to make prediction.
    Query: '100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle"}[RATE_INTERVAL])))'
    """
    logger.info("loading CPU data to make prediction")
    control_plane_ip = get_control_plane_ip()
    cpu_query_test = '100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle"}[' + rate_interval + '])))'  # GET the CPU usage for the entire cluster
    cpu_query = '100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle", exported_instance=~"'+control_plane_ip+':9100"}[' + rate_interval + '])))'
    #prom_client = PrometheusConnect(url=prometheus_url, disable_ssl=True, headers={"Cookie": "_oauth2_proxy=" + token})
    prom_client = PrometheusConnect(url=prometheus_url, disable_ssl=True)
    end_time = datetime.datetime.now()
    start_time = end_time - datetime.timedelta(minutes=time_windows_forecast)
    results = prom_client.custom_query_range(
        query=cpu_query,
        start_time=start_time,
        end_time=end_time,
        step=query_step
    )

    if results:
        logger.info("CPU query has data")

        df = pd.DataFrame([
            {
                "timestamp": value[0],
                "value": float(value[1]),
                **result["metric"]
            }
            for result in results
            for value in result["values"]
        ])

        df["timestamp"] = pd.to_datetime(df["timestamp"], unit="s")

    else:
        df = pd.DataFrame([{ "timestamp": pd.Timestamp.now(),"value": 0.0}])
        logger.info("CPU query has no data, defaulting to 0")


    return df

def create_lstm_dataset(df, input_size=12, output_size=12):
     pass
#     feature_cols = ['value', 'value_lag1','value_lag2', 'hour', 'minute', 'dayofweek']
#     X, y = [], []
#     for i in range(len(df) - input_size - output_size):
#         window = df[feature_cols].iloc[i:i+input_size].values  # shape: (input_size, n_features)
#         target = df['value'].iloc[i+input_size:i+input_size+output_size].values  # shape: (output_size,)
#         X.append(window)
#         y.append(target)
#     return np.array(X), np.array(y)
#
# def make_future_prediction_cpu(df, time_window_prediction, query_step_in_seconds):
#     """
#     The function takes the CPU data and make prediction about its future average load (in a specific time window)
#     """
#     query_step_seconds = int(query_step_in_seconds.rstrip("s"))
#     query_step_decimal =query_step_seconds / 60
#     n_periods  = int(time_window_prediction / query_step_decimal)
#
#     train, test = train_test_split(df, test_size=0.2, shuffle=False)
#     index_future_dates = pd.date_range(start=test['timestamp'].iloc[-1], periods=n_periods + 1, freq=query_step_in_seconds)
#     loaded_model = joblib.load("arima_model.pkl")
#     pred=loaded_model.predict(start=len(df), end=len(df) + n_periods, typ='levels').rename('ARIMA Predictions')
#     # print(comp_pred)
#     pred.index = index_future_dates
#     #pred.plot(legend=True)
#
#     return np.mean(pred)

def make_future_prediction_cpu_LSTM(df,query_step,TIME_WINDOW_FORECAST_IN_MINUTES, TIME_WINDOW_PREDICTION_IN_MINUTES ):
    pass
#     df['value_lag1'] = df['value'].shift(12)  # shift di un'ora
#     df['value_lag2'] = df['value'].shift(24)  # shift di due'ora
#     df['timestamp'] = pd.to_datetime(df['timestamp'])  # Assicura il tipo datetime
#     df['hour'] = df['timestamp'].dt.hour
#     df['minute'] = df['timestamp'].dt.minute
#     df['dayofweek'] = df['timestamp'].dt.dayofweek
#     df = df.dropna()
#
#     FEATURE_COLS = ['value', 'value_lag1', 'value_lag2', 'hour', 'minute', 'dayofweek']
#
#     X, y = create_lstm_dataset(df, input_size=12, output_size=12)
#
#     model = load_model("cpu_forecast_model.h5", custom_objects={'mse': mse}, compile=False)  # Add custom_objects
#     scaler = joblib.load("scaler.pkl")
#
#     # 4. Crea ultima finestra per predizione
#     last_window = df[FEATURE_COLS].iloc[-12:].values  # shape: (input_size, n_features)
#     last_window_scaled = scaler.transform(last_window).reshape(1, 12, len(FEATURE_COLS))
#
#     # 5. Predizione
#     prediction = model.predict(last_window_scaled)[0]
#
#     # 6. Visualizza
#     print(prediction)
#
#     return prediction.mean()

def basic_prediciton(df):
    time_window = pd.Timedelta(minutes=30)
    latest_time = df["timestamp"].max()
    window_df = df[df["timestamp"] >= (latest_time - time_window)]

    return window_df["value"].mean()
