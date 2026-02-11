#!/bin/bash
# Automated eBPF experiment for measuring kube-proxy data plane latency
# Uses bpftrace + pktgen to measure iptables vs nftables performance

set -e

# ========================================
# Configuration
# ========================================

DURATION=${1:-10}  # Test duration in seconds
PACKET_RATE=${2:-10000}  # Packets per second
WARMUP_PACKETS=${3:-100}  # Warmup packet count (default: 100)
WORKER_IP=""
PROXY_MODE=""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="logs/ebpf"

# Source common functions
source "$SCRIPT_DIR/common.sh"

echo "=============================================="
echo "  eBPF Kube-Proxy Latency Experiment"
echo "=============================================="
echo "Duration: ${DURATION}s"
echo "Packet rate: ${PACKET_RATE} pps"
echo "Warmup: ${WARMUP_PACKETS} packets"
echo ""

# ========================================
# Pre-flight Checks
# ========================================

echo "[1/7] Running pre-flight checks..."

# Check root privileges
if [ "$EUID" -ne 0 ]; then
    echo "ERROR: This script must be run as root (sudo)"
    exit 1
fi

# Check bpftrace installation
if ! command -v bpftrace &> /dev/null; then
    echo "ERROR: bpftrace not found. Install with:"
    echo "  Ubuntu: sudo apt install bpftrace"
    echo "  RHEL: sudo yum install bpftrace"
    exit 1
fi

# Check kernel version (need 4.9+ for eBPF)
KERNEL_VERSION=$(uname -r | cut -d. -f1)
if [ "$KERNEL_VERSION" -lt 4 ]; then
    echo "ERROR: Kernel version too old. Need 4.9+, found $(uname -r)"
    exit 1
fi

# Check pktgen module
if ! lsmod | grep -q pktgen; then
    echo "Loading pktgen kernel module..."
    modprobe pktgen || {
        echo "ERROR: Failed to load pktgen module"
        exit 1
    }
fi

echo "✓ All prerequisites met"
echo ""

# ========================================
# Get Worker Service Information
# ========================================

echo "[2/7] Detecting worker service..."

WORKER_IP=$(kubectl get svc worker -o jsonpath='{.spec.clusterIP}' 2>/dev/null)
if [ -z "$WORKER_IP" ]; then
    echo "ERROR: Worker service not found. Deploy with:"
    echo "  kubectl apply -f knative/worker-service.yaml"
    exit 1
fi

echo "✓ Worker ClusterIP: $WORKER_IP"
echo ""

# ========================================
# Detect Kube-Proxy Mode
# ========================================

echo "[3/7] Detecting kube-proxy mode..."

PROXY_MODE=$(get_kubeproxy_mode)

echo "✓ Kube-proxy mode: $PROXY_MODE"
echo ""

# ========================================
# Check Worker Position in iptables Chain
# ========================================

echo "[3.5/7] Checking worker position in KUBE-SERVICES chain..."

WORKER_POSITION=$(sudo iptables -t nat -L KUBE-SERVICES --line-numbers -n | grep "$WORKER_IP" | grep "dpt:50051" | head -1 | awk '{print $1}')
TOTAL_RULES=$(sudo iptables -t nat -L KUBE-SERVICES --line-numbers -n | tail -n +3 | wc -l)

if [ -n "$WORKER_POSITION" ] && [ -n "$TOTAL_RULES" ] && [ "$TOTAL_RULES" -gt 0 ]; then
    if [ "$PROXY_MODE" = "nftables" ]; then
        echo "✓ Worker position: $WORKER_POSITION / $TOTAL_RULES [nftables uses hash tables - position irrelevant]"
    else
        echo "✓ Worker position: $WORKER_POSITION / $TOTAL_RULES"
    fi
else
    echo "⚠ Could not determine worker position in iptables chain"
    WORKER_POSITION="unknown"
    TOTAL_RULES="unknown"
fi
echo ""

