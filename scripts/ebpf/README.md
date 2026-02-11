# eBPF-based Kube-Proxy Latency Measurement

This approach uses **eBPF (Extended Berkeley Packet Filter)** to directly measure kernel-level packet processing latency in kube-proxy, bypassing all application overhead.

## Why eBPF?

**Advantages over gRPC approach:**
- ✅ **No application overhead** - measures pure kernel packet processing
- ✅ **Bypasses conntrack cache** - every packet is traced
- ✅ **Direct measurement** of `ipt_do_table` (iptables) vs `nft_do_chain` (nftables)
- ✅ **Microsecond precision** - nanosecond timestamps from kernel
- ✅ **Production-safe** - eBPF has built-in safety guarantees

**What it measures:**
```
Packet arrives → [kube-proxy rule traversal] → Packet forwarded
                  ↑                          ↑
                  eBPF entry probe          eBPF exit probe
                  (start timer)             (measure latency)
```

## Prerequisites

### 1. Install bpftrace

**Ubuntu/Debian:**
```bash
sudo apt update
sudo apt install -y bpftrace linux-headers-$(uname -r)
```

**RHEL/CentOS:**
```bash
sudo yum install -y bpftrace kernel-devel
```

**Verify installation:**
```bash
bpftrace --version
# Should show: bpftrace v0.x.x
```

### 2. Kernel Requirements

- **Minimum kernel version**: 4.9 (for eBPF support)
- **Recommended**: 5.4+ (for better eBPF features)

**Check kernel version:**
```bash
uname -r
```

### 3. Root Access

eBPF tracing requires root privileges:
```bash
sudo -i
```

## Quick Start

### Automated Experiment (Recommended)

```bash
# Run 30-second test at 10,000 packets/sec
sudo bash scripts/ebpf/run-experiment.sh 30 10000

# Custom duration and rate
sudo bash scripts/ebpf/run-experiment.sh 60 5000  # 60s, 5k pps
```

**What it does:**
1. Checks prerequisites (bpftrace, pktgen, root)
2. Detects worker service ClusterIP and kube-proxy mode
3. Starts eBPF tracing
4. Generates UDP packets using pktgen (bypasses conntrack)
5. Collects latency statistics
6. Saves results to `logs/ebpf/`

### Manual eBPF Tracing

**Step 1: Start bpftrace**
```bash
sudo bpftrace scripts/ebpf/trace-kubeproxy.bt
```

**Step 2: Generate traffic (in another terminal)**
```bash
# Get worker IP
WORKER_IP=$(kubectl get svc worker -o jsonpath='{.spec.clusterIP}')

# Use any traffic generator
# Option 1: Use existing load generator
go run loadgen-dataplane/load_generator.go --worker=$WORKER_IP:50051 --rps=100 --num-requests=1000

# Option 2: Simple UDP flood (bypasses conntrack)
sudo hping3 -c 10000 -d 64 -S -p 50051 --flood $WORKER_IP
```

**Step 3: Stop tracing (Ctrl-C)**
- Press Ctrl-C to stop
- bpftrace will display final statistics

## Output Interpretation

### Sample Output

```
====================================================
  FINAL REPORT
====================================================

--- IPTABLES MODE ---
Total invocations: 10000
Min latency: 12 us
Avg latency: 45 us
Max latency: 156 us

Latency distribution (logarithmic buckets):
@ipt_latency_us:
[8, 16)     1234 |@@@@@@                    |
[16, 32)    4567 |@@@@@@@@@@@@@@@@@@@@@@@@@@|
[32, 64)    3456 |@@@@@@@@@@@@@@@@@@@       |
[64, 128)    654 |@@@                       |
[128, 256)    89 |                          |

Latency distribution (linear 0-100us, 5us buckets):
@ipt_latency_linear:
[10, 15)    1234 |@@@@@@                    |
[15, 20)    2345 |@@@@@@@@@@@@              |
[20, 25)    3456 |@@@@@@@@@@@@@@@@@@@       |
[25, 30)    2234 |@@@@@@@@@@@@              |
[30, 35)    1123 |@@@@@@                    |
...
```

### Key Metrics

1. **Total invocations**: Number of times packet processing was traced
2. **Min/Avg/Max latency**: Latency statistics in microseconds
3. **Histogram**: Distribution of latencies
   - Logarithmic: Overview of range
   - Linear: Detailed distribution in 0-100µs range

### Expected Results

**iptables mode (O(n)):**
- With 100 services: ~15-25µs avg
- With 1,000 services: ~30-50µs avg
- With 10,000 services: ~100-200µs avg
- **Latency increases linearly with service count**

**nftables mode (O(1)):**
- With 100 services: ~10-15µs avg
- With 1,000 services: ~10-15µs avg
- With 10,000 services: ~10-15µs avg
- **Latency remains constant regardless of service count**

