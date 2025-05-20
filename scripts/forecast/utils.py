from kubernetes import client, config
import logging
import os
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
            local = True
    except Exception as e:
        logging.error(f"Error during configuration loading: {e}")
        raise
    return local