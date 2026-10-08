
import pandas as pd
import datetime
from prometheus_api_client import PrometheusConnect
import torch
import torch.nn as nn
from utils import *
import numpy as np
import urllib3
import logging

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
logger = logging.getLogger(__name__)
ACTIVE_INSTANCE_STALENESS_MINUTES = 5
INITIAL_BOOT_SPIKE_EXCLUSION_MINUTES = 5

# --- Parametri della logica di anticipo (anti-lag) ---
# Finestra recente usata per stimare la pendenza del segnale di carico.
ANTICIPATION_WINDOW_MINUTES = 5
# Sotto questi livelli il rapporto di crescita e' rumore, non segnale: con una
# baseline vicina a zero il rapporto proiezione/baseline esplode e saturerebbe
# il cap anche su carichi irrilevanti. Sono i default usati quando le chiavi
# corrispondenti mancano nella ConfigMap forecast-parameters.
DEFAULT_MIN_SIGNIFICANT_REQUEST_RATE = 0.5   # req/s
DEFAULT_MIN_SIGNIFICANT_CPU_PERCENT = 5.0    # %

def get_cluster_nodes_cores(prometheus_url):
    """
    Interroga Prometheus per ottenere i core di ogni macchina (machine_cpu_cores)
    e pulisce la label 'instance' estraendo solo l'IP (rimuovendo :10250).
    Ritorna un dizionario: { "192.168.111.108": 8, "192.168.111.101": 4 }
    """
    prom_client = PrometheusConnect(url=prometheus_url, disable_ssl=True)
    query = "machine_cpu_cores"
    
    try:
        results = prom_client.custom_query(query=query)
        cores_map = {}
        for metric in results:
            instance = metric.get('metric', {}).get('instance', '')
            value = metric.get('value', [0, 0])[1]
            
            if instance:
                # Isola solo l'IP eliminando la porta (es. "192.168.111.108:10250" -> "192.168.111.108")
                ip = instance.split(':')[0]
                cores_map[ip] = int(value)
        return cores_map
    except Exception as e:
        logger.error(f"Errore nel recupero di machine_cpu_cores: {e}")
        return {}

