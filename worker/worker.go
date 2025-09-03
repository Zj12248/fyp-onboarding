package main

import (
	"context"
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

	// Simulate CPU spin
	duration := time.Duration(req.DurationMs) * time.Millisecond
	end := time.Now().Add(duration)
	for time.Now().Before(end) {
		// busy-wait
	}

	e2e := time.Since(start).Milliseconds()
	return &pb.WorkResponse{
		Status:       "done",
		E2ELatencyMs: e2e,
	}, nil
}

func main() {
	// Read port from environment variable (knative sets port dynamically)
	port := os.Getenv("PORT")

	if port == "" { // for local testing
		port = "50051"
	}

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterWorkerServiceServer(s, &server{})
	log.Printf("Worker listening on :%s", port)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
