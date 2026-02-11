# fyp-onboarding
Kube-proxy data plane latency comparison: iptables vs nftables using eBPF.

## Overview

This project measures the **pure kernel-level packet forwarding latency** in Kubernetes kube-proxy to prove:
- **iptables mode**: O(n) linear rule traversal - latency increases with service count
- **nftables mode**: O(1) hash table lookup - constant latency regardless of scale

**Measurement approach:** eBPF tracing directly measures kernel functions (`ipt_do_table` vs `nft_do_chain`) with pktgen bypassing conntrack cache.

## Directory Guide

```
.
├── Dockerfile                      # Worker container image
├── go.mod / go.sum                 # Go dependencies
├── worker.proto                    # Protocol buffer definition
├── README.md                       # This file
│
├── knative/
│   └── worker-service.yaml         # Worker Deployment + Service
│
├── loadgen-dataplane/              # (Legacy) gRPC load generator
│   └── load_generator.go           # Application-level latency test
│
├── scripts/
│   ├── ebpf/                       # ⭐ PRIMARY: eBPF measurement tools
│   │   ├── trace-kubeproxy.bt     # bpftrace script for kernel tracing
│   │   ├── run-experiment.sh       # Automated eBPF + pktgen experiment
│   │   └── README.md               # Complete eBPF documentation
│   │
│   ├── create-dummy-services/      # ⭐ Fast Go tool (6-10x faster)
│   │   ├── main.go                 # Parallel service creation with EndpointSlices
│   │   └── go.mod
│   │
│   ├── delete-dummy-services/      # ⭐ Fast Go tool
│   │   ├── main.go                 # Parallel service deletion
│   │   └── go.mod
│   │
│   ├── create-dummy-services.sh    # (Legacy) Bash version
│   ├── delete-dummy-services.sh    # (Legacy) Bash version
│   ├── verify-setup.sh             # Cluster verification tool
│   ├── enable-kube-proxy-metrics.sh # Prometheus metrics setup
│   └── README-go-tools.md          # Go tools documentation
│
├── worker/
│   └── worker.go                   # gRPC echo server (for legacy tests)
│
└── workerpb/                       # Generated protobuf code
    ├── worker.pb.go
    └── worker_grpc.pb.go
```
        
## Prerequisites

### 1. Kubernetes Cluster
- Multi-node cluster (CloudLab, AWS EKS, GKE, etc.)
- Kernel 4.9+ (for eBPF support)
- Root access to cluster nodes

### 2. Install Tools

**On your development machine:**
```bash
# Clone repository
git clone https://github.com/Zj12248/fyp-onboarding.git
cd fyp-onboarding

# Install Go dependencies for service creation
cd scripts/create-dummy-services && go mod download && cd ../..
cd scripts/delete-dummy-services && go mod download && cd ../..
```

**On cluster nodes (for eBPF tracing):**
```bash
# Install bpftrace and kernel headers
sudo apt update
sudo apt install bpftrace linux-headers-$(uname -r)

# Verify installation
bpftrace --version
```

---

## Deployment Guide

1. **Setup a Kubernetes cluster** (multi-node).

2. **Clone this repository** into the cluster node or local machine with kubectl access:
   ```bash
   git clone https://github.com/Zj12248/fyp-onboarding.git
   cd fyp-onboarding
   ```

3. **(Optional) Enable kube-proxy metrics** for Prometheus monitoring:
   ```bash
   bash scripts/enable-kube-proxy-metrics.sh
   ```

4. **If the worker image is NOT pushed to Docker Hub** (or another registry), follow steps 5–7. *(Ensure Docker is installed: `sudo apt install docker.io`)* Otherwise, skip to step 8.

5. **Build the image**:
   ```bash
   sudo docker build -t zj3214/worker:latest -f Dockerfile .
   ```

6. **Log in to Docker**:
   ```bash
   docker login -u zj3214
   ```

7. **Push the image** to the registry:
   ```bash
   sudo docker push zj3214/worker:latest
   ```

8. **Update the image** in [knative/worker-service.yaml](knative/worker-service.yaml) to match your Docker Hub username.