def load_data_instance_cpu(prometheus_url, past_time_window, rate_interval="5m"):
    """
    This function perform the query to retrieve the data of the CPU load in order to make prediction.
    Query: '100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle"}[RATE_INTERVAL])))'
    Returns resampled data at 5-minute intervals for PyTorch LSTM model.
    """
    logger.info("loading CPU data to make prediction")

    control_plane_ips = get_control_plane_ip()

    # Costruisci la query PromQL
    if control_plane_ips and len(control_plane_ips) > 0:
        # Aggiungi ":9100" a ogni IP
        ips_with_port = [f"{ip}:9100" for ip in control_plane_ips]
        # Costruisci la regex per l'esclusione
        regex = "|".join(ips_with_port)
        cpu_query = (
            '100*(1 - (avg(rate(node_cpu_seconds_total{mode="idle", '
            f'instance!~"{regex}", job="node-exporter"}}[{rate_interval}])) by (instance)))'
        )
    else:
        # Se non ci sono control plane IPs, query senza esclusioni
        logger.warning("No control plane IPs found, querying all instances")
        cpu_query = (
            '100*(1 - (avg(rate(node_cpu_seconds_total{mode="idle", '
            f'job="node-exporter"}}[{rate_interval}])) by (instance)))'
        )
    
    logger.info(f"Constructed PromQL query: {cpu_query}")
    
    prom_client = PrometheusConnect(url=prometheus_url, disable_ssl=True)
    end_time = datetime.datetime.now()
    start_time = end_time - datetime.timedelta(minutes=past_time_window)
    results = prom_client.custom_query_range(
        query=cpu_query,
        start_time=start_time,
        end_time=end_time,
        step="1m"
    )

    if not results:
        logger.info("CPU query has no data, defaulting to empty DataFrame")
        return pd.DataFrame(), True

    logger.info("CPU query has data")

    data = []
    active_results = []
    staleness_delta = datetime.timedelta(minutes=ACTIVE_INSTANCE_STALENESS_MINUTES)

    for metric in results:
        metric_values = metric.get('values', [])
        if len(metric_values) == 0:
            continue
        last_timestamp = datetime.datetime.fromtimestamp(float(metric_values[-1][0]))
        if (end_time - last_timestamp) > staleness_delta:
            instance = metric.get('metric', {}).get('instance', 'unknown')
            logger.info(
                f"Skipping inactive instance {instance}: last sample at {last_timestamp} "
                f"(older than {ACTIVE_INSTANCE_STALENESS_MINUTES} minutes)"
            )
            continue
        active_results.append(metric)

    if not active_results:
        logger.info("No active instances found in Prometheus data")
        return pd.DataFrame(), False

    for metric in active_results:
        instance = metric['metric']['instance']
        for timestamp, value in metric['values']:
            data.append({
                'ds': pd.to_datetime(timestamp, unit='s'),
                'cpu_util_percent': float(value),
                'instance': instance
            })

    # Crea il DataFrame
    df = pd.DataFrame(data)
    df = df.sort_values(['instance', 'ds'])

    # Resample a 1 minuti e interpola per ogni istanza
    dfs_resampled = []
    for instance, df_instance in df.groupby('instance'):
        df_instance = df_instance.set_index('ds').sort_index()
        df_resampled = df_instance[['cpu_util_percent']].resample('1min').mean()
        df_resampled = df_resampled.interpolate(method='linear', limit_direction='both')
        df_resampled['instance'] = instance
        df_resampled = df_resampled.reset_index()
        dfs_resampled.append(df_resampled)

    df_final = pd.concat(dfs_resampled, ignore_index=True)
    return df_final, False



def load_request_rate(prometheus_url, past_time_window, request_rate_query):
    """
    Interroga Prometheus per la frequenza di richieste aggregata dell'applicazione.

    La query va fornita dall'esterno (ConfigMap) perche' dipende dall'applicazione
    monitorata: deve restituire una singola serie in richieste/secondo, gia'
    aggregata sull'intero cluster (es. sum(rate(mub_request_processing_latency_milliseconds_count[2m]))).

    Ritorna un np.array dei valori campionati a TIME_STEP_MINUTES, oppure None se
    la metrica non e' disponibile (in tal caso il chiamante degrada senza rumore).
    """
    if not request_rate_query:
        return None

    try:
        prom_client = PrometheusConnect(url=prometheus_url, disable_ssl=True)
        end_time = datetime.datetime.now()
        start_time = end_time - datetime.timedelta(minutes=past_time_window)
        results = prom_client.custom_query_range(
            query=request_rate_query,
            start_time=start_time,
            end_time=end_time,
            step=f"{TIME_STEP_MINUTES}m"
        )
    except Exception as e:
        logger.warning(f"[Request Rate] Query fallita ({e}). Anticipo su request rate disabilitato per questo ciclo.")
        return None

    if not results:
        logger.info("[Request Rate] La query non ha restituito dati.")
        return None

    if len(results) > 1:
        logger.warning(
            f"[Request Rate] La query ha restituito {len(results)} serie: attesa una sola serie aggregata. "
            "Le serie vengono sommate campione per campione."
        )

    # Somma le serie (se piu' di una) allineandole sul timestamp
    series_by_ts = {}
    for metric in results:
        for timestamp, value in metric.get('values', []):
            try:
                v = float(value)
            except (TypeError, ValueError):
                continue
            if np.isnan(v):
                continue
            series_by_ts[float(timestamp)] = series_by_ts.get(float(timestamp), 0.0) + v

    if not series_by_ts:
        logger.info("[Request Rate] Nessun campione valido nella serie.")
        return None

    values = np.array([series_by_ts[ts] for ts in sorted(series_by_ts)], dtype=float)
    logger.info(f"[Request Rate] {len(values)} campioni, ultimo valore {values[-1]:.2f} req/s.")
    return values


