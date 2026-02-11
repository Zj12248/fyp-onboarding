#!/bin/bash
#
# RTT measurement using hping3 with randomized source ports
# Each packet uses a different source port to bypass conntrack and force full rule traversal
#
# Usage: sudo bash measure-rtt-hping3.sh [packet_count] [warmup_count]
#

set -e

# ========================================
# Configuration
# ========================================

PACKET_COUNT=${1:-100}      # Number of RTT measurements
WARMUP_COUNT=${2:-10}        # Warmup packets
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
OUTPUT_DIR="$PROJECT_ROOT/logs/rtt"

# Source common functions
source "$SCRIPT_DIR/common.sh"

echo "=============================================="
echo "  RTT Measurement with hping3"
echo "=============================================="
echo "Packet count: $PACKET_COUNT"
echo "Warmup: $WARMUP_COUNT packets"
echo ""

# ========================================
# Pre-flight Checks
# ========================================

echo "[1/5] Running pre-flight checks..."

if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}ERROR: This script must be run as root (sudo)${NC}"
    exit 1
fi

if ! command -v hping3 &> /dev/null; then
    echo -e "${YELLOW}hping3 not found. Installing...${NC}"
    apt-get install -y hping3
    echo -e "${GREEN}✓ hping3 installed${NC}"
fi

echo -e "${GREEN}✓ Prerequisites met${NC}"
echo ""

# ========================================
# Get Worker Service
# ========================================

echo "[2/5] Detecting worker service..."

WORKER_IP=$(get_worker_ip)
if [ -z "$WORKER_IP" ]; then
    echo -e "${RED}ERROR: Worker service not found${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Worker ClusterIP: $WORKER_IP${NC}"
echo ""

# ========================================
# Detect Kube-Proxy Mode
# ========================================

echo "[3/5] Detecting kube-proxy mode..."

PROXY_MODE=$(get_kubeproxy_mode)

echo -e "${GREEN}✓ Kube-proxy mode: $PROXY_MODE${NC}"
echo ""

# ========================================
# Setup Output
# ========================================

echo "[4/5] Setting up output directory..."

mkdir -p "$OUTPUT_DIR"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
LOG_FILE="$OUTPUT_DIR/rtt_${PROXY_MODE}_${TIMESTAMP}.log"
RAW_FILE="$OUTPUT_DIR/rtt_${PROXY_MODE}_${TIMESTAMP}_raw.txt"

cat > "$LOG_FILE" <<EOF
============================================
  RTT Measurement with hping3
============================================
Timestamp: $(date '+%Y-%m-%d %H:%M:%S')
Kube-proxy mode: $PROXY_MODE
Worker ClusterIP: $WORKER_IP
Packet count: $PACKET_COUNT
Warmup: $WARMUP_COUNT packets
============================================

EOF

echo -e "${GREEN}✓ Logs will be saved to: $LOG_FILE${NC}"
echo ""

# ========================================
# Clean Connection Tracking State
# ========================================

echo "Flushing conntrack table for clean state..."
sudo conntrack -F > /dev/null 2>&1 || true
echo -e "${GREEN}✓ Conntrack flushed${NC}"
echo ""

# ========================================
# Warmup Phase
# ========================================

if [ "$WARMUP_COUNT" -gt 0 ]; then
    echo "[5/5] Warming up with $WARMUP_COUNT packets..."
    
    SRC_PORT=10000
    for i in $(seq 1 $WARMUP_COUNT); do
        hping3 -2 -p 50051 -s $SRC_PORT -c 1 $WORKER_IP > /dev/null 2>&1 || true
        SRC_PORT=$((SRC_PORT + 1))
    done
    
    echo -e "${GREEN}✓ Warmup complete${NC}"
    echo ""
fi

# ========================================
# RTT Measurement
# ========================================

echo "Starting RTT measurements..."
echo "Each packet uses a unique sequential source port to bypass conntrack"
echo ""

# Array to store RTT values
declare -a rtt_values

