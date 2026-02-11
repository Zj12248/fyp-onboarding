#!/bin/bash
#
# Measure single packet latency after system warmup
# Usage: sudo bash measure-single-packet.sh [warmup_packets] [service_count]
#
# Example: sudo bash measure-single-packet.sh 1000 5000
#

set -e

WARMUP_PACKETS=${1:-1000}
SERVICE_COUNT=${2:-1000}
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo "======================================================"
echo "  Single Packet Latency Measurement (Post-Warmup)"
echo "======================================================"
echo ""

# ========================================
# Pre-flight checks
# ========================================

if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}ERROR: This script must be run as root${NC}"
    exit 1
fi

if ! command -v bpftrace &> /dev/null; then
    echo -e "${RED}ERROR: bpftrace not found${NC}"
    echo "Install with: sudo apt install bpftrace linux-headers-\$(uname -r)"
    exit 1
fi

if ! command -v kubectl &> /dev/null; then
    echo -e "${RED}ERROR: kubectl not found${NC}"
    exit 1
fi

# ========================================
# Get worker service info
# ========================================

echo -e "${BLUE}[1/4] Detecting worker service...${NC}"
WORKER_IP=$(kubectl get svc worker -o jsonpath='{.spec.clusterIP}' 2>/dev/null)

if [ -z "$WORKER_IP" ]; then
    echo -e "${RED}ERROR: Worker service not found${NC}"
    echo "Deploy with: kubectl apply -f knative/worker-service.yaml"
    exit 1
fi

echo "  Worker ClusterIP: $WORKER_IP:50051"

# ========================================
# Detect kube-proxy mode
# ========================================

echo -e "${BLUE}[2/4] Detecting kube-proxy mode...${NC}"
MODE=$(kubectl -n kube-system get cm kube-proxy -o jsonpath='{.data.config\.conf}' 2>/dev/null | grep 'mode:' | awk '{print $2}' | tr -d '"' || echo "iptables")

if [ -z "$MODE" ]; then
    MODE="iptables"
fi

echo "  Kube-proxy mode: $MODE"
echo "  Expected trace: ${MODE} rules"

# Check worker position in iptables chain
if [ "$MODE" = "iptables" ]; then
    WORKER_POSITION=$(sudo iptables -t nat -L KUBE-SERVICES --line-numbers -n | grep "$WORKER_IP" | grep "dpt:50051" | head -1 | awk '{print $1}')
    TOTAL_RULES=$(sudo iptables -t nat -L KUBE-SERVICES --line-numbers -n | tail -n +3 | wc -l)
    
    if [ -n "$WORKER_POSITION" ] && [ -n "$TOTAL_RULES" ] && [ "$TOTAL_RULES" -gt 0 ]; then
        RELATIVE_POSITION=$(awk "BEGIN {printf \"%.2f\", ($WORKER_POSITION / $TOTAL_RULES) * 100}")
        echo "  Worker position: $WORKER_POSITION / $TOTAL_RULES (${RELATIVE_POSITION}%)"
        
        if awk "BEGIN {exit !($RELATIVE_POSITION < 25)}"; then
            echo -e "  ${YELLOW}⚠ WARNING: Worker is near beginning of chain (may underestimate O(n) cost)${NC}"
        fi
    else
        WORKER_POSITION="unknown"
        TOTAL_RULES="unknown"
        RELATIVE_POSITION="unknown"
    fi
else
    WORKER_POSITION="N/A"
    TOTAL_RULES="N/A"
    RELATIVE_POSITION="N/A"
fi
echo ""

# ========================================
# System warmup
# ========================================

echo -e "${BLUE}[3/4] Warming up system with $WARMUP_PACKETS packets...${NC}"

# Detect network interface for worker service
WORKER_IFACE=$(ip route get $WORKER_IP | grep -oP 'dev \K\S+' | head -1)
if [ -z "$WORKER_IFACE" ]; then
    WORKER_IFACE="eth0"
fi
echo "  Using interface: $WORKER_IFACE"

# Load pktgen module
if ! lsmod | grep -q pktgen; then
    echo "  Loading pktgen module..."
    modprobe pktgen