# ========================================
# Setup Output Directory
# ========================================

echo "[4/7] Setting up output directory..."

mkdir -p "$OUTPUT_DIR"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
LOG_FILE="$OUTPUT_DIR/ebpf_${PROXY_MODE}_${TIMESTAMP}.log"

# Write experiment metadata to log file
cat > "$LOG_FILE" <<EOF
============================================
  eBPF Kube-Proxy Latency Experiment
============================================
Timestamp: $(date '+%Y-%m-%d %H:%M:%S')
Kube-proxy mode: $PROXY_MODE
Worker ClusterIP: $WORKER_IP
Duration: ${DURATION}s
Packet rate: ${PACKET_RATE} pps
Warmup packets: ${WARMUP_PACKETS}
Worker position: $WORKER_POSITION / $TOTAL_RULES
============================================

EOF

echo "✓ Logs will be saved to: $LOG_FILE"
echo ""

# ========================================
# Configure pktgen
# ========================================

echo "[5/7] Configuring pktgen..."

# Find network interface
IFACE=$(ip route get "$WORKER_IP" | grep -oP 'dev \K\S+' | head -1)
if [ -z "$IFACE" ]; then
    echo "ERROR: Could not determine network interface for $WORKER_IP"
    exit 1
fi

echo "Using interface: $IFACE"

# Reset pktgen
echo "reset" > /proc/net/pktgen/pgctrl 2>/dev/null || true
sleep 1

# Configure thread
PGDEV=/proc/net/pktgen/kpktgend_0
echo "rem_device_all" > $PGDEV
echo "add_device $IFACE" > $PGDEV

# Configure device
PGDEV=/proc/net/pktgen/$IFACE

# Calculate count based on duration and rate
PACKET_COUNT=$((DURATION * PACKET_RATE))

# Clear conntrack before starting to ensure clean state
echo "Clearing conntrack table..."
conntrack -F 2>/dev/null || true

cat > $PGDEV <<EOF
count $PACKET_COUNT
clone_skb 0
pkt_size 64
delay 0
dst $WORKER_IP
dst_min $WORKER_IP
dst_max $WORKER_IP
udp_src_min 1024
udp_src_max 65535
udp_dst_min 50051
udp_dst_max 50051
EOF

echo "✓ pktgen configured: $PACKET_COUNT packets to $WORKER_IP:50051"
echo ""

# ========================================
# System Warmup
# ========================================

if [ "$WARMUP_PACKETS" -gt 0 ]; then
    echo "[6/8] Warming up system with $WARMUP_PACKETS packets..."
    
    # Configure pktgen for warmup (sequential ports)
    cat > $PGDEV <<EOF
count $WARMUP_PACKETS
clone_skb 0
pkt_size 64
delay 0
dst $WORKER_IP
dst_min $WORKER_IP
dst_max $WORKER_IP
udp_src_min 1024
udp_src_max 65535
udp_dst_min 50051
udp_dst_max 50051
EOF
    
    # Start warmup
    echo "start" > /proc/net/pktgen/pgctrl
    
    # Wait for warmup to complete
    sleep 2
    while true; do
        SENT=$(cat /proc/net/pktgen/$IFACE 2>/dev/null | grep "Sofar:" | awk '{print $2}')
        if [ -z "$SENT" ] || [ "$SENT" -ge "$WARMUP_PACKETS" ]; then
            break
        fi
        sleep 0.5
    done
    
    echo "✓ Warmup complete! System caches primed."
    echo ""
    
    # Reset pktgen for actual test
    echo "reset" > /proc/net/pktgen/pgctrl 2>/dev/null || true
    sleep 1
    
    # Reconfigure thread
    PGDEV=/proc/net/pktgen/kpktgend_0
    echo "rem_device_all" > $PGDEV
    echo "add_device $IFACE" > $PGDEV
    
    # Reconfigure device for actual measurement
    PGDEV=/proc/net/pktgen/$IFACE
    cat > $PGDEV <<EOF
