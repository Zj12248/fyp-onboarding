# fyp-onboarding
A project to familiarise benchmarking on vhive, knative.

## Directory Guide
ª   Dockerfile (For Dockerising Worker only)
ª   go.mod
ª   go.sum
ª   hellotest.go (Simple Connection Test Script)
ª   README.md
ª   worker.proto (protobuf)
ª   
+---knative
ª       worker-service.yaml (Knative Service Manifest)
ª       
+---loadgen
ª   ª   load.log
ª   ª   loadgen (Compiled binary of load_generator.go)
ª   ª   load_generator.go (Main Load Generator script)
ª   ª   
ª   +---logs
+---loadgen_basic
ª       load_gen_basic.go (Simple load generator - sends one request. For debug purposes)
ª       
+---worker
ª       worker.go (Main Worker script)
ª       
+---workerpb (client/server interface)
        worker.pb.go
        worker_grpc.pb.go
        
## Deployment Guide
1. Setup a single cluster node according to the [vHive Quickstart Guide](https://github.com/ease-lab/vhive) or use the provided setup script.
2. Clone this repository into the node.
3. If the worker image is **not** pushed into Docker Hub (or another registry), follow steps 4–6. *(Ensure Docker is installed: `sudo apt install docker.io`)* Otherwise, skip to step 7.
4. Build the image: `docker build -t <userid>/worker:latest -f Dockerfile .`
5. Log in to Docker: `docker login -u <username>`
6. Push the image into the registry: `docker push <userid>/worker:latest`
7. Deploy into Knative: `kubectl apply -f knative/worker-service.yaml`
8. Check if the worker is ready: `kubectl get ksvc worker`
9. Get the Knative service URL (endpoint). Default external port is **80**.
10. Run the Load Generator (replace `<URL:80>` with the worker endpoint): `go run loadgen/load_generator.go --worker=<URL:80>`
11. The Load Generator runs and saves output in the `/logs` folder. It measures **requests** and **end-to-end latency (E2E)**.

