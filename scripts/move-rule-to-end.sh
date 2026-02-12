#!/bin/bash

# Usage: sudo ./move-rule-to-end.sh <SERVICE-IP> [PROXY-MODE]
# Example: sudo ./move-rule-to-end.sh 10.96.0.15 iptables-nft
# Example: sudo ./move-rule-to-end.sh 10.96.0.15 nftables

SERVICE_IP=$1
PROXY_MODE=${2:-iptables-nft}  # Default to iptables-nft if not specified

if [ -z "$SERVICE_IP" ]; then
    echo "Usage: $0 <service-cluster-ip> [proxy-mode]"
    echo "  proxy-mode: iptables, iptables-nft, or nftables (default: iptables-nft)"
    echo "Error: No Service IP provided."
    exit 1
fi

echo "--- Moving Rule for $SERVICE_IP to End of Chain (Mode: $PROXY_MODE) ---"

# ============================================================
# IPTABLES MODE
# ============================================================
if [ "$PROXY_MODE" = "iptables" ] || [ "$PROXY_MODE" = "iptables-nft" ]; then
    # 1. Check if the rule exists
    RULE_EXISTS=$(iptables -t nat -S KUBE-SERVICES | grep "$SERVICE_IP")

    if [ -z "$RULE_EXISTS" ]; then
        echo "Error: Rule for $SERVICE_IP not found in iptables KUBE-SERVICES."
        exit 1
    fi

    # 2. Extract the exact matching logic (removing the '-A' add command)
    CLEAN_RULE=$(echo "$RULE_EXISTS" | sed 's/^-A KUBE-SERVICES //')
    echo "Found Rule: $CLEAN_RULE"

    # 3. Delete the rule from its current position
    echo "1. Deleting rule..."
    eval "iptables -t nat -D KUBE-SERVICES $CLEAN_RULE"

    # 4. Append the rule to the end
    echo "2. Appending rule to bottom..."
    eval "iptables -t nat -A KUBE-SERVICES $CLEAN_RULE"

    # 5. Verify
    echo "Done. Verifying position..."
    TOTAL_RULES=$(iptables -t nat -L KUBE-SERVICES -n --line-numbers | tail -n +3 | wc -l)
    MY_RULE_LINE=$(iptables -t nat -L KUBE-SERVICES -n --line-numbers | grep "$SERVICE_IP" | head -1 | awk '{print $1}')

    echo "Total Rules: $TOTAL_RULES"
    echo "Your Rule is at Line: $MY_RULE_LINE"

    if [ "$MY_RULE_LINE" -eq "$TOTAL_RULES" ]; then
        echo "SUCCESS: Rule is at the bottom (line $MY_RULE_LINE of $TOTAL_RULES)."
    else
        echo "WARNING: Rule is at line $MY_RULE_LINE but total rules is $TOTAL_RULES."
    fi

# ============================================================
# NFTABLES MODE
# ============================================================
elif [ "$PROXY_MODE" = "nftables" ]; then
    echo "NOTE: nftables uses O(1) hash lookups - position is irrelevant."
    
    # Check if nft command exists
    if ! command -v nft &> /dev/null; then
        echo "Error: nft command not found. Install with: sudo apt install nftables"
        exit 1
    fi

    TABLE_NAME="kube-proxy"
    
    # Check if table exists
    if ! nft list table ip $TABLE_NAME &> /dev/null; then
        echo "Error: nftables table '$TABLE_NAME' not found."
        exit 1
    fi

    # Verify service exists
    SERVICE_FOUND=$(nft list table ip $TABLE_NAME | grep -c "$SERVICE_IP")
    
    if [ "$SERVICE_FOUND" -gt 0 ]; then
        echo "✓ Service $SERVICE_IP found in nftables"
        echo "SUCCESS: No positioning needed (hash table = O(1) lookup)."
        exit 0
    else
        echo "WARNING: Service $SERVICE_IP not found in nftables."
        exit 0
    fi

else
    echo "Error: Unknown proxy mode '$PROXY_MODE'."
    echo "Supported modes: iptables, iptables-nft, nftables"
    exit 1
fi
