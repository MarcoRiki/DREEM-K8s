# CONFIG-MAPS
This folder contains the ConfigMap used in this project.

## forecast-parameters
Read by the forecaster at every run of Menouer et al.'s Algorithm 1, so the values can be changed during an experiment. A missing key keeps the default, which is the paper's value.

- Enabled: if `false`, the forecaster takes no decision
- Menouer_load_up: `Load_up`, in percent (default 70). A scale-up needs a pending pod and the CPU, memory or storage load of the active workers at or above it
- Menouer_load_down: `Load_down`, in percent (default 40). A scale-down needs no pending pod and the CPU, memory and storage loads all at or below it
- Menouer_pending_pod_seconds: `Pending_pod` (default 120). A pod pending for longer triggers a scale-up whatever the load
- Menouer_period_seconds: period of the algorithm (default 60). While a ClusterConfiguration is in progress no decision is taken

## cluster-configuration-parameters

- minNodes: minimum number of workers to be active in the cluster. The paper keeps one node
- maxNodes: max number of workers which are available in the cluster
- hardChecks: `false` or `true` (the default). With `false` the operator switches off the node PROMETHEE ranks first, as Menouer et al. do: the pods that don't fit on the other nodes are left Pending. With `true` it considers only the nodes whose pods all fit on the other nodes (resources, taints, required node affinity, required pod affinity and anti-affinity, `DoNotSchedule` topology spread). `minNodes` and `maxNodes` apply in both cases
