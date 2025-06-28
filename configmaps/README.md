# CONFIG-MAPS
This folder contains the ConfigMap used in this project.

## forecast-parameters
- Prometheus_time_window_prediction_in_minutes: it represents the past time window on which the analysis is based to make the prediction.
- Future_time_window: it represent how much time in minutes you want to forecast. In the naive forecast method it is useless
- Past_time_window: it represent how much time in minutes you watch to make the prediction
- Thresholds_min: percentage of CPU under which a node must be shutted down
- Thresholds_max: percentage of CPU above which a node must be turned down
- Forecast_period: time distance between two forecast sessions
- Prediction_model: it is the model used to forecast the CPU usage. It only accept `naive` value in this version 

## cluster-configuration-parameters

- minNodes: minimum number of nodes to be active in the cluster
- maxNodes: max number of nodes which are available in the cluster