9. **Deploy the worker**:
   ```bash
   kubectl apply -f knative/worker-service.yaml
   kubectl wait --for=condition=Ready pod -l app=worker
   ```

10. **Verify worker is running**:
    ```bash
    kubectl get pods -l app=worker
    kubectl get svc worker -o jsonpath='{.spec.clusterIP}'
    ```

11. **(Optional) Verify complete setup**:
    ```bash
    bash scripts/verify-setup.sh
    ```
    Shows: service counts, kube-proxy mode, rule counts, worker status

---

## Running Experiments

### Method 1: eBPF Tracing (Recommended)

**Measures pure kernel-level kube-proxy forwarding latency with pktgen bypassing conntrack.**

#### Quick Start

```bash
# Run full automated experiment suite (tests at 100, 1k, 5k, 10k, 20k services)
sudo bash scripts/ebpf/run-full-experiment.sh

# View CSV results summary
cat logs/ebpf/experiment_summary_*.csv

# View individual detailed logs
ls -lt logs/ebpf/
```

**What it does:**
- Creates dummy services at multiple scales automatically
- Runs eBPF measurements at each scale
- Extracts metrics to CSV for easy graphing
- Cleans up between tests
- ~25-30 minutes total runtime

**Custom parameters:**
```bash
# Custom duration (60s per test) and packet rate
sudo bash scripts/ebpf/run-full-experiment.sh 60 10000 100
#                                            ^   ^     ^
#                                         duration rate warmup
```

#### Analyzing Results

```bash
# View summary CSV (easily import into Excel/Python)
column -t -s',' logs/ebpf/experiment_summary_*.csv

# Extract specific metrics
CSV_FILE=$(ls -t logs/ebpf/experiment_summary_*.csv | head -1)
echo "Service Count vs Max Latency:"
awk -F',' 'NR>1 {print $1 " services → " $9 " µs"}' $CSV_FILE

# Plot with Python (if pandas/matplotlib installed)
python3 << 'EOF'
import pandas as pd
import matplotlib.pyplot as plt

df = pd.read_csv('logs/ebpf/experiment_summary_*.csv')
plt.figure(figsize=(10, 6))
plt.plot(df['ServiceCount'], df['MaxLatency_us'], marker='o', label='Max (P100)')
plt.plot(df['ServiceCount'], df['MeanLatency_us'], marker='s', label='Mean')
plt.xlabel('Service Count')
plt.ylabel('Latency (µs)')
plt.title('Kube-Proxy Latency vs Scale')
plt.legend()
plt.grid(True)
plt.savefig('latency_vs_services.png')
print("Saved plot to latency_vs_services.png")
EOF
```

#### Compare iptables vs nftables

```bash
# Test iptables mode
kubectl -n kube-system edit cm kube-proxy
# Set: mode: "iptables"
kubectl -n kube-system delete pods -l k8s-app=kube-proxy
kubectl -n kube-system wait --for=condition=Ready pod -l k8s-app=kube-proxy
sleep 60

# Run full experiment (~25 min)
sudo bash scripts/ebpf/run-full-experiment.sh

# Save results
mv logs/ebpf/experiment_summary_*.csv logs/ebpf/iptables_results.csv

# Switch to nftables mode
kubectl -n kube-system edit cm kube-proxy
# Set: mode: "nftables"
kubectl -n kube-system delete pods -l k8s-app=kube-proxy
kubectl -n kube-system wait --for=condition=Ready pod -l k8s-app=kube-proxy
sleep 60

# Run full experiment again
sudo bash scripts/ebpf/run-full-experiment.sh

# Save results
mv logs/ebpf/experiment_summary_*.csv logs/ebpf/nftables_results.csv

# Compare side-by-side
echo "=== iptables ==="
column -t -s',' logs/ebpf/iptables_results.csv
echo ""
echo "=== nftables ==="
column -t -s',' logs/ebpf/nftables_results.csv
```

**See [scripts/ebpf/README.md](scripts/ebpf/README.md) for complete eBPF documentation.**

---

### Method 2: gRPC Load Generator (Legacy)

**Measures end-to-end application latency (includes TCP, gRPC overhead).**

