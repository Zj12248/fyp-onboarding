#!/bin/bash

# Usage: sudo ./move-rule-to-end.sh <SERVICE-IP>
# Example: sudo ./move-rule-to-end.sh 10.96.0.15

SERVICE_IP=$1

if [ -z "$SERVICE_IP" ]; then
    echo "Usage: $0 <service-cluster-ip>"
    echo "Error: No Service IP provided."
    exit 1
fi

echo "--- Moving Rule for $SERVICE_IP to End of Chain ---"

# 1. Check if the rule exists
# We grep for the IP in the NAT table's KUBE-SERVICES chain
RULE_EXISTS=$(iptables -t nat -S KUBE-SERVICES | grep "$SERVICE_IP")

if [ -z "$RULE_EXISTS" ]; then
    echo "Error: Rule for $SERVICE_IP not found in KUBE-SERVICES."
    exit 1
fi

# 2. Extract the exact matching logic (removing the '-A' add command)
# Example Output: -A KUBE-SERVICES -d 10.96.0.15/32 -p tcp -m comment --comment "default/my-service:grpc" -m tcp --dport 80 -j KUBE-SVC-XYZ
CLEAN_RULE=$(echo "$RULE_EXISTS" | sed 's/^-A KUBE-SERVICES //')

echo "Found Rule: $CLEAN_RULE"

# 3. Delete the rule from its current position
echo "1. Deleting rule..."
iptables -t nat -D KUBE-SERVICES $CLEAN_RULE

# 4. Append the rule to the end
echo "2. Appending rule to bottom..."
iptables -t nat -A KUBE-SERVICES $CLEAN_RULE

# 5. Verify
echo "Done. Verifying position..."
TOTAL_RULES=$(iptables -t nat -L KUBE-SERVICES -n --line-numbers | tail -n +3 | wc -l)
MY_RULE_LINE=$(iptables -t nat -L KUBE-SERVICES -n --line-numbers | grep "$SERVICE_IP" | awk '{print $1}')

echo "Total Rules: $TOTAL_RULES"
echo "Your Rule is at Line: $MY_RULE_LINE"

if [ "$MY_RULE_LINE" -eq "$TOTAL_RULES" ]; then
    echo "SUCCESS: Rule is at the bottom (line $MY_RULE_LINE of $TOTAL_RULES)."
else
    echo "WARNING: Rule is at line $MY_RULE_LINE but total rules is $TOTAL_RULES."
fi
