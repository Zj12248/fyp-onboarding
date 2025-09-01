#!/usr/bin/env bash
set -euo pipefail
kubectl -n vhive-lab port-forward svc/worker-svc 50051:50051