## Experimental Workflow

### Full Experiment Across Service Counts

```bash
#!/bin/bash
# Run experiments at different scales

for count in 100 500 1000 5000 10000; do
  echo "=========================================="
  echo "Testing with $count dummy services"
  echo "=========================================="
  
  # Create services
  cd scripts/create-dummy-services
  go run main.go -count $count
  
  # Wait for kube-proxy sync
  sleep 120
  
  # Run eBPF experiment
  cd ../..
  sudo bash scripts/ebpf/run-experiment.sh 30 10000
  
  # Parse results
  LOG_FILE=$(ls -t logs/ebpf/*.log | head -1)
  AVG_LATENCY=$(grep "Avg latency:" $LOG_FILE | awk '{print $3}')
  echo "Result: $count services → $AVG_LATENCY µs"
  
  # Cleanup
  cd scripts/delete-dummy-services
  go run main.go
  sleep 60
done
```

### Compare iptables vs nftables

```bash
# Test with iptables
kubectl -n kube-system edit cm kube-proxy
# Set: mode: "iptables"
kubectl -n kube-system delete pods -l k8s-app=kube-proxy
sleep 60

sudo bash scripts/ebpf/run-experiment.sh 60 10000
# Save results as iptables_baseline.log

# Switch to nftables
kubectl -n kube-system edit cm kube-proxy
# Set: mode: "nftables"
kubectl -n kube-system delete pods -l k8s-app=kube-proxy
sleep 60

sudo bash scripts/ebpf/run-experiment.sh 60 10000
# Save results as nftables_baseline.log

# Compare
diff logs/ebpf/iptables_baseline.log logs/ebpf/nftables_baseline.log
```

## Troubleshooting

### "ERROR: bpftrace not found"
```bash
sudo apt install bpftrace
```

### "ERROR: Could not attach probe"
- Check kernel version: `uname -r` (need 4.9+)
- Install kernel headers: `sudo apt install linux-headers-$(uname -r)`
- Verify function exists: `sudo bpftrace -l 'kprobe:ipt_do_table'`

### "No packets traced yet"
- Verify worker is running: `kubectl get pods -l app=worker`
- Check worker ClusterIP: `kubectl get svc worker`
- Ensure traffic is flowing (check pktgen status)
- Verify kube-proxy is running: `kubectl -n kube-system get pods -l k8s-app=kube-proxy`

### pktgen module not loading
```bash
# Check if module exists
modinfo pktgen

# If not found, install kernel modules
sudo apt install linux-modules-$(uname -r)
```

### High system load during tracing
- Reduce packet rate: `sudo bash scripts/ebpf/run-experiment.sh 30 5000`
- Use shorter duration: `sudo bash scripts/ebpf/run-experiment.sh 10 10000`
- eBPF itself has minimal overhead (<1% CPU typically)

## Files in this Directory

- **trace-kubeproxy.bt** - bpftrace script for kernel tracing
- **run-experiment.sh** - Automated experiment runner with pktgen
- **README.md** - This documentation

## Comparison: eBPF vs gRPC Approach

| Aspect | eBPF + pktgen | gRPC Load Generator |
|--------|---------------|---------------------|
| **Accuracy** | Direct kernel measurement | Application-level (includes TCP, gRPC overhead) |
| **Overhead** | ~0.1-0.5µs | ~100-200µs |
| **Conntrack bypass** | Yes (randomized ports) | No (unless new connections) |
| **Setup complexity** | Higher (requires root, kernel modules) | Lower (just Go) |
| **Measurement** | Pure kube-proxy forwarding | End-to-end latency |
| **Best for** | Proving O(n) vs O(1) mathematically | Real-world application performance |

## Advanced Usage

### Custom bpftrace Script

Measure additional metrics:
```bash
sudo bpftrace -e '
kprobe:ipt_do_table { @start[tid] = nsecs; }
kretprobe:ipt_do_table /@start[tid]/ {
  @latency_ns = hist(nsecs - @start[tid]);
  @avg_ns = avg(nsecs - @start[tid]);
  delete(@start[tid]);
}
interval:s:10 { print(@avg_ns); clear(@avg_ns); }
END { print(@latency_ns); }
'
```

### Export to CSV

Parse bpftrace output for plotting:
```bash
# Extract average latencies
grep "Avg latency:" logs/ebpf/*.log | awk '{print $3}' > latencies.csv

# Plot with gnuplot, matplotlib, etc.
```

## References

- [BPF Performance Tools](http://www.brendangregg.com/bpf-performance-tools-book.html)
- [bpftrace Reference Guide](https://github.com/iovisor/bpftrace/blob/master/docs/reference_guide.md)
- [Linux pktgen Documentation](https://www.kernel.org/doc/Documentation/networking/pktgen.txt)
