package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	pb "fyp-onboarding/workerpb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	// Command-line flag for worker host:port
	workerAddr := flag.String("worker", "localhost:50051", "Worker gRPC host:port")
	flag.Parse()

	fmt.Printf("Loadgen Test Script running\n")
	fmt.Printf("Connecting to worker at %s...\n", *workerAddr)

	// Open a simple log file
	f, _ := os.Create("load_test.log")
	defer f.Close()
	log.SetOutput(f)

	// Connect to Worker
	conn, err := grpc.Dial(
		*workerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	fmt.Println("Connection successful")

	client := pb.NewWorkerServiceClient(conn)

	// Send one test request
	fmt.Println("Sending test request...")
	start := time.Now()
	resp, err := client.DoWork(
		context.Background(),
		&pb.WorkRequest{DurationMs: 500}, // ask worker to busy-wait 500ms
	)
	if err != nil {
		log.Fatalf("Request failed: %v", err)
	}

	e2e := time.Since(start).Milliseconds()
	fmt.Printf("Response: Status=%s, WorkerE2E=%dms, ClientE2E=%dms\n",
		resp.Status, resp.E2ELatencyMs, e2e)
}
