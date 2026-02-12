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
    # Check if nft command exists
    if ! command -v nft &> /dev/null; then
        echo "Error: nft command not found. Install with: sudo apt install nftables"
        exit 1
    fi

    # 1. Find the kube-proxy table and chain
    # Kube-proxy creates tables like "kube-proxy" with chains like "services"
    TABLE_NAME="kube-proxy"
    CHAIN_NAME="services"

    # Check if table exists
    if ! nft list table ip $TABLE_NAME &> /dev/null; then
        echo "Error: nftables table '$TABLE_NAME' not found."
        echo "Available tables:"
        nft list tables
        exit 1
    fi

    # 2. Find the rule with the service IP
    echo "Searching for rule with IP $SERVICE_IP..."
    RULE_HANDLE=$(nft -a list chain ip $TABLE_NAME $CHAIN_NAME 2>/dev/null | grep "$SERVICE_IP" | grep -oP 'handle \K\d+' | head -1)

    if [ -z "$RULE_HANDLE" ]; then
        echo "Error: Rule for $SERVICE_IP not found in nftables chain 'ip $TABLE_NAME $CHAIN_NAME'."
        echo "Trying alternative chain names..."
        
        # Try other possible chain names
        for alt_chain in "service-endpoints" "kube-services" "KUBE-SERVICES"; do
            RULE_HANDLE=$(nft -a list chain ip $TABLE_NAME $alt_chain 2>/dev/null | grep "$SERVICE_IP" | grep -oP 'handle \K\d+' | head -1)
            if [ -n "$RULE_HANDLE" ]; then
                CHAIN_NAME=$alt_chain
                echo "Found in chain: $alt_chain"
                break
            fi
        done
        
        if [ -z "$RULE_HANDLE" ]; then
            echo "Error: Could not find rule in any known chain."
            echo "Available chains in table $TABLE_NAME:"
            nft list table ip $TABLE_NAME | grep "chain"
            exit 1
        fi
    fi

    echo "Found Rule Handle: $RULE_HANDLE"

    # 3. Get the full rule definition
    FULL_RULE=$(nft -a list chain ip $TABLE_NAME $CHAIN_NAME | grep "handle $RULE_HANDLE")
    echo "Full Rule: $FULL_RULE"

    # 4. Extract the rule without the handle
    RULE_SPEC=$(echo "$FULL_RULE" | sed 's/ # handle [0-9]\+$//')
    echo "Rule Spec: $RULE_SPEC"

    # 5. Delete the rule by handle
    echo "1. Deleting rule (handle $RULE_HANDLE)..."
    nft delete rule ip $TABLE_NAME $CHAIN_NAME handle $RULE_HANDLE

    if [ $? -ne 0 ]; then
        echo "Error: Failed to delete rule."
        exit 1
    fi

    # 6. Add the rule back at the end
    echo "2. Adding rule to end of chain..."
    nft add rule ip $TABLE_NAME $CHAIN_NAME $RULE_SPEC

    if [ $? -ne 0 ]; then
        echo "Error: Failed to add rule back."
        exit 1
    fi

    # 7. Verify position
    echo "Done. Verifying position..."
    TOTAL_RULES=$(nft -a list chain ip $TABLE_NAME $CHAIN_NAME | grep "handle" | wc -l)
    NEW_RULE_HANDLE=$(nft -a list chain ip $TABLE_NAME $CHAIN_NAME | grep "$SERVICE_IP" | grep -oP 'handle \K\d+' | head -1)
    
    # Get the position by counting rules
    RULE_POSITION=$(nft -a list chain ip $TABLE_NAME $CHAIN_NAME | grep "handle" | grep -n "handle $NEW_RULE_HANDLE" | cut -d: -f1)

    echo "Total Rules: $TOTAL_RULES"
    echo "Your Rule Handle: $NEW_RULE_HANDLE (Position: $RULE_POSITION)"

    if [ "$RULE_POSITION" -eq "$TOTAL_RULES" ]; then
        echo "SUCCESS: Rule is at the bottom (position $RULE_POSITION of $TOTAL_RULES)."
    else
        echo "WARNING: Rule is at position $RULE_POSITION but total rules is $TOTAL_RULES."
    fi

else
    echo "Error: Unknown proxy mode '$PROXY_MODE'."
    echo "Supported modes: iptables, iptables-nft, nftables"
    exit 1
fi
