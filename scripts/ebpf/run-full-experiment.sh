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
# Initial Cleanup
# ========================================

echo -e "${BLUE}[Initial] Cleaning up all existing dummy services...${NC}"
delete_dummy_services "$PROJECT_ROOT" || true
sleep 30
echo -e "${GREEN}✓ Starting with clean state${NC}"
echo ""

# Track current service count
CURRENT_SERVICE_COUNT=0
ITERATION_INDEX=0

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
        START_INDEX=$((CURRENT_SERVICE_COUNT + 1))
        if ! create_dummy_services "$SERVICES_TO_ADD" "$START_INDEX" "$PROJECT_ROOT"; then
            echo -e "${RED}✗ Failed to create services${NC}"
            ITERATION_INDEX=$((ITERATION_INDEX + 1))
            continue
        fi
        CREATE_DURATION=$(($(date +%s) - START_TIME))
        echo -e "${GREEN}✓ Created $SERVICES_TO_ADD services in ${CREATE_DURATION}s${NC}"
        CURRENT_SERVICE_COUNT=$SERVICE_COUNT
        echo ""
        
        # Step 2: Wait for kube-proxy sync - dynamic wait time
        WAIT_TIME=$((20 + ITERATION_INDEX * 40))
        echo -e "${BLUE}[2/4] Waiting for kube-proxy to sync rules (${WAIT_TIME}s)...${NC}"
        wait_for_kubeproxy_sync $WAIT_TIME
    else
        echo -e "${YELLOW}Already at $SERVICE_COUNT services, skipping creation${NC}"
        echo ""
    fi
    
    # Verify service count
    ACTUAL_COUNT=$(get_dummy_service_count)
    echo -e "${GREEN}✓ Verified: $ACTUAL_COUNT dummy services + worker${NC}"
    echo ""
    
    # Step 3: Run eBPF experiment
    echo -e "${BLUE}[3/4] Running eBPF measurement...${NC}"
    bash "$SCRIPT_DIR/run-experiment.sh" $DURATION $PACKET_RATE $WARMUP_PACKETS
    
    # Find the most recent log file
    LATEST_LOG=$(ls -t "$PROJECT_ROOT/logs/ebpf/ebpf_${PROXY_MODE}_"*.log | head -1)
    
    echo -e "${GREEN}✓ Measurement complete: $LATEST_LOG${NC}"
    echo ""
    
    # Extract metrics from log file
    echo -e "${CYAN}Extracting metrics...${NC}"
    
    # Get worker position from log
    IFS='|' read -r WORKER_POS TOTAL_RULES <<< "$(extract_worker_position_from_log "$LATEST_LOG")"
    
    # Extract latency metrics (match bpftrace output: "Average Latency:", "Maximum Latency:")
    MEAN_LATENCY=$(extract_metric_from_log "$LATEST_LOG" "Average Latency:")
    MAX_LATENCY=$(extract_metric_from_log "$LATEST_LOG" "Maximum Latency:")
    
    # Extract percentiles from histogram
    IFS='|' read -r P50 P95 P99 <<< "$(extract_percentiles_from_histogram "$LATEST_LOG")"
    
    # Append to CSV with percentiles
    append_csv_row "$RESULTS_FILE" "$SERVICE_COUNT" "$PROXY_MODE" "$MEAN_LATENCY" "$MAX_LATENCY" "$P50" "$P95" "$P99" "$LATEST_LOG"
    
    echo -e "${GREEN}✓ Results recorded${NC}"
    echo ""
    
    echo "Quick Summary:"
    echo "  Service count: $SERVICE_COUNT"
    echo "  Worker position: $WORKER_POS / $TOTAL_RULES"
    echo "  Mean latency: ${MEAN_LATENCY:-N/A} us"
    echo "  Max latency: ${MAX_LATENCY:-N/A} us"
    echo "  P50: ${P50:-N/A} us"
    echo "  P95: ${P95:-N/A} us"
    echo "  P99: ${P99:-N/A} us"
    echo ""
    
    # Pause between tests
    if [ "$SERVICE_COUNT" != "${SERVICE_COUNTS[-1]}" ]; then
        echo -e "${YELLOW}Pausing 30 seconds before next test...${NC}"
        sleep 30
        echo ""
    fi
    
    # Increment iteration index
    ITERATION_INDEX=$((ITERATION_INDEX + 1))
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
