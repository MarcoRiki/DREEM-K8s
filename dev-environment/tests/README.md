# TESTS
here you can find all the scripts used to test DREEM and see its behavior

### Configuration
Talos Machine
Control Plane: 4GB RAM + 2 CPU + 15GB storage
Worker: 3GB RAM + 2 CPU + 15 GB storage

#### Baseline (0 load): 
Control plane: CPU between 3 and 30%, RAM ~45%
Workers: CPU between 10 and 40%, RAM ~70%

60-70 users tipically overload the worker

### Parameters Tuning
for single node
base_users = 25
amplitude = 20
period = 3*60  # un'onda ogni 60 secondi
spawn_rate = 2  # utenti al secondo