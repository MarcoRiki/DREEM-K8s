#!/bin/bash

#  DA TESTARE 


install_kind() {
  echo "Kind not found. Installing Kind..."
  curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.18.0/kind-linux-amd64
  chmod +x ./kind
  mv ./kind /usr/local/bin/kind
}

install_clusterctl() {
  echo "Clusterctl not found. Installing Clusterctl..."
  curl -Lo clusterctl https://github.com/kubernetes-sigs/cluster-api/releases/download/v0.4.0/clusterctl-linux-amd64
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

install_kubectl() {
  echo "Kubectl not found. Installing kubectl..."
  curl -LO https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl
  chmod +x ./kubectl
  mv ./kubectl /usr/local/bin/kubectl
}

install_docker() {
  echo "Docker not found. Installing Docker..."
  curl -fsSL https://get.docker.com -o get-docker.sh
  sh get-docker.sh
  sudo usermod -aG docker $USER
}

add_repo() {
  echo "Adding Prometheus community Helm repository..."
  helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
  helm repo update
}

check_and_install() {
  # Check if Kind is installed
  if ! command -v kind &> /dev/null
  then
    install_kind
  else
    echo "Kind is already installed."
  fi

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

  # Check if kubectl is installed
  if ! command -v kubectl &> /dev/null
  then
    install_kubectl
  else
    echo "Kubectl is already installed."
  fi

  # Check if Docker is installed
  if ! command -v docker &> /dev/null
  then
    install_docker
  else
    echo "Docker is already installed."
  fi
}

# Run the check and installation
check_and_install
add_repo