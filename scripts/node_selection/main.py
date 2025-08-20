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
consumption_annotation = 'dreemk8s.io/consumption-profile'
maximum_replicas = 'dreemk8s.io/maximum-replicas'

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

    # Ottieni la risorsa Machine
    machine_resource = dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="Machine")

    # Lista tutte le Machines nel namespace
    machines = machine_resource.get()
    cp = []
    for machine in machines.items:
        labels = machine.metadata.labels or {}
        # print("a.", labels['cluster.x-k8s.io/control-plane'])

        # Cerca le macchine di tipo control-plane
        if labels['cluster.x-k8s.io/control-plane'] == "":
            # Machine ha uno status.addresses
            addresses = machine.status.addresses or []
            for address in addresses:
                if address["type"] == "InternalIP":
                    cp.append(address["address"])

    return cp if cp is not None else None

# def get_name_from_ip(ip_add):
#     """
#     This function performs a reverse lookup: from the IP of the node, it gets the name
#     """
#     k8s_client = client.ApiClient()
#     dyn_client = DynamicClient(k8s_client)
#     #  Machine resources
#     machine_resource = dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="Machine")
#
#     machines = machine_resource.get()
#     value = None
#     retry =0
#     while retry < 5 and value is None:
#         for machine in machines.items:
#             addresses = machine.get('status', {}).get('addresses', [])
#             for addr in addresses:
#                 if addr['type'] == 'InternalIP' and addr['address'] == ip_add.partition(":")[0]:
#                     value = machine
#         retry+=1
#     return value


def get_nodes_resource_usage(control_plane_ip, machine_list, prometheus_url):
    """
    This function perform the query to retrieve the data of the CPU load in order to make prediction.
    """
    min_machine= None
    min_machinedep = None
    cpu_usage = dict()
    md_names = dict()
    for m in machine_list:
        machine_name = m.metadata.name
        addresses = m.status.addresses
        machine_ip = ""

        for address in addresses:
            if address["type"] == "InternalIP":
                machine_ip = address["address"]


        if machine_ip not in control_plane_ip:
            min_name, min_cpu, min_md = get_node_cpu_usage(machine_ip, m, prometheus_url)
            logger.info(f"cpu usage for machine {min_cpu}: {min_name}")
            cpu_usage[machine_name] = min_cpu
            md_names[machine_name] = min_md


    if cpu_usage:
        min_machine = min(cpu_usage, key=cpu_usage.get)
        min_machinedep = md_names[min_machine]

    logger.info(f"data retrieved:{cpu_usage}")

    return min_machine, min_machinedep



def get_node_cpu_usage(node_ip, machine, prometheus_url):
    """
    For each node, retrieve the cpu consumption
    """
    logger.info("get node usage data")
    query = ' avg by (exported_instance) (rate(node_cpu_seconds_total{mode!="idle", exported_instance=~"'+node_ip+'.*"}[5m]))'
    prom_client = PrometheusConnect(url=prometheus_url, disable_ssl=True)
    results = prom_client.custom_query(query=query)

    if results:
        logger.info("usage query has data")
        df = pd.DataFrame([
            {
                'instance': d['metric'].get('exported_instance'),
                'value': float(d['value'][1]),
                'name': machine.metadata.name,
                'md': machine.metadata.labels.get("cluster.x-k8s.io/deployment-name")
            }
            for d in results if 'exported_instance' in d['metric']
        ])


    else:
        df = pd.DataFrame([{"exported_instance": "null", "value": math.inf}])
        logger.info("CPU query has no data, defaulting to 0")

    min_name = df.loc[df['value'].idxmin(), 'name']
    min_cpu = df.loc[df['value'].idxmin(), 'value']
    min_md = df.loc[df['value'].idxmin(), 'md']
    return min_name, min_cpu, min_md

def get_prometheus_url():
    """
    Get the url of the prometheus service in the management cluster
    """

    if load_configuration():
        return "http://localhost:9090/"
    else:
        return "http://kind-prometheus-kube-prome-prometheus.monitoring.svc.cluster.local:9090/"


def return_machine_by_profile(profile, machine_resource, md_by_consumption,prometheus_url):
    """
    for a given enrgy profile, return the machine with the lowest cpu usage
    """
    # take a MD
    random_md = random.choice(md_by_consumption[profile])
    random_md_name = random_md.metadata.name
    min_machine = None

    # get the associated machines
    machines = machine_resource.get(
        label_selector=f"cluster.x-k8s.io/deployment-name={random_md_name}"
    )

    cpu_usage = dict()

    for m in machines.items:
        machine_name = m.metadata.name
        addresses = m.status.addresses
        machine_ip = ""
        for address in addresses:
            if address["type"] == "InternalIP":
                machine_ip = address["address"]

        cpu = get_node_cpu_usage(machine_ip, m, prometheus_url)
        cpu_usage[machine_name] = cpu


    if cpu_usage:
        min_machine = min(cpu_usage, key=cpu_usage.get)

    else:
        logger.info("No CPU data available.")


    return random_md_name, min_machine

