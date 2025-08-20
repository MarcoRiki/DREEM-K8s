from kubernetes import config
import logging
import os
from kubernetes import client
from kubernetes.dynamic import DynamicClient

logger = logging.getLogger(__name__)
def load_configuration():
    """
    The function loads the Kubernetes configuration
    """
    logger.info("K8s configuration loaded")
    local = False
    try:
        # Controlla se siamo in un cluster Kubernetes
        if os.path.exists("/var/run/secrets/kubernetes.io/serviceaccount/token"):
            config.load_incluster_config()
        else:
            config.load_kube_config()
            local = True
    except Exception as e:
        logging.error(f"Error during configuration loading: {e}")
        raise
    return local



def get_control_plane_ip():

    k8s_client = client.ApiClient()
    dyn_client = DynamicClient(k8s_client)

    # Ottieni la risorsa Machine
    machine_resource = dyn_client.resources.get(api_version="cluster.x-k8s.io/v1beta1", kind="Machine")

    # Lista tutte le Machines nel namespace
    machines = machine_resource.get()
    cp=[]
    for machine in machines.items:
        labels = machine.metadata.labels or {}
        #print("a.", labels['cluster.x-k8s.io/control-plane'])

        # Cerca le macchine di tipo control-plane
        if labels['cluster.x-k8s.io/control-plane'] == "":
            # Machine ha uno status.addresses
            addresses = machine.status.addresses or []
            for address in addresses:
                if address["type"] == "InternalIP":
                    cp.append(address["address"])


    return cp if cp is not None else None

