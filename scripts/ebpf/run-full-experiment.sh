#!/bin/bash
#
# Full automated experiment: Test kube-proxy latency at multiple service counts
# Usage: sudo bash run-full-experiment.sh [duration] [packet_rate] [warmup_packets]
#
# Example: sudo bash run-full-experiment.sh 10 10000 100
#

set -e

# Source common functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/common.sh"

DURATION=${1:-10}
PACKET_RATE=${2:-10000}
WARMUP_PACKETS=${3:-100}
SERVICE_COUNTS=(100 1000 5000 10000 20000)
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

print_experiment_header "Full eBPF Kube-Proxy Experiment Suite"

echo "Configuration:"
echo "  Service counts: ${SERVICE_COUNTS[@]}"
echo "  Duration per test: ${DURATION}s"
echo "  Packet rate: ${PACKET_RATE} pps"
echo "  Warmup: ${WARMUP_PACKETS} packets"
echo ""
echo "Estimated total time: ~$((${#SERVICE_COUNTS[@]} * (DURATION + 200))) seconds"
echo ""

# ========================================
# Pre-flight Checks
# ========================================

echo -e "${BLUE}[Pre-flight] Checking prerequisites...${NC}"

if ! check_prerequisites true true; then
    exit 1
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
RESULTS_FILE="$PROJECT_ROOT/logs/ebpf/experiment_summary_$(date +%Y%m%d_%H%M%S).csv"
create_csv_header "$RESULTS_FILE"

echo -e "${GREEN}Results will be saved to: $RESULTS_FILE${NC}"
echo ""

# ========================================
# Main Experiment Loop
# ========================================

for SERVICE_COUNT in "${SERVICE_COUNTS[@]}"; do
    print_experiment_header "Testing with $SERVICE_COUNT services"
    
    # Step 1: Delete existing dummy services
    echo -e "${BLUE}[1/5] Cleaning up existing dummy services...${NC}"
    delete_dummy_services "$PROJECT_ROOT" || true
    sleep 30
    echo -e "${GREEN}✓ Cleanup complete${NC}"
    echo ""
    
    # Step 2: Create dummy services
    echo -e "${BLUE}[2/5] Creating $SERVICE_COUNT dummy services...${NC}"
    START_TIME=$(date +%s)
    if ! create_dummy_services "$SERVICE_COUNT" "$PROJECT_ROOT"; then
        echo -e "${RED}✗ Failed to create services${NC}"
        continue
    fi
    CREATE_DURATION=$(($(date +%s) - START_TIME))
    echo -e "${GREEN}✓ Created $SERVICE_COUNT services in ${CREATE_DURATION}s${NC}"
    echo ""
    
    # Step 3: Wait for kube-proxy sync
    echo -e "${BLUE}[3/5] Waiting for kube-proxy to sync rules (120s)...${NC}"
    wait_for_kubeproxy_sync 120
    
    # Verify service count
    ACTUAL_COUNT=$(get_dummy_service_count)
    echo -e "${GREEN}✓ Verified: $ACTUAL_COUNT dummy services + worker${NC}"
    echo ""
    
    # Step 4: Verify setup
    echo -e "${BLUE}[4/5] Verifying setup...${NC}"
    bash "$PROJECT_ROOT/scripts/verify-setup.sh" || true
    echo ""
    
    # Step 5: Run eBPF experiment
    echo -e "${BLUE}[5/5] Running eBPF measurement...${NC}"
    bash "$SCRIPT_DIR/run-experiment.sh" $DURATION $PACKET_RATE $WARMUP_PACKETS
    
    # Find the most recent log file
    LATEST_LOG=$(ls -t "$PROJECT_ROOT/logs/ebpf/ebpf_${PROXY_MODE}_"*.log | head -1)
    
    echo -e "${GREEN}✓ Measurement complete: $LATEST_LOG${NC}"
    echo ""
    
    # Extract metrics from log file
    echo -e "${CYAN}Extracting metrics...${NC}"
    
    # Get worker position from log
    IFS='|' read -r WORKER_POS TOTAL_RULES REL_POS <<< "$(extract_worker_position_from_log "$LATEST_LOG")"
    
    # Extract latency metrics
    MEAN_LATENCY=$(extract_metric_from_log "$LATEST_LOG" "Mean latency:")
    MIN_LATENCY=$(extract_metric_from_log "$LATEST_LOG" "Min latency:")
    MAX_LATENCY=$(extract_metric_from_log "$LATEST_LOG" "Max latency:")
    STDDEV=$(extract_metric_from_log "$LATEST_LOG" "Std deviation:")
    
    # Append to CSV
    append_csv_row "$RESULTS_FILE" "$SERVICE_COUNT" "$PROXY_MODE" "$WORKER_POS" "$TOTAL_RULES" "$REL_POS" \
                   "$MEAN_LATENCY" "$STDDEV" "$MIN_LATENCY" "$MAX_LATENCY" "TBD" "TBD" "TBD" "$LATEST_LOG"
    
    echo -e "${GREEN}✓ Results recorded${NC}"
    echo ""
    
    echo "Quick Summary:"
    echo "  Service count: $SERVICE_COUNT"
    echo "  Worker position: $WORKER_POS / $TOTAL_RULES ($REL_POS%)"
    echo "  Mean latency: $MEAN_LATENCY us"
    echo "  Std deviation: $STDDEV us"
    echo "  Range: $MIN_LATENCY - $MAX_LATENCY us"
    echo ""
    
    # Check worker position
    check_worker_position_warning "$REL_POS"
    
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

echo -e "${GREEN}✓ Full experiment suite finished!${NC}"
echo ""
echo "Results Summary:"
echo "=================="
column -t -s',' "$RESULTS_FILE"
echo ""
echo -e "${CYAN}Detailed results saved to: $RESULTS_FILE${NC}"
echo ""
echo "Next steps:"
echo "  1. Review individual log files for detailed distributions"
echo "  2. Extract P50/P95/P99 from histograms if needed"
echo "  3. Generate graphs from CSV data"
echo ""
