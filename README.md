# fyp-onboarding
Kube-proxy data plane latency comparison: iptables-nft vs nftables.

## Directory Guide
ª   Dockerfile (For Dockerising Worker)
ª   go.mod
ª   go.sum
ª   README.md
ª   worker.proto (protobuf definition)
ª   
+---knative
ª       worker-service.yaml (Kubernetes Deployment + Service manifest)
ª       
+---loadgen-dataplane
ª       load_generator.go (Data plane latency test - simple packet sender)
ª       
+---loadgen-onboarding
ª       load_generator.go (Original load generator with grid search - for reference)
ª
+---scripts
ª       create-dummy-services.sh (Create dummy Kubernetes services)
ª       delete-dummy-services.sh (Delete all dummy services)
ª       enable-kube-proxy-metrics.sh (Enable Prometheus metrics scraping)
ª       kube-proxy-servicemonitor.yaml (Prometheus ServiceMonitor config)
ª       verify-setup.sh (Verify cluster setup)
ª       
+---worker
ª       worker.go (gRPC server - echo mode for latency measurement)
ª       
+---workerpb (generated protobuf Go code)
        worker.pb.go
        worker_grpc.pb.go
        
## Deployment Guide
1. Setup a Kubernetes cluster (multi-node).
2. Clone this repository into the cluster node or local machine with kubectl access. (git clone -b data-plane-exp https://github.com/Zj12248/fyp-onboarding.git)
3. **(Optional) Enable kube-proxy metrics for Prometheus monitoring:**
   ```bash
   bash scripts/enable-kube-proxy-metrics.sh
   kubectl apply -f scripts/kube-proxy-servicemonitor.yaml
   ```
   Verify metrics are accessible: `curl localhost:10249/metrics | grep kubeproxy_sync`
4. If the worker image is **not** pushed into Docker Hub (or another registry), follow steps 5–7. *(Ensure Docker is installed: `sudo apt install docker.io`)* Otherwise, skip to step 8.
5. Build the image: `sudo docker build -t zj3214/worker:latest -f Dockerfile .`
6. Log in to Docker: `docker login -u zj3214`
7. Push the image into the registry: `sudo docker push zj3214/worker:latest`
8. Update the image in `knative/worker-service.yaml` to match your Docker Hub username.
9. Create dummy services to simulate production load: `bash scripts/create-dummy-services.sh 100`
10. Deploy the worker: `kubectl apply -f knative/worker-service.yaml`
11. Check if the worker is ready: `kubectl get pods -l app=worker`
12. Get the worker ClusterIP: `kubectl get svc worker -o jsonpath='{.spec.clusterIP}'`
13. **(Optional) Verify complete setup:** `bash scripts/verify-setup.sh`
   - Shows service counts, kube-proxy mode, rule counts, worker status
   - Provides ready-to-run test command with correct parameters

## Running Experiments

### Data Plane Latency Test
Run from within the cluster or a node with cluster network access:

```bash
# Get the worker ClusterIP first
WORKER_IP=$(kubectl get svc worker -o jsonpath='{.spec.clusterIP}')

go run loadgen-dataplane/load_generator.go \
  --worker=$WORKER_IP:50051 \
  --rps=50 \
  --num-requests=2000 \
  --proxy-mode=nftables \
  --service-count=10000
```

**Parameters:**
- `--worker`: Worker service ClusterIP and port (e.g., 10.100.189.92:50051)
- `--rps`: Requests per second (constant load)
- `--num-requests`: Total number of requests to send
- `--proxy-mode`: Current kube-proxy mode (for logging: iptables-nft or nftables)
- `--service-count`: Total number of services in cluster (dummy + worker)

**Results:**
- Log: `logs/dataplane/PM_<proxy-mode>_SC_<count>_RPS_<rps>_<timestamp>.log`
- CSV: `logs/dataplane/PM_<proxy-mode>_SC_<count>_RPS_<rps>_<timestamp>.csv`

### Experimental Workflow

1. **Create dummy services** (e.g., 100 services):
   ```bash
   ./scripts/create-dummy-services.sh 100
   ```

2. **Deploy worker** (becomes service #101):
   ```bash
   kubectl apply -f knative/worker-service.yaml
   kubectl wait --for=condition=Ready pod -l app=worker
   ```

3. **Run test with iptables-nft**:
   ```bash
   # Get worker ClusterIP
   WORKER_IP=$(kubectl get svc worker -o jsonpath='{.spec.clusterIP}')
   
   go run loadgen-dataplane/load_generator.go \
     --worker=$WORKER_IP:50051 \
     --rps=50 --num-requests=2000 \
     --proxy-mode=iptables-nft --service-count=101
   ```

4. **Switch kube-proxy mode** (external to this repo):
   ```bash
   kubectl -n kube-system edit configmap kube-proxy
   # Change: mode: "iptables" → mode: "nftables"
   kubectl -n kube-system delete pods -l k8s-app=kube-proxy
   kubectl -n kube-system wait --for=condition=Ready pod -l k8s-app=kube-proxy
   ```

5. **Run test with nftables**:
   ```bash
   # Worker ClusterIP remains the same
   WORKER_IP=$(kubectl get svc worker -o jsonpath='{.spec.clusterIP}')
   
   go run loadgen-dataplane/load_generator.go \
     --worker=$WORKER_IP:50051 \
     --rps=50 --num-requests=2000 \
     --proxy-mode=nftables --service-count=101
   ```

6. **Cleanup**:
   ```bash
   kubectl delete -f knative/worker-service.yaml
   ./scripts/delete-dummy-services.sh
   ```

7. **Repeat for different service counts** (10, 50, 100, 500, 1000)

### Expected Results
- **iptables-nft**: Latency increases linearly with service count (O(n) rule scan)
- **nftables**: Latency remains relatively constant (O(1) hash table lookup)

