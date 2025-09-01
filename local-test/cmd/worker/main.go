package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	pb "cloudlab-single-node/proto"
	"cloudlab-single-node/internal/spin"
)

type server struct {
	pb.UnimplementedWorkerServer
	instance string
}

func (s *server) Invoke(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	recv := time.Now().UnixNano()
	// CPU spin for the requested duration (ms)
	spin.SpinFor(time.Duration(req.GetWorkMs()) * time.Millisecond)
	send := time.Now().UnixNano()

	return &pb.InvokeReply{
		Id:                  req.GetId(),
		Ack:                 "ok",
		ServerRecvUnixNano:  recv,
		ServerSendUnixNano:  send,
		WorkerInstance:      s.instance,
	}, nil
}

func main() {
	port := getenv("PORT", "50051")
	instance := getenv("WORKER_INSTANCE", hostnameOr("worker"))

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterWorkerServer(grpcServer, &server{instance: instance})

	log.Printf("worker starting on :%s instance=%s", port, instance)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func hostnameOr(def string) string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return def
	}
	return h
}
