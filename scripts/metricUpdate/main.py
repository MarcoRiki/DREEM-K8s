# script to update the dreemk8s.io/consumption-profile metric on the MachineDeployment
# access prometheus api to get the latest power consumption and CPU usage and update the metric accordingly
import datetime
from prometheus_api_client import PrometheusConnect
import collections
import pandas as pd
import numpy as np
import argparse
from kubernetes import client, config

mapping_ip = {
    "restart-srv05": "192.168.17.85",
    "restart-srv10": "192.168.17.90",
}

# Inversione del dizionario per mappare gli IP di Prometheus ai nomi iDRAC
mapping_ip_to_node = {v: k for k, v in mapping_ip.items()}

control_plane_ips = ["192.168.11.65"]


def get_cpu_usage(prom_client, hours, step): 
    # DF structure: timestamp index, columns are node names (IPs), values are CPU usage percentages (0-100 int)
    query = f'1 - avg by (instance) (rate(node_cpu_seconds_total{{mode="idle", instance!~"{("|").join(control_plane_ips)}.*" }}[5m]))'
    end_time = datetime.datetime.now()
    start_time = end_time - datetime.timedelta(hours=hours)
    result = prom_client.custom_query_range(
        query=query,
        start_time=start_time,
        end_time=end_time,
        step=f"{step}s"
    )
    cpu_usage = pd.DataFrame()
    for node_data in result:
        instance = node_data['metric']['instance'].split(':')[0]  
        values = node_data['values']  # list of [timestamp, value]
        timestamps = [datetime.datetime.fromtimestamp(float(v[0])) for v in values]
        usage_values = [int(float(v[1]) * 100) for v in values]  # convert to percentage (0-100)
        node_series = pd.Series(data=usage_values, index=timestamps, name=instance)
        cpu_usage = pd.concat([cpu_usage, node_series], axis=1)

    return cpu_usage

def get_power_consumption(prom_client, consumption_metric, hours, step):
    end_time = datetime.datetime.now()
    start_time = end_time - datetime.timedelta(hours=hours)
    query = f'{consumption_metric}'
    result = prom_client.custom_query_range(
        query=query,
        start_time=start_time,
        end_time=end_time,
        step=f"{step}s"
    )

    power_consumption = pd.DataFrame()
    for node_data in result:
        node_name = mapping_ip.get(node_data['metric']['node_name'])
        if not node_name:
            continue
        values = node_data['values']  # list of [timestamp, value]
        timestamps = [datetime.datetime.fromtimestamp(float(v[0])) for v in values]
        consumption_values = [int(float(v[1])) for v in values]  # power consumption in watts
        node_series = pd.Series(data=consumption_values, index=timestamps, name=node_name)
        power_consumption = pd.concat([power_consumption, node_series], axis=1)
    
    return power_consumption

def get_workload_distribution(utilization_series):
    cleaned_utilization = utilization_series.dropna()
    n = len(cleaned_utilization)
    if n == 0:
        return {}

    # Conta le frequenze di ogni valore intero 0-100
    counts = collections.Counter(cleaned_utilization.astype(int))

    # Genera f(u) come dizionario {utilizzazione_intera_0_100: probabilità}
    sorted_items = sorted(counts.items(), key=lambda x: x[0])
    f_u = {int(u): count / n for u, count in sorted_items}

    return f_u

def isotonic_regression(values):
    # Pool Adjacent Violators Algorithm per ottenere una sequenza non decrescente
    fitted_values = []
    block_weights = []

    for value in values:
        fitted_values.append(float(value))
        block_weights.append(1.0)

        while len(fitted_values) >= 2 and fitted_values[-2] > fitted_values[-1]:
            total_weight = block_weights[-2] + block_weights[-1]
            averaged_value = (
                fitted_values[-2] * block_weights[-2]
                + fitted_values[-1] * block_weights[-1]
            ) / total_weight

            fitted_values[-2] = averaged_value
            block_weights[-2] = total_weight
            fitted_values.pop()
            block_weights.pop()

    expanded_values = []
    for value, weight in zip(fitted_values, block_weights):
        expanded_values.extend([value] * int(weight))

    return np.array(expanded_values)

def get_cpu_cores_per_node(prom_client):
    query = 'count(node_cpu_seconds_total{mode="idle", instance!~"' + ('|').join(control_plane_ips) + '.*"}) by (instance)'
    result = prom_client.custom_query(query)
    cpu_cores = {}
    for node_data in result:
        instance = node_data['metric']['instance'].split(':')[0]
        cpu_cores[instance] = int(float(node_data['value'][1]))
    return cpu_cores   

def map_cpu_to_power(cpu_usage, total_power, interpolate_missing=False):
    cpu_usage.index = pd.to_datetime(cpu_usage.index)
    total_power.index = pd.to_datetime(total_power.index)

    cpu_usage = cpu_usage.sort_index()
    total_power = total_power.sort_index()

    power_mapping = {}

    for node_cpu in cpu_usage.columns:
        # Allinea i nomi delle colonne usando la mappatura IP se differiscono
        node_power = node_cpu
        if node_cpu not in total_power.columns:
            # Prova a convertire l'host/IP nel formato iDRAC node_name
            node_power = mapping_ip_to_node.get(node_cpu)
            
        if not node_power or node_power not in total_power.columns:
            continue

        df_cpu = cpu_usage[[node_cpu]].dropna()
        df_power = total_power[[node_power]].dropna()

        # Allineamento temporale nearest entro 1 minuto
        merged = pd.merge_asof(
            df_cpu,
            df_power,
            left_index=True,
            right_index=True,
            suffixes=('_cpu', '_power'),
            direction='nearest',
            tolerance=pd.Timedelta("1m"),
        )

        # Interpolazione temporale dei Watt mancanti
        merged["_power"] = merged[f"{node_power}_power"].interpolate(method="time")
        merged["_cpu"] = merged[f"{node_cpu}_cpu"]

        merged = merged.dropna(subset=["_cpu", "_power"])

        mapping = {}
        for cpu_value, power_value in zip(merged["_cpu"], merged["_power"]):
            cpu_percent = int(round(cpu_value))
            cpu_percent = max(0, min(100, cpu_percent))

            if cpu_percent not in mapping:
                mapping[cpu_percent] = []
            mapping[cpu_percent].append(int(power_value))

        node_mapping = {
            cpu_percent: int(np.mean(power_values))
            for cpu_percent, power_values in mapping.items()
        }

        if not node_mapping:
            continue

        if interpolate_missing:
            sorted_node_mapping = dict(sorted(node_mapping.items()))
            adjusted_values = isotonic_regression(list(sorted_node_mapping.values()))
            monotone_series = pd.Series(adjusted_values, index=sorted_node_mapping.keys())
            
            # Reindex su tutti gli interi possibili da 0 a 100
            full_index = range(0, 101)
            s_interpolated = monotone_series.reindex(full_index).interpolate(method="linear", limit_direction="both")

            power_mapping[node_cpu] = {
                int(cpu): int(round(power)) for cpu, power in s_interpolated.items()
            }
        else:
            power_mapping[node_cpu] = {
                int(cpu): node_mapping[cpu] for cpu in sorted(node_mapping.keys())
            }

    return power_mapping


def calculate_node_wap(node_distributions, mapping_power):
    wap = {}
    for node, distribution in node_distributions.items():
        if node not in mapping_power:
            continue
            
        total_wap = 0.0
        # Itera direttamente sulla distribuzione reale f(u) osservata
        for u_int, f_u in distribution.items():
            # Cerca la potenza corrispondente. Se manca il punto esatto, 
            # usa il valore di fallback del punto più vicino o 0
            p_u = mapping_power[node].get(u_int)
            
            if p_u is None:
                # Fallback di sicurezza se l'interpolazione 0-100 è disattivata
                available_keys = list(mapping_power[node].keys())
                if available_keys:
                    closest_key = min(available_keys, key=lambda x: abs(x - u_int))
                    p_u = mapping_power[node][closest_key]
                else:
                    p_u = 0
            
            # WAP parziale = f(u) * P(u)
            total_wap += f_u * p_u
            
        wap[node] = total_wap.__round__(3)  # Arrotonda a 3 decimali
    return wap

def update_create_consumption_profile_metric(wap):
    # prendi la risorsa Node corrispondente e aggiorna o crea la metrica custom dreemk8s.io/consumption-profile con il valore di WAP calcolato
    # wap: {node ip: wap_value}
    config.load_incluster_config()  # Carica la configurazione del cluster quando eseguito all'interno di Kubernetes
    v1 = client.CoreV1Api()
    for node_ip, wap_value in wap.items():
        node_name = mapping_ip_to_node.get(node_ip)
        if not node_name:
            continue
        
        # Recupera il nodo
        try:
            node = v1.read_node(node_name)
        except client.exceptions.ApiException as e:
            print(f"Error fetching node {node_name}: {e}")
            continue

        # Aggiorna o crea la metrica custom
        if node.metadata.annotations is None:
            node.metadata.annotations = {}
        
        annotation_key = "dreemk8s.io/consumption-profile"
        node.metadata.annotations[annotation_key] = str(wap_value)

        try:
            v1.patch_node(node_name, node)
            print(f"Updated {annotation_key} for node {node_name} with value {wap_value}")
        except client.exceptions.ApiException as e:
            print(f"Error updating node {node_name}: {e}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--prometheus-url", default="http://localhost:9090")
    parser.add_argument("--hours", type=int, default=24, help="Number of hours to consider for metrics")
    parser.add_argument("--step", type=int, default=30, help="Step in seconds for Prometheus queries")
    args = parser.parse_args()

    prometheus = PrometheusConnect(
        url=args.prometheus_url,
        disable_ssl=True,
    )

    cpu_nodes = get_cpu_usage(prometheus, args.hours, args.step)
    print("CPU Usage per Node (last {} hours):".format(args.hours))
    print(cpu_nodes.head(10))
    print("\n")

    node_consumption = get_power_consumption(prometheus, "sum(idrac_power_supply_output_watts) by (node_name)", args.hours, args.step)
    print("Power Consumption per Node (last {} hours):".format(args.hours))
    print(node_consumption.head(10))
    print("\n")
    
    node_distributions = {}
    # CORREZIONE QUI: Rimosso l'operatore walrus inline che causava il SyntaxError
    for node in cpu_nodes.columns:
        workload_distribution = get_workload_distribution(cpu_nodes[node])
        node_distributions[node] = workload_distribution

    cpu_cores = get_cpu_cores_per_node(prometheus)
    print("CPU Cores per Node:")
    for node, cores in cpu_cores.items():
        print(f"{node}: {cores} cores")
    print("\n")

    # Mappatura della potenza (con Isotonic Regression e interpolazione lineare 0-100 attiva)
    mapping_power = map_cpu_to_power(cpu_nodes, node_consumption, interpolate_missing=True)
    print("CPU to Power Mapping (with interpolation 0-100):")
    for node, mapping in mapping_power.items():
        # Stampiamo solo i primi punti per brevità di log
        short_mapping = {k: mapping[k] for k in sorted(mapping.keys())[:10]}
        print(f"{node}: {short_mapping} ...")
    print("\n")

    # Calcolo della WAP Assoluta (In Watt pesati sul workload)
    wap = calculate_node_wap(node_distributions, mapping_power)

    print("WAP per Node:")
    for node, value in wap.items():
        print(f"{node}: {value:.2f}")
    print("\n")

    # Normalized Wap
    # normalized_wap = {}
    # for node, value in wap.items():
    #     if node in cpu_cores and cpu_cores[node] > 0:
    #         normalized_wap[node] = value / cpu_cores[node]
    #     else:
    #         normalized_wap[node] = 0.0
    
    # print("Normalized WAP per Core on Node:")
    # for node, value in normalized_wap.items():
    #     print(f"{node}: {value:.4f} W/core")


    # aggiungi (o aggiorna) la metrica dreemk8s.io/consumption-profile sui Nodes con il valore di WAP calcolato
    update_create_consumption_profile_metric(wap)