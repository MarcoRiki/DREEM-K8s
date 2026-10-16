# DREEM-K8s: Menouer et al.'s node autoscaler

This branch reimplements the reactive node autoscaler of Menouer et al. on top of the DREEM-K8s operator, as a baseline to compare DREEM against:

- T. Menouer, C. Cérin, P. Darmon, "Reactive Autoscaling of Kubernetes Nodes", FRAME@HPDC 2024, doi 10.1145/3659994.3660310 (Algorithm 1, PROMETHEE in Sec. 3.1 and 3.3, example in Sec. 4);
- T. Menouer, C. Cérin, C. Barrios Hernández, "Innovative Node Autoscaling Mechanism in the Kubernetes Ecosystem", IEEE TPDS 37(11), 2026, doi 10.1109/TPDS.2026.3732572, whose reactive phase is the same algorithm. Its proactive phase (an hourly node count learned from weeks of history) is not reimplemented.

The authors did not publish their autoscaler. Everything else is DREEM's: the same CRDs, the same actuation (drain, then power off through Redfish; power on, wait for Ready, uncordon), so that a comparison measures the decisions only. DREEM's own forecaster (LSTM) and TOPSIS selection are not in this branch.

## The algorithm
Every minute, over the active workers:

- **scale-up** if a pod is pending and the CPU, memory or storage load is at least `Load_up` (70%), and a powered-off node exists;
- otherwise **scale-up** if a pod has been pending for more than `Pending_pod` (120 s) and a powered-off node exists;
- otherwise **scale-down** if no pod is pending, the CPU, memory and storage loads are all at most `Load_down` (40%), and more than one node is active.

The load of a resource is its usage over its capacity, both summed over the active workers. The node to switch on or off is chosen with PROMETHEE: every candidate is compared with every other one on each criterion (preference 1 if strictly better, 0 otherwise), all weights are 1, and the node with the highest net outranking flow wins.

| step | candidates | maximized | minimized |
|---|---|---|---|
| scale-up | powered-off workers | CPUs, memory, storage | energy consumption |
| scale-down | active workers | energy consumption | CPUs, memory, storage, running pods |

| what | where |
|---|---|
| decision rule (pure, tested on the paper's example) | `scripts/forecast/menouer.py`, `test_menouer.py` |
| loop: loads from node-exporter, pending pods, `ClusterConfiguration` of ±1 node | `scripts/forecast/main.py`, `pending.py` |
| PROMETHEE ranking | `operators/internal/controller/promethee.go` |
| candidates, `hardChecks` | `operators/internal/controller/nodeselecting_controller.go` |
| feasibility of a scale-down (`hardChecks: true` only) | `operators/internal/controller/migration_plan.go` |

## Adaptations
What the papers leave open, or what the testbed requires, and how it is resolved here:

1. **Active nodes.** In the paper the master node is a worker that never stops and counts in the loads. Here the control plane runs no workload: it is left out of the loads and of the candidates, and the floor is `minNodes` workers (the paper's is one active node). The paper starts with one active node; the experiments start with all workers on.
2. **Load.** "Allocated" is the capacity, as in the paper's example (6 CPUs used over 8, then over 16). "Used" is measured by node-exporter: CPU time not idle (rate over 2 minutes), `MemTotal - MemAvailable`, and the root filesystem's size minus the space available. While a worker that has just been switched on has no sample yet, no decision is taken.
3. **Pending pods.** The pods the scheduler marked Unschedulable, waiting since that mark; DaemonSet pods, pods a preemption already found room for, and pods no empty worker could host are left out. The paper counts every pending pod.
4. **Pending nodes.** The workers that are NotReady, i.e. switched off. A node is switched on only while fewer than `maxNodes` are active.
5. **One node per action.** No decision is taken while a `ClusterConfiguration` is in progress, which includes the boot or the drain of the node. The paper does not say what the algorithm does while a node is booting.
6. **Energy consumption.** Not specified in the paper. Here it is the `dreemk8s.io/consumption-profile` annotation, the figure DREEM uses. The TPDS paper calls it efficiency in one sentence about the scale-down, but its algorithm, like the FRAME paper, switches off the node that consumes the most.
7. **Capacities** come from the Node object, which stays readable while the machine is off; memory and storage are compared in whole GiB, so that a few KiB of difference between identical machines does not count as a preference. **Running pods** are the node's pods in phase Running.
8. **Ties.** The papers take the node with the highest net flow and do not say how to break a tie; with identical capacities the scale-down weighs only pods against consumption and ties are common. The node is drawn at random among the tied ones.
9. **Migration.** The algorithm migrates the pods of the removed node without checking where they can go: that is `hardChecks: false`. With `hardChecks: true` only the nodes whose pods all fit on the other nodes are candidates, the check DREEM makes.
10. **Parameters.** The paper's values (70%, 40%, 120 s, 60 s), in the `forecast-parameters` ConfigMap.

## Configuration, build and tests
The parameters are in two ConfigMaps, described in [deploy/configmaps](./deploy/configmaps/README.md); the Helm chart in [deploy/helm/dreem-k8s](./deploy/helm/dreem-k8s/README.md) installs everything in namespace `dreem`. The operator switches servers on and off through Redfish with the Secret `bmc-credentials-<node>` of each worker.

```sh
cd operators && make docker-buildx-all IMG=<registry>/menouer-operator:<tag> IMGFOR=<registry>/menouer-forecaster:<tag>
cd operators && make test                                  # operator
cd scripts/forecast && python -m unittest test_menouer     # decision rule
```

The images accept the arguments of DREEM's Deployments (`--max-configurations`, `--rate-interval`) and ignore them, so they can replace DREEM's images in its Deployments.

## Architecture
The operator has three controllers and CRDs, installed in the management cluster:
* ClusterConfiguration: the number of workers requested by the forecaster
* NodeSelecting: the node to switch on or off (PROMETHEE)
* NodeHandling: the drain and the power action

Click [here](./operators/README.md) to understand how they work.

## Project Directories

* `operators`: the Kubernetes operator
* `scripts`: the forecaster (Menouer et al.'s decision rule) and the CronJob that updates the nodes' energy figure
* `deploy`: ConfigMaps, Helm chart and manifests to deploy it in your cluster
* `dev-environment`: scripts to install a development environment with Kind or Proxmox
* `documentation`: images and eventually additional documentation
