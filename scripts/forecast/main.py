import os
import random
import string
import time
import argparse
from dataclasses import dataclass, field
from kubernetes.client.rest import ApiException
from forecast import (
    get_cluster_nodes_cores,
    load_data_instance_cpu,
    load_request_rate,
    compute_anticipation_factor,
    basic_prediction,
    evaluate_predicted_cpu,
    LSTM_torch_forecast_CPU,
    DEFAULT_MIN_SIGNIFICANT_REQUEST_RATE,
    DEFAULT_MIN_SIGNIFICANT_CPU_PERCENT,
)
import logging
import utils
from pending import list_unschedulable_pods
from kubernetes import client
import torch
import torch.nn as nn

from utils import *


class LSTMForecastExtended(nn.Module):
    def __init__(self, hidden=128, layers=4, horizon=30, recency_bias=2.0):
        super().__init__()
        self.lstm = nn.LSTM(
            input_size=1,
            hidden_size=hidden,
            num_layers=layers,
            batch_first=True
        )
        self.attn = nn.Linear(hidden, 1)
        self.recency_bias = recency_bias
        self.fc = nn.Linear(hidden, horizon)

    def forward(self, x):
        out, _ = self.lstm(x)
        if hasattr(self, "attn"):
            scores = self.attn(out).squeeze(-1)
        else:
            scores = torch.zeros(out.size(0), out.size(1), device=out.device)
        positions = torch.linspace(0.0, 1.0, steps=out.size(1), device=out.device).unsqueeze(0)
        recency_bias = getattr(self, "recency_bias", 2.0)
        scores = scores + recency_bias * positions
        weights = torch.softmax(scores, dim=1)
        context = torch.sum(out * weights.unsqueeze(-1), dim=1)
        return self.fc(context)
    

class LSTMForecastShort(nn.Module):
    def __init__(self, hidden=128, layers=3, horizon=10, recency_bias=1.0):
        super().__init__()
        self.lstm = nn.LSTM(
            input_size=1,
            hidden_size=hidden,
            num_layers=layers,
            batch_first=True
        )
        self.attn = nn.Linear(hidden, 1)
        self.recency_bias = recency_bias
        self.fc = nn.Linear(hidden, horizon)

    def forward(self, x):
        out, _ = self.lstm(x)
        if hasattr(self, "attn"):
            scores = self.attn(out).squeeze(-1)
        else:
            scores = torch.zeros(out.size(0), out.size(1), device=out.device)
        positions = torch.linspace(0.0, 1.0, steps=out.size(1), device=out.device).unsqueeze(0)
        recency_bias = getattr(self, "recency_bias", 1.0)
        scores = scores + recency_bias * positions
        weights = torch.softmax(scores, dim=1)
        context = torch.sum(out * weights.unsqueeze(-1), dim=1)
        return self.fc(context)

logger = logging.getLogger(__name__)

# Percorsi dei checkpoint risolti rispetto alla posizione dello script, non alla
# working directory del processo.
_SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
MODEL_PATH_EXTENDED = os.path.join(_SCRIPT_DIR, "4_best_model_cpu_1min_60min_30min_2bias.pth")
MODEL_PATH_SHORT = os.path.join(_SCRIPT_DIR, "3_best_model_cpu_1min_20min_10min_1bias.pth")

# Default usati quando le chiavi corrispondenti mancano nella ConfigMap
# forecast-parameters, che e' l'unica sorgente di configurazione per questi due
# parametri. Query vuota = anticipo basato sul request rate disattivato, con
# fallback automatico sulla derivata della CPU.
DEFAULT_REQUEST_RATE_QUERY = ""
DEFAULT_ANTICIPATION_MULTIPLIER = 1.5

lstm_model_extended = None
lstm_model_short = None


