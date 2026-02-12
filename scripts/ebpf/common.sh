#!/bin/bash
#
# Common functions library for eBPF kube-proxy experiments
# Source this file: source scripts/ebpf/common.sh
#

# Colors
export RED='\033[0;31m'
export GREEN='\033[0;32m'
export YELLOW='\033[1;33m'
export BLUE='\033[0;34m'
export CYAN='\033[0;36m'
export NC='\033[0m' # No Color

# ========================================
# Pre-flight Checks
# ========================================

check_prerequisites() {
    local require_root=${1:-true}
    local require_bpftrace=${2:-true}
    
    if [ "$require_root" = "true" ] && [ "$EUID" -ne 0 ]; then
        echo -e "${RED}ERROR: This script must be run as root (sudo)${NC}"
        return 1
    fi
    
    if ! command -v kubectl &> /dev/null; then
        echo -e "${RED}ERROR: kubectl not found${NC}"
        return 1
    fi
    
    if [ "$require_bpftrace" = "true" ] && ! command -v bpftrace &> /dev/null; then
        echo -e "${RED}ERROR: bpftrace not found${NC}"
        echo "Install with: sudo apt install bpftrace linux-headers-\$(uname -r)"
        return 1
    fi
    
    # Check kernel version for eBPF
    if [ "$require_bpftrace" = "true" ]; then
        KERNEL_VERSION=$(uname -r | cut -d. -f1)
        if [ "$KERNEL_VERSION" -lt 4 ]; then
            echo -e "${RED}ERROR: Kernel version too old. Need 4.9+, found $(uname -r)${NC}"
            return 1
        fi
    fi
    
    return 0
}

# ========================================
# Service Management
# ========================================

get_worker_ip() {
    kubectl get svc worker -o jsonpath='{.spec.clusterIP}' 2>/dev/null
}

ensure_worker_deployed() {
    local project_root="$1"
    
    WORKER_IP=$(get_worker_ip)
    if [ -z "$WORKER_IP" ]; then
        echo -e "${YELLOW}Worker service not found. Deploying...${NC}"
        kubectl apply -f "$project_root/knative/worker-service.yaml"
        kubectl wait --for=condition=Ready pod -l app=worker --timeout=80s
        WORKER_IP=$(get_worker_ip)
        
        if [ -z "$WORKER_IP" ]; then
            echo -e "${RED}ERROR: Failed to deploy worker service${NC}"
            return 1
        fi
    fi
    
    echo "$WORKER_IP"
    return 0
}

create_dummy_services() {
    local count="$1"
    local start_index="$2"
    local project_root="$3"
    
    # Find go binary (needed when running with sudo)
    local GO_BIN=$(which go 2>/dev/null || find /usr/local/go/bin /usr/bin /home/*/go/bin -name go 2>/dev/null | head -1)
    if [ -z "$GO_BIN" ]; then
        echo -e "${RED}ERROR: go binary not found in PATH${NC}"
        echo "Hint: Run with sudo -E to preserve PATH, or ensure go is in /usr/local/go/bin"
        return 1
    fi
    
    echo -e "${BLUE}Creating $count dummy services starting from index $start_index...${NC}"
    cd "$project_root/scripts/create-dummy-services"
    "$GO_BIN" run main.go -count "$count" -start-index "$start_index"
    local result=$?
    cd - > /dev/null
    return $result
}

delete_dummy_services() {
    local project_root="$1"
    
    # Find go binary (needed when running with sudo)
    local GO_BIN=$(which go 2>/dev/null || find /usr/local/go/bin /usr/bin /home/*/go/bin -name go 2>/dev/null | head -1)
    if [ -z "$GO_BIN" ]; then
        echo -e "${RED}ERROR: go binary not found in PATH${NC}"
        echo "Hint: Run with sudo -E to preserve PATH, or ensure go is in /usr/local/go/bin"
        return 1
    fi
    
    echo -e "${BLUE}Deleting dummy services...${NC}"
    cd "$project_root/scripts/delete-dummy-services"
    "$GO_BIN" run main.go
    local result=$?
    cd - > /dev/null
    return $result
}

get_dummy_service_count() {
    kubectl get svc -l type=dummy --no-headers 2>/dev/null | wc -l
}

# ========================================
# Kube-Proxy Detection
# ========================================

