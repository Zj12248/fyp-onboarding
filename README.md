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
│   │   ├── run-full-experiment.sh  # Full automation at multiple scales
│   │   ├── common.sh               # Shared utility functions
│   │   └── README.md               # Complete eBPF documentation
│   │
│   ├── rtt/                        # RTT measurement tools
│   │   └── measure-rtt-hping3.sh   # Round-trip latency with conntrack bypass
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
git clone -b data-plane-exp https://github.com/Zj12248/fyp-onboarding.git
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
   git clone -b data-plane-exp https://github.com/Zj12248/fyp-onboarding.git
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
# Custom duration (60s per test), packet rate, warmup, and rule position
sudo bash scripts/ebpf/run-full-experiment.sh 60 10000 100 last
#                                            ^   ^     ^   ^
#                                         duration rate warmup position

# Best-case positioning (rule at position 1)
sudo bash scripts/ebpf/run-full-experiment.sh 20 5000 100 first

# Worst-case positioning (rule at end) - default
sudo bash scripts/ebpf/run-full-experiment.sh 20 5000 100 last
```

**Rule Position Options:**
- `first` - Worker rule at position 1 (best-case O(1)) - immediate match
- `last` - Worker rule at end (worst-case O(n)) - traverses all rules (default)
- Only affects iptables mode; nftables always uses O(1) hash lookup

#### Analyzing Results

# View summary CSV (easily import into Excel/Python)
- column -t -s',' logs/ebpf/experiment_summary_*.csv


#### Compare iptables vs nftables

```bash
# Test iptables mode (worst-case)
kubectl -n kube-system edit cm kube-proxy
# Set: mode: "iptables"
kubectl -n kube-system delete pods -l k8s-app=kube-proxy
kubectl -n kube-system wait --for=condition=Ready pod -l k8s-app=kube-proxy
sleep 60

# Run full experiment (~25 min)
sudo bash scripts/ebpf/run-full-experiment.sh 20 5000 100 last

# Save results
mv logs/ebpf/experiment_summary_*.csv logs/ebpf/iptables_worst_case.csv

# Switch to nftables mode
kubectl -n kube-system edit cm kube-proxy
# Set: mode: "nftables"
kubectl -n kube-system delete pods -l k8s-app=kube-proxy
kubectl -n kube-system wait --for=condition=Ready pod -l k8s-app=kube-proxy
sleep 60

# Run full experiment again
sudo bash scripts/ebpf/run-full-experiment.sh 20 5000 100 last

# Save results
mv logs/ebpf/experiment_summary_*.csv logs/ebpf/nftables_results.csv

# Compare side-by-side
echo "=== iptables (worst-case) ==="
column -t -s',' logs/ebpf/iptables_worst_case.csv
echo ""
echo "=== nftables ==="
column -t -s',' logs/ebpf/nftables_results.csv
```

#### Compare iptables Best-Case vs Worst-Case

```bash
# Test best-case positioning (rule at position 1)
sudo bash scripts/ebpf/run-full-experiment.sh 20 5000 100 first
mv logs/ebpf/experiment_summary_*.csv logs/ebpf/iptables_best_case.csv

# Test worst-case positioning (rule at end)
sudo bash scripts/ebpf/run-full-experiment.sh 20 5000 100 last
mv logs/ebpf/experiment_summary_*.csv logs/ebpf/iptables_worst_case.csv

# Compare to show O(1) vs O(n) behavior
echo "=== Best-Case (Position 1 - O(1)) ==="
column -t -s',' logs/ebpf/iptables_best_case.csv
echo ""
echo "=== Worst-Case (Position N - O(n)) ==="
column -t -s',' logs/ebpf/iptables_worst_case.csv
```

**See [scripts/ebpf/README.md](scripts/ebpf/README.md) for complete eBPF documentation.**

---

### Method 2: RTT Measurement with hping3 (implementation abandoned)

**Measures round-trip time (RTT) through kube-proxy with conntrack bypass.**

This complements eBPF one-way measurements by providing end-to-end RTT including:
- Outbound kube-proxy rule traversal
- Network propagation to worker
- Return path through kube-proxy
- Network propagation back to sender

#### Key Feature: Conntrack Bypass

Each packet uses a **unique sequential source port** to create a unique 5-tuple, forcing:
- Full kube-proxy rule traversal every time (no NAT cache hits)
- Fresh connection state for each measurement
- Realistic worst-case scenario for service discovery

#### Quick Start

```bash
# Run full automated experiment suite (tests at 100, 1k, 5k, 10k, 20k, 30k services)
sudo bash scripts/rtt/run-full-rtt-experiment.sh

# View CSV results summary
cat logs/rtt/rtt_experiment_summary_*.csv

# View individual detailed logs
ls -lt logs/rtt/
```

**What it does:**
- Creates dummy services at multiple scales automatically
- Runs RTT measurements at each scale
- Extracts metrics (Min/Mean/Max/P50/P95/P99) to CSV
- Cleans up between tests
- ~60 minutes total runtime

---

### Method 3: gRPC Load Generator

**Measures end-to-end application latency using actual gRPC requests.**

This method provides **application-level RTT measurements** including:
- Full TCP 3-way handshake
- gRPC protocol overhead
- Worker service processing time
- Complete round-trip through kube-proxy

**Key differences from eBPF:**
- Measures at application layer (not kernel)
- Uses worker pool (100 concurrent workers) for efficiency
- Includes network + gRPC + worker processing overhead
- Results are higher but more representative of real application behavior

#### Quick Start (Full Experiment Mode)

```bash
# Get worker ClusterIP 
# (configure proxymode --> iptables-nft / nftables)
# (configure rule-position --> last / first)
WORKER_IP=$(kubectl get svc worker -o jsonpath='{.spec.clusterIP}')