def compute_load_anticipation(prometheus_url, df_cpu, request_rate_query,
                              anticipation_lead_minutes, multiplier_anticipo,
                              min_request_rate=DEFAULT_MIN_SIGNIFICANT_REQUEST_RATE,
                              min_cpu_percent=DEFAULT_MIN_SIGNIFICANT_CPU_PERCENT):
    """
    Calcola il fattore di anticipo da applicare alla CPU predetta.

    Priorita' al request rate applicativo: le richieste si accodano *prima* che
    la CPU salga, quindi la loro derivata anticipa quella della CPU di un tempo
    pari alla profondita' della coda. Se la metrica applicativa non e'
    disponibile si ripiega sulla derivata della CPU aggregata, che e' lo stesso
    ragionamento applicato a un segnale piu' lento.
    """
    if multiplier_anticipo <= 1.0:
        return 1.0

    rate_series = load_request_rate(prometheus_url, past_time_window=30,
                                    request_rate_query=request_rate_query)
    if rate_series is not None:
        return compute_anticipation_factor(
            rate_series,
            lead_time_minutes=anticipation_lead_minutes,
            max_multiplier=multiplier_anticipo,
            min_significant_level=min_request_rate,
            label="request-rate",
        )

    # Fallback: derivata della CPU media di cluster
    if df_cpu is None or df_cpu.empty:
        return 1.0
    cpu_mean_series = (
        df_cpu.groupby('ds')['cpu_util_percent'].mean().sort_index().values
    )
    logger.info("[Anticipo] Request rate non disponibile: fallback sulla derivata della CPU di cluster.")
    return compute_anticipation_factor(
        cpu_mean_series,
        lead_time_minutes=anticipation_lead_minutes,
        max_multiplier=multiplier_anticipo,
        min_significant_level=min_cpu_percent,
        label="cpu-trend",
    )


def forecast(prometheus_url, past_time_window, future_time_window, min_threshold, max_threshold,
             model, mean_time_to_boot, rate_interval="5m",
             multiplier_anticipo=DEFAULT_ANTICIPATION_MULTIPLIER,
             request_rate_query=DEFAULT_REQUEST_RATE_QUERY,
             anticipation_lead_minutes=5.0,
             min_request_rate=DEFAULT_MIN_SIGNIFICANT_REQUEST_RATE,
             min_cpu_percent=DEFAULT_MIN_SIGNIFICANT_CPU_PERCENT):
    """
    The function returns the number of nodes that must be turned on (positive number) or off (negative number)
    """
    logger.info("CPU forecast started")
    result = 0

    # 1. Recuperiamo la mappa dei core dei nodi (IP -> Cores)
    node_cores_map = get_cluster_nodes_cores(prometheus_url)

    if model == "naive":
        logger.info("naive CPU prediction is started")
        df_cpu, err = load_data_instance_cpu(prometheus_url, past_time_window, rate_interval)
        if err:
            logger.error("error in loading data for forecast")
            return 0
        if df_cpu.empty:
            logger.info("No active instances found. Skipping scaling decision.")
            return 0

        anticipation = compute_load_anticipation(
            prometheus_url, df_cpu, request_rate_query,
            anticipation_lead_minutes, multiplier_anticipo,
            min_request_rate, min_cpu_percent)

        predicted_cpu_dict = {}
        grouped = df_cpu.groupby('instance')
        for instance, group_df in grouped:
            pred_cpu = basic_prediction(group_df, past_time_window)
            pred_cpu = min(float(pred_cpu) * anticipation, 100.0)
            predicted_cpu_dict[instance] = pred_cpu
            logger.info(f"CPU predicted: {pred_cpu}% for the next {future_time_window} minutes for node {instance}")

        result = result = evaluate_predicted_cpu(predicted_cpu_dict, node_cores_map, min_threshold, max_threshold)
        logger.info(f"Scaling result:{result}")

    elif model == "LSTM":
        logger.info("LSTM CPU prediction is started")
        
        # RIPRISTINO: Usiamo INPUT_TIME (la finestra corretta per l'LSTM) invece di past_time_window
        # Se INPUT_TIME non è definita localmente, la recuperiamo dalle variabili globali/utils come nel vecchio codice
        lstm_past_window_extended = globals().get('INPUT_TIME_EXTENDED', past_time_window)
        
        df_cpu, err = load_data_instance_cpu(prometheus_url, lstm_past_window_extended, rate_interval)
        if err:
            logger.error("error in loading data for forecast")
            return 0
        if df_cpu.empty:
            logger.info("No active instances found. Skipping scaling decision.")
            return 0
        
        # Inizializzazione dei modelli globali (lazy loading).
        # Entrambi i modelli sono caricati insieme: il modello breve e' il
        # fallback obbligatorio per i nodi appena accesi, quindi non si puo'
        # considerare l'inizializzazione riuscita se manca uno dei due.
        global lstm_model_extended
        global lstm_model_short
        if lstm_model_extended is None or lstm_model_short is None:
            try:
                logger.info("Loading PyTorch LSTM models...")
                device = torch.device('cpu')

                model_extended = LSTMForecastExtended(horizon=HORIZON_EXTENDED)
                checkpoint = torch.load(MODEL_PATH_EXTENDED, map_location=device)
                model_extended.load_state_dict(checkpoint['model_state_dict'])
                model_extended.to(device)
                model_extended.eval()

                model_short = LSTMForecastShort(horizon=HORIZON_SHORT)
                checkpoint_short = torch.load(MODEL_PATH_SHORT, map_location=device)
                model_short.load_state_dict(checkpoint_short['model_state_dict'])
                model_short.to(device)
                model_short.eval()

                # Pubblicati solo dopo che entrambi sono pronti
                lstm_model_extended = model_extended
                lstm_model_short = model_short
            except Exception as e:
                logger.error(f"Critical error loading LSTM model file: {e}")
                return 0

        anticipation = compute_load_anticipation(
            prometheus_url, df_cpu, request_rate_query,
            anticipation_lead_minutes, multiplier_anticipo,
            min_request_rate, min_cpu_percent)

        predicted_cpu_dict = {}
        grouped = df_cpu.groupby('instance')

        for instance, group_df in grouped:
            logger.info(f"Processing forecast for instance: {instance}")
            logger.info(f"CPU Data for instance {instance}: {group_df}")
            try:
                pred_cpu = LSTM_torch_forecast_CPU(group_df, lstm_model_extended, mean_time_to_boot, lstm_model_short)
                if pred_cpu is None:
                    continue
                pred_cpu = max(float(pred_cpu), 0.0)
                if anticipation > 1.0:
                    boosted = min(pred_cpu * anticipation, 100.0)
                    logger.info(
                        f"[Anti-Lag] {instance}: predizione {pred_cpu:.2f}% -> {boosted:.2f}% "
                        f"(fattore di anticipo {anticipation:.3f})"
                    )
                    pred_cpu = boosted
                predicted_cpu_dict[instance] = pred_cpu
                logger.info(f"CPU predicted mean load: {pred_cpu:.2f}% for node {instance}")
            except Exception as e:
                logger.error(f"Error forecasting instance {instance}: {e}")
                pass

        if len(predicted_cpu_dict) == 0:
            return 0
            
        result = result = evaluate_predicted_cpu(predicted_cpu_dict, node_cores_map, min_threshold, max_threshold)
        logger.info(f"Scaling result:{result}")

    return result



