from kubernetes import config
import logging
import os
from kubernetes import client
import base64
import tempfile
import yaml

logger = logging.getLogger(__name__)
TIME_STEP_MINUTES = 1
INPUT_TIME_EXTENDED=1*60
INPUT_WINDOW_EXTENDED = int(INPUT_TIME_EXTENDED/TIME_STEP_MINUTES)
HORIZON_TIME_EXTENDED=1*30
HORIZON_EXTENDED = int(HORIZON_TIME_EXTENDED/TIME_STEP_MINUTES)

INPUT_TIME_SHORT=1*20
INPUT_WINDOW_SHORT = int(INPUT_TIME_SHORT/TIME_STEP_MINUTES)
HORIZON_TIME_SHORT=1*10
HORIZON_SHORT = int(HORIZON_TIME_SHORT/TIME_STEP_MINUTES)

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
    """
    Get control plane IP from  Node resources
    
    """
    v1 = client.CoreV1Api()
        
    try:
            # Get Node resources 
            nodes = v1.list_node()
            
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
                logger.warning(f"No control plane nodes found in cluster")
                return None
            
            return cp
            
    except Exception as e:
        logger.error(f"Error getting control plane IP: {e}")
        return None

from kubernetes import client, config

def get_schedulable_node_ips():
    """
    Ritorna un set di IP dei soli nodi worker attivi e schedulabili.
    Esclude i nodi che sono in Cordon, Drain o in fase di spegnimento.
    """
    try:
        # La configurazione K8s e' gia' stata caricata all'avvio da
        # load_configuration(): ricaricarla a ogni chiamata e' inutile e
        # nasconde gli errori. (config.load_kubeconfig non esiste: il nome
        # corretto e' config.load_kube_config, il typo faceva sempre fallire
        # il ramo locale.)
        v1 = client.CoreV1Api()
        nodes = v1.list_node(_preload_content=True)
        schedulable_ips = set()
        
        for node in nodes.items:
            labels = node.metadata.labels or {}

            # 0. Salta cp
            if 'node-role.kubernetes.io/control-plane' in labels or 'node-role.kubernetes.io/master' in labels:
                logger.info(f"[Filtro Nodi] Ignorato nodo {node.metadata.name} perché è un nodo di controllo (control-plane/master).")
                continue
            # 1. Salta i nodi che hanno l'impostazione unschedulable (Cordon/Drain attivo)
            if node.spec.unschedulable:
                logger.info(f"[Filtro Nodi] Ignorato nodo {node.metadata.name} perché in stato unschedulable (Cordon/Drain attivo).")
                continue

            # 2. Salta i nodi che NON sono in stato Ready
            is_ready = False
            if node.status.conditions:
                for condition in node.status.conditions:
                    if condition.type == "Ready" and condition.status == "True":
                        is_ready = True
                        break
            
            if not is_ready:
                logger.info(f"[Filtro Nodi] Ignorato nodo {node.metadata.name} perché in stato NotReady.")
                continue
                
            # Cerca l'indirizzo IP interno del nodo nelle InternalIP addresses
            for address in (node.status.addresses or []):
                if address.type == "InternalIP":
                    schedulable_ips.add(address.address)
                    logger.info(f"[Filtro Nodi] Nodo {node.metadata.name} schedulabile con IP: {address.address}")
                    break
            else:
                logger.warning(f"[Filtro Nodi] Nodo {node.metadata.name} senza InternalIP, ignorato.")
        return schedulable_ips
    except Exception as e:
        logger.error(f"Errore nel recupero dello stato dei nodi da K8s API: {e}")
        # In caso di errore (es. permessi RBAC mancanti nel pod), ritorna None
        return None