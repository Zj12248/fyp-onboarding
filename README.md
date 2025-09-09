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
        
## Steps to run 
1) Setup single cluster node according to vhive-quickstart guide/utilise script.
2) Clone this repo into the node
3) If worker image not pushed into docker hub(or other registry), follow steps 4-6. Else skip.
4) Build image with following command: docker build -t <userid>/worker:latest -f worker/Dockerfile .
5) Docker login: docker login
6) Push image into registry: docker push <userid>/worker:latest
7) Deploy into knative: kubectl apply -f worker.yaml
8) Check worker status if ready: kubectl get ksvc worker
9) Get URL - endpoint to connect to. Default Port is 80 for external connections into Knative Function.
10) Run Load Generator. Replace URL with above: go run loadgen/load_generator.go --worker=<URL:80>
11) Load Generator runs. Output in /logs folder. Measures requests and E2E.
