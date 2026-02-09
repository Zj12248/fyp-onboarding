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
echo "[3] Kube-proxy Rules (checking first node):"
FIRST_NODE=$(kubectl get nodes -o name | head -1 | cut -d'/' -f2)
echo "  Node: $FIRST_NODE"

# Use nicolaka/netshoot which has iptables/nft, and use host networking (profile=sysadmin)
if [[ "$PROXY_MODE" == *"nftables"* ]]; then
  echo "  Attempting to check nftables rules..."
  # Note: This requires K8s 1.23+ for 'debug' with profiles
  kubectl debug node/$FIRST_NODE -it --image=nicolaka/netshoot --profile=sysadmin -- sh -c "nft list ruleset 2>/dev/null | grep -c 'dummy-service'" 2>/dev/null || echo "  (Could not run privileged debug pod. Run manually on node: sudo nft list ruleset | grep dummy-service)"
else
  echo "  Attempting to check iptables rules..."
  kubectl debug node/$FIRST_NODE -it --image=nicolaka/netshoot --profile=sysadmin -- sh -c "iptables -t nat -L KUBE-SERVICES 2>/dev/null | grep -c 'dummy-service'" 2>/dev/null || echo "  (Could not run privileged debug pod. Run manually on node: sudo iptables -t nat -L KUBE-SERVICES | grep dummy-service)"
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
