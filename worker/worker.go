package main

import (
	"context"
	"log"
	"net"
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
	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterWorkerServiceServer(s, &server{})
	log.Println("Worker listening on :50051")
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
