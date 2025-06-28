import random

import math

from flask import Flask, jsonify
import pandas as pd
import logging
from kubernetes import client, config
import os
from kubernetes.dynamic import DynamicClient
from prometheus_api_client import PrometheusConnect

app = Flask(__name__)

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
)

logger = logging.getLogger(__name__)

def load_configuration():
    """
    The function loads the Kubernetes configuration
    """
    local = False
    try:
        if os.path.exists("/var/run/secrets/kubernetes.io/serviceaccount/token"):
            config.load_incluster_config()

        else:
            config.load_kube_config()
            local=True
    except Exception as e:
        logging.error(f"Error during configuration loading: {e}")
        raise
    return local

def get_control_plane_ip_managed_cluster():
    """
    This function returns the IP of the control-plane in the managed cluster.
    This is mainly used to avoid mixing worker metrics with the control-plane ones.
    """
    k8s_client = client.ApiClient()
    dyn_client = DynamicClient(k8s_client)

    # Get Machines
    machine_resource = dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="Machine")
    machines = machine_resource.get()

    for machine in machines.items:
        labels = machine.metadata.labels or {}

        # Machine type control-plane
        if labels['cluster.x-k8s.io/control-plane'] == "":
            # Machine ha uno status.addresses
            addresses = machine.status.addresses or []
            for address in addresses:
                if address["type"] == "InternalIP":
                    ip = address["address"]
                    logger.info(f"control-plane Ip (managed-cluster): {ip}")
                    return ip
    logger.error(f"control-plane Ip not retrieved")
    return None

def get_name_from_ip(ip_add):
    """
    This function performs a reverse lookup: from the IP of the node, it gets the name
    """
    k8s_client = client.ApiClient()
    dyn_client = DynamicClient(k8s_client)

    #  Machine resources
    machine_resource = dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="Machine")

    machines = machine_resource.get()
    value =None
    retry =0
    #print(machines.items)
    while retry < 5 and value is None:
        #print(retry)
        for machine in machines.items:
            #print(machine['status']['addresses'])
            addresses = machine.get('status', {}).get('addresses', [])
            for addr in addresses:
                if addr['type'] == 'InternalIP' and addr['address'] == ip_add.partition(":")[0]:
                    #print(machine['metadata']['name'])
                    #print("ip", ip_add)
                    value = machine
        retry+=1
    return value


def get_nodes_resource_usage(prometheus_url,control_plane_ip):
    """
    This function perform the query to retrieve the data of the CPU load in order to make prediction.
    """
    logger.info("get CPU usage data")
    query= ' avg by (exported_instance) (rate(node_cpu_seconds_total{mode!="idle"}[5m]))'

    prom_client = PrometheusConnect(url=prometheus_url, disable_ssl=True)
    results = prom_client.custom_query(query=query)
    #print(results)
    if results:
        logger.info("usage query has data")
        df = pd.DataFrame([
            {
                'instance': d['metric'].get('exported_instance'),
                'value': float(d['value'][1])
            }
            for d in results if 'exported_instance' in d['metric']
        ])



    else:
        df = pd.DataFrame([{"exported_instance": "null", "value": math.inf}])
        logger.info("CPU query has no data, defaulting to 0")

    # skip control-plane (on that nodes, pods are not schedulated, therefore its consumption does not count
    df = df[df['instance'] != control_plane_ip+':9100']
    min = df.loc[df['value'].idxmin(), 'instance']


    return min

def get_prometheus_url():
    """
    Get the url of the prometheus service in the management cluster
    """
    local_conf= load_configuration()

    if local_conf:
        return "http://localhost:9090/"
    else:
        return "http://kind-prometheus-kube-prome-prometheus.monitoring.svc.cluster.local:9090/"


def scale_down():
    """
    This function selects the machine whose CPU usage is the lowest one

    """
    logger.info("scale DOWN function started")
    prometheus_url = get_prometheus_url()
    logger.info(f"Prometheus URL: {prometheus_url}")
    control_plane_ip = get_control_plane_ip_managed_cluster()
    cpu_min_node_ip = get_nodes_resource_usage(prometheus_url, control_plane_ip)
    associated_md ="None"
    node = "None"
    cpu_min_node = get_name_from_ip(cpu_min_node_ip)
    #print(cpu_min_node)
    if cpu_min_node is not None:
        associated_md = cpu_min_node.metadata.labels.get("cluster.x-k8s.io/deployment-name")
        node = cpu_min_node.metadata.name

    return associated_md, node

def scale_up():
    """
    In this version, it randomly chooses a machinedeployment and returns random
    """
    logger.info("scale UP function started")

    k8s_client = client.ApiClient()
    dyn_client = DynamicClient(k8s_client)

    # Get MachineDeployments
    md_resource = dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="MachineDeployment")
    md = md_resource.get()

    machine_deployments = md.items

    if machine_deployments:
        selectedMD = random.choice(machine_deployments)
        return selectedMD.metadata.name, "random"
    else:
        return "null", "null"


@app.route("/nodes/scaleUp", methods=["GET"])
def handle_scale_up():
    logger.info("Request at: /nodes/scaleUp")
    md, node = scale_up()
    response = {"machineDeployment": md, "selectedNode": node}
    logger.info(f"scale up response: {response}")
    return jsonify(response), 200

@app.route("/nodes/scaleDown", methods=["GET"])
def handle_scale_down():
    logger.info("Request at: /nodes/scaleDown")
    md, node = scale_down()
    response = {"machineDeployment": md, "selectedNode": node}
    logger.info(f"scale down response: {response}")
    return jsonify(response), 200

if __name__ == "__main__":
    host="0.0.0.0"
    port=8000
    #load_configuration()
    logger.info(f"Starting server on port {port}")
    app.run(host=host, port=port)