fi

# Configure pktgen for warmup
PGDEV="/proc/net/pktgen/kpktgend_0"
PGCTL="/proc/net/pktgen/pgctrl"

echo "rem_device_all" > $PGCTL
echo "add_device $WORKER_IFACE" > $PGCTL

PGDEV="/proc/net/pktgen/$WORKER_IFACE"
echo "count $WARMUP_PACKETS" > $PGDEV
echo "clone_skb 0" > $PGDEV
echo "pkt_size 64" > $PGDEV
echo "delay 0" > $PGDEV
echo "dst $WORKER_IP" > $PGDEV
echo "dst_mac $(ip neigh show $WORKER_IP | awk '{print $5}' | head -1)" > $PGDEV
echo "udp_dst_min 50051" > $PGDEV
echo "udp_dst_max 50051" > $PGDEV
echo "udp_src_min 1024" > $PGDEV
echo "udp_src_max 65535" > $PGDEV
echo "flag UDPSRC_RND" > $PGDEV

echo "  Starting warmup..."
echo "start" > $PGCTL

# Wait for warmup to complete
sleep 2

echo -e "${GREEN}  Warmup complete!${NC}"
echo ""

# ========================================
# Measure single packet
# ========================================

echo -e "${BLUE}[4/4] Tracing single packet...${NC}"
echo "  Measurement will capture ONE packet with nanosecond precision"
echo ""

# Start bpftrace in background
TRACE_OUTPUT=$(mktemp)
bpftrace "$SCRIPT_DIR/trace-single-packet.bt" > "$TRACE_OUTPUT" 2>&1 &
TRACE_PID=$!

# Give bpftrace time to attach probes
sleep 2

# Send one packet with randomized port
RANDOM_PORT=$((1024 + RANDOM % 64511))

# Use hping3 for single packet (cleaner than pktgen for one packet)
if command -v hping3 &> /dev/null; then
    hping3 -2 -p 50051 -s $RANDOM_PORT -c 1 $WORKER_IP &> /dev/null || true
else
    # Fallback: use nc or pktgen for 1 packet
    echo "count 1" > $PGDEV
    echo "udp_src_min $RANDOM_PORT" > $PGDEV
    echo "udp_src_max $RANDOM_PORT" > $PGDEV
    echo "start" > $PGCTL
fi

# Wait for trace to complete (will auto-exit after one packet)
sleep 3

# Check if bpftrace is still running
if ps -p $TRACE_PID > /dev/null 2>&1; then
    kill $TRACE_PID 2>/dev/null || true
fi

# ========================================
# Display results
# ========================================

echo ""
echo "======================================================"
echo "  Single Packet Measurement Results"
echo "======================================================"
cat "$TRACE_OUTPUT"
echo "======================================================"
echo ""

# Extract latency value
LATENCY=$(grep -oP '(?<=Latency: )\d+\.\d+' "$TRACE_OUTPUT" || echo "N/A")

if [ "$LATENCY" != "N/A" ]; then
    echo -e "${GREEN}✓ Successfully measured: ${LATENCY} µs${NC}"
    echo ""
    echo "Context:"
    echo "  Service count: ~$SERVICE_COUNT"
    echo "  Kube-proxy mode: $MODE"
    echo "  Worker position: $WORKER_POSITION / $TOTAL_RULES (${RELATIVE_POSITION}%)"
    echo "  Post-warmup steady-state latency"
else
    echo -e "${RED}✗ Failed to capture packet${NC}"
    echo ""
    echo "Troubleshooting:"
    echo "  1. Check worker is running: kubectl get pods -l app=worker"
    echo "  2. Verify ClusterIP: kubectl get svc worker"
    echo "  3. Try installing hping3: sudo apt install hping3"
fi

# Cleanup
rm -f "$TRACE_OUTPUT"

echo ""
echo "To measure at different scales:"
echo "  1. Create more services: cd scripts/create-dummy-services && go run main.go -count 5000"
echo "  2. Re-run this script: sudo bash scripts/ebpf/measure-single-packet.sh 1000 5000"
