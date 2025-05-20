import math
from operator import contains

from flask import Flask, jsonify
import pandas as pd
import logging
from kubernetes import client, config
from kubernetes.client.rest import ApiException
import os
from kubernetes.dynamic import DynamicClient
from prometheus_api_client import PrometheusConnect
import datetime

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
        # Controlla se siamo in un cluster Kubernetes
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
                    ip = address["address"]
                    logger.info(f"control-plane Ip (managed-cluster): {ip}")
                    return ip
    logger.error(f"control-plane Ip not retrieved")
    return None

def get_control_plane_ip_management_cluster():
    v1 = client.CoreV1Api()


    nodes = v1.list_node()
    control_plane_ip = None
    for node in nodes.items:
        labels = node.metadata.labels or {}
        if "node-role.kubernetes.io/control-plane" in labels or "node-role.kubernetes.io/master" in labels:
            for addr in node.status.addresses:
                if addr.type == "InternalIP":
                    control_plane_ip = addr.address
                    break
        if control_plane_ip:
            break

    if not control_plane_ip:
        raise Exception("Impossibile trovare l'IP del nodo control-plane")



    return control_plane_ip


def get_name_from_ip(ip_add):

    k8s_client = client.ApiClient()
    dyn_client = DynamicClient(k8s_client)

    #  Machine resources
    machine_resource = dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="Machine")

    # Lista tutte le Machines nel namespace
    machines = machine_resource.get()
    value =None
    retry =0
    #print(machines.items)
    while retry < 1 and value is None:
        #print(retry)
        for machine in machines.items:
            #print(machine['status']['addresses'])
            addresses = machine.get('status', {}).get('addresses', [])
            for addr in addresses:
                #print("addr['address']",addr['address'])
                #print("ip_add.partition(:)[0] ", ip_add.partition(":")[0])
                if addr['type'] == 'InternalIP' and addr['address'] == ip_add.partition(":")[0]:
                    print(machine['metadata']['name'])
                    #print("ip", ip_add)
                    value = machine['metadata']['name']
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

    df = df[df['instance'] != control_plane_ip+':9100']
    #print(df)
    min = df.loc[df['value'].idxmin(), 'instance']


    return min

def get_prometheus_url():
    service_name= "kind-prometheus-kube-prome-prometheus"
    namespace = "monitoring"
    local_conf= load_configuration()

    if local_conf:
        return "http://localhost:9090/"

    v1 = client.CoreV1Api()

    control_plane_ip = get_control_plane_ip_management_cluster()
    # Ottieni la porta NodePort del servizio
    service = v1.read_namespaced_service(name=service_name, namespace=namespace)
    node_port = None
    for port in service.spec.ports:
        if port.node_port:
            node_port = port.node_port
            break

    if not node_port:
        raise Exception("impossible finding Nodeport port")

    return f"http://{control_plane_ip}:{node_port}/"

def scale_down():
    logger.info("scale DOWN function started")
    prometheus_url = get_prometheus_url()
    logger.info(f"Prometheus URL: {prometheus_url}")
    control_plane_ip = get_control_plane_ip_managed_cluster()
   # print("control plane IP:",control_plane_ip)
    cpu_min_node_ip = get_nodes_resource_usage(prometheus_url, control_plane_ip)

    cpu_min_node = get_name_from_ip(cpu_min_node_ip)

    return cpu_min_node

def scale_up():
    logger.info("scale UP function started")
    logger.info("select random node")
    return "random"

@app.route("/nodes/scaleUp", methods=["GET"])
def handle_scale_up():
    selected_node = scale_up()
    response = {"selectedNode": selected_node}
    logger.info(f"scale up response: {response}")
    return jsonify(response), 200

@app.route("/nodes/scaleDown", methods=["GET"])
def handle_scale_down():
    logger.info("Richiesta ricevuta a /nodes/scaleDown")
    selected_node = scale_down()
    response = {"selectedNode": selected_node}
    logger.info(f"scale down response: {response}")
    return jsonify(response), 200

if __name__ == "__main__":
    host="0.0.0.0"
    port=8000
    #load_configuration()
    logger.info(f"Starting server on port {port}")
    app.run(host=host, port=port)

