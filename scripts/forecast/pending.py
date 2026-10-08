"""
Rilevamento dei pod non schedulabili, usato dallo scale-up reattivo (profilo QOS)
e dal veto sugli scale-down del forecast.

Un pod in attesa non consuma CPU, quindi il forecast non lo vede: questo modulo
fornisce il segnale che la sola CPU non puo' dare.
"""
import logging
from datetime import datetime, timezone

from kubernetes import client
from kubernetes.utils import parse_quantity

logger = logging.getLogger(__name__)

CONTROL_PLANE_LABELS = ("node-role.kubernetes.io/control-plane", "node-role.kubernetes.io/master")


def _is_worker(node):
    labels = node.metadata.labels or {}
    return not any(label in labels for label in CONTROL_PLANE_LABELS)


def _pod_requests(pod):
    """
    CPU (core) e memoria (byte) richieste dal pod, come le conta il kube-scheduler:
    il massimo tra la somma dei container e il singolo init container piu' grande.
    """
    def total(containers):
        cpu = mem = 0
        for c in containers or []:
            requests = (c.resources.requests if c.resources else None) or {}
            cpu += parse_quantity(requests.get("cpu", "0"))
            mem += parse_quantity(requests.get("memory", "0"))
        return cpu, mem

    cpu, mem = total(pod.spec.containers)
    for init_container in pod.spec.init_containers or []:
        init_cpu, init_mem = total([init_container])
        cpu, mem = max(cpu, init_cpu), max(mem, init_mem)
    return cpu, mem


def _fits_empty_node(pod, node):
    """
    True se il pod starebbe sul nodo vuoto: nodeSelector soddisfatto e richieste
    entro l'allocatable. Serve a scartare i pod che nessun nodo aggiuntivo potrebbe
    ospitare, che altrimenti scalerebbero il cluster fino a maxNodes senza effetto.
    Le affinity required non sono valutate.
    """
    labels = node.metadata.labels or {}
    for key, value in (pod.spec.node_selector or {}).items():
        if labels.get(key) != value:
            return False
    allocatable = node.status.allocatable or {}
    cpu, mem = _pod_requests(pod)
    return (cpu <= parse_quantity(allocatable.get("cpu", "0"))
            and mem <= parse_quantity(allocatable.get("memory", "0")))


def _is_daemonset_pod(pod):
    return any(ref.kind == "DaemonSet" for ref in pod.metadata.owner_references or [])


def list_unschedulable_pods():
    """
    Ritorna una lista di (namespace/nome, secondi di attesa) dei pod che il
    kube-scheduler ha marcato Unschedulable e che un worker vuoto potrebbe ospitare.
    I secondi di attesa partono dalla lastTransitionTime della condition
    PodScheduled, quindi sopravvivono a un riavvio del forecast.
    Ritorna None se l'API non risponde: il chiamante deve distinguere
    "nessun pod in attesa" da "impossibile saperlo".
    """
    v1 = client.CoreV1Api()
    try:
        pods = v1.list_pod_for_all_namespaces(field_selector="status.phase=Pending").items
        workers = [n for n in v1.list_node().items if _is_worker(n)]
    except client.exceptions.ApiException as e:
        logger.error(f"[Pending] Impossibile leggere pod/nodi: {e}")
        return None

    now = datetime.now(timezone.utc)
    unschedulable = []
    for pod in pods:
        name = f"{pod.metadata.namespace}/{pod.metadata.name}"
        if pod.spec.node_name or pod.metadata.deletion_timestamp:
            continue
        # preemption in corso: lo scheduler ha gia' trovato posto liberandolo
        if pod.status.nominated_node_name:
            continue
        # i pod DaemonSet sono legati a un nodo specifico, accenderne un altro non li aiuta
        if _is_daemonset_pod(pod):
            continue
        scheduled = next((c for c in pod.status.conditions or [] if c.type == "PodScheduled"), None)
        if scheduled is None or scheduled.status != "False" or scheduled.reason != "Unschedulable":
            continue
        if not any(_fits_empty_node(pod, node) for node in workers):
            logger.debug(f"[Pending] {name} non entra in nessun worker, ignorato")
            continue
        since = scheduled.last_transition_time or pod.metadata.creation_timestamp
        unschedulable.append((name, (now - since).total_seconds()))
    return unschedulable
