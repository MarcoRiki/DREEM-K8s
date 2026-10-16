# SCRIPT

This folder contains scripts that are used to automate various tasks related to the project.

## Forecast
The `forecast` script runs the decision rule of Menouer et al.'s reactive node autoscaler (Algorithm 1 of "Reactive Autoscaling of Kubernetes Nodes", FRAME@HPDC 2024). Every minute it reads the CPU, memory and storage load of the active workers from Prometheus, the Unschedulable pods and the powered-off workers, and when the algorithm asks for one node more or one less it creates a new `ClusterConfiguration` object, so that the controller performs the scaling action. The rule itself is in `menouer.py`, tested by `test_menouer.py` (`python -m unittest test_menouer`), which reproduces the example of Section 4 of the paper.

## MetricUpdate
The `metricUpdate` script is used to retrieve the actual consumption of the bare metal servers and update the `dreemk8s.io/consumption-profile` annotation of their nodes. It runs as a CronJob. The annotation is the energy figure that the controller's PROMETHEE ranking minimizes when it switches a node on and maximizes when it switches one off.
