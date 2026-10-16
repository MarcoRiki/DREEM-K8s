"""
Menouer et al.'s reactive node autoscaler on top of the DREEM-K8s operator.

Every Menouer_period_seconds the script reads the state of the cluster (loads of the
active workers from node-exporter, Unschedulable pods, powered-off workers), applies
Algorithm 1 (menouer.py) and, when it asks for one node more or one less, creates a
ClusterConfiguration. The operator then picks the node with PROMETHEE and switches it on
or off, through the same NodeHandling (drain, Redfish) that DREEM uses.
"""
import argparse
import logging
import random
import string
import time
from dataclasses import dataclass

import requests
import urllib3
from kubernetes import client
from kubernetes.client.rest import ApiException

import menouer
from pending import list_unschedulable_pods
from utils import load_configuration

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
logger = logging.getLogger(__name__)

CC_GROUP = "cluster.dreemk8s"
CC_VERSION = "v1alpha1"
CC_PLURAL = "clusterconfigurations"
CC_NAMESPACE = "dreem"
CC_TERMINAL_PHASES = ("Finished", "Failed")
# label on the ClusterConfiguration with the branch of Algorithm 1 that created it
CC_TRIGGER_LABEL = "cluster.dreemk8s/trigger"

CONTROL_PLANE_LABELS = ("node-role.kubernetes.io/control-plane", "node-role.kubernetes.io/master")

# Window of the CPU rate. The algorithm reads the load every minute, so the window is
# short; two minutes hold at least four samples at the default 30s scrape interval.
CPU_RATE_WINDOW = "2m"
NODE_EXPORTER = 'job="node-exporter"'
# used and capacity of every resource, per node-exporter instance
LOAD_QUERIES = {
    "cpu": (
        f'count by (instance) (node_cpu_seconds_total{{{NODE_EXPORTER},mode="idle"}})'
        f' - sum by (instance) (rate(node_cpu_seconds_total{{{NODE_EXPORTER},mode="idle"}}[{CPU_RATE_WINDOW}]))',
        f'count by (instance) (node_cpu_seconds_total{{{NODE_EXPORTER},mode="idle"}})',
    ),
    "memory": (
        f'sum by (instance) (node_memory_MemTotal_bytes{{{NODE_EXPORTER}}} - node_memory_MemAvailable_bytes{{{NODE_EXPORTER}}})',
        f'sum by (instance) (node_memory_MemTotal_bytes{{{NODE_EXPORTER}}})',
    ),
    # the root filesystem; the space reserved to root counts as used, as for the pods
    "storage": (
        f'max by (instance) (node_filesystem_size_bytes{{{NODE_EXPORTER},mountpoint="/"}}'
        f' - node_filesystem_avail_bytes{{{NODE_EXPORTER},mountpoint="/"}})',
        f'max by (instance) (node_filesystem_size_bytes{{{NODE_EXPORTER},mountpoint="/"}})',
    ),
}


def read_menouer_params():
    """
    Reads the forecast-parameters ConfigMap: Enabled, which switches the autoscaler on
    and off, and the parameters of Algorithm 1. A missing key keeps its default.
    """
    p = menouer.Params()
    enabled = True
    try:
        cm = client.CoreV1Api().read_namespaced_config_map(name="forecast-parameters", namespace="dreem").data or {}
        # case-insensitive: a missing key leaves the autoscaler enabled
        enabled = str(cm.get("Enabled", enabled)).strip().lower() == "true"
        p.load_up = float(cm.get("Menouer_load_up", p.load_up))
        p.load_down = float(cm.get("Menouer_load_down", p.load_down))
        p.pending_pod_seconds = float(cm.get("Menouer_pending_pod_seconds", p.pending_pod_seconds))
        # at least 1s: with 0 the loop would spin
        p.period_seconds = max(1, int(cm.get("Menouer_period_seconds", p.period_seconds)))
    except ApiException as e:
        logger.error(f"Error reading the forecast-parameters ConfigMap: {e}. Using defaults.")
    except (TypeError, ValueError) as e:
        logger.error(f"Invalid value in forecast-parameters ConfigMap: {e}. Using defaults.")
        p = menouer.Params()
    return p, enabled