CC_GROUP = "cluster.dreemk8s"
CC_VERSION = "v1alpha1"
CC_PLURAL = "clusterconfigurations"
CC_NAMESPACE = "dreem"
CC_TERMINAL_PHASES = ("Finished", "Failed")
# label sulla ClusterConfiguration con il percorso che l'ha generata (forecast | pending)
CC_TRIGGER_LABEL = "cluster.dreemk8s/trigger"


def scaling_in_progress():
    """
    True se esiste una ClusterConfiguration non ancora conclusa. Il sistema ne
    gestisce una alla volta, quindi finche' ce n'e' una in volo nessun percorso
    (forecast o pending) puo' crearne un'altra. In caso di errore API si assume
    che sia in corso, per non scalare alla cieca.
    """
    api = client.CustomObjectsApi()
    try:
        existing_crs = api.list_namespaced_custom_object(
            group=CC_GROUP, version=CC_VERSION, namespace=CC_NAMESPACE, plural=CC_PLURAL)
    except ApiException as e:
        logger.error(f"error listing ClusterConfigurations: {e}")
        return True

    for cr in existing_crs.get("items", []):
        phase = cr.get("status", {}).get("phase", "")
        if phase not in CC_TERMINAL_PHASES:
            logger.debug(f"ClusterConfiguration '{cr['metadata']['name']}' in phase '{phase}'")
            return True
    return False


