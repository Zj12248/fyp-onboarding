#!/bin/bash
# Create dummy services to increase kube-proxy rule count
# Usage: ./create-dummy-services.sh <number_of_services>

NUM_SERVICES=${1:-5000}

echo "Creating $NUM_SERVICES dummy services..."

for i in $(seq 1 $NUM_SERVICES); do
  kubectl create service clusterip dummy-service-$i \
    --tcp=80:80 \
    --dry-run=client -o yaml | \
  kubectl label -f - type=dummy --dry-run=client -o yaml | \
  kubectl apply -f -
  
  if [ $((i % 10)) -eq 0 ]; then
    echo "Created $i services..."
  fi
done

echo ""
echo "Created $NUM_SERVICES dummy services"
echo "Verifying:"
kubectl get svc -l type=dummy --no-headers | wc -l

echo ""
echo "Waiting for kube-proxy to update rules..."
sleep 5

echo ""
echo "=============================================="
echo "  DUMMY SERVICES CREATED!"
echo "=============================================="
echo "Created $NUM_SERVICES dummy services for kube-proxy testing."
echo ""
echo "Useful commands:"
echo "  - View all services: kubectl get svc --all-namespaces"
echo "  - View dummy services: kubectl get svc -l type=dummy"
echo "  - Check kube-proxy mode: kubectl -n kube-system get cm kube-proxy -o yaml | grep mode:"
echo "  - Check kube-proxy logs: kubectl -n kube-system logs -l k8s-app=kube-proxy --tail=20"
echo "  - View iptables rules: sudo iptables -t nat -L KUBE-SERVICES | grep dummy"
echo "  - View nftables rules: sudo nft list ruleset | grep dummy"
echo ""
echo "Next steps:"
echo "  1. Deploy worker: kubectl apply -f knative/worker-service.yaml"
echo "  2. Run test: go run loadgen-dataplane/load_generator.go --service-count=$((NUM_SERVICES + 1))"
echo "=============================================="
echo ""