count $PACKET_COUNT
clone_skb 0
pkt_size 64
delay 0
dst $WORKER_IP
dst_min $WORKER_IP
dst_max $WORKER_IP
udp_src_min 1024
udp_src_max 65535
udp_dst_min 50051
udp_dst_max 50051
flag UDPSRC_RND
flag UDPDST_RND
EOF
else
    echo "[6/8] Skipping warmup (WARMUP_PACKETS=0)"
    echo ""
fi

# ========================================
# Start eBPF Tracing
# ========================================

echo "[7/8] Starting eBPF tracing..."
echo "This will run for ${DURATION} seconds..."
echo ""

# Start bpftrace in background (append to existing metadata)
bpftrace "$SCRIPT_DIR/trace-kubeproxy.bt" >> "$LOG_FILE" 2>&1 &
BPFTRACE_PID=$!

# Give bpftrace time to attach probes
sleep 3

# Check if bpftrace is still running
if ! kill -0 $BPFTRACE_PID 2>/dev/null; then
    echo "ERROR: bpftrace failed to start. Check log:"
    cat "$LOG_FILE"
    exit 1
fi

echo "✓ bpftrace started (PID: $BPFTRACE_PID)"
echo ""

# ========================================
# Start Packet Generation
# ========================================

echo "[8/8] Starting packet generation..."
echo ""

# Start pktgen
echo "start" > /proc/net/pktgen/pgctrl

# Monitor progress and clear conntrack periodically
START_TIME=$(date +%s)
LAST_CONNTRACK_CLEAR=0
while [ $(($(date +%s) - START_TIME)) -lt $DURATION ]; do
    ELAPSED=$(($(date +%s) - START_TIME))
    REMAINING=$((DURATION - ELAPSED))
    
    # Get packet count
    SENT=$(cat /proc/net/pktgen/$IFACE 2>/dev/null | grep "Sofar:" | awk '{print $2}')
    
    # Clear conntrack every 50,000 packets to prevent port collision caching
    if [ -n "$SENT" ] && [ $((SENT / 50000)) -gt $LAST_CONNTRACK_CLEAR ]; then
        LAST_CONNTRACK_CLEAR=$((SENT / 50000))
        conntrack -F 2>/dev/null || true
        printf "\r[%d/%ds] Packets: %s [conntrack cleared @ %d×50k] " $ELAPSED $DURATION $SENT $LAST_CONNTRACK_CLEAR
    else
        printf "\r[%d/%ds] Packets sent: %s " $ELAPSED $DURATION $SENT
    fi
    
    sleep 2
done

echo ""
echo ""
echo "✓ Packet generation complete"
echo ""

# ========================================
# Stop Tracing and Collect Results
# ========================================

echo "Stopping eBPF tracing..."
kill -INT $BPFTRACE_PID
wait $BPFTRACE_PID 2>/dev/null || true

echo "✓ Tracing stopped"
echo ""

# ========================================
# Display Results
# ========================================

echo "=============================================="
echo "  EXPERIMENT COMPLETE"
echo "=============================================="
echo ""
echo "Configuration:"
echo "  - Kube-proxy mode: $PROXY_MODE"
echo "  - Worker ClusterIP: $WORKER_IP"
echo "  - Worker position: $WORKER_POSITION / $TOTAL_RULES"
echo "  - Duration: ${DURATION}s"
echo "  - Warmup: ${WARMUP_PACKETS} packets"
echo "  - Target packet rate: ${PACKET_RATE} pps"
echo "  - Interface: $IFACE"
echo ""
echo "Results saved to: $LOG_FILE"
echo ""
echo "View results:"
echo "  cat $LOG_FILE"
echo ""
echo "Summary:"
tail -n 50 "$LOG_FILE" | grep -A 20 "FINAL REPORT" || echo "Processing..."
echo ""
echo "Worker Position: $WORKER_POSITION / $TOTAL_RULES"
echo ""
echo "=============================================="
