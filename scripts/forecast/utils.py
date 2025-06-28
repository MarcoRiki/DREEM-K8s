from kubernetes import config
import logging
import os

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