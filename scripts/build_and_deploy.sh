#!/usr/bin/env bash
set -euo pipefail

# Ensure we're in repo root
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_DIR"

# Generate gRPC stubs if needed
if ! command -v protoc >/dev/null 2>&1; then
  echo "protoc not found. Please install protoc (protobuf compiler)."
  exit 1
fi
protoc --go_out=. --go-grpc_out=. proto/loadtest.proto

# Use minikube's image builder so images are available to the cluster
eval "$(minikube -p minikube docker-env)"

# Build images
docker build -t worker:local -f Dockerfile.worker .
docker build -t loadgen:local -f Dockerfile.loadgen .

# Deploy
kubectl apply -f k8s/namespace.yaml
kubectl -n vhive-lab apply -f k8s/configmap.yaml
kubectl -n vhive-lab apply -f k8s/worker-deployment.yaml
kubectl -n vhive-lab apply -f k8s/worker-service.yaml

echo "Waiting for worker to be Ready..."
kubectl -n vhive-lab rollout status deploy/worker

# Run the loadgen job
kubectl -n vhive-lab delete job loadgen --ignore-not-found
kubectl -n vhive-lab apply -f k8s/loadgen-job.yaml

echo "Follow logs with:"
echo "  kubectl -n vhive-lab logs job/loadgen -f"
