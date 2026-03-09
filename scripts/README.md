# SCRIPT

This folder contains scripts that are used to automate various tasks related to the project.

## Forecast
The `forecast` script is used to generate forecasts based on the data collected via Prometheus.
The forecast is used to predict whether the scaling action is required or not.
If required, the script creates a new `ClusterConfiguration` object to trigger the DREEM controller to perform the scaling action.

## NodeSelection
The `node_selection` script is used to select the nodes that will be used for the scaling according to some predefined criteria.
This script was used in the early stages of the project to select the nodes for scaling, but it is not currently used in the project as the DREEM controller has its own node selection mechanism.

## MetricUpdate
The `metricUpdate` script is used to retrieve the actual consumption of the bare metal servers and update a Custom Annotation on the associated MachineDeployment object. It will run as a CronJob every 5 minutes to ensure that the DREEM controller has the most up-to-date information about the resource consumption of the nodes in the cluster. This information is crucial for making informed scaling decisions based on the actual usage of the resources.