#!/usr/bin/env bash
set -euo pipefail

# Install docker if missing (Ubuntu)
if ! command -v docker >/dev/null 2>&1; then
  sudo apt-get update
  sudo apt-get install -y docker.io
  sudo usermod -aG docker "$USER" || true
  echo ">> You may need to log out/in for docker group to take effect."
fi

# Install kubectl if missing
if ! command -v kubectl >/dev/null 2>&1; then
  curl -fsSL https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl -o kubectl
  chmod +x kubectl && sudo mv kubectl /usr/local/bin/
fi

# Install minikube if missing
if ! command < /dev/null -v minikube >/dev/null 2>&1; then
  curl -fsSL https://storage.googleapis.com/minikube/releases/latest/minikube-linux-amd64 -o minikube
  chmod +x minikube && sudo mv minikube /usr/local/bin/
fi

# Start single-node cluster with docker driver
minikube start --driver=docker --cpus=4 --memory=8192

# Make sure you can schedule pods on the control-plane node
kubectl taint nodes --all node-role.kubernetes.io/control-plane- || true

# (Optional) metrics-server
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml || true

echo "Cluster is up."
kubectl get nodes -o wide