# Start at port 25000 for actual measurements
SRC_PORT=25000

for i in $(seq 1 $PACKET_COUNT); do
    
    # Send single packet and capture RTT
    # hping3 output format: "rtt=X.Y ms"
    OUTPUT=$(hping3 -2 -p 50051 -s $SRC_PORT -c 1 $WORKER_IP 2>&1)
    
    # Extract RTT in milliseconds
    RTT=$(echo "$OUTPUT" | grep -oP 'rtt=\K[0-9.]+' || echo "")
    
    if [ -n "$RTT" ]; then
        # Convert to microseconds for consistency with eBPF measurements
        RTT_US=$(awk "BEGIN {printf \"%.0f\", $RTT * 1000}")
        rtt_values+=($RTT_US)
        echo "$RTT_US" >> "$RAW_FILE"
        
        # Progress indicator
        if [ $((i % 10)) -eq 0 ]; then
            printf "\r[%d/%d] Latest RTT: %.2f ms (%.0f µs)" $i $PACKET_COUNT $RTT $RTT_US
        fi
    else
        echo "Warning: Failed to measure RTT for packet $i (port $SRC_PORT)" >&2
    fi
    
    # Increment port for next packet
    SRC_PORT=$((SRC_PORT + 1))
done

echo ""
echo ""
echo -e "${GREEN}✓ Measurement complete${NC}"
echo ""

# ========================================
# Calculate Statistics
# ========================================

echo "Calculating statistics..."

if [ ${#rtt_values[@]} -eq 0 ]; then
    echo -e "${RED}ERROR: No RTT measurements collected${NC}"
    exit 1
fi

# Sort array for percentile calculations
IFS=$'\n' sorted_rtt=($(sort -n <<<"${rtt_values[*]}"))
unset IFS

# Calculate statistics
COUNT=${#sorted_rtt[@]}
MIN=${sorted_rtt[0]}
MAX=${sorted_rtt[-1]}

# Mean
SUM=0
for val in "${sorted_rtt[@]}"; do
    SUM=$((SUM + val))
done
MEAN=$((SUM / COUNT))

# Percentiles
P50_IDX=$((COUNT / 2))
P95_IDX=$((COUNT * 95 / 100))
P99_IDX=$((COUNT * 99 / 100))

P50=${sorted_rtt[$P50_IDX]}
P95=${sorted_rtt[$P95_IDX]}
P99=${sorted_rtt[$P99_IDX]}

# ========================================
# Write Results
# ========================================

cat >> "$LOG_FILE" <<EOF
====================================================
  RESULTS
====================================================

Statistics (in microseconds):
  Total measurements: $COUNT
  Min RTT:            $MIN µs ($(awk "BEGIN {printf \"%.3f\", $MIN/1000}") ms)
  Mean RTT:           $MEAN µs ($(awk "BEGIN {printf \"%.3f\", $MEAN/1000}") ms)
  Max RTT:            $MAX µs ($(awk "BEGIN {printf \"%.3f\", $MAX/1000}") ms)
  
Percentiles:
  P50 (Median):       $P50 µs ($(awk "BEGIN {printf \"%.3f\", $P50/1000}") ms)
  P95:                $P95 µs ($(awk "BEGIN {printf \"%.3f\", $P95/1000}") ms)
  P99:                $P99 µs ($(awk "BEGIN {printf \"%.3f\", $P99/1000}") ms)

Note: Each packet used a unique sequential source port to bypass conntrack cache.
Warmup used ports 10000+, measurements used ports 25000+.
This forces full kube-proxy rule traversal for every measurement.

Raw data saved to: $RAW_FILE

====================================================
EOF

# ========================================
# Display Results
# ========================================

echo "=============================================="
echo "  MEASUREMENT COMPLETE"
echo "=============================================="
echo ""
cat "$LOG_FILE" | tail -n +13
echo ""
echo "Results saved to: $LOG_FILE"
echo "Raw data: $RAW_FILE"
echo ""
echo "=============================================="