def _slope_per_minute(values, window_points):
    """
    Pendenza ai minimi quadrati (unita' del segnale al minuto) sugli ultimi
    window_points campioni. Ritorna 0.0 se i punti non bastano.
    """
    y = np.asarray(values, dtype=float)
    y = y[np.isfinite(y)]
    if len(y) < 3:
        return 0.0
    y = y[-window_points:]
    if len(y) < 3:
        return 0.0
    x = np.arange(len(y), dtype=float) * TIME_STEP_MINUTES
    slope = float(np.polyfit(x, y, 1)[0])
    return slope


def compute_anticipation_factor(signal, lead_time_minutes, max_multiplier,
                                min_significant_level, label,
                                window_minutes=ANTICIPATION_WINDOW_MINUTES):
    """
    Traduce la crescita recente di un segnale di carico in un fattore
    moltiplicativo (>= 1.0) da applicare alla CPU predetta.

    L'idea: la decisione di scaling diventa effettiva solo dopo lead_time_minutes
    (cicli di stabilizzazione + boot del nodo). Se nel frattempo il carico cresce
    con pendenza `slope`, il livello atteso a quel punto e'
    `baseline + slope * lead_time`. Il rapporto fra livello proiettato e livello
    attuale e' il fattore di anticipo: se le richieste raddoppiano prima che il
    nodo sia pronto, la CPU predetta va scalata di conseguenza.

    Il fattore e' adimensionale, quindi funziona sia su req/s sia su CPU%.
    """
    if signal is None or len(signal) < 3:
        return 1.0

    window_points = max(3, int(round(window_minutes / TIME_STEP_MINUTES)))
    recent = np.asarray(signal, dtype=float)
    recent = recent[np.isfinite(recent)][-window_points:]
    if len(recent) < 3:
        return 1.0

    baseline = float(np.median(recent))
    if baseline < min_significant_level:
        logger.info(
            f"[Anticipo/{label}] Livello attuale {baseline:.2f} sotto la soglia di significativita' "
            f"({min_significant_level}). Nessun anticipo."
        )
        return 1.0

    slope = _slope_per_minute(signal, window_points)
    if slope <= 0:
        logger.info(f"[Anticipo/{label}] Nessuna crescita (pendenza {slope:.3f}/min). Nessun anticipo.")
        return 1.0

    projected = baseline + slope * lead_time_minutes
    factor = projected / baseline
    factor = float(min(max(factor, 1.0), max_multiplier))

    logger.info(
        f"[Anticipo/{label}] baseline={baseline:.2f}, pendenza=+{slope:.3f}/min, "
        f"lead time={lead_time_minutes:.1f} min -> proiezione={projected:.2f} "
        f"(fattore {factor:.3f}, cap {max_multiplier})"
    )
    return factor


def basic_prediction(df, prediction_window):
    """
    it takes the last X minutes and average the utilization
    """
    time_window = pd.Timedelta(minutes=prediction_window)
    latest_time = df["ds"].max()
    window_df = df[df["ds"] >= (latest_time - time_window)]
    #print(window_df["cpu_util_percent"])

    return window_df["cpu_util_percent"].mean()

