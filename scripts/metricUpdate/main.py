# Computes the dreemk8s.io/consumption-profile annotation of each worker node as its
# Weighted Average Power (WAP), following Malla & Christensen (IPCCC 2018), Eq. (12):
#
#     WAP_i(t) = sum_u f_t(u) * P_i(u)
#
#   f_i(u)  utilization distribution that the CURRENT cluster workload would induce on
#           node i: at each instant, the demand of the active workers (in cores) is
#           averaged per active node and divided by the capacity of node i, i.e. the
#           average utilization of a cluster made of copies of node i (the paper's
#           "distribution of average utilization"). All nodes are evaluated against the
#           same workload, so WAP differences reflect the hardware only. With equal
#           capacities, f_i(u) is the distribution of the average utilization.
#   P_i(u)  power-utilization curve of node i, estimated from its own CPU and iDRAC
#           power telemetry. Per-level averages and the node capacity are persisted
#           across runs in a ConfigMap, so that levels observed in earlier windows are
#           not lost and powered-off nodes can still be scored.
#
# The same ConfigMap keeps, for logging only, the last WAP values computed for each node
# (wap_history); they are never read back into the computation.
import argparse
import datetime
import json
import math

import numpy as np
import pandas as pd
from kubernetes import client, config
from prometheus_api_client import PrometheusConnect

# iDRAC node_name -> node IP (as exposed by node_exporter)
mapping_ip = {
    "restart-srv05": "192.168.17.85",
    "restart-srv10": "192.168.17.90",
}
# node IP -> Kubernetes node name
mapping_ip_to_node = {v: k for k, v in mapping_ip.items()}

control_plane_ips = ["192.168.11.65"]

ANNOTATION_KEY = "dreemk8s.io/consumption-profile"
LEVELS = np.arange(0, 101)  # discrete utilization levels, in percent


# --------------------------------------------------------------------------- #
# Telemetry                                                                    #
# --------------------------------------------------------------------------- #

def to_level(percent):
    """Utilization in percent -> discrete level floor(u), clipped to [0, 100]."""
    return int(min(100, max(0, math.floor(percent + 1e-9))))


def query_range(prom_client, query, hours, step):
    end_time = datetime.datetime.now()
    start_time = end_time - datetime.timedelta(hours=hours)
    return prom_client.custom_query_range(
        query=query, start_time=start_time, end_time=end_time, step=f"{step}s"
    )


def series_to_frame(result, column_of, scale=1.0):
    """Prometheus range result -> DataFrame (timestamp index, one column per node)."""
    columns = {}
    for node_data in result:
        column = column_of(node_data["metric"])
        if not column:
            continue
        timestamps = [datetime.datetime.fromtimestamp(float(v[0])) for v in node_data["values"]]
        values = [float(v[1]) * scale for v in node_data["values"]]
        columns[column] = pd.Series(values, index=pd.to_datetime(timestamps))
    if not columns:
        return pd.DataFrame()
    return pd.DataFrame(columns).sort_index()


def get_cpu_usage(prom_client, hours, step):
    """CPU utilization in percent (0-100, float) of each worker node; columns are node IPs.
    Each sample is 1 - idle rate over the previous 5 minutes, averaged over the node's cores.
    A node that is powered off has no samples (NaN) at those instants."""
    excluded = "|".join(control_plane_ips)
    query = (
        '1 - avg by (instance) (rate(node_cpu_seconds_total'
        f'{{mode="idle", instance!~"{excluded}.*"}}[5m]))'
    )
    result = query_range(prom_client, query, hours, step)
    return series_to_frame(result, lambda m: m["instance"].split(":")[0], scale=100.0)


def get_power_consumption(prom_client, power_query, hours, step):
    """Power in watts reported by each node's BMC; columns are node IPs."""
    result = query_range(prom_client, power_query, hours, step)
    return series_to_frame(result, lambda m: mapping_ip.get(m.get("node_name")))


def get_cpu_cores_per_node(prom_client):
    """Number of logical CPUs of each worker node currently exporting metrics."""
    excluded = "|".join(control_plane_ips)
    query = f'count(node_cpu_seconds_total{{mode="idle", instance!~"{excluded}.*"}}) by (instance)'
    cores = {}
    for node_data in prom_client.custom_query(query):
        instance = node_data["metric"]["instance"].split(":")[0]
        cores[instance] = int(float(node_data["value"][1]))
    return cores


# --------------------------------------------------------------------------- #
# Workload distribution f_t(u)                                                 #
# --------------------------------------------------------------------------- #

def get_demand_per_active_node(cpu_usage, capacities):
    """Average demand per active worker at each instant, in capacity units (cores):
    d(t) = (1/|A(t)|) * sum_{j in A(t)} c_j * u_j(t). Nodes without samples at an
    instant (e.g. powered off) are not part of A(t)."""
    known = [n for n in cpu_usage.columns if n in capacities]
    missing = set(cpu_usage.columns) - set(known)
    if missing:
        print(f"WARNING: no capacity known for {sorted(missing)}; excluded from the workload")
    busy = cpu_usage[known] / 100.0 * pd.Series({n: capacities[n] for n in known})
    return (busy.sum(axis=1, min_count=1) / busy.notna().sum(axis=1)).dropna()