def label_md(md_resource, md_list):
    no_annotation = []
    yes_annotation = []
    md_by_consumption = {
        "high": [],
        "medium": [],
        "low": []
    }

    # Check if there are MachineDeployments with the annotation
    for md in md_list:
        annotations = md.metadata.annotations or {}

        if any(key.lower() == consumption_annotation.lower() for key in annotations.keys()):
            yes_annotation.append(md)
        else:
            no_annotation.append(md)

    # if some MD have the annotation, set the default value for the other's and retrieve CPU data
    if len(yes_annotation) > 0:
        logger.info("machinedeployment labelled, use sorting algorithm")
        for md in no_annotation:
            annotations = dict(md.metadata.annotations) if md.metadata.annotations else {}
            annotations[consumption_annotation] = "medium"
            md.metadata.annotations = annotations

            md_resource.patch(
                name=md.metadata.name,
                namespace=md.metadata.namespace,
                body={"metadata": {"annotations": annotations}},
                content_type='application/merge-patch+json'
            )

        for m in md_list:
            annotations = dict(m.metadata.annotations) if m.metadata.annotations else {}

            md_by_consumption[annotations[consumption_annotation]].append(m)
    return yes_annotation, no_annotation, md_by_consumption

def scale_down():
    """
    This function selects the machine whose CPU usage is the lowest one
    """
    logger.info("scale DOWN function started")
    prometheus_url = get_prometheus_url()
    k8s_client = client.ApiClient()
    dyn_client = DynamicClient(k8s_client)


    #  retrieve machinedeployment
    md_resource = dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="MachineDeployment")
    md_list = md_resource.get().items

    yes_annotation, no_annotation, md_by_consumption = label_md(md_resource, md_list)

    # if MD do not have the label, use the standard algorithm
    if len(yes_annotation) ==0:
        logger.info("machinedeployment not labelled, choosing the lower used machine")
        control_plane_ip = get_control_plane_ip_managed_cluster()
        if control_plane_ip is None:
            return None, None

        machine_resource= dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="Machine")
        machine_list = machine_resource.get().items

        cpu_min_node, associated_md = get_nodes_resource_usage(control_plane_ip, machine_list, prometheus_url)
        return associated_md, cpu_min_node
    else:

        machine_resource = dyn_client.resources.get(
            api_version="cluster.x-k8s.io/v1beta1",
            kind="Machine"
        )


        if len(md_by_consumption["high"]):
            return return_machine_by_profile("high", machine_resource, md_by_consumption,prometheus_url)
        elif len(md_by_consumption["medium"]):
            return return_machine_by_profile("medium", machine_resource, md_by_consumption, prometheus_url)
        elif len(md_by_consumption["low"]):
            return return_machine_by_profile("low", machine_resource, md_by_consumption, prometheus_url)


     # choose the most power-hungry md and randomly select a machine from there



def scale_up():
    """
    In this version, it randomly chooses a machinedeployment and returns random
    """
    logger.info("scale UP function started")
    load_configuration()
    k8s_client = client.ApiClient()
    dyn_client = DynamicClient(k8s_client)

    # Get MachineDeployments
    md_resource = dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="MachineDeployment")
    md = md_resource.get()

    machine_deployments = md.items
    number_replicas = dict()
    for md in machine_deployments:
        annotations = md.metadata.annotations or {}
        max_replicas = annotations.get(maximum_replicas)
        if max_replicas is not None:
            number_replicas[md.metadata.name] = int(max_replicas)

    scalable_mds = [
        md for md in machine_deployments
        if md.metadata.name in number_replicas and
           md.status.replicas < number_replicas[md.metadata.name]
    ]

    yes_annotation, no_annotation, md_by_consumption = label_md(md_resource, machine_deployments)
    if len(yes_annotation)==0:
        logger.info("md not labelled, picking random")


        selectedMD = random.choice(machine_deployments)
        return selectedMD.metadata.name, "random"

    else:
            for level in ["low", "medium", "high"]:
                # Filter the md which can be scaled
                candidates = [
                    md for md in md_by_consumption[level]
                    if md.metadata.name in number_replicas and
                       getattr(md.status, 'replicas', 1) < number_replicas[md.metadata.name]
                ]
                if candidates:
                    selected_md = random.choice(candidates).metadata.name
                    return selected_md, "random"


            return None, None


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
    logger.info(f"Starting server on port {port}")
    app.run(host=host, port=port)
    load_configuration()