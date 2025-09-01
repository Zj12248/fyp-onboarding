package main

import (
	"context"
	"log"
	"time"

	pb "fyp-onboarding/workerpb"

	"google.golang.org/grpc"
)

func main() {
	conn, err := grpc.Dial("localhost:50051", grpc.WithInsecure())
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()

	client := pb.NewWorkerServiceClient(conn)

	rps := 10                // requests per second
	durationMs := int32(5000) // CPU spin duration in ms

	ticker := time.NewTicker(time.Second / time.Duration(rps))
	defer ticker.Stop()

	for i := 0; i < 5; i++ { // send 5 requests for test
		<-ticker.C

		start := time.Now()
		resp, err := client.DoWork(context.Background(), &pb.WorkRequest{DurationMs: durationMs})
		if err != nil {
			log.Printf("Error calling worker: %v", err)
			continue
		}

		e2e := time.Since(start).Milliseconds()
		log.Printf("Request %d: Worker status=%s, Worker E2E=%dms, Measured E2E=%dms",
			i+1, resp.Status, resp.E2ELatencyMs, e2e)
	}
}