def create_cluster_configuration(requiredNodes, min_nodes, max_nodes, trigger):
    """
    it creates a new CluterConfiguration CR in the Kubernetes cluster.
    Returns True if the CR has been created.
    """
    logger.info(f"create a new ClusterConfiguration resource: required Nodes {requiredNodes}, min_nodes {min_nodes} , max_nodes {max_nodes}, trigger {trigger}")
    if scaling_in_progress():
        logger.info("A ClusterConfiguration is still in progress. Skipping creation.")
        return False

    random_string = ''.join(random.choices(string.ascii_lowercase + string.digits, k=8))
    cr_name = f"clusterconfiguration-{random_string}"

    body = {
        "apiVersion": f"{CC_GROUP}/{CC_VERSION}",
        "kind": "ClusterConfiguration",
        "metadata": {
            "name": cr_name,
            "namespace": CC_NAMESPACE,
            "labels": {CC_TRIGGER_LABEL: trigger},
        },
        "spec": {
            "requiredNodes": int(requiredNodes),
            "maxNodes": int(max_nodes),
            "minNodes": int(min_nodes)
        }
    }

    try:
        client.CustomObjectsApi().create_namespaced_custom_object(
            group=CC_GROUP,
            version=CC_VERSION,
            namespace=CC_NAMESPACE,
            plural=CC_PLURAL,
            body=body
        )
        logger.info(f" ClusterConfiguration successfully created: {cr_name}")
        return True
    except ApiException as e:
        logger.error(f"error during ClusterConfiguration creation: {e}")
        return False


@dataclass
class ForecastParams:
    """Parametri della ConfigMap forecast-parameters; i valori qui sono i default."""
    past_time_window: int = 5
    future_time_window: int = 60
    min_threshold: int = 65
    max_threshold: int = 45
    forecast_period_in_minutes: int = 30
    model: str = "LSTM"
    mean_time_to_boot: int = 1
    enabled: bool = True
    stabilization_cycles: int = 1
    request_rate_query: str = DEFAULT_REQUEST_RATE_QUERY
    anticipation_multiplier: float = DEFAULT_ANTICIPATION_MULTIPLIER
    min_request_rate: float = DEFAULT_MIN_SIGNIFICANT_REQUEST_RATE
    min_cpu_percent: float = DEFAULT_MIN_SIGNIFICANT_CPU_PERCENT
    # scale-up reattivo sui pod Unschedulable (solo profilo QOS)
    pending_check_period_seconds: int = 15
    pending_scale_up_after_seconds: int = 90
    pending_post_scale_up_grace_seconds: int = 30
    pending_scale_down_cooldown_minutes: int = 10


def read_forecast_cm():
    """
    The function reads the forecast-parameters ConfigMap and updates the default values
    """
    p = ForecastParams()

    # update the CM if values are present
    v1 = client.CoreV1Api()
    try:
        config_map = v1.read_namespaced_config_map(name="forecast-parameters", namespace="dreem")
        cm=config_map.data or {}
        p.past_time_window = int(
            cm.get("Past_time_window", p.past_time_window))
        p.future_time_window = int(
            cm.get("Prometheus_time_window_prediction_in_minutes", p.future_time_window))
        p.min_threshold = int(cm.get("Thresholds_min", p.min_threshold))
        p.max_threshold = int(cm.get("Thresholds_max", p.max_threshold))
        p.forecast_period_in_minutes = int(cm.get("Forecast_period_in_minutes", p.forecast_period_in_minutes))
        p.model = cm.get("Prediction_model", p.model)
        p.mean_time_to_boot = int(cm.get("Mean_time_to_boot", p.mean_time_to_boot))
        # Confronto case-insensitive: con str(True) == "True" la chiave mancante
        # avrebbe disabilitato il forecast invece di lasciarlo attivo.
        p.enabled = str(cm.get("Enabled", p.enabled)).strip().lower() == "true"
        p.stabilization_cycles = int(cm.get("Stabilization_cycles", p.stabilization_cycles))
        p.request_rate_query = (cm.get("Request_rate_query", p.request_rate_query) or "").strip()
        p.anticipation_multiplier = float(
            cm.get("Anticipation_max_multiplier", p.anticipation_multiplier))
        p.min_request_rate = float(
            cm.get("Anticipation_min_request_rate", p.min_request_rate))
        p.min_cpu_percent = float(
            cm.get("Anticipation_min_cpu_percent", p.min_cpu_percent))
        # almeno 1s: con 0 il loop principale girerebbe a vuoto
        p.pending_check_period_seconds = max(1, int(
            cm.get("Pending_check_period_seconds", p.pending_check_period_seconds)))
        p.pending_scale_up_after_seconds = int(
            cm.get("Pending_scale_up_after_seconds", p.pending_scale_up_after_seconds))
        p.pending_post_scale_up_grace_seconds = int(
            cm.get("Pending_post_scale_up_grace_seconds", p.pending_post_scale_up_grace_seconds))
        p.pending_scale_down_cooldown_minutes = int(
            cm.get("Pending_scale_down_cooldown_minutes", p.pending_scale_down_cooldown_minutes))
    except client.exceptions.ApiException as e:
        logging.error(f"Error during the reading of the ConfigMap: {e}")
    except (TypeError, ValueError) as e:
        logging.error(f"Invalid value in forecast-parameters ConfigMap: {e}. Using defaults.")

    return p

