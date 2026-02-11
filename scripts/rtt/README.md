# RTT Measurement Tools

Round-trip time measurement through kube-proxy with conntrack bypass (randomized source ports force full rule traversal).

## Setup

```bash
sudo apt install hping3
```

## Usage

```bash
# Default: 100 packets, 10 warmup
sudo bash scripts/rtt/measure-rtt-hping3.sh

# Custom: 500 packets, 20 warmup
sudo bash scripts/rtt/measure-rtt-hping3.sh 500 20

# View results
cat logs/rtt/rtt_iptables_*.log
```

## Output Files

- `logs/rtt/rtt_<mode>_<timestamp>.log` - Statistics (Min/Mean/Max/P50/P95/P99 in µs)
- `logs/rtt/rtt_<mode>_<timestamp>_raw.txt` - Raw values (one per line in µs)

## Quick Workflows

**iptables vs nftables:**
```bash
# Switch mode in: kubectl -n kube-system edit cm kube-proxy
kubectl -n kube-system delete pods -l k8s-app=kube-proxy && sleep 60
sudo bash scripts/rtt/measure-rtt-hping3.sh
grep "Mean RTT:" logs/rtt/rtt_*.log | tail -1
```

**Scale testing:**
```bash
for count in 100 1000 5000 10000; do
  cd scripts/create-dummy-services && go run main.go -count $count && cd ../..
  sleep 30
  sudo bash scripts/rtt/measure-rtt-hping3.sh 100
  cd scripts/delete-dummy-services && go run main.go && cd ../..
done
```

## Troubleshooting

- **"hping3: command not found"** → `sudo apt install hping3`
- **"Permission denied"** → Run with `sudo`
- **"Worker service not found"** → `kubectl apply -f knative/worker-service.yaml`