<details>
<summary>Click to expand legacy gRPC workflow</summary>

```bash
# Get worker ClusterIP
WORKER_IP=$(kubectl get svc worker -o jsonpath='{.spec.clusterIP}')

# Run load test
go run loadgen-dataplane/load_generator.go \
  --worker=$WORKER_IP:50051 \
  --rps=50 \
  --num-requests=2000 \
  --proxy-mode=iptables \
  --service-count=1000
```

**Note:** This method includes application overhead and benefits from conntrack caching, making it less accurate for pure kube-proxy performance measurement.

</details>

---

## Expected Results

### eBPF Measurements (Pure Kernel Latency)

**iptables mode (O(n) linear):**
```
100 services    → ~15-25µs avg
500 services    → ~30-40µs avg
1,000 services  → ~45-60µs avg
5,000 services  → ~100-150µs avg
10,000 services → ~200-300µs avg
```
📈 **Latency increases linearly** - each service adds ~0.02µs

**nftables mode (O(1) constant):**
```
100 services    → ~10-15µs avg
500 services    → ~10-15µs avg
1,000 services  → ~10-15µs avg
5,000 services  → ~10-15µs avg
10,000 services → ~10-15µs avg
```
✅ **Latency stays constant** - hash table lookup is O(1)

### Key Insights

1. **eBPF proves O(n) vs O(1)** - Direct kernel measurement shows clear algorithmic difference
2. **Conntrack bypass matters** - pktgen's randomized ports force full rule traversal
3. **Scale impact** - iptables becomes ~20x slower than nftables at 10k services
4. **Production implications** - Large clusters (1000+ services) benefit significantly from nftables

---

## Tools Documentation

### Fast Service Creation (Go Tools)

```bash
# Create services
cd scripts/create-dummy-services
go run main.go -count 10000 -workers 50

# Delete services
cd scripts/delete-dummy-services
go run main.go
```

See [scripts/README-go-tools.md](scripts/README-go-tools.md) for details.

### Cluster Verification

```bash
bash scripts/verify-setup.sh
```

Shows:
- Service counts (total, dummy, worker)
- Kube-proxy mode and pod status
- Rule counts (KUBE-SERVICES chain for iptables, service map for nftables)
- Worker deployment status and ClusterIP

---

## Troubleshooting

### "ERROR: bpftrace not found"
```bash
sudo apt install bpftrace linux-headers-$(uname -r)
```

### "ERROR: Worker service not found"
```bash
kubectl apply -f knative/worker-service.yaml
kubectl wait --for=condition=Ready pod -l app=worker
```

### "No packets traced yet"
- Verify worker is running: `kubectl get pods -l app=worker`
- Check ClusterIP exists: `kubectl get svc worker`
- Ensure pktgen module loaded: `lsmod | grep pktgen`

### Kube-proxy not syncing rules
```bash
# Check logs
kubectl -n kube-system logs -l k8s-app=kube-proxy --tail=50

# Verify mode
kubectl -n kube-system get cm kube-proxy -o yaml | grep mode:

# Restart if needed
kubectl -n kube-system delete pods -l k8s-app=kube-proxy
```

### High node load during experiments
- Reduce service count: Test at 100, 500, 1k first
- Lower packet rate: `sudo bash scripts/ebpf/run-experiment.sh 30 5000`
- Use nftables mode for large scales (handles 10k+ services better)

---

## References

- [eBPF Performance Tools](http://www.brendangregg.com/bpf-performance-tools-book.html)
- [Kubernetes kube-proxy modes](https://kubernetes.io/docs/concepts/services-networking/service/#virtual-ips-and-service-proxies)
- [Linux pktgen](https://www.kernel.org/doc/Documentation/networking/pktgen.txt)
- [bpftrace Reference](https://github.com/iovisor/bpftrace/blob/master/docs/reference_guide.md)
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
   bash ./scripts/delete-dummy-services.sh
   ```

7. **Repeat for different service counts** (10, 50, 100, 500, 1000)

### Expected Results
- **iptables-nft**: Latency increases linearly with service count (O(n) rule scan)
- **nftables**: Latency remains relatively constant (O(1) hash table lookup)