def evaluate_predicted_cpu(predicted_cpu_dict, node_cores_map, min_threshold=40, max_threshold=60):
    """
    Decide lo scaling dalla CPU media prevista dei worker attivi e schedulabili.

    Ritorna:
     1: Scale-up
    -1: Scale-down
     0: Stabile

    La media e' pesata sui core: CPU usata prevista (somma di pred% x core) diviso
    i core totali. Sostituisce due regole che guardavano il nodo sbagliato:
      - lo scale-up scattava se UN SOLO nodo superava max_threshold. Un nodo caldo
        non si raffredda accendendone un altro (i pod in esecuzione non si
        spostano), quindi il cluster cresceva con una media intorno al 35%;
      - lo scale-down chiedeva piu' core liberi del nodo piu' grande + 2, e
        min_threshold (Thresholds_min nella ConfigMap) non era mai usato.

    Regole, con la banda [min_threshold, max_threshold] come isteresi:
      - scale-up   se la media prevista supera max_threshold;
      - scale-down se la media prevista e' sotto min_threshold E, tolto il nodo
        piu' grande, la media sui nodi rimasti resta sotto max_threshold (cosi'
        lo scale-down non provoca subito uno scale-up).
    """
    # Recuperiamo in tempo reale la lista degli IP validi/schedulabili da Kubernetes
    schedulable_ips = get_schedulable_node_ips()

    # Filtriamo il dizionario delle predizioni tenendo solo i nodi reali e attivi
    filtered_predicted_cpu = {}
    for instance, pred_percent in predicted_cpu_dict.items():
        ip = instance.split(':')[0]

        # Se abbiamo la lista da K8s e l'IP non è schedulabile, lo ignoriamo
        if schedulable_ips is not None and ip not in schedulable_ips:
            logger.info(f"[Filtro Nodi] Ignorato nodo {instance} ({pred_percent:.2f}%) perché in fase di spegnimento/cordon.")
            continue

        filtered_predicted_cpu[instance] = pred_percent

    if not filtered_predicted_cpu:
        logger.info("Nessun nodo attivo e schedulabile con una predizione. Nessuna decisione.")
        return 0

    # CPU usata prevista e core totali sui soli nodi rimasti vivi
    total_cluster_cores = 0
    total_predicted_used_cores = 0.0
    active_nodes_cores = []
    for instance, pred_percent in filtered_predicted_cpu.items():
        ip = instance.split(':')[0]
        cores = node_cores_map.get(ip, 4)
        active_nodes_cores.append(cores)
        total_cluster_cores += cores
        total_predicted_used_cores += (pred_percent / 100.0) * cores

    avg_percent = 100.0 * total_predicted_used_cores / total_cluster_cores
    hottest_instance = max(filtered_predicted_cpu, key=filtered_predicted_cpu.get)
    logger.info(
        f"[Media Worker] Nodi attivi: {len(filtered_predicted_cpu)}, core totali: {total_cluster_cores}, "
        f"usati previsti: {total_predicted_used_cores:.2f} -> media prevista {avg_percent:.2f}% "
        f"(banda {min_threshold}-{max_threshold}%). Nodo piu' carico: {hottest_instance} "
        f"al {filtered_predicted_cpu[hottest_instance]:.2f}% (non usato per decidere)."
    )

    # 1. SCALE-UP: il cluster nel suo insieme supera la soglia alta
    if avg_percent > max_threshold:
        logger.info(f"[Scale-Up Trigger] Media prevista {avg_percent:.2f}% sopra la soglia {max_threshold}%")
        return 1

    # 2. SCALE-DOWN: sotto la soglia bassa, e senza il nodo piu' grande si resta sotto quella alta
    if len(filtered_predicted_cpu) <= 1:
        logger.info("Solo un nodo attivo e schedulabile nel cluster. Scale-down non possibile.")
        return 0

    remaining_cores = total_cluster_cores - max(active_nodes_cores)
    avg_after_percent = 100.0 * total_predicted_used_cores / remaining_cores
    if avg_percent < min_threshold and avg_after_percent < max_threshold:
        logger.info(
            f"[Scale-Down Approved] Media prevista {avg_percent:.2f}% sotto {min_threshold}%; "
            f"senza il nodo piu' grande sarebbe {avg_after_percent:.2f}% (< {max_threshold}%)."
        )
        return -1

    logger.info(
        f"[Stabile] Media prevista {avg_percent:.2f}%, senza il nodo piu' grande {avg_after_percent:.2f}%."
    )
    return 0

