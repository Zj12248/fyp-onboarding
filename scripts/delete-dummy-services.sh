#!/bin/bash
# Delete all dummy services
# Usage: ./delete-dummy-services.sh

echo "Deleting all dummy services..."
kubectl delete svc -l type=dummy

echo "Remaining dummy services:"
kubectl get svc -l type=dummy
