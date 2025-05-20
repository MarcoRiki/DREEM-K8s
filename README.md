# DREEM-K8s
This Kubernetes project aims to create a mechanism to dynamically turn servers in a cluster on and off based on workload conditions, with the ultimate goal of reducing the cluster's power consumption.
It leverages ClusterAPI to dynamically add or remove nodes from your cluster.


## Architecture
Since DREEM is based on ClusterAPI, you have two clusters:
* the management cluster where DREEM, ClusterAPI and Prometheus are installed
* the managed cluster which will be dynamically scaled. It is a vanilla cluster with the addition of Prometheus (it is used by the management cluster to scrape metrics and perform the scaling).

The basic architecture consists of three main operators and CRDs which are deployed in the management cluster:
* ClusterConfiguration
* NodeSelecting
* NodeHandling

Click [here](./operators/README.md) to understand how they work.


## Installation
It is possible to install the project on your infrastructure by leveraging the proper infrastructure provider in [ClusterAPI](https://cluster-api.sigs.k8s.io/reference/providers).
This project has been tested in Docker and Proxmox.

NB: in order for the scale-up to work properly, the worker name must be in the form: `whateveryouwant`-`number`(eg. workersample-0, workersample-1...)

## Project Directories

* `operators`: you can find all the Kubernetes operators used in this project
* `documentation`: images and eventually additional documentation
* `configmaps`: it contains all the manifests for the Kubernetes ConfigMaps used in the project
* `dev-environment`: it contains the script to install an environment to use DREEM with Kind or Proxmox.
* `deploy`: manifests to deploy DREEM in your cluster

