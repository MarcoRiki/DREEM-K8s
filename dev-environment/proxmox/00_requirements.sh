#!/bin/bash

#  DA TESTARE 


install_clusterctl() {
  echo "Clusterctl not found. Installing Clusterctl..."
  curl -Lo clusterctl https://github.com/kubernetes-sigs/cluster-api/releases/download/v1.9.6/clusterctl-linux-amd64
  chmod +x ./clusterctl
  mv ./clusterctl /usr/local/bin/clusterctl
}

install_helm() {
  echo "Helm not found. Installing Helm..."
  curl https://get.helm.sh/helm-v3.10.2-linux-amd64.tar.gz --output helm.tar.gz
  tar -zxvf helm.tar.gz
  mv linux-amd64/helm /usr/local/bin/helm
  rm -rf linux-amd64 helm.tar.gz
}

add_repo() {
  echo "Adding Prometheus community Helm repository..."
  helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
  helm repo update
}

check_and_install() {

  # Check if Clusterctl is installed
  if ! command -v clusterctl &> /dev/null
  then
    install_clusterctl
  else
    echo "Clusterctl is already installed."
  fi

  # Check if Helm is installed
  if ! command -v helm &> /dev/null
  then
    install_helm
  else
    echo "Helm is already installed."
  fi
}

# Run the check and installation
check_and_install
add_repo