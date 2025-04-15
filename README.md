# DREEM-K8s
This Kubernetes project aims to create a mechanism to dynamically turn servers in a cluster on and off based on workload conditions, with the ultimate goal of reducing the cluster's power consumption.


## Architecture
Basic architecture consists of three main operators and CRDs:
* ClusterConfiguration
* NodeSelecting
* NodeHandling
Click [here](./operators/README.md) to understand how they work 


## Installation
It is possible to install the project on your local machine using Kind.
Some environment variables has to be set:
- `DREEM_DOCKER=true`: if you are fully installing DREEM on Kind
- `DREEM_DEBUG=true`: if you are making developing using local controllers

## Project Directories

* `operators`: you can find all the Kubernetes operators used in this project
* `documentation`: images and eventually additional documentation
* `configmaps`: it contains all the manifests for the Kubernetes ConfigMaps used in the project
* `dev-environment`: it contains the script to install a local dev environment to test DREEM using Kind

