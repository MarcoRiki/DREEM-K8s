# CONFIG-MAPS
This folder contains the ConfigMap used in this project.

## forecast-parameters
- Prometheus_rate_interval: sliding window value of the rate function in Prometheus
- Prometheus_time_window_prediction_in_minutes: it represents the time interval on which the historical analysis is based to make the prediction.
- Prometheus_time_window_forecast_in_minutes: it represent how much time you want to forecast
- Prometheus_query_step: it is the resolution of Prometheus scraped metrics
- Thresholds_min: percentage of CPU under which a node must be shutted down
- Thresholds_max: percentage of CPU above which a node must be turned down
- Forecast_period_in_minutes: time distance between two forecast sessions

## cluster-configuration-parameters

- minNodes: minimum number of nodes to be active in the cluster
- maxNodes: max number of nodes which are available in the cluster