# Run full automated experiment (100, 1k, 5k, 10k, 20k services)
go run loadgen-dataplane/load_generator.go \
  --worker=$WORKER_IP:50051 \
  --rps=200 \
  --num-requests=15000 \
  --proxy-mode=<iptable / nftables> \
  --rule-position=<first / last> \
  --full-experiment

# View summary results
cat logs/dataplane/experiment_summary_*.csv
```

**What it does:**
- Creates dummy services at multiple scales automatically
- Runs gRPC load tests at each scale
- Tracks worker position in iptables rules
- Extracts metrics (Mean, P50, P95, P99) to CSV
- Cleans up between tests
- ~30-35 minutes total runtime (50 seconds per test)

#### Single Test Mode

For testing at a specific service count:

```bash
WORKER_IP=$(kubectl get svc worker -o jsonpath='{.spec.clusterIP}')

# Single test at current service count
go run loadgen-dataplane/load_generator.go \
  --worker=$WORKER_IP:50051 \
  --rps=1000 \
  --num-requests=50000 \
  --proxy-mode=iptables-nft \
  --rule-position=last \
  --service-count=20000
```

#### Output Files

**Individual Test:**
- `logs/dataplane/PM_<mode>_SC_<count>_RPS_<rps>_<timestamp>.log` - Statistics summary
- `logs/dataplane/PM_<mode>_SC_<count>_RPS_<rps>_<timestamp>.csv` - Raw per-request data

**Full Experiment:**
- `logs/dataplane/experiment_summary_<timestamp>.csv` - Aggregated results

#### CSV Format

**Individual test CSV** includes metadata:
```csv
# ProxyMode: iptables-nft
# ServiceCount: 1000
# WorkerPosition: 42
# TotalRules: 1523
# RPS: 200
# NumRequests: 10000
seq,rtt_us,data_plane_latency_us,worker_processing_us
0,523.45,245.67,32.11
1,525.32,246.21,33.90
...
```

**Summary CSV** format:
```csv
ServiceCount,ProxyMode,WorkerPosition,TotalRules,NumRequests,SuccessRate,MeanLatency_us,P50_us,P95_us,P99_us,RTTMean_us,RTTP95_us,RTTP99_us,LogFile
```

#### Understanding the Metrics

- **RTT (rtt_us)**: Total round-trip time including all overhead
- **Data Plane Latency (data_plane_latency_us)**: One-way network latency = (RTT - WorkerProcessing) / 2
- **Worker Processing (worker_processing_us)**: Time worker spent processing request
- **Worker Position**: Position in iptables KUBE-SERVICES chain (affects iptables latency)

#### Command-Line Flags

- `--worker` - Worker gRPC endpoint (host:port)
- `--rps` - Requests per second (default: 200)
- `--num-requests` - Total requests to send (default: 10000)
- `--proxy-mode` - Kube-proxy mode: `iptables-nft` or `nftables`
- `--service-count` - Service count for single test mode (default: 1)
- `--full-experiment` - Run full experiment at 100/1k/5k/10k/20k services
- `--rule-position` - Worker rule position: `first` (best-case O(1)) or `last` (worst-case O(n)) (default: `last`)

**Rule Position Significance:**
- **`first`**: Places worker rule at position 1 in iptables KUBE-SERVICES chain → immediate match (best-case O(1))
- **`last`**: Places worker rule at end of chain → traverses all rules (worst-case O(n))
- Only affects iptables mode; nftables always uses O(1) hash lookup regardless of position

**Example: Compare Best-Case vs Worst-Case iptables:**
```bash
WORKER_IP=$(kubectl get svc worker -o jsonpath='{.spec.clusterIP}')

# Best-case: rule at position 1 (O(1) behavior)
go run loadgen-dataplane/load_generator.go \
  --worker=$WORKER_IP:50051 \
  --proxy-mode=iptables-nft \
  --rule-position=first \
  --full-experiment

# Worst-case: rule at end (O(n) behavior)
go run loadgen-dataplane/load_generator.go \
  --worker=$WORKER_IP:50051 \
  --proxy-mode=iptables-nft \
  --rule-position=last \
  --full-experiment
```

#### Use Cases

1. **Application-level validation** - Confirms end-to-end behavior
2. **Compare with eBPF** - Should be higher due to protocol overhead
3. **Worker position correlation** - Verify iptables O(n) at application layer
4. **Real-world scenarios** - Includes all actual overhead applications see

**Note:** Results are higher than eBPF measurements due to TCP/gRPC overhead. Use eBPF for pure kernel-level kube-proxy performance.

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

## References

- [eBPF Performance Tools](http://www.brendangregg.com/bpf-performance-tools-book.html)
- [Kubernetes kube-proxy modes](https://kubernetes.io/docs/concepts/services-networking/service/#virtual-ips-and-service-proxies)
- [Linux pktgen](https://www.kernel.org/doc/Documentation/networking/pktgen.txt)
- [bpftrace Reference](https://github.com/iovisor/bpftrace/blob/master/docs/reference_guide.md)
   kubectl -n kube-system delete pods -l k8s-app=kube-proxy
   kubectl -n kube-system wait --for=condition=Ready pod -l k8s-app=kube-proxy
   ```

### Expected Results
- **iptables-nft**: Latency increases linearly with service count (O(n) rule scan)
- **nftables**: Latency remains relatively constant (O(1) hash table lookup)

