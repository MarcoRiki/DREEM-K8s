
import pandas as pd
import datetime
from prometheus_api_client import PrometheusConnect
from sklearn.model_selection import train_test_split
import numpy as np
import joblib
import urllib3
import logging

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
logger = logging.getLogger(__name__)

token='cH7ysJzIYmtGmZ7Lg1sV3b01B_AjyLV1XgG7RB-mJwCJ42DAUetgANnBvXcXJPVqEkYh3-7lEeSOZEZwe8d-yqfDzLrPTBJRJAMMuBHoYuAe3Rfyy7RDHrQYfS5D|1744705570|Z4QhnHIUVxJAO2oO93SdFeAxiXfNtWJeNc_naHRCNqU='


def load_data_cpu(prometheus_url, rate_interval, time_windows_forecast, query_step):
    """
    This function perform the query to retrieve the data of the CPU load in order to make prediction.
    Query: '100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle"}[RATE_INTERVAL])))'
    """
    logger.info("loading CPU data to make prediction")

    cpu_query = '100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle"}[' + rate_interval + '])))'  # GET the CPU usage for the entire cluster
    prom_client = PrometheusConnect(url=prometheus_url, disable_ssl=True, headers={"Cookie": "_oauth2_proxy=" + token})
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

def make_future_prediction_cpu(df, time_window_prediction, query_step_in_seconds):
    """
    The function takes the CPU data and make prediction about its future average load (in a specific time window)
    """
    query_step_seconds = int(query_step_in_seconds.rstrip("s"))
    query_step_decimal =query_step_seconds / 60
    n_periods  = int(time_window_prediction / query_step_decimal)

    train, test = train_test_split(df, test_size=0.2, shuffle=False)
    index_future_dates = pd.date_range(start=test['timestamp'].iloc[-1], periods=n_periods + 1, freq=query_step_in_seconds)
    loaded_model = joblib.load("arima_model.pkl")
    pred=loaded_model.predict(start=len(df), end=len(df) + n_periods, typ='levels').rename('ARIMA Predictions')
    # print(comp_pred)
    pred.index = index_future_dates
    #pred.plot(legend=True)

    return np.mean(pred)

