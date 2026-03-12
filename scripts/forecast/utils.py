from kubernetes import config
import logging
import os
from kubernetes import client
from kubernetes.dynamic import DynamicClient
import base64
import tempfile
import yaml

logger = logging.getLogger(__name__)

INPUT_TIME=2*60
INPUT_WINDOW = int(INPUT_TIME/5)
HORIZON_TIME=1*60
HORIZON = int(HORIZON_TIME/5)
BATCH_SIZE = 512

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



def get_control_plane_ip(cluster_namespace, external_cluster_name):
    """
    Get control plane IP from the external cluster's Node resources
    Args:
        cluster_namespace: namespace where the CAPI cluster is defined
        external_cluster_name: name of the CAPI cluster
    """
    v1 = client.CoreV1Api()
    
    try:
        # Retrieve the kubeconfig secret
        secret_name = f"{external_cluster_name}-kubeconfig"
        secret = v1.read_namespaced_secret(name=secret_name, namespace=cluster_namespace)
        
        # Decode the kubeconfig from the secret
        kubeconfig_data = base64.b64decode(secret.data['value']).decode('utf-8')
        
        # Write kubeconfig to a temporary file
        with tempfile.NamedTemporaryFile(mode='w', delete=False, suffix='.yaml') as temp_kubeconfig:
            temp_kubeconfig.write(kubeconfig_data)
            temp_kubeconfig_path = temp_kubeconfig.name
        
        try:
            # Load the external cluster configuration
            external_api_client = config.new_client_from_config(config_file=temp_kubeconfig_path)
            external_v1 = client.CoreV1Api(external_api_client)
            
            # Get Node resources from the external cluster
            nodes = external_v1.list_node()
            
            cp = []
            for node in nodes.items:
                labels = node.metadata.labels or {}
                
                # Look for control-plane nodes (check both modern and legacy labels)
                is_control_plane = (
                    'node-role.kubernetes.io/control-plane' in labels or
                    'node-role.kubernetes.io/master' in labels
                )
                
                if is_control_plane:
                    addresses = node.status.addresses or []
                    for address in addresses:
                        if address.type == "InternalIP":
                            cp.append(address.address)
                            logger.info(f"Found control plane node {node.metadata.name} with IP: {address.address}")
            
            if len(cp) == 0:
                logger.warning(f"No control plane nodes found in cluster {external_cluster_name}")
                return None
            
            return cp
            
        finally:
            # Clean up temporary file
            if os.path.exists(temp_kubeconfig_path):
                os.unlink(temp_kubeconfig_path)
                
    except Exception as e:
        logger.error(f"Error getting control plane IP: {e}")
        return None

