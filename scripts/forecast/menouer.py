"""
Decision rule of the reactive node autoscaler of Menouer et al., Algorithm 1 of
"Reactive Autoscaling of Kubernetes Nodes" (FRAME@HPDC 2024), which their TPDS paper
"Innovative Node Autoscaling Mechanism in the Kubernetes Ecosystem" (2026) reuses as its
reactive phase. main.py runs it every Period_seconds on the state of the cluster.

Only the decision is here, without Kubernetes or Prometheus, so that it can be tested on
its own (test_menouer.py). Which node is switched on or off is chosen by the operator,
with PROMETHEE.
"""
from dataclasses import dataclass, field

SCALE_UP = 1
SCALE_DOWN = -1
NO_ACTION = 0


@dataclass
class Params:
    """Menouer keys of the forecast-parameters ConfigMap; the defaults are the paper's values."""
    load_up: float = 70.0             # Load_up, percent
    load_down: float = 40.0           # Load_down, percent
    pending_pod_seconds: float = 120.0  # Pending_pod
    period_seconds: int = 60          # the algorithm runs every minute


@dataclass
class ClusterState:
    """What Algorithm 1 reads at every run."""
    # L_cpu, L_memory, L_storage: used / capacity of the active workers, in percent
    load_cpu: float
    load_memory: float
    load_storage: float
    # active workers (Active_nodes) and powered-off ones (Pending_nodes, in the paper
    # "nodes that are stopped and waiting to be activated")
    active_nodes: int
    pending_nodes: int
    # seconds each Unschedulable pod has been waiting (Pending_pods is its length)
    pending_pod_ages: list[float] = field(default_factory=list)


@dataclass
class Decision:
    action: int   # SCALE_UP, SCALE_DOWN or NO_ACTION
    rule: str     # the branch of Algorithm 1 that fired, also the ClusterConfiguration's trigger
    reason: str


def decide(state, params, min_nodes, max_nodes):
    """
    Algorithm 1, its three branches in their order:
      1. scale-up if a pod is pending, the load of CPU, memory or storage is at least
         Load_up, and a powered-off node exists;
      2. otherwise scale-up if a pod has been pending for more than Pending_pod and a
         powered-off node exists;
      3. otherwise scale-down if no pod is pending, the loads of CPU, memory and storage
         are all at most Load_down, and more than one node is active.
    min_nodes and max_nodes are the bounds of cluster-configuration-parameters: the
    paper's floor is one node (Active_nodes > 1), here it is minNodes, and a node is
    switched on only while fewer than maxNodes are active.
    """
    loads = {"cpu": state.load_cpu, "memory": state.load_memory, "storage": state.load_storage}
    pending = state.pending_pod_ages
    can_scale_up = state.pending_nodes > 0 and state.active_nodes < max_nodes

    if pending and can_scale_up:
        over = [f"{name} {value:.1f}%" for name, value in loads.items() if value >= params.load_up]
        if over:
            return Decision(SCALE_UP, "load-up",
                            f"{len(pending)} pending pod(s) and load >= {params.load_up:g}%: {', '.join(over)}")
        oldest = max(pending)
        if oldest > params.pending_pod_seconds:
            return Decision(SCALE_UP, "pending-pod",
                            f"a pod has been pending for {oldest:.0f}s > {params.pending_pod_seconds:g}s")

    if (not pending and all(value <= params.load_down for value in loads.values())
            and state.active_nodes > min_nodes):
        return Decision(SCALE_DOWN, "load-down",
                        f"no pending pod and every load <= {params.load_down:g}%")

    return Decision(NO_ACTION, "", "")
