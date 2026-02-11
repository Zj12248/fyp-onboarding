#!/bin/bash
#
# Full automated RTT experiment: Test kube-proxy RTT latency at multiple service counts
# Usage: sudo bash run-full-rtt-experiment.sh [packet_count] [warmup_count]
#
# Example: sudo bash run-full-rtt-experiment.sh 100 10
#

set -e

# Source common functions from ebpf directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../ebpf/common.sh"

PACKET_COUNT=${1:-100}
WARMUP_COUNT=${2:-10}
SERVICE_COUNTS=(100 1000 5000 10000 20000 30000)
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

print_experiment_header "Full RTT Kube-Proxy Experiment Suite"

echo "Configuration:"
echo "  Service counts: ${SERVICE_COUNTS[@]}"
echo "  Packets per test: ${PACKET_COUNT}"
echo "  Warmup: ${WARMUP_COUNT} packets"
echo ""
echo "Estimated total time: ~$((${#SERVICE_COUNTS[@]} * 10)) minutes"
echo ""

# ========================================
# Pre-flight Checks
# ========================================

echo -e "${BLUE}[Pre-flight] Checking prerequisites...${NC}"

if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}ERROR: This script must be run as root (sudo)${NC}"
    exit 1
fi

if ! command -v kubectl &> /dev/null; then
    echo -e "${RED}ERROR: kubectl not found${NC}"
    exit 1
fi

if ! command -v hping3 &> /dev/null; then
    echo -e "${YELLOW}hping3 not found. Installing...${NC}"
    apt-get install -y hping3
    echo -e "${GREEN}✓ hping3 installed${NC}"
fi

# Ensure worker is deployed
WORKER_IP=$(ensure_worker_deployed "$PROJECT_ROOT")
if [ -z "$WORKER_IP" ]; then
    exit 1
fi

echo -e "${GREEN}✓ Worker service ready: $WORKER_IP${NC}"
echo ""

# Get kube-proxy mode
PROXY_MODE=$(get_kubeproxy_mode)
echo -e "${CYAN}Kube-proxy mode: $PROXY_MODE${NC}"
echo ""

# Create results summary file
RESULTS_FILE="$PROJECT_ROOT/logs/rtt/rtt_experiment_summary_$(date +%Y%m%d_%H%M%S).csv"
mkdir -p "$(dirname "$RESULTS_FILE")"
echo "ServiceCount,ProxyMode,WorkerPosition,TotalRules,MinRTT_us,MeanRTT_us,MaxRTT_us,P50_us,P95_us,P99_us,LogFile" > "$RESULTS_FILE"

echo -e "${GREEN}Results will be saved to: $RESULTS_FILE${NC}"
echo ""

# ========================================
# Initial Cleanup
# ========================================

echo -e "${BLUE}[Initial] Cleaning up all existing dummy services...${NC}"
delete_dummy_services "$PROJECT_ROOT" || true
sleep 30
echo -e "${GREEN}✓ Starting with clean state${NC}"
echo ""

# Track current service count
CURRENT_SERVICE_COUNT=0

# ========================================
# Main Experiment Loop
# ========================================

for SERVICE_COUNT in "${SERVICE_COUNTS[@]}"; do
    print_experiment_header "Testing with $SERVICE_COUNT services"
    
    # Calculate how many services to add
    SERVICES_TO_ADD=$((SERVICE_COUNT - CURRENT_SERVICE_COUNT))
    
    if [ $SERVICES_TO_ADD -gt 0 ]; then
        # Step 1: Create additional dummy services
        echo -e "${BLUE}[1/4] Creating $SERVICES_TO_ADD additional dummy services (total: $SERVICE_COUNT)...${NC}"
        START_TIME=$(date +%s)
        if ! create_dummy_services "$SERVICES_TO_ADD" "$PROJECT_ROOT"; then
            echo -e "${RED}✗ Failed to create services${NC}"
            continue
        fi
        CREATE_DURATION=$(($(date +%s) - START_TIME))
        echo -e "${GREEN}✓ Created $SERVICES_TO_ADD services in ${CREATE_DURATION}s${NC}"
        CURRENT_SERVICE_COUNT=$SERVICE_COUNT
        echo ""
        
        # Step 2: Wait for kube-proxy sync
        echo -e "${BLUE}[2/4] Waiting for kube-proxy to sync rules (120s)...${NC}"
        wait_for_kubeproxy_sync 120
    else
        echo -e "${YELLOW}Already at $SERVICE_COUNT services, skipping creation${NC}"
        echo ""
    fi
    
    # Verify service count
    ACTUAL_COUNT=$(get_dummy_service_count)
    echo -e "${GREEN}✓ Verified: $ACTUAL_COUNT dummy services + worker${NC}"
    echo ""
    
    # Step 3: Get worker position
    echo -e "${BLUE}[3/4] Checking worker position...${NC}"
    if [ "$PROXY_MODE" = "iptables" ]; then
        WORKER_POS=$(sudo iptables -t nat -L KUBE-SERVICES --line-numbers -n | grep "$WORKER_IP" | grep "dpt:50051" | head -1 | awk '{print $1}')
        TOTAL_RULES=$(sudo iptables -t nat -L KUBE-SERVICES --line-numbers -n | tail -n +3 | wc -l)
    else
        WORKER_POS="N/A"
        TOTAL_RULES="N/A"
    fi
    echo -e "${GREEN}✓ Worker position: $WORKER_POS / $TOTAL_RULES${NC}"
    echo ""
    
    # Step 4: Run RTT measurement
    echo -e "${BLUE}[4/4] Running RTT measurement...${NC}"
    bash "$SCRIPT_DIR/measure-rtt-hping3.sh" $PACKET_COUNT $WARMUP_COUNT
    
    # Find the most recent log file
    LATEST_LOG=$(ls -t "$PROJECT_ROOT/logs/rtt/rtt_${PROXY_MODE}_"*.log | head -1)
    
    echo -e "${GREEN}✓ Measurement complete: $LATEST_LOG${NC}"
    echo ""
    
    # Extract metrics from log file
    echo -e "${CYAN}Extracting metrics...${NC}"
    
    MIN_RTT=$(grep "Min RTT:" "$LATEST_LOG" | grep -oP '\d+' | head -1 || echo "")
    MEAN_RTT=$(grep "Mean RTT:" "$LATEST_LOG" | grep -oP '\d+' | head -1 || echo "")
    MAX_RTT=$(grep "Max RTT:" "$LATEST_LOG" | grep -oP '\d+' | head -1 || echo "")
    P50_RTT=$(grep "P50 (Median):" "$LATEST_LOG" | grep -oP '\d+' | head -1 || echo "")
    P95_RTT=$(grep "P95:" "$LATEST_LOG" | grep -oP '\d+' | head -1 || echo "")
    P99_RTT=$(grep "P99:" "$LATEST_LOG" | grep -oP '\d+' | head -1 || echo "")
    
    # Append to CSV
    echo "$SERVICE_COUNT,$PROXY_MODE,$WORKER_POS,$TOTAL_RULES,$MIN_RTT,$MEAN_RTT,$MAX_RTT,$P50_RTT,$P95_RTT,$P99_RTT,$LATEST_LOG" >> "$RESULTS_FILE"
    
    echo -e "${GREEN}✓ Results recorded${NC}"
    echo ""
    
    echo "Quick Summary:"
    echo "  Service count: $SERVICE_COUNT"
    echo "  Worker position: $WORKER_POS / $TOTAL_RULES"
    echo "  Mean RTT: ${MEAN_RTT:-N/A} µs"
    echo "  Range: ${MIN_RTT:-N/A} - ${MAX_RTT:-N/A} µs"
    echo "  P50: ${P50_RTT:-N/A} µs"
    echo "  P95: ${P95_RTT:-N/A} µs"
    echo "  P99: ${P99_RTT:-N/A} µs"
    echo ""
    
    # Pause between tests
    if [ "$SERVICE_COUNT" != "${SERVICE_COUNTS[-1]}" ]; then
        echo -e "${YELLOW}Pausing 30 seconds before next test...${NC}"
        sleep 30
        echo ""
    fi
done

# ========================================
# Final Cleanup and Summary
# ========================================

print_experiment_header "ALL EXPERIMENTS COMPLETE"

echo -e "${BLUE}Cleaning up dummy services...${NC}"
delete_dummy_services "$PROJECT_ROOT" || true
echo ""

echo -e "${GREEN}✓ Full RTT experiment suite finished!${NC}"
echo ""
echo "Results Summary:"
echo "=================="
column -t -s',' "$RESULTS_FILE"
echo ""
echo -e "${CYAN}Detailed results saved to: $RESULTS_FILE${NC}"
echo ""
echo "Next steps:"
echo "  1. Review individual log files for detailed statistics"
echo "  2. Compare with eBPF one-way measurements"
echo "  3. Generate graphs from CSV data"
echo ""