get_kubeproxy_mode() {
    # 1. Get ONLY the config data (ignores metadata/timestamps)
    #    We try 'config.conf' (standard) and fallback to 'config' (older clusters)
    local config_data=$(kubectl -n kube-system get cm kube-proxy -o jsonpath='{.data.config\.conf}' 2>/dev/null)
    if [ -z "$config_data" ]; then
        config_data=$(kubectl -n kube-system get cm kube-proxy -o jsonpath='{.data.config}' 2>/dev/null)
    fi

    # 2. Parse the mode
    #    - grep -E "^[[:space:]]*mode:": Finds line starting with 'mode:' (ignoring indentation)
    #    - awk '{print $2}': Prints the value
    #    - tr -d '"': Removes quotes
    local mode=$(echo "$config_data" | grep -E "^[[:space:]]*mode:" | awk '{print $2}' | tr -d '"')

    # 3. Logic: Default to "iptables" if empty or null
    if [ -z "$mode" ]; then
        echo "iptables"
    else
        echo "$mode"
    fi
}

get_worker_position() {
    local worker_ip="$1"
    local proxy_mode="$2"
    
    if [ "$proxy_mode" != "iptables" ]; then
        echo "N/A|N/A|N/A"
        return 0
    fi
    
    local worker_pos=$(sudo iptables -t nat -L KUBE-SERVICES --line-numbers -n | grep "$worker_ip" | grep "dpt:50051" | head -1 | awk '{print $1}')
    local total_rules=$(sudo iptables -t nat -L KUBE-SERVICES --line-numbers -n | tail -n +3 | wc -l)
    
    if [ -n "$worker_pos" ] && [ -n "$total_rules" ] && [ "$total_rules" -gt 0 ]; then
        local rel_pos=$(awk "BEGIN {printf \"%.2f\", ($worker_pos / $total_rules) * 100}")
        echo "$worker_pos|$total_rules|$rel_pos"
    else
        echo "unknown|unknown|unknown"
    fi
}

# ========================================
# Metric Extraction
# ========================================

extract_metric_from_log() {
    local log_file="$1"
    local pattern="$2"
    
    # Pattern matches: "Average Latency:       123 us" or "Maximum Latency:       456 us"
    grep "$pattern" "$log_file" | grep -oP '\d+' | tail -1 || echo ""
}

extract_worker_position_from_log() {
    local log_file="$1"
    
    local worker_pos=$(grep "Worker position:" "$log_file" | grep -oP '\d+(?= /)' | head -1 || echo "N/A")
    local total_rules=$(grep "Worker position:" "$log_file" | grep -oP '/ \K\d+' | head -1 || echo "N/A")
    
    echo "$worker_pos|$total_rules"
}

extract_percentiles_from_histogram() {
    local log_file="$1"
    
    # Extract histogram data: look for lines like "[16, 32)     123 |@@@@|"
    # Histogram format from bpftrace hist():
    #   [lower, upper)   count |bars|
    
    # Parse histogram into arrays of bucket ranges and counts
    local -a buckets_lower=()
    local -a buckets_upper=()
    local -a counts=()
    local total_count=0
    
    # Read histogram lines (between "Latency Distribution" and next section)
    while IFS= read -r line; do
        # Match histogram bucket format: [lower, upper) count |bars|
        if [[ $line =~ ^\[([0-9]+),\ ([0-9]+)\)\ +([0-9]+) ]]; then
            local lower="${BASH_REMATCH[1]}"
            local upper="${BASH_REMATCH[2]}"
            local count="${BASH_REMATCH[3]}"
            
            buckets_lower+=("$lower")
            buckets_upper+=("$upper")
            counts+=("$count")
            total_count=$((total_count + count))
        fi
    done < <(sed -n '/Latency Distribution/,/^$/p' "$log_file")
    
    if [ $total_count -eq 0 ]; then
        echo "N/A|N/A|N/A"
        return
    fi
    
    # Calculate percentile thresholds
    local p50_threshold=$((total_count * 50 / 100))
    local p95_threshold=$((total_count * 95 / 100))
    local p99_threshold=$((total_count * 99 / 100))
    
    # Find percentile values
    local cumsum=0
    local p50="N/A" p95="N/A" p99="N/A"
    
    for i in "${!counts[@]}"; do
        cumsum=$((cumsum + counts[i]))
        
        if [ "$p50" = "N/A" ] && [ $cumsum -ge $p50_threshold ]; then
            p50="${buckets_lower[i]}"
        fi
        
        if [ "$p95" = "N/A" ] && [ $cumsum -ge $p95_threshold ]; then
            p95="${buckets_lower[i]}"
        fi
        
        if [ "$p99" = "N/A" ] && [ $cumsum -ge $p99_threshold ]; then
            p99="${buckets_lower[i]}"
        fi
    done
    
    echo "$p50|$p95|$p99"
}

