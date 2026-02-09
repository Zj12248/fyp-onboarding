#!/bin/bash
# Verify cluster setup for kube-proxy data plane latency testing
# Usage: ./verify-setup.sh

echo "=============================================="
echo "  CLUSTER SETUP VERIFICATION"
echo "=============================================="
echo ""

# 1. Service counts
echo "[1] Service Counts:"
TOTAL_SERVICES=$(kubectl get svc --all-namespaces --no-headers | wc -l)
DUMMY_SERVICES=$(kubectl get svc -l type=dummy --no-headers 2>/dev/null | wc -l)
WORKER_SERVICE=$(kubectl get svc worker -n default --no-headers 2>/dev/null | wc -l)

echo "  Total services in cluster: $TOTAL_SERVICES"
echo "  Dummy services (type=dummy): $DUMMY_SERVICES"
if [ "$WORKER_SERVICE" -eq 1 ]; then
  echo "  Worker service: ✓ DEPLOYED"
else
  echo "  Worker service: ✗ NOT DEPLOYED"
fi
echo ""

# 2. Kube-proxy configuration
echo "[2] Kube-proxy Configuration:"
PROXY_MODE=$(kubectl -n kube-system get cm kube-proxy -o yaml 2>/dev/null | grep -A1 "mode:" | tail -1 | awk '{print $2}' | tr -d '"')
if [ -z "$PROXY_MODE" ] || [ "$PROXY_MODE" = "" ]; then
  PROXY_MODE="iptables (default)"
fi
echo "  Mode: $PROXY_MODE"

# Check if kube-proxy pods are running
PROXY_PODS=$(kubectl -n kube-system get pods -l k8s-app=kube-proxy --no-headers 2>/dev/null | wc -l)
PROXY_READY=$(kubectl -n kube-system get pods -l k8s-app=kube-proxy --no-headers 2>/dev/null | grep -c "Running")
echo "  Kube-proxy pods: $PROXY_READY/$PROXY_PODS ready"
echo ""

# 3. Rule counts (check one node)
echo "[3] Kube-proxy Rules (checking first node):"
FIRST_NODE=$(kubectl get nodes -o name | head -1 | cut -d'/' -f2)
echo "  Node: $FIRST_NODE"

if [[ "$PROXY_MODE" == *"nftables"* ]]; then
  echo "  Attempting to check nftables rules..."
  kubectl debug node/$FIRST_NODE -it --image=busybox -- sh -c "nft list ruleset 2>/dev/null | grep -c 'dummy-service'" 2>/dev/null || echo "  (Cannot access node directly - run on node: sudo nft list ruleset | grep dummy-service | wc -l)"
else
  echo "  Attempting to check iptables rules..."
  kubectl debug node/$FIRST_NODE -it --image=busybox -- sh -c "iptables -t nat -L KUBE-SERVICES 2>/dev/null | grep -c 'dummy-service'" 2>/dev/null || echo "  (Cannot access node directly - run on node: sudo iptables -t nat -L KUBE-SERVICES | grep dummy-service | wc -l)"
fi
echo ""

# 4. Kube-proxy metrics (if accessible)
echo "[4] Kube-proxy Metrics:"
PROXY_POD=$(kubectl -n kube-system get pods -l k8s-app=kube-proxy -o name 2>/dev/null | head -1)
if [ -n "$PROXY_POD" ]; then
  echo "  Fetching sync metrics from $PROXY_POD..."
  
  # Try to get sync duration metrics
  SYNC_DURATION=$(kubectl -n kube-system exec $PROXY_POD -- curl -s localhost:10249/metrics 2>/dev/null | grep "kubeproxy_sync_proxy_rules_duration_seconds_sum" | awk '{print $2}')
  SYNC_COUNT=$(kubectl -n kube-system exec $PROXY_POD -- curl -s localhost:10249/metrics 2>/dev/null | grep "kubeproxy_sync_proxy_rules_duration_seconds_count" | awk '{print $2}')
  
  if [ -n "$SYNC_DURATION" ] && [ -n "$SYNC_COUNT" ]; then
    AVG_SYNC=$(echo "scale=4; $SYNC_DURATION / $SYNC_COUNT" | bc 2>/dev/null)
    echo "  Total sync duration: ${SYNC_DURATION}s"
    echo "  Total sync count: $SYNC_COUNT"
    echo "  Average sync time: ${AVG_SYNC}s"
  else
    echo "  (Metrics endpoint not accessible)"
  fi
  
  # Service/endpoint changes
  SVC_CHANGES=$(kubectl -n kube-system exec $PROXY_POD -- curl -s localhost:10249/metrics 2>/dev/null | grep "kubeproxy_sync_proxy_rules_service_changes_total" | awk '{print $2}')
  if [ -n "$SVC_CHANGES" ]; then
    echo "  Service changes processed: $SVC_CHANGES"
  fi
else
  echo "  No kube-proxy pod found"
fi
echo ""

# 5. Worker pod status
echo "[5] Worker Deployment Status:"
WORKER_POD=$(kubectl get pods -l app=worker -n default --no-headers 2>/dev/null)
if [ -n "$WORKER_POD" ]; then
  POD_STATUS=$(echo "$WORKER_POD" | awk '{print $3}')
  POD_NAME=$(echo "$WORKER_POD" | awk '{print $1}')
  echo "  Pod: $POD_NAME"
  echo "  Status: $POD_STATUS"
  
  if [ "$POD_STATUS" = "Running" ]; then
    WORKER_IP=$(kubectl get pod -l app=worker -n default -o jsonpath='{.items[0].status.podIP}' 2>/dev/null)
    echo "  Pod IP: $WORKER_IP"
    echo "  Service DNS: worker.default.svc.cluster.local:50051"
  fi
else
  echo "  Worker not deployed"
fi
echo ""

# 6. Recommendations
echo "[6] Recommendations:"
if [ "$DUMMY_SERVICES" -eq 0 ]; then
  echo "  ⚠ No dummy services found. Create some with: ./scripts/create-dummy-services.sh 100"
fi

if [ "$WORKER_SERVICE" -eq 0 ]; then
  echo "  ⚠ Worker service not deployed. Deploy with: kubectl apply -f knative/worker-service.yaml"
fi

if [ "$PROXY_READY" -ne "$PROXY_PODS" ]; then
  echo "  ⚠ Some kube-proxy pods not ready. Check: kubectl -n kube-system get pods -l k8s-app=kube-proxy"
fi

if [ "$DUMMY_SERVICES" -gt 0 ] && [ "$WORKER_SERVICE" -eq 1 ] && [ "$PROXY_READY" -eq "$PROXY_PODS" ]; then
  echo "  ✓ Setup looks good! Ready to run experiments."
  echo "  Run test: go run loadgen-dataplane/load_generator.go --service-count=$((DUMMY_SERVICES + 1))"
fi

echo ""
echo "=============================================="
echo ""
