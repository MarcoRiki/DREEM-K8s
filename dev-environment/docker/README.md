# DEVELOPMENT ENVIRONMENT
This folder contains all the files required to install a dev environment on your machine using Kind.

## Requirements
To properly run the project, the following resources are required:
- Docker
- Kind
- Kubectl
- Clusterctl
- Helm

## Installation
It is possible to run the five `.sh` script to automatically install the environment in Kind.
- `00_requirements.sh` installs all the resources mentioned in the `Requirements` section
- `01_install_kind_cluster.sh` creates a Kind cluster with a single control plane. It will be used as Management Cluster for ClusterAPI
- `02_install_cluster_clusterAPI.sh` installs the Docker infrastructure provider for docker and creates the managed cluster cluster with a single control plane and three MachineDeploments with replicas=1. The `capi.yaml` files contains the configuration.
- `03_install_dreem.sh` creates the required namespaces, installs Prometheus, the metric server, the DREEM resources in the management cluster and it installs Prometheus in the new CAPI cluster.


## Delete Environment
To delete the environment it is possible to use `04_delete_environment.sh` script. It will delete the ClusterAPI cluster and then the Kind cluster.
