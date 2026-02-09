#!/bin/bash
# Verify cluster setup for kube-proxy data plane latency testing

echo "=============================================="
echo "  CLUSTER SETUP VERIFICATION"
echo "=============================================="
echo ""

# 1. Service counts
echo "[1] Service Counts:"
TOTAL_SERVICES=$(kubectl get svc --all-namespaces --no-headers | wc -l)
# Assuming you actually label services with 'type=dummy'
DUMMY_SERVICES=$(kubectl get svc -l type=dummy --no-headers 2>/dev/null | wc -l)
WORKER_SERVICE=$(kubectl get svc worker -n default --no-headers 2>/dev/null | wc -l)

echo "  Total services in cluster: $TOTAL_SERVICES"
echo "  Dummy services (type=dummy): $DUMMY_SERVICES"
if [ "$WORKER_SERVICE" -ge 1 ]; then
  echo "  Worker service: ✓ DEPLOYED"
else
  echo "  Worker service: ✗ NOT DEPLOYED"
fi
echo ""

# 2. Kube-proxy configuration
echo "[2] Kube-proxy Configuration:"
# Check ConfigMap, if fails, check DaemonSet args
PROXY_MODE=$(kubectl -n kube-system get cm kube-proxy -o yaml 2>/dev/null | grep -A1 "mode:" | tail -1 | awk '{print $2}' | tr -d '"')

if [ -z "$PROXY_MODE" ]; then
    # Fallback: Try to find it in the DaemonSet arguments
    PROXY_MODE=$(kubectl -n kube-system get ds kube-proxy -o jsonpath='{.spec.template.spec.containers[0].command}' 2>/dev/null | grep -o 'proxy-mode=[a-zA-Z0-9]*' | cut -d= -f2)
fi

if [ -z "$PROXY_MODE" ]; then
  PROXY_MODE="Unknown (Likely iptables)"
fi
echo "  Mode: $PROXY_MODE"

PROXY_PODS=$(kubectl -n kube-system get pods -l k8s-app=kube-proxy --no-headers 2>/dev/null | wc -l)
PROXY_READY=$(kubectl -n kube-system get pods -l k8s-app=kube-proxy --no-headers 2>/dev/null | grep -c "Running")
echo "  Kube-proxy pods: $PROXY_READY/$PROXY_PODS ready"
echo ""

# 3. Rule counts
echo "[3] Kube-proxy Rules (on current node):"
echo "  Node: $(hostname)"

# Check rule count based on mode
if [[ "$PROXY_MODE" == *"nftables"* ]]; then
  echo -n "  Total nftables rules: "
  sudo nft list ruleset 2>/dev/null | grep -c 'rule' || echo '0'
  
  echo -n "  Service map entries (O(1) hash lookup): "
  sudo nft list map inet kube-proxy services 2>/dev/null | grep -c 'elements' || echo '0'
  
  echo -n "  Dummy service entries: "
  sudo nft list ruleset 2>/dev/null | grep -c 'dummy-service' || echo '0'
else
  echo -n "  Total iptables rules: "
  sudo iptables-save 2>/dev/null | wc -l || echo '0'
  
  echo -n "  KUBE-SERVICES chain rules (O(n) scan): "
  sudo iptables -t nat -L KUBE-SERVICES --line-numbers -n 2>/dev/null | tail -n +3 | wc -l || echo '0'
  
  echo -n "  Dummy service rules in chain: "
  sudo iptables -t nat -L KUBE-SERVICES -n 2>/dev/null | grep -c 'dummy-service' || echo '0'
fi
echo ""

# 4. Kube-proxy metrics
echo "[4] Kube-proxy Metrics:"
# Removed 'curl' inside pod. Using direct log check or skipping if curl unavailable.
# It is very hard to reliably curl kube-proxy metrics from outside without auth/proxy.
# We will check if the pod logs imply it's working.
PROXY_POD=$(kubectl -n kube-system get pods -l k8s-app=kube-proxy -o name 2>/dev/null | head -1)

if [ -n "$PROXY_POD" ]; then
  echo "  Target Pod: $PROXY_POD"
  echo "  Note: Skipping direct metric curl (curl usually missing in kube-proxy images)."
  echo "  Checking for errors in logs:"
  kubectl -n kube-system logs $PROXY_POD --tail=20 | grep -i "error" || echo "  No recent errors found in logs."
else
  echo "  No kube-proxy pod found"
fi
echo ""

# 5. Worker pod status
echo "[5] Worker Deployment Status:"
# FIX: Added 'head -1' to ensure single pod selection
WORKER_POD=$(kubectl get pods -l app=worker -n default --no-headers 2>/dev/null | head -1)

if [ -n "$WORKER_POD" ]; then
  POD_STATUS=$(echo "$WORKER_POD" | awk '{print $3}')
  POD_NAME=$(echo "$WORKER_POD" | awk '{print $1}')
  echo "  Pod: $POD_NAME"
  echo "  Status: $POD_STATUS"
  
  if [ "$POD_STATUS" = "Running" ]; then
    WORKER_IP=$(kubectl get pod "$POD_NAME" -n default -o jsonpath='{.status.podIP}' 2>/dev/null)
    WORKER_CLUSTER_IP=$(kubectl get svc worker -n default -o jsonpath='{.spec.clusterIP}' 2>/dev/null)
    echo "  Pod IP: $WORKER_IP"
    echo "  Service ClusterIP: $WORKER_CLUSTER_IP"
    echo "  Use in load generator: $WORKER_CLUSTER_IP:50051"
  fi
else
  echo "  Worker not deployed"
fi
echo ""

# 6. Recommendations
echo "[6] Recommendations:"
if [ "$DUMMY_SERVICES" -eq 0 ]; then
  echo "  ⚠ No dummy services found. Create some with: bash scripts/create-dummy-services.sh 100"
fi

if [ "$WORKER_SERVICE" -eq 0 ]; then
  echo "  ⚠ Worker service not deployed. Deploy with: kubectl apply -f knative/worker-service.yaml"
fi

if [ "$PROXY_READY" -ne "$PROXY_PODS" ]; then
  echo "  ⚠ Some kube-proxy pods not ready. Check: kubectl -n kube-system get pods -l k8s-app=kube-proxy"
fi

if [ "$DUMMY_SERVICES" -gt 0 ] && [ "$WORKER_SERVICE" -ge 1 ] && [ "$PROXY_READY" -eq "$PROXY_PODS" ]; then
  echo "  ✓ Setup looks good! Ready to run experiments."
  if [ -n "$WORKER_CLUSTER_IP" ]; then
    echo "  Run test: go run loadgen-dataplane/load_generator.go --worker=$WORKER_CLUSTER_IP:50051 --service-count=$((DUMMY_SERVICES + 1)) --proxy-mode=$PROXY_MODE --rps=50 --num-requests=2000"
  fi
fi

echo ""
echo "=============================================="
echo ""