def parse_hard_checks(value):
    """
    Interpreta la chiave hardChecks con la stessa regola dell'operatore: vuota o
    assente vale true, altrimenti sono ammessi solo true e false, senza
    distinguere le maiuscole. Qualsiasi altro valore solleva ValueError.
    """
    normalized = (value or "").strip().lower()
    if normalized in ("", "true"):
        return True
    if normalized == "false":
        return False
    raise ValueError(f"hardChecks must be true or false, got {value!r}")


def read_cluster_configuration_cm():
    """
       The function reads the clusterConfiguration-parameters ConfigMap and updates the default values
       """
    # set some default values
    min_nodes=1
    max_nodes=5
    # profilo vuoto = scale-up reattivo disattivato
    profile = ""
    # con hardChecks false gli scale-down del forecast non hanno piu' veti (vedi forecast_step)
    hard_checks = True

    # update the CM if values are present
    v1 = client.CoreV1Api()
    try:
        config_map = v1.read_namespaced_config_map(name="cluster-configuration-parameters", namespace="dreem")
        cm = config_map.data or {}
        min_nodes = int(cm.get("minNodes", min_nodes))
        max_nodes = int(cm.get("maxNodes", max_nodes))
        profile = (cm.get("selectionProfile", profile) or "").strip().upper()
        # con selectionPolicy RANDOM il profilo non conta, compreso lo scale-up
        # reattivo di QOS: l'operatore sceglie i nodi a caso senza i suoi pesi
        if (cm.get("selectionPolicy") or "").strip().upper() == "RANDOM":
            profile = ""
        # letta per ultima: un valore non valido lascia attivi i controlli senza
        # far perdere le chiavi gia' lette
        hard_checks = parse_hard_checks(cm.get("hardChecks"))
    except client.exceptions.ApiException as e:
        logging.error(f"Error during the reading of the ConfigMap: {e}. Using defaults.")
    except (TypeError, ValueError) as e:
        logging.error(f"Invalid value in cluster-configuration-parameters ConfigMap: {e}. Using defaults.")

    return min_nodes, max_nodes, profile, hard_checks

# def get_prometheus_url(args=None):
#     local_conf= utils.load_configuration()

#     if local_conf:
#         return "http://localhost:9090/"
#     else:
#         return args.prometheus_url


def get_active_worker():
    """
    The function returns the number of active nodes in the managed cluster.
    Only worker nodes that are Ready are counted.
    """
    v1 = client.CoreV1Api()
    nodes = v1.list_node()

    active_nodes = 0
    for node in nodes.items:
        labels = node.metadata.labels or {}

        if "node-role.kubernetes.io/control-plane" in labels:
            continue
        if "node-role.kubernetes.io/master" in labels:
            continue

        is_ready = False
        for condition in node.status.conditions or []:
            if condition.type == "Ready" and condition.status == "True":
                is_ready = True
                break

        if is_ready:
            active_nodes += 1

    return active_nodes
    


@dataclass
class ScalingState:
    """Stato condiviso tra il percorso reattivo (pending) e quello predittivo (forecast)."""
    # Stabilization mechanism: track consecutive occurrences of same scaling_label
    previous_scaling_label: int | None = None
    scaling_label_count: int = 0
    # istante (monotonic) da cui nessuna ClusterConfiguration e' in corso, None
    # mentre una e' in volo. Parte dall'avvio: dopo un riavvio si attende
    # comunque la grace prima di uno scale-up reattivo.
    idle_since: float | None = field(default_factory=time.monotonic)
    # l'ultima ClusterConfiguration creata e' uno scale-up reattivo non ancora concluso
    reactive_in_flight: bool = False
    # fino a quando (monotonic) gli scale-down del forecast sono bloccati
    scale_down_blocked_until: float = 0.0