def LSTM_torch_forecast_CPU_short(model, mean_time_to_boot, cpu_series_short):
    """
    Fa il forecast per una singola istanza usando il modello PyTorch LSTM a breve termine.
    
    Args:
        df_instance: DataFrame con colonne 'ds' e 'cpu_util_percent' per una singola istanza
        model: Modello PyTorch già caricato
        mean_time_to_boot: Tempo medio di boot in minuti
        cpu_series_short: Serie CPU degli ultimi INPUT_WINDOW_SHORT punti
    
    Returns:
        Media del forecast escludendo il tempo di boot
    """
    # Valori di normalizzazione dal training (dal notebook)
    cpu_mean = 37.8755
    cpu_std = 15.6728

    if len(cpu_series_short) < INPUT_WINDOW_SHORT:
        logger.warning(f"Not enough data for LSTM short-term forecast: {len(cpu_series_short)} < {INPUT_WINDOW_SHORT}. Using fallback.")
        fallback_pred = float(np.percentile(cpu_series_short, 90)) if len(cpu_series_short) > 0 else 0.0
        logger.warning(f"Using fallback from observed data (p90={fallback_pred:.2f}%).")
        return fallback_pred
    
    device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
    model.eval()

    # Prendi gli ultimi INPUT_WINDOW_SHORT punti
    x = torch.tensor(cpu_series_short, dtype=torch.float32)
    # Normalizza
    x = ((x - cpu_mean) / cpu_std).unsqueeze(0).unsqueeze(-1).to(device)

    # Fai la predizione
    with torch.no_grad():
        pred = model(x).cpu().numpy()[0]

    # Denormalizza
    pred_denorm = pred * cpu_std + cpu_mean


    valore_predetto_p90 = float(np.percentile(pred_denorm, 90))
    
    logger.info(f"Predicted values (denormalized): {pred_denorm}")
    
    # Cap a 100% per consistenza matematica
    valore_predetto_p90 = min(valore_predetto_p90, 100.0)
    
    return valore_predetto_p90
    