# ========================================
# pktgen Configuration
# ========================================

load_pktgen_module() {
    if ! lsmod | grep -q pktgen; then
        echo -e "${BLUE}Loading pktgen kernel module...${NC}"
        modprobe pktgen || {
            echo -e "${RED}ERROR: Failed to load pktgen module${NC}"
            return 1
        }
    fi
    return 0
}

get_network_interface() {
    local dst_ip="$1"
    
    local iface=$(ip route get "$dst_ip" | grep -oP 'dev \K\S+' | head -1)
    if [ -z "$iface" ]; then
        echo -e "${RED}ERROR: Could not determine network interface for $dst_ip${NC}"
        return 1
    fi
    echo "$iface"
}

configure_pktgen() {
    local iface="$1"
    local dst_ip="$2"
    local packet_count="$3"
    
    local pgdev="/proc/net/pktgen/$iface"
    
    cat > "$pgdev" <<EOF
count $packet_count
clone_skb 0
pkt_size 64
delay 0
dst $dst_ip
dst_min $dst_ip
dst_max $dst_ip
udp_src_min 1024
udp_src_max 65535
udp_dst_min 50051
udp_dst_max 50051
flag UDPSRC_RND
flag UDPDST_RND
EOF
}

reset_pktgen() {
    echo "reset" > /proc/net/pktgen/pgctrl 2>/dev/null || true
    sleep 1
}

setup_pktgen_thread() {
    local iface="$1"
    
    local pgdev="/proc/net/pktgen/kpktgend_0"
    echo "rem_device_all" > "$pgdev"
    echo "add_device $iface" > "$pgdev"
}

start_pktgen() {
    echo "start" > /proc/net/pktgen/pgctrl
}

get_pktgen_packet_count() {
    local iface="$1"
    
    cat "/proc/net/pktgen/$iface" 2>/dev/null | grep "Sofar:" | awk '{print $2}'
}

# ========================================
# Timing and Progress
# ========================================

wait_for_kubeproxy_sync() {
    local seconds=${1:-120}
    
    echo -e "${BLUE}Waiting ${seconds}s for kube-proxy to sync rules...${NC}"
    sleep "$seconds"
}

# ========================================
# Kube-Proxy Control
# ========================================

stop_kubeproxy() {
    echo -e "${YELLOW}Stopping kube-proxy...${NC}"
    # Patch daemonset to add node selector that matches no nodes
    kubectl -n kube-system patch daemonset kube-proxy \
        -p '{"spec":{"template":{"spec":{"nodeSelector":{"non-existing-node":"true"}}}}}' 2>/dev/null || {
        echo -e "${RED}Failed to stop kube-proxy${NC}"
        return 1
    }
    echo "Waiting for kube-proxy pods to terminate..."
    sleep 15
    echo -e "${GREEN}✓ Kube-proxy stopped${NC}"
}

start_kubeproxy() {
    echo -e "${YELLOW}Starting kube-proxy...${NC}"
    # Remove the node selector to restore normal scheduling
    kubectl -n kube-system patch daemonset kube-proxy --type json \
        -p '[{"op":"remove","path":"/spec/template/spec/nodeSelector/non-existing-node"}]' 2>/dev/null || {
        echo -e "${RED}Failed to start kube-proxy${NC}"
        return 1
    }
    echo "Waiting for kube-proxy pods to start..."
    sleep 15
    kubectl -n kube-system wait --for=condition=Ready pod -l k8s-app=kube-proxy --timeout=60s 2>/dev/null || true
    echo -e "${GREEN}✓ Kube-proxy started${NC}"
}

move_worker_rule_to_end() {
    local worker_ip="$1"
    local script_dir="$2"
    
    if [ -z "$worker_ip" ]; then
        echo -e "${RED}Error: Worker IP not provided${NC}"
        return 1
    fi
    
    echo -e "${CYAN}Moving worker rule to end of KUBE-SERVICES chain...${NC}"
    bash "$script_dir/../move-rule-to-end.sh" "$worker_ip"
    
    # Verify position
    local total_rules=$(iptables -t nat -L KUBE-SERVICES -n --line-numbers | tail -n +3 | wc -l)
    local worker_line=$(iptables -t nat -L KUBE-SERVICES -n --line-numbers | grep "$worker_ip" | grep "dpt:50051" | awk '{print $1}')
    
    if [ "$worker_line" -eq "$total_rules" ]; then
        echo -e "${GREEN}✓ Worker rule at position $worker_line/$total_rules (worst-case)${NC}"
        return 0
    else
        echo -e "${YELLOW}⚠ Worker rule at position $worker_line/$total_rules (not at end)${NC}"
        return 1
    fi
}

