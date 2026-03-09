# create clusterconfiguration resource with random requiredNodes between 1 and 5 at fixed interval
# the scaling is +1 or -1 with respect to the current number of nodes, eg. if there are 3 nodes, requiredNodes will be set to 2 or 4

#first cycle
SLEEP=120
OLD_REQ_NODES=4
COUNTER=1

echo "Setting requiredNodes to $OLD_REQ_NODES"
    kubectl apply -f - <<EOF
apiVersion: cluster.dreemk8s/v1alpha1
kind: ClusterConfiguration
metadata:
  labels:
    app.kubernetes.io/name: operators
    app.kubernetes.io/managed-by: kustomize
  name: clusterconfiguration-sample$COUNTER
  namespace: dreem
spec:
  maxNodes: 5
  minNodes: 1
  requiredNodes: $OLD_REQ_NODES
EOF
    sleep $SLEEP


while true; do
    OFFSET=$(( ( RANDOM % 2 )  *2 -1 )) 
    REQ_NODES=$((OLD_REQ_NODES+OFFSET))
    echo " OLD_REQ_NODES: $OLD_REQ_NODES"
    OLD_REQ_NODES=$REQ_NODES
    COUNTER=$((COUNTER+1))
    echo " OFFSET: $OFFSET"
    echo "Setting requiredNodes to $REQ_NODES"
    kubectl apply -f - <<EOF
apiVersion: cluster.dreemk8s/v1alpha1
kind: ClusterConfiguration
metadata:
  labels:
    app.kubernetes.io/name: operators
    app.kubernetes.io/managed-by: kustomize
  name: clusterconfiguration-sample$COUNTER
  namespace: dreem
spec:
  maxNodes: 5
  minNodes: 1
  requiredNodes: $REQ_NODES
EOF
    sleep $SLEEP
done