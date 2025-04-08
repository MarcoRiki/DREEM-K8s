from operator import contains

from flask import Flask, jsonify
import logging
from kubernetes import client, config
from kubernetes.client.rest import ApiException
import os

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
    try:
        # Controlla se siamo in un cluster Kubernetes
        if os.path.exists("/var/run/secrets/kubernetes.io/serviceaccount/token"):
            config.load_incluster_config()
        else:
            config.load_kube_config()
    except Exception as e:
        logging.error(f"Error during configuration loading: {e}")
        raise

def get_nodes_resource_usage():

    api = client.CustomObjectsApi()

    try:
        nodes_metrics = api.list_cluster_custom_object("metrics.k8s.io", "v1beta1", "nodes")
    except client.exceptions.ApiException as e:
        if e.status == 404:
            raise RuntimeError("L'API metrics.k8s.io non è disponibile. Assicurati che Metrics Server sia installato nel cluster.") from e
        else:
            raise


    min_cpu_usage = float('inf')
    min_node_name = None

    for node in nodes_metrics.get('items', []):

        node_name = node['metadata']['name']
        cpu_usage = node['usage']['cpu']
        memory_usage = node['usage']['memory']

        if "control-plane" in node_name:
            continue

        # Convert CPU usage to a numeric value (assuming it's in millicores)
        cpu_usage_number = int(cpu_usage[:-1])  # Remove the last character (e.g., 'n') and convert to int
        memory_usage_number = int(memory_usage[:-2])  # Remove the last two characters (e.g., 'Ki') and convert to int
        
        # Check if this node has the lowest CPU usage so far
        if cpu_usage_number < min_cpu_usage:
            min_cpu_usage = cpu_usage_number
            min_node_name = node_name

    min = min_node_name
    return min


def scale_up():
    logger.info("scale UP function started")
    cpu_min_node = get_nodes_resource_usage()
    return cpu_min_node

def scale_down():
    logger.info("scale DOWN function started")

    return "prova"

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
    load_configuration()
    logger.info(f"Starting server on port {port}")
    app.run(host=host, port=port)

