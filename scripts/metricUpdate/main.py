# script to update the dreemk8s.io/consumption-profile metric on the MachineDeployment
# access prometheus api to get the latest power consumption and CPU usage and update the metric accordingly
#from kubernetes import client, config
import datetime
from prometheus_api_client import PrometheusConnect
import base64
import requests
import prometheus_client as p
import numpy
import collections
import pandas as pd

mapping_bmc = {
    "restart-srv11": "192.168.24.91",
    "restart-srv12": "192.168.24.92",
    "restart-srv13": "192.168.24.93",
    #"restart-srv01": "192.168.24.81"
}

mapping_ip = {
    "restart-srv11": "192.168.103.25",
    "restart-srv12": "192.168.103.22",
    "restart-srv13": "192.168.103.23",
    "restart-srv01": "192.168.24.81"
}
    


def get_cpu_usage(prom_client): # get the last 24 hours CPU usage values (rounded to int) per node and create a df
    #DF structure: timestamp index, columns are node names, values are CPU usage percentages (0-100 int)
    query = f'1 - avg by (instance) (rate(node_cpu_seconds_total{{mode="idle"}}[5m]))'
    end_time = datetime.datetime.now()
    start_time = end_time - datetime.timedelta(hours=5)
    result = prom_client.custom_query_range(
        query=query,
        start_time=start_time,
        end_time=end_time,
        step="15s"
    )
    print(result)
    cpu_usage = pd.DataFrame()
    for node_data in result:
        instance = node_data['metric']['instance']
        values = node_data['values']  # list of [timestamp, value]
        timestamps = [datetime.datetime.fromtimestamp(float(v[0])) for v in values]
        usage_values = [int(float(v[1]) * 100) for v in values]  # convert to percentage
        node_series = pd.Series(data=usage_values, index=timestamps, name=instance)
        cpu_usage = pd.concat([cpu_usage, node_series], axis=1)

    return cpu_usage

def get_power_consumption(prom_client, consumption_metric):
    query = f'{consumption_metric}'
    result = prom_client.query(query)
    if result and 'data' in result and 'result' in result['data'] and len(result['data']['result']) > 0:
        return float(result['data']['result'][0]['value'][1])
    return 0.0

def get_workload_distribution(utilization_list):
    n = len(utilization_list)
    # Conta le frequenze di ogni valore 0-100
    counts = collections.Counter(utilization_list)
    
    # Crea f(u) come dizionario {utilizzazione: probabilità}
    # u è normalizzato a 0-1 (quindi 83% diventa 0.83)
    f_u = {(u) / 100: count / n for u, count in counts.items()}
    
    return f_u   

def get_cpu_per_node(prom_client):
    # get number of cpu cores per node
    query = 'count(node_cpu_seconds_total{mode="idle"}) by (instance)'
    result = prom_client.custom_query(query)
    cpu_cores = {}
    for node_data in result:
        instance = node_data['metric']['instance']
        cpu_cores[instance] = int(float(node_data['value'][1]))
    return cpu_cores   

def interpolate_missing_values(utilization_list): #logarithmic interpolation of missing values
    #print("Original list:", utilization_list)
    pass

def map_cpu_to_power(cpu_usage, total_power):
    pass

def getToken(token_url, username, password):
    data = {
        "grant_type": "password",
        "client_id": "monitoring",
        "username": username,
        "password": password,
        "client_secret": "b440c096-c8b0-4323-8082-b61b233a98b7",
    }

    r = requests.post(token_url, data=data, verify=False)
    #print("Status:", r.status_code)
    #print("Body:", r.json())

    return r.json()['access_token']

def prometheus_authenticate(prometheus_url, token):

    prom = PrometheusConnect(
        url=prometheus_url,
        headers={
            "Authorization": f"Bearer {token}"
        },
        disable_ssl=True,
    )
    return prom

if __name__=="__main__":

    prometheus = PrometheusConnect(
        url="localhost:9090",
        disable_ssl=True,
    )

    cpu_nodes = get_cpu_usage(prometheus)
    #node_consumption = get_power_consumption(prometheus, "sum(idrac_power_supply_output_watts) by (instance)")
    # cpu_nodes_interpolated = {}
    # for node, usage in cpu_nodes.items():
    #     cpu_nodes_interpolated[node] = interpolate_missing_values(usage)
    
    # node_distributions = {}
    # for node, usage in cpu_nodes.items():
    #     workload_distribution = get_workload_distribution(usage)
    #     node_distributions[node] = workload_distribution
    
    # cpu_cores = get_cpu_per_node(prometheus)





    
    