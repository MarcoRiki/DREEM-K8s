# DREEM-K8s
This Kubernetes project aims to create a mechanism to dynamically turn servers in a cluster on and off based on workload conditions, with the ultimate goal of reducing the cluster's power consumption.


## Architecture
Basic architecture consists of three main operators and CRDs:
* ClusterConfiguration
* NodeSelecting
* NodeHandling
Click [here](./operators/README.md) to understand how they work 


## Installation

### Project Directories

* `operators`: you can find all the Kubernetes operators used in this project
* `documentation`: images and eventually additional documentation
* `configmaps`: it contains all the manifests for the Kubernetes ConfigMaps used in the project


### Preliminary Notes

### Requirements
- kube-prom-stack
- metric-server