def read_cluster_configuration_cm():
    """The node bounds of the cluster-configuration-parameters ConfigMap."""
    min_nodes, max_nodes = 1, 5
    try:
        cm = client.CoreV1Api().read_namespaced_config_map(
            name="cluster-configuration-parameters", namespace="dreem").data or {}
        min_nodes = int(cm.get("minNodes", min_nodes))
        max_nodes = int(cm.get("maxNodes", max_nodes))
    except ApiException as e:
        logger.error(f"Error reading the cluster-configuration-parameters ConfigMap: {e}. Using defaults.")
    except (TypeError, ValueError) as e:
        logger.error(f"Invalid value in cluster-configuration-parameters ConfigMap: {e}. Using defaults.")
        min_nodes, max_nodes = 1, 5
    return min_nodes, max_nodes


def scaling_in_progress():
    """
    True while a ClusterConfiguration has not ended. The operator handles one at a time,
    so the autoscaler acts one node at a time and waits for each action to end. On an
    API error it is assumed in progress, so as not to scale blindly.
    """
    try:
        existing = client.CustomObjectsApi().list_namespaced_custom_object(
            group=CC_GROUP, version=CC_VERSION, namespace=CC_NAMESPACE, plural=CC_PLURAL)
    except ApiException as e:
        logger.error(f"error listing ClusterConfigurations: {e}")
        return True
    for cr in existing.get("items", []):
        phase = cr.get("status", {}).get("phase", "")
        if phase not in CC_TERMINAL_PHASES:
            logger.debug(f"ClusterConfiguration '{cr['metadata']['name']}' in phase '{phase}'")
            return True
    return False


def create_cluster_configuration(required_nodes, min_nodes, max_nodes, trigger):
    """Creates a ClusterConfiguration asking for required_nodes workers. True if created."""
    logger.info(f"create a new ClusterConfiguration resource: required Nodes {required_nodes}, "
                f"min_nodes {min_nodes}, max_nodes {max_nodes}, trigger {trigger}")
    random_string = ''.join(random.choices(string.ascii_lowercase + string.digits, k=8))
    body = {
        "apiVersion": f"{CC_GROUP}/{CC_VERSION}",
        "kind": "ClusterConfiguration",
        "metadata": {
            "name": f"clusterconfiguration-{random_string}",
            "namespace": CC_NAMESPACE,
            "labels": {CC_TRIGGER_LABEL: trigger},
        },
        "spec": {
            "requiredNodes": int(required_nodes),
            "maxNodes": int(max_nodes),
            "minNodes": int(min_nodes),
        },
    }
    try:
        client.CustomObjectsApi().create_namespaced_custom_object(
            group=CC_GROUP, version=CC_VERSION, namespace=CC_NAMESPACE, plural=CC_PLURAL, body=body)
        logger.info(f"ClusterConfiguration successfully created: {body['metadata']['name']}")
        return True
    except ApiException as e:
        logger.error(f"error during ClusterConfiguration creation: {e}")
        return False


@dataclass
class Worker:
    name: str
    ready: bool
    addresses: set


def list_workers():
    """The worker nodes, Ready (active) or not (powered off), with their addresses."""
    workers = []
    for node in client.CoreV1Api().list_node().items:
        labels = node.metadata.labels or {}
        if any(label in labels for label in CONTROL_PLANE_LABELS):
            continue
        ready = any(c.type == "Ready" and c.status == "True" for c in node.status.conditions or [])
        addresses = {a.address for a in node.status.addresses or []}
        workers.append(Worker(node.metadata.name, ready, addresses | {node.metadata.name}))
    return workers


def query_by_instance(prometheus_url, promql):
    """Runs an instant query and returns {instance host: value}, the port stripped."""
    response = requests.get(f"{prometheus_url.rstrip('/')}/api/v1/query",
                            params={"query": promql}, timeout=10, verify=False)
    response.raise_for_status()
    values = {}
    for sample in response.json()["data"]["result"]:
        instance = sample["metric"].get("instance", "")
        host = instance.rsplit(":", 1)[0].strip("[]") if ":" in instance else instance
        values[host] = float(sample["value"][1])
    return values


