PROTO=proto/loadtest.proto

.PHONY: proto build worker loadgen deploy logs clean

proto:
	protoc --go_out=. --go-grpc_out=. $(PROTO)

build: proto
	docker build -t worker:local -f Dockerfile.worker .
	docker build -t loadgen:local -f Dockerfile.loadgen .

deploy:
	kubectl apply -f k8s/namespace.yaml
	kubectl -n vhive-lab apply -f k8s/configmap.yaml
	kubectl -n vhive-lab apply -f k8s/worker-deployment.yaml
	kubectl -n vhive-lab apply -f k8s/worker-service.yaml
	kubectl -n vhive-lab delete job loadgen --ignore-not-found
	kubectl -n vhive-lab apply -f k8s/loadgen-job.yaml

logs:
	kubectl -n vhive-lab logs job/loadgen -f

clean:
	kubectl delete ns vhive-lab --ignore-not-found
