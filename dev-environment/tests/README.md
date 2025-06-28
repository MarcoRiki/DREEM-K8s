# TESTS
here you can find all the scripts used to test DREEM and see its behavior

# PRELIMINARY TEST
### Configuration
Talos Machines:
* Control Plane: 4GB RAM + 4 CPU + 15GB storage
* Worker: 4GB RAM + 2 CPU + 15 GB storage

60-70 users tipically overload the worker

### Workload
A YOLO docker image is used together with Locust to create time-varying workload on the node.

#### Parameters Tuning


### HPA
in order to create load on the new nodes, an HPA is added to scale the number of replicas of YOLO according to the CPU usage.


### DREEM
At the beginning of the test, the configuration consists of 1 Control Plane and 2 worker nodes.


### ClusterAutoscaler


### Test Results
Take data from prometheus (numero di nodi al variare del tempo, da correlare al carico di locust; vedi metriche GitHub forecast), crd (per log), csv and graph on locust, python terminal log


ogni 10 min, dreem fa forecast per capire se c'è bisogno di scalare (quindi prende i dati sul consumo medio, controlla se gli ultimi 2 minuti di utilizzo superano le soglie definite).
HPA è settato per fare scaleUp quando si supera ~75% (60+25%) e scaleDown se si va sotto il ~45% (60-25%). HPA controlla ogni 10 minuti se c'è bisogno di scalare.

l'andamento delle richieste è di tipo sinusoidale, con un periodo di un'ora. l'intero test dura 3 ore, quindi 3 sinusoidi complete



### Installation

After the cluster set-up, you need to install the Yolo deployment, the HPA to let the deployment scale and the metric server (in order to capture metrics for HPA)


### TEST 27-05-2025 DREEM
Start: 15:30 (CP ip .116)
End: 18:45 

### TEST 03-06-2025 CA
Start: 10:10 (CP ip .114)
End: 13:50

## TEST BASELINE SENZA SCALING
start 21:20 (cp ip .114)
end 8:20