def track_scaling_progress(state, params):
    """
    Aggiorna idle_since osservando le ClusterConfiguration. La CR non ha un
    timestamp di fine, quindi la fine si rileva qui, al primo tick in cui
    nessuna e' piu' in corso. Se era uno scale-up reattivo, parte il cooldown
    che blocca gli scale-down del forecast.
    """
    in_progress = scaling_in_progress()
    if in_progress:
        if state.idle_since is not None:
            logger.info("ClusterConfiguration in progress: scaling decisions paused until it ends")
        state.idle_since = None
        return

    if state.idle_since is None:
        now = time.monotonic()
        state.idle_since = now
        logger.info("ClusterConfiguration ended")
        if state.reactive_in_flight:
            state.reactive_in_flight = False
            state.scale_down_blocked_until = now + params.pending_scale_down_cooldown_minutes * 60
            logger.info(f"[Pending] reactive scale-up ended: forecast scale-downs blocked for "
                        f"{params.pending_scale_down_cooldown_minutes} minutes")


def reactive_step(params, state, pending, min_nodes, max_nodes):
    """
    Scale-up reattivo (profilo QOS), simile al Cluster Autoscaler: +1 nodo se
    almeno un pod e' Unschedulable da Pending_scale_up_after_seconds.
    Il tempo di boot e' gia' coperto dal blocco sulla ClusterConfiguration in
    volo; dopo la sua fine si attende solo Pending_post_scale_up_grace_seconds,
    il tempo perche' lo scheduler piazzi i pod sul nodo appena acceso.
    """
    oldest = max(age for _, age in pending)
    logger.info(f"[Pending] {len(pending)} unschedulable pod(s), oldest waiting {oldest:.0f}s")

    if state.idle_since is None:
        return
    idle_for = time.monotonic() - state.idle_since
    if idle_for < params.pending_post_scale_up_grace_seconds:
        logger.info(f"[Pending] waiting grace after last scaling "
                    f"({idle_for:.0f}/{params.pending_post_scale_up_grace_seconds}s)")
        return
    overdue = [name for name, age in pending if age >= params.pending_scale_up_after_seconds]
    if not overdue:
        return

    # il controller supporta solo incrementi di un nodo
    required_worker = get_active_worker() + 1
    if required_worker > max_nodes:
        logger.info("[Pending] reached infrastructure constraints, reactive scale-up not possible")
        return

    logger.info(f"[Pending] scale-up to {required_worker} workers: {len(overdue)} pod(s) pending for over "
                f"{params.pending_scale_up_after_seconds}s (e.g. {', '.join(overdue[:5])})")
    if create_cluster_configuration(required_worker, min_nodes, max_nodes, trigger="pending"):
        state.idle_since = None
        state.reactive_in_flight = True
        # il numero di nodi cambia: i voti del forecast calcolati sul vecchio numero non valgono piu'
        state.previous_scaling_label = None
        state.scaling_label_count = 0


def scale_down_veto(state, pending):
    """
    Motivo per cui uno scale-down del forecast va ignorato, None se e' ammesso.
    Vale per ogni profilo: i pod in attesa non usano CPU, quindi il forecast
    vedrebbe carico basso e spegnerebbe proprio la capacita' che manca.
    """
    if pending is None:
        return "unable to check for unschedulable pods"
    if pending:
        return f"{len(pending)} unschedulable pod(s)"
    remaining = state.scale_down_blocked_until - time.monotonic()
    if remaining > 0:
        return f"cooldown after reactive scale-up ({remaining / 60:.1f} minutes left)"
    return None


