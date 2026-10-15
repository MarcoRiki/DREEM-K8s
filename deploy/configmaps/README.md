# CONFIG-MAPS
This folder contains the ConfigMap used in this project.

## forecast-parameters
- Prometheus_time_window_prediction_in_minutes: it represents the past time window in minutes on which the analysis is based to make the prediction.
- Future_time_window: it represent how much time in minutes you want to forecast. In the naive forecast method it is useless
- Past_time_window: it represent how much time in minutes you watch to make the prediction
- Thresholds_min: percentage of CPU under which a node must be shutted down
- Thresholds_max: percentage of CPU above which a node must be turned down
- Forecast_period: time distance between two forecast sessions
- Prediction_model: it is the model used to forecast the CPU usage. It only accept `naive` value in this version
- Mean_time_to_boot: it is the mean time in minutes that a node takes to be active after the boot. It is used to make the forecast more accurate
- Enabled: it is a boolean value that represent if the forecast is enabled or not. If it is set to false, the forecast script will not be performed
- Pending_check_period_seconds: period of the main loop. Unschedulable pods are checked every tick, the CPU forecast runs only when Forecast_period has elapsed
- Pending_scale_up_after_seconds: with `selectionProfile: QOS`, a pod that stays Unschedulable for this long triggers a +1 node scale-up, without stabilization cycles
- Pending_post_scale_up_grace_seconds: time to wait after a ClusterConfiguration ends before the next reactive scale-up, so the scheduler can place pods on the new node
- Pending_scale_down_cooldown_minutes: after a reactive scale-up, forecast scale-downs are blocked for this long. In every profile, forecast scale-downs are also blocked while any pod is Unschedulable. Neither block applies when `hardChecks` is `false`

## cluster-configuration-parameters

- minNodes: minimum number of nodes to be active in the cluster
- maxNodes: max number of nodes which are available in the cluster
- selectionProfile: `NEUTRAL`, `QOS` or `ENERGY`. `QOS` also enables the reactive scale-up on Unschedulable pods. Ignored with `selectionPolicy: RANDOM`
- selectionPolicy: how the operator picks the node to switch on or off. `TOPSIS` (the default) ranks the candidates with the weights of `selectionProfile`; `RANDOM` picks one of them at random, as a baseline to compare DREEM against. With `RANDOM` the profile is ignored everywhere: no TOPSIS weights, and no reactive scale-up even with `QOS`
- hardChecks: `true` (the default) or `false`. With `false` a scale-down is never refused because the pods have nowhere to go: the operator stops requiring that every pod of the node fits on the other nodes (resources, taints, required node affinity, required pod affinity and anti-affinity, `DoNotSchedule` topology spread), so the pods that don't fit are left Pending, and the forecaster stops blocking its scale-downs while pods are Unschedulable or during the cooldown after a reactive scale-up. `minNodes` and `maxNodes` still apply. The operator and the forecaster read this same key, so they always agree on it