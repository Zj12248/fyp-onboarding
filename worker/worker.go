package main

import (
	"context"
	"fmt"
	"log"
	"math"
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

	duration := time.Duration(req.DurationMs) * time.Millisecond
	end := time.Now().Add(duration)

	var count uint64
	val := 1.0

	for time.Now().Before(end) { // Busy spin
		for i := 0; i < 1000; i++ { // inner loop to increase CPU work
			val = val*1.0001 + 1.0
			val = val/1.0001 + 0.9999
			val = val*val - val/2 + 3.14159
			val = val / (val + 1.0) // some expensive floating-point ops

			// Prevent runaway growth
			if val > 1e6 {
				val = math.Mod(val, 99999)
			}

			count++
		}
	}

	e2e := time.Since(start).Milliseconds()
	log.Printf("[Worker] Finished request: DurationMs=%d, E2ELatencyMs=%d, Iterations=%d", req.DurationMs, e2e, count)
	fmt.Printf("[Worker CLI] Request finished: DurationMs=%d, E2E=%d ms, Iterations=%d\n", req.DurationMs, e2e, count)

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