def forecast_step(args, params, state, pending, min_nodes, max_nodes, hard_checks):
    """Un ciclo del forecast CPU, eseguito ogni Forecast_period_in_minutes."""
    # Latenza di reazione reale del sistema: i cicli di stabilizzazione ancora
    # da accumulare, piu' il tempo di boot del nodo. E' l'orizzonte su cui la
    # crescita del carico va proiettata per non arrivare tardi.
    anticipation_lead_minutes = (
        max(1, params.stabilization_cycles) * params.forecast_period_in_minutes + params.mean_time_to_boot
    )

    active_worker = get_active_worker()
    scaling_label = forecast(args.prometheus_url, params.past_time_window, params.future_time_window,
                             params.min_threshold, params.max_threshold, params.model,
                             params.mean_time_to_boot, args.rate_interval,
                             multiplier_anticipo=params.anticipation_multiplier,
                             request_rate_query=params.request_rate_query,
                             anticipation_lead_minutes=anticipation_lead_minutes,
                             min_request_rate=params.min_request_rate,
                             min_cpu_percent=params.min_cpu_percent)

    # Stabilization logic: only create ClusterConfiguration if scaling_label is stable (same value for 2 consecutive cycles)
    if scaling_label == state.previous_scaling_label:
        state.scaling_label_count += 1
    else:
        state.scaling_label_count = 1
        state.previous_scaling_label = scaling_label

    logger.info(f"Scaling label: {scaling_label} (stable for {state.scaling_label_count} cycle(s))")

    # create the CRD only if the cluster configuration (aka number of nodes) has to be updated
    # and the scaling_label has been stable for at least 2 consecutive cycles
    if scaling_label == 0 or scaling_label is None or state.scaling_label_count < params.stabilization_cycles:
        return
    if scaling_label < 0:
        veto = scale_down_veto(state, pending)
        # con hardChecks false il veto non blocca lo scale-down, ma resta nel log
        # per sapere quando i controlli sarebbero intervenuti
        if veto and hard_checks:
            logger.info(f"forecast scale-down skipped: {veto}")
            return
        if veto:
            logger.info(f"forecast scale-down not vetoed, hardChecks is false: {veto}")

    required_worker = active_worker + scaling_label
    if required_worker >= int(min_nodes) and required_worker <= int(max_nodes):
        if create_cluster_configuration(required_worker, min_nodes, max_nodes, trigger="forecast"):
            state.idle_since = None
        # Reset counter after creating a ClusterConfiguration
        state.scaling_label_count = 0
    else:
        logger.info("reached infrastructure constaints, scaling not possible")


def main():
    parser = argparse.ArgumentParser(description='Forecast script for DREEM-K8s')
    parser.add_argument('--start-after', type=int, default=0,
                        help='Minutes to wait before starting the first forecast (default: 0)')
    parser.add_argument('--prometheus-url', type=str, default="http://localhost:9090",
                        help='URL of the Prometheus service')
    parser.add_argument('--rate-interval', type=str, default="5m",
                        help='Interval for Prometheus rate function (default: 5m)')

    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")

    # Sleep iniziale prima del primo forecast
    if args.start_after > 0:
        logger.info(f"Waiting {args.start_after} minutes before starting first forecast")
        time.sleep(args.start_after * 60)

    utils.load_configuration()
    state = ScalingState()
    next_forecast_at = 0.0
    was_enabled = None

    # Un unico loop a tick di Pending_check_period_seconds: il controllo dei pod
    # gira a ogni tick, il forecast CPU solo quando e' scaduto il suo periodo.
    # Un solo thread decide, quindi i due percorsi non competono per la
    # ClusterConfiguration e lo stato condiviso non richiede lock.
    while True:
        params = read_forecast_cm()
        min_nodes, max_nodes, profile, hard_checks = read_cluster_configuration_cm()
        if not params.enabled:
            if was_enabled is not False:
                logger.info("forecast is disabled, skipping forecast and scaling")
            was_enabled = False
            time.sleep(params.pending_check_period_seconds)
            continue
        was_enabled = True

        track_scaling_progress(state, params)
        forecast_due = time.monotonic() >= next_forecast_at
        reactive_enabled = profile == "QOS"

        # una sola lettura dei pod per tick, e solo se qualcuno la usa
        pending = list_unschedulable_pods() if (reactive_enabled or forecast_due) else []

        if reactive_enabled and pending:
            reactive_step(params, state, pending, min_nodes, max_nodes)

        if forecast_due:
            forecast_step(args, params, state, pending, min_nodes, max_nodes, hard_checks)
            next_forecast_at = time.monotonic() + params.forecast_period_in_minutes * 60
            logger.info(f"forecast finished, next forecast in {params.forecast_period_in_minutes} minutes")

        time.sleep(params.pending_check_period_seconds)



if __name__ == "__main__":
    main()
