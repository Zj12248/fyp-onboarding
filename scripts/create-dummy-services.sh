#!/bin/bash
# Create dummy services to increase kube-proxy rule count
# Usage: ./create-dummy-services.sh <number_of_services>

# Default to 10,000 if not set
NUM_SERVICES=${1:-10000}
BATCH_SIZE=1000

echo "Preparing to generate $NUM_SERVICES dummy services..."

# Create a temporary file
BATCH_FILE="/tmp/dummy-batch.yaml"

start_time=$(date +%s)

for i in $(seq 1 $NUM_SERVICES); do
  # Cycle through 192.0.2.1 to 192.0.2.254 (TEST-NET-1)
  FAKE_IP="192.0.2.$((($i % 254) + 1))"
  
  # 1. Define Service (NOTE: NO SELECTOR allows manual Endpoints)
  cat >> $BATCH_FILE <<EOF
---
apiVersion: v1
kind: Service
metadata:
  name: dummy-service-$i
  labels:
    type: dummy
spec:
  type: ClusterIP
  ports:
  - port: 80
    targetPort: 80
    protocol: TCP
---
apiVersion: v1
kind: Endpoints
metadata:
  name: dummy-service-$i
  labels:
    type: dummy
subsets:
- addresses:
  - ip: $FAKE_IP
  ports:
  - port: 80
    protocol: TCP
EOF

  # 2. Apply in batches to prevent kubectl/API server crashes
  if [ $((i % BATCH_SIZE)) -eq 0 ]; then
    echo "Applying batch: Services $i of $NUM_SERVICES..."
    # Use 'create' instead of 'apply' for speed on new objects; use --save-config=false to save space
    kubectl create -f $BATCH_FILE --save-config=false 2>/dev/null || kubectl apply -f $BATCH_FILE
    
    # Clear the file for the next batch
    > $BATCH_FILE
  fi
done

# Apply any remaining objects
if [ -s $BATCH_FILE ]; then
    echo "Applying final batch..."
    kubectl create -f $BATCH_FILE --save-config=false 2>/dev/null || kubectl apply -f $BATCH_FILE
fi

rm $BATCH_FILE

end_time=$(date +%s)
duration=$((end_time - start_time))

echo ""
echo "=============================================="
echo " Creation Complete in ${duration}s"
echo "=============================================="
echo "Verifying API Server count:"
ACTUAL_COUNT=$(kubectl get svc -l type=dummy --no-headers | wc -l)
echo "Found $ACTUAL_COUNT / $NUM_SERVICES services."

echo ""
echo "NOTE: It may take several minutes for kube-proxy to program these rules."
echo "If using 'iptables' mode, creating 50k services may severely degrade node performance."

echo ""
echo "Diagnostic commands:"
echo "  1. Check kube-proxy logs for sync lag:"
echo "     kubectl -n kube-system logs -l k8s-app=kube-proxy --tail=20"
echo "  2. Count iptables rules (Run on Node):"
echo "     sudo iptables-save | grep 'dummy-service' | wc -l"
echo "  3. Verify endpoints:"
echo "     kubectl get endpoints dummy-service-1"
echo ""
