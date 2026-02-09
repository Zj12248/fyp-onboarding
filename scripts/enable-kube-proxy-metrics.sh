#!/bin/sh
# Enable kube-proxy metrics for Prometheus scraping
# Full credit: BenTheElder
# https://github.com/kubernetes-sigs/kind/blob/b6bc112522651d98c81823df56b7afa511459a3b/hack/ci/e2e-k8s.sh#L190-L205

# Get the current config
original_kube_proxy=$(kubectl get -oyaml -n=kube-system configmap/kube-proxy)
echo "Original kube-proxy config (metricsBindAddress):"
echo "${original_kube_proxy}" | grep metricsBindAddress

# Patch it - change metricsBindAddress to bind on all interfaces
fixed_kube_proxy=$(
    printf '%s' "${original_kube_proxy}" | sed \
        's/\(.*metricsBindAddress:\)\( .*\)/\1 "0.0.0.0:10249"/' \
)

echo ""
echo "Patched kube-proxy config (metricsBindAddress):"
echo "${fixed_kube_proxy}" | grep metricsBindAddress

# Apply the change
printf '%s' "${fixed_kube_proxy}" | kubectl apply -f -

# Restart kube-proxy pods to apply the change
echo ""
echo "Restarting kube-proxy pods..."
kubectl -n kube-system rollout restart ds kube-proxy

echo ""
echo "Waiting for rollout to complete..."
kubectl -n kube-system rollout status ds kube-proxy

echo ""
echo "✓ Kube-proxy metrics enabled!"
echo "Metrics will be available at: http://<node-ip>:10249/metrics"
echo ""
echo "To test from a node:"
echo "  curl localhost:10249/metrics | grep kubeproxy_sync"