def cluster_loads(prometheus_url, active):
    """
    L_cpu, L_memory and L_storage of the paper: used x 100 / capacity, both summed over
    the active workers. None if a worker has no sample yet (a node just switched on),
    so that no decision is taken on a partial cluster.
    """
    loads = {}
    for resource, (used_query, capacity_query) in LOAD_QUERIES.items():
        try:
            used = query_by_instance(prometheus_url, used_query)
            capacity = query_by_instance(prometheus_url, capacity_query)
        except (requests.RequestException, KeyError, ValueError) as e:
            logger.error(f"[Menouer] {resource} load query failed: {e}")
            return None
        used_sum = capacity_sum = 0.0
        for worker in active:
            host = next((h for h in worker.addresses if h in used and h in capacity), None)
            if host is None:
                logger.info(f"[Menouer] no {resource} sample for {worker.name} yet, decision skipped")
                return None
            used_sum += used[host]
            capacity_sum += capacity[host]
        loads[resource] = 100.0 * used_sum / capacity_sum if capacity_sum > 0 else 0.0
    return loads


def menouer_step(prometheus_url, params):
    """One run of Algorithm 1."""
    if scaling_in_progress():
        logger.info("[Menouer] a ClusterConfiguration is in progress, decision skipped")
        return
    min_nodes, max_nodes = read_cluster_configuration_cm()
    try:
        workers = list_workers()
    except ApiException as e:
        logger.error(f"[Menouer] cannot list the nodes: {e}")
        return
    active = [w for w in workers if w.ready]
    pending = list_unschedulable_pods()
    if pending is None:
        logger.info("[Menouer] cannot read the pending pods, decision skipped")
        return
    loads = cluster_loads(prometheus_url, active) if active else {"cpu": 0.0, "memory": 0.0, "storage": 0.0}
    if loads is None:
        return

    state = menouer.ClusterState(
        load_cpu=loads["cpu"], load_memory=loads["memory"], load_storage=loads["storage"],
        active_nodes=len(active), pending_nodes=len(workers) - len(active),
        pending_pod_ages=[age for _, age in pending])
    decision = menouer.decide(state, params, min_nodes, max_nodes)
    oldest = f", oldest {max(state.pending_pod_ages):.0f}s" if pending else ""
    logger.info(f"[Menouer] L_cpu {state.load_cpu:.1f}%, L_memory {state.load_memory:.1f}%, "
                f"L_storage {state.load_storage:.1f}%, {len(pending)} pending pod(s){oldest}, "
                f"{state.active_nodes} active and {state.pending_nodes} powered-off worker(s)")
    if decision.action == menouer.NO_ACTION:
        return
    logger.info(f"[Menouer] {'scale-up' if decision.action > 0 else 'scale-down'} ({decision.rule}): {decision.reason}")
    create_cluster_configuration(state.active_nodes + decision.action, min_nodes, max_nodes, decision.rule)


def main():
    parser = argparse.ArgumentParser(description="Menouer et al.'s reactive node autoscaler for DREEM-K8s")
    parser.add_argument('--start-after', type=int, default=0,
                        help='Minutes to wait before the first decision (default: 0)')
    parser.add_argument('--prometheus-url', type=str, default="http://localhost:9090",
                        help='URL of the Prometheus service')
    # accepted so that a Deployment of DREEM's forecaster can run this image unchanged
    parser.add_argument('--rate-interval', type=str, default=None,
                        help=f'Unused: the CPU load is always a rate over {CPU_RATE_WINDOW}')
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
    if args.rate_interval is not None:
        logger.info(f"--rate-interval {args.rate_interval} ignored: the CPU load is a rate over {CPU_RATE_WINDOW}")

    if args.start_after > 0:
        logger.info(f"Waiting {args.start_after} minutes before the first decision")
        time.sleep(args.start_after * 60)

    load_configuration()
    was_enabled = None
    while True:
        started = time.monotonic()
        params, enabled = read_menouer_params()
        if enabled:
            if was_enabled is not True:
                logger.info(f"[Menouer] enabled: Load_up {params.load_up:g}%, Load_down {params.load_down:g}%, "
                            f"Pending_pod {params.pending_pod_seconds:g}s, every {params.period_seconds}s")
            menouer_step(args.prometheus_url, params)
        elif was_enabled is not False:
            logger.info("[Menouer] disabled (Enabled is false), no scaling")
        was_enabled = enabled
        time.sleep(max(0.0, params.period_seconds - (time.monotonic() - started)))


if __name__ == "__main__":
    main()
