#!/bin/bash
# Create dummy services to increase kube-proxy rule count
# Usage: ./create-dummy-services.sh <number_of_services>

NUM_SERVICES=${1:-100}

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
