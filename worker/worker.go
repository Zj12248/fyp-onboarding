package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	pb "fyp-onboarding/workerpb"

	"google.golang.org/grpc"
)

type server struct {
	pb.UnimplementedWorkerServiceServer
}

func (s *server) DoWork(ctx context.Context, req *pb.WorkRequest) (*pb.WorkResponse, error) {
	start := time.Now()
	log.Printf("[Worker] Received request: DurationMs=%d", req.DurationMs)
	fmt.Printf("[Worker CLI] Request received: DurationMs=%d\n", req.DurationMs)

	// Simulate CPU spin
	duration := time.Duration(req.DurationMs) * time.Millisecond
	end := time.Now().Add(duration)
	for time.Now().Before(end) {
		// busy-wait
	}

	e2e := time.Since(start).Milliseconds()
	log.Printf("[Worker] Finished request: DurationMs=%d, E2ELatencyMs=%d", req.DurationMs, e2e)
	fmt.Printf("[Worker CLI] Request finished: DurationMs=%d, E2E=%d ms\n", req.DurationMs, e2e)

	return &pb.WorkResponse{
		Status:       "done",
		E2ELatencyMs: e2e,
	}, nil
}

func main() {
	// Read port from environment variable (Knative sets port dynamically)
	port := os.Getenv("PORT")
	if port == "" { // local testing
		port = "50051"
	}

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("[Worker] failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterWorkerServiceServer(s, &server{})
	log.Printf("[Worker] Listening on port :%s", port)
	fmt.Printf("[Worker CLI] Worker started on port :%s\n", port)

	if err := s.Serve(lis); err != nil {
		log.Fatalf("[Worker] failed to serve: %v", err)
	}
}