def LSTM_torch_forecast_CPU(df_instance, model, mean_time_to_boot, model_short):
    """
    Fa il forecast per una singola istanza usando il modello PyTorch LSTM.
    
    Args:
        df_instance: DataFrame con colonne 'ds' e 'cpu_util_percent' per una singola istanza
        model: Modello PyTorch già caricato
        mean_time_to_boot: Tempo medio di boot in minuti
    
    Returns:
        Media del forecast escludendo il tempo di boot
    """
    # Valori di normalizzazione dal training (dal notebook)
    cpu_mean = 37.8755
    cpu_std = 15.6728
    
    # Estrai la serie CPU
    cpu_series = df_instance['cpu_util_percent'].values
    
    # Verifica che ci siano abbastanza dati
    if len(cpu_series) < INPUT_WINDOW_EXTENDED:
        if len(cpu_series) == 0:
            logger.warning("No data available for forecast. Returning None.")
            return None

        # points_to_skip = int(INITIAL_BOOT_SPIKE_EXCLUSION_MINUTES / TIME_STEP_MINUTES)
        # observed_valid = cpu_series[points_to_skip:] if points_to_skip < len(cpu_series) else cpu_series
        # fallback_pred = float(np.percentile(observed_valid, 50))
        # logger.warning(
        #     f"Not enough data for LSTM forecast: {len(cpu_series)} < {INPUT_WINDOW_EXTENDED}. "
        #     f"Using fallback from observed data after excluding first {INITIAL_BOOT_SPIKE_EXCLUSION_MINUTES} minutes "
        #     f"(p50={fallback_pred:.2f}%)."
        # )
        # return fallback_pred
        # load the LSTMForecastShort model for short-term prediction
        logger.warning(
            f"Not enough data for LSTM forecast. Using fallback to LSTM short-term model."
        )
        cpu_series_short = cpu_series[-INPUT_WINDOW_SHORT:]  # Take the last INPUT_WINDOW_SHORT points
        return LSTM_torch_forecast_CPU_short(model_short, mean_time_to_boot, cpu_series_short)
    

    logger.info(f"Using LSTM extended model for forecast with {len(cpu_series)} data points.")
    device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
    model.eval()

    # Prendi gli ultimi INPUT_WINDOW_EXTENDED punti
    x = torch.tensor(cpu_series[-INPUT_WINDOW_EXTENDED:], dtype=torch.float32)
    # Normalizza
    x = ((x - cpu_mean) / cpu_std).unsqueeze(0).unsqueeze(-1).to(device)

    # Fai la predizione
    with torch.no_grad():
        pred = model(x).cpu().numpy()[0]

    # Denormalizza
    pred_denorm = pred * cpu_std + cpu_mean
    
    # Escludi i primi minuti necessari per il boot
    points_to_skip = int(mean_time_to_boot / TIME_STEP_MINUTES)
    pred_valid = pred_denorm[points_to_skip:] if points_to_skip < len(pred_denorm) else pred_denorm

    valore_predetto_p90 = float(np.percentile(pred_valid, 90))

    # NOTA: la logica di anticipo (anti-lag) e' applicata a monte in forecast(),
    # cosi' vale sia per il modello esteso sia per il fallback a breve termine.

    logger.info(f"skipping first {points_to_skip} points due to boot time ({mean_time_to_boot} min), valid predictions: {len(pred_valid)}")
    logger.info(f"Predicted values (denormalized): {pred_denorm}")
    logger.info(f"Predicted values after excluding boot time: {pred_valid}")
    # Cap a 100% per consistenza matematica
    valore_predetto_p90 = min(valore_predetto_p90, 100.0)
    return valore_predetto_p90


def LSTM_univariate_CPU(df, model, mean_time_to_boot):
    #best_model-3.keras
    #X_max = 93.0
    #X_min = 3
    #window_size= 20
    
    #model_12.keras
    # X_max = 91
    # X_min = 18
    #window_size= 20
    
     #model_0.keras - version 89
    # X_max = 96
    # X_min = 0
    # window_size= 25
    
     #model_0-2.keras - models63
    X_max = 95
    X_min = 0
    window_size= 15
    
    
    # set column CPU total usage in the dataset
    # | timestamp | value | instance

    #smoothing values
    df['cpu_smooth'] = df['value'].rolling(window=5, min_periods=1).mean()

    # normalize data
    df['cpu_normalized'] = (df['cpu_smooth'] - X_min) / (X_max - X_min)
    # create the dataset for LSTM
    lstm_df = create_realtime_input( df['cpu_normalized'], window_size)

    # make prediction
    y_pred = model.predict(lstm_df)

    # return predicted value
    y_pred_flatten = y_pred.flatten()
    y_pred_denorm = y_pred_flatten * (X_max - X_min) + X_min

    # exclude the first minutes which are needed to boot up a new server
    points_to_not_consider = int(mean_time_to_boot) * 4 # query step is 30s, hence 2 points per minute
    y_pred_valid = y_pred_denorm[points_to_not_consider:]
    #print("input values:", df['cpu_smooth'])
   # print("new values:", y_pred_valid)
    return y_pred_valid.mean()


def create_realtime_input(series, window_size=10):
    """
    It extracts the last time window from the serie to make a realtime prediction

    Returns:
        np.array: array with shape (1, window_size, 1) for the LSTM
    """
    recent_window = series[-window_size:]
    X_seq = np.array([[val] for val in recent_window])  # shape: (window_size, 1)
    return X_seq[np.newaxis, ...]  #  (1, window_size, 1)