def get_induced_utilization(demand, capacity):
    """Utilization (percent) that the demand per active node would induce on a node of the
    given capacity, i.e. the average utilization of a cluster made of copies of that node."""
    return (100.0 * demand / capacity).clip(upper=100.0)


def get_workload_distribution(utilization):
    """Empirical PMF of a utilization series: {level: fraction of instants at that level}."""
    levels = utilization.dropna().map(to_level)
    if levels.empty:
        return {}
    freq = levels.value_counts(normalize=True).sort_index()
    return {int(u): float(p) for u, p in freq.items()}


# --------------------------------------------------------------------------- #
# Power-utilization curve P_i(u)                                               #
# --------------------------------------------------------------------------- #

def get_power_per_level(cpu_usage, power, tolerance="1min"):
    """For each node, the average measured power at each utilization level it visited
    in the current window. CPU and power samples are paired by nearest timestamp;
    CPU samples without a power sample within `tolerance` are discarded."""
    per_level = {}
    for node in cpu_usage.columns:
        if node not in power.columns:
            continue
        df_cpu = cpu_usage[[node]].dropna().rename(columns={node: "cpu"})
        df_power = power[[node]].dropna().rename(columns={node: "power"})
        if df_cpu.empty or df_power.empty:
            continue
        merged = pd.merge_asof(
            df_cpu, df_power,
            left_index=True, right_index=True,
            direction="nearest", tolerance=pd.Timedelta(tolerance),
        ).dropna(subset=["power"])
        if merged.empty:
            continue
        merged["level"] = merged["cpu"].map(to_level)
        means = merged.groupby("level")["power"].mean()
        per_level[node] = {int(u): float(p) for u, p in means.items()}
    return per_level


def merge_power_levels(stored, current):
    """Per-level averages from the current window replace the stored ones; levels not
    visited in the current window keep the value from the last window that observed them."""
    merged = {node: dict(levels) for node, levels in stored.items()}
    for node, levels in current.items():
        merged.setdefault(node, {}).update(levels)
    return merged


def isotonic_regression(values):
    """Pool Adjacent Violators: least-squares non-decreasing fit (equal weights)."""
    fitted, weights = [], []
    for value in values:
        fitted.append(float(value))
        weights.append(1)
        while len(fitted) >= 2 and fitted[-2] > fitted[-1]:
            total = weights[-2] + weights[-1]
            fitted[-2] = (fitted[-2] * weights[-2] + fitted[-1] * weights[-1]) / total
            weights[-2] = total
            fitted.pop()
            weights.pop()
    expanded = []
    for value, weight in zip(fitted, weights):
        expanded.extend([value] * weight)
    return np.array(expanded)


def build_power_curve(level_means):
    """Isotonic regression of the per-level averages, then linear interpolation over all
    levels 0-100. Outside the observed range the curve is held at the nearest observed value."""
    xs = sorted(level_means)
    ys = isotonic_regression([level_means[u] for u in xs])
    return dict(zip(LEVELS.tolist(), np.interp(LEVELS, xs, ys).tolist()))


# --------------------------------------------------------------------------- #
# WAP                                                                          #
# --------------------------------------------------------------------------- #

def calculate_wap(distributions, curves):
    """WAP_i = sum_u f_i(u) * P_i(u), in watts."""
    return {
        node: round(sum(p_u * curves[node][u] for u, p_u in distribution.items()), 3)
        for node, distribution in distributions.items()
        if node in curves and distribution
    }


# --------------------------------------------------------------------------- #
# Kubernetes                                                                   #
# --------------------------------------------------------------------------- #

def load_node_profiles(v1, namespace, name):
    """Stored per-node capacity, per-level average power and WAP log:
    ({node: c}, {node: {level: W}}, {node: [{"time": ..., "wap": W}, ...]})."""
    try:
        cm = v1.read_namespaced_config_map(name, namespace)
    except client.exceptions.ApiException as e:
        if e.status == 404:
            return {}, {}, {}
        raise
    capacities, levels, history = {}, {}, {}
    for node, payload in (cm.data or {}).items():
        entry = json.loads(payload)
        if entry.get("capacity") is not None:
            capacities[node] = float(entry["capacity"])
        node_levels = {int(u): float(p) for u, p in entry.get("levels", {}).items()}
        if node_levels:
            levels[node] = node_levels
        if entry.get("wap_history"):
            history[node] = entry["wap_history"]
    return capacities, levels, history


def append_wap_history(history, wap, max_entries):
    """Appends the WAP of this run to each node's log, keeping the newest max_entries."""
    now = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds")
    merged = {node: list(entries) for node, entries in history.items()}
    for node, value in wap.items():
        merged.setdefault(node, []).append({"time": now, "wap": value})
    return {node: entries[-max_entries:] for node, entries in merged.items() if max_entries > 0}