move_worker_rule_to_position() {
    local worker_ip="$1"
    local script_dir="$2"
    local position="${3:-last}"
    
    if [ -z "$worker_ip" ]; then
        echo -e "${RED}Error: Worker IP not provided${NC}"
        return 1
    fi
    
    # Get proxy mode
    local proxy_mode=$(get_kubeproxy_mode)
    
    if [ "$position" = "first" ]; then
        echo -e "${CYAN}Moving worker rule to FIRST position (best-case O(1))...${NC}"
    else
        echo -e "${CYAN}Moving worker rule to LAST position (worst-case O(n))...${NC}"
    fi
    
    bash "$script_dir/../move-rule-to-end.sh" "$worker_ip" "$proxy_mode" "$position"
    
    # Verify position
    local total_rules=$(iptables -t nat -L KUBE-SERVICES -n --line-numbers | tail -n +3 | wc -l)
    local worker_line=$(iptables -t nat -L KUBE-SERVICES -n --line-numbers | grep "$worker_ip" | grep "dpt:50051" | awk '{print $1}')
    
    if [ "$position" = "first" ]; then
        if [ "$worker_line" -eq 1 ]; then
            echo -e "${GREEN}✓ Worker rule at position $worker_line/$total_rules (best-case O(1))${NC}"
            return 0
        else
            echo -e "${YELLOW}⚠ Worker rule at position $worker_line/$total_rules (not at first)${NC}"
            return 1
        fi
    else
        if [ "$worker_line" -eq "$total_rules" ]; then
            echo -e "${GREEN}✓ Worker rule at position $worker_line/$total_rules (worst-case O(n))${NC}"
            return 0
        else
            echo -e "${YELLOW}⚠ Worker rule at position $worker_line/$total_rules (not at end)${NC}"
            return 1
        fi
    fi
}

# ========================================
# Output Formatting
# ========================================

print_experiment_header() {
    local title="$1"
    
    echo "=========================================="
    echo "  $title"
    echo "=========================================="
    echo ""
}

print_config_summary() {
    local proxy_mode="$1"
    local worker_ip="$2"
    local worker_pos="$3"
    local total_rules="$4"
    local rel_pos="$5"
    local duration="$6"
    local packet_rate="$7"
    local warmup="$8"
    
    echo "Configuration:"
    echo "  - Kube-proxy mode: $proxy_mode"
    echo "  - Worker ClusterIP: $worker_ip"
    echo "  - Worker position: $worker_pos / $total_rules (${rel_pos}%)"
    echo "  - Duration: ${duration}s"
    echo "  - Warmup: ${warmup} packets"
    echo "  - Packet rate: ${packet_rate} pps"
}

# ========================================
# CSV Management
# ========================================

create_csv_header() {
    local csv_file="$1"
    
    mkdir -p "$(dirname "$csv_file")"
    echo "ServiceCount,ProxyMode,WorkerPosition,TotalRules,MeanLatency_us,MaxLatency_us,P50_us,P95_us,P99_us,LogFile" > "$csv_file"
}

append_csv_row() {
    local csv_file="$1"
    local service_count="$2"
    local proxy_mode="$3"
    local worker_pos="$4"
    local total_rules="$5"
    local mean="$6"
    local max="$7"
    local p50="$8"
    local p95="$9"
    local p99="${10}"
    local log_file="${11}"
    
    echo "$service_count,$proxy_mode,$worker_pos,$total_rules,$mean,$max,$p50,$p95,$p99,$log_file" >> "$csv_file"
}

# ========================================
# Validation
# ========================================

check_worker_position_warning() {
    local rel_pos="$1"
    
    if [ "$rel_pos" != "N/A" ] && [ "$rel_pos" != "unknown" ]; then
        if awk "BEGIN {exit !($rel_pos < 25)}"; then
            echo -e "${YELLOW}⚠ WARNING: Worker is near beginning of chain (${rel_pos}%) - may underestimate O(n) cost${NC}"
            return 1
        fi
    fi
    return 0
}
