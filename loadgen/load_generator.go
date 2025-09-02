package main

import (
	"context"
	pb "fyp-onboarding/workerpb"
	"log"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	// Open log file
	f, _ := os.Create("load.log")
	defer f.Close()
	log.SetOutput(f)

	// Connect to Worker
	conn, err := grpc.NewClient(
		"localhost:50051",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()
	client := pb.NewWorkerServiceClient(conn)

	rps := 5                 // requests per second
	durationMs := int32(500) // CPU spin duration in ms
	numRequests := 40        // total requests in batch

	ticker := time.NewTicker(time.Second / time.Duration(rps))
	defer ticker.Stop()

	var wg sync.WaitGroup
	wg.Add(numRequests) // Track all requests

	for i := 0; i < numRequests; i++ {
		<-ticker.C

		go func(idx int) {
			defer wg.Done() // Mark request done

			start := time.Now()
			resp, err := client.DoWork(
				context.Background(),
				&pb.WorkRequest{DurationMs: durationMs},
			)
			if err != nil {
				log.Printf("Error calling worker: %v", err)
				return
			}

			e2e := time.Since(start).Milliseconds()
			log.Printf(
				"Request %d: Worker status=%s, Worker E2E=%dms, Measured E2E=%dms",
				idx, resp.Status, resp.E2ELatencyMs, e2e,
			)
		}(i + 1)
	}

	wg.Wait() // Wait for all goroutines to finish
}