def save_node_profiles(v1, namespace, name, capacities, levels, history):
    data = {
        node: json.dumps({
            "capacity": capacities.get(node),
            "levels": {str(u): round(p, 2) for u, p in sorted(levels.get(node, {}).items())},
            "wap_history": history.get(node, []),
        })
        for node in sorted(set(capacities) | set(levels) | set(history))
    }
    body = client.V1ConfigMap(metadata=client.V1ObjectMeta(name=name, namespace=namespace), data=data)
    try:
        v1.replace_namespaced_config_map(name, namespace, body)
    except client.exceptions.ApiException as e:
        if e.status != 404:
            raise
        v1.create_namespaced_config_map(namespace, body)


def update_consumption_profile(v1, wap):
    for node_ip, value in wap.items():
        node_name = mapping_ip_to_node.get(node_ip)
        if not node_name:
            continue
        body = {"metadata": {"annotations": {ANNOTATION_KEY: str(value)}}}
        try:
            v1.patch_node(node_name, body)
            print(f"Updated {ANNOTATION_KEY} for node {node_name}: {value}")
        except client.exceptions.ApiException as e:
            print(f"Error updating node {node_name}: {e}")


# --------------------------------------------------------------------------- #
# Main                                                                         #
# --------------------------------------------------------------------------- #

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--prometheus-url", default="http://localhost:9090")
    parser.add_argument("--hours", type=int, default=24, help="Observation window T, in hours")
    parser.add_argument("--step", type=int, default=30, help="Sampling period, in seconds")
    parser.add_argument("--power-query", default="sum(idrac_power_supply_output_watts) by (node_name)")
    parser.add_argument("--capacities", default=None,
                        help='JSON {"node IP": capacity} overriding the core count, e.g. with a '
                             'per-node performance score when per-core speed differs')
    parser.add_argument("--curves-namespace", default="default", help="Namespace of the node-profile ConfigMap")
    parser.add_argument("--curves-configmap", default="dreem-power-curves")
    parser.add_argument("--wap-history", type=int, default=100,
                        help="WAP values kept per node in the ConfigMap, for logging only")
    parser.add_argument("--dry-run", action="store_true", help="Compute and print only; no Kubernetes access")
    args = parser.parse_args()

    prometheus = PrometheusConnect(url=args.prometheus_url, disable_ssl=True)

    cpu_usage = get_cpu_usage(prometheus, args.hours, args.step)
    power = get_power_consumption(prometheus, args.power_query, args.hours, args.step)
    if cpu_usage.empty:
        raise SystemExit("No CPU samples in the observation window")

    v1 = None
    stored_capacities, stored_levels, stored_history = {}, {}, {}
    if not args.dry_run:
        config.load_incluster_config()
        v1 = client.CoreV1Api()
        stored_capacities, stored_levels, stored_history = load_node_profiles(
            v1, args.curves_namespace, args.curves_configmap)

    # Capacity c_i: stored values (needed for powered-off nodes), updated with the live
    # core counts, and overridden by --capacities if given
    capacities = dict(stored_capacities)
    capacities.update({n: float(c) for n, c in get_cpu_cores_per_node(prometheus).items()})
    if args.capacities:
        capacities.update({n: float(c) for n, c in json.loads(args.capacities).items()})

    # Power curves P_i(u): current window merged with the levels persisted by previous runs
    levels = merge_power_levels(stored_levels, get_power_per_level(cpu_usage, power))
    curves = {node: build_power_curve(node_levels) for node, node_levels in levels.items() if node_levels}
    for node in sorted(set(capacities) | set(levels)):
        node_levels = levels.get(node)
        if not node_levels:
            print(f"{node}: capacity {capacities.get(node)}, no power observations; WAP not computed")
            continue
        print(f"{node}: capacity {capacities.get(node)}, power observed at {len(node_levels)} levels "
              f"({min(node_levels)}%-{max(node_levels)}%)")

    # Workload distributions f_i(u): the same cluster workload, mapped onto each node's capacity
    demand = get_demand_per_active_node(cpu_usage, capacities)
    distributions = {}
    for node in curves:
        if node not in capacities:
            print(f"WARNING: no capacity known for {node}; WAP not computed")
            continue
        distributions[node] = get_workload_distribution(get_induced_utilization(demand, capacities[node]))

    wap = calculate_wap(distributions, curves)
    print(f"Average demand per active node: {demand.mean():.2f} cores")
    print("WAP per node:")
    for node, value in wap.items():
        mean_u = sum(u * p for u, p in distributions[node].items())
        print(f"  {node}: {value:.2f} W (induced mean utilization {mean_u:.1f}%)")

    if not args.dry_run:
        history = append_wap_history(stored_history, wap, args.wap_history)
        save_node_profiles(v1, args.curves_namespace, args.curves_configmap, capacities, levels, history)
        update_consumption_profile(v1, wap)