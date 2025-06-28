# DEVELOPMENT ENVIRONMENT
This folder contains all the files required to install an environment on Proxmox, using the [ClusterAPI proxmox infrastructure provider](https://github.com/ionos-cloud/cluster-api-provider-proxmox).

## Requirements
To properly run the project, the following resources are required:
- Docker
- Kind (to deploy locally your management cluster, otherwise you can set another VM on Proxmox)
- Kubectl
- Clusterctl
- Helm


## Installation

### Installing the management cluster
An initial cluster is needed to run the ClusterAPI and DREEM's controllers (like a kind cluster or another VM on Proxmox).
The following scripts assumes you already have the management cluster and proceed with the installation of the components.

Once the management is up and running, executes:
```bash
clusterctl init --infrastructure proxmox --ipam in-cluster --control-plane talos --bootstrap talos
```


### Installing the managed cluster on Proxmox
To use Proxmox as infrastructure provider, firstly an account with API access is required.
Then you have to setup some variabile (or you can export as env variables) in `~/.cluster-api/clusterctl.yaml`:
```yaml
PROXMOX_URL: "https://<proxmox-url>:8006"
PROXMOX_TOKEN: 'username@pve!realmexample'
PROXMOX_SECRET: "secret-xxxx-xxxx-...."
```


the `cluster.yaml` file contains a basic manifest to deploy a CAPI cluster on your Proxmox server. It can be generate through the command `clusterctl generate cluster` You must edit the following fields:
* ProxmoxCluster
    * `spec.allowedNodes`: the nodes on your proxmox server that can be used to instanciate new VM
    * `spec.controlPlaneEndpoint`: the VirtualIP associated to the control plane of the managed cluster
    * `spec.ipv4Config.addresses`: the pool of allowed addresses for the VM on proxmox
    * `spec.ipv4Config.gateway`: the gateway of the network 
    * `spec.ipv4Config.prefix`: the subnet prexix
* TalosControlPlane
    * `spec.replicas`: number of replicas of the control plane. They will get an address from the pool, but they will all share the same VirtualIP
    * `spec.controlPlaneConfig.strategicPatches`: change the vip to match the one written in `spec.controlPlaneEndpoint`
* ProxmoxMachineTemplate
    * `spec.template.spec.sourceNode`: the node where the template has been created
    * `spec.template.spec.templateID`: the unique ID of the template VM
    * `spec.template.spec.network.default.bridge`: name of the bridge used by proxmox
    * `spec.template.spec.disks.bootVolume.disk`: format of the disk used for the VM
    * edit the HW configuration like memoryMiB, numCores, sizeGb of the disk etc
    * you may want two different HW configuration for the control plane and for the worker, that's the purporse of the two ProxmoxMachineTemplate resources.
* MachineDeployment
    * `spec.replicas`: number of instanciated worker

Assuming you are running a terminal from this directory, executes:
```bash
kubectl apply -f cluster.yaml
```
and let clusterAPI create the cluster on Proxmox. This can take several minutes.

Then, let's see if the cluster has been provisioned
```bash
clusterctl describe cluster <your cluster name>
```

Export the Kubeconfig to access the new cluster:
```bash
clusterctl get kubeconfig <your cluster name> > <name kubeconfig>
```

Let's see if the nodes are Ready:
```bash
kubectl get nodes -o wide --kubeconfig <name kubeconfig>
```

Now install the prometheus stack required for DREEM to work:
```bash
helm install kind-prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --create-namespace \
  --set prometheus.service.nodePort=30000 \
  --set prometheus.service.type=NodePort \
  --set grafana.service.nodePort=31000 \
  --set grafana.service.type=NodePort \
  --set alertmanager.service.nodePort=32000 \
  --set alertmanager.service.type=NodePort \
  --set prometheus-node-exporter.service.nodePort=32001 \
  --set prometheus-node-exporter.service.type=NodePort --kubeconfig <name kubeconfig> 
```

## Delete Cluster on Proxmox
```bash
kubectl delete cluster <your cluster name>
```
