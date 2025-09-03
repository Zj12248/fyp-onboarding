package main

import (
	"context"
	"flag"
	"fmt"
	pb "fyp-onboarding/workerpb"
	"log"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func RunExperiment(client pb.WorkerServiceClient, rps int, durationMs int32, distribution string) {

	// Generate unique log file per experiment (grid search)
	runID := fmt.Sprintf("RPS%d_Dur%d_%s_%s",
		rps, durationMs, distribution, time.Now().Format("150405"))
	logFile := fmt.Sprintf("logs/%s.log", runID)

	// Ensure logs directory exists
	os.MkdirAll("logs", os.ModePerm)

	f, err := os.Create(logFile)
	if err != nil {
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer f.Close()

	// new logger just for this run
	logger := log.New(f, "", log.LstdFlags)
	logger.Printf("Starting experiment: RPS=%d, Duration=%dms, Dist=%s", rps, durationMs, distribution)

	// 12 minutes per run
	endTime := time.Now().Add(12 * time.Minute)

	// create sync group to allow sending of request while worker is still busy with previous request. (unblock)
	var wg sync.WaitGroup

	var ticker *time.Ticker

	// uniform distribution of requests
	if distribution == "uniform" {
		ticker = time.NewTicker(time.Second / time.Duration(rps))
		defer ticker.Stop()
	}

	reqCount := 0
	for time.Now().Before(endTime) {
		if distribution == "uniform" {
			<-ticker.C
		} else { // poisson distribution
			meanInterval := float64(time.Second) / float64(rps)
			delay := time.Duration(rand.ExpFloat64() * meanInterval)
			time.Sleep(delay)
		}

		wg.Add(1)
		reqCount++

		go func(idx int) {
			defer wg.Done()

			start := time.Now()
			resp, err := client.DoWork(
				context.Background(),
				&pb.WorkRequest{DurationMs: durationMs},
			)
			if err != nil {
				logger.Printf("Error (req %d): %v", idx, err)
				return
			}

			e2e := time.Since(start).Milliseconds()
			logger.Printf("Req %d: Status=%s, WorkerE2E=%dms, ClientE2E=%dms",
				idx, resp.Status, resp.E2ELatencyMs, e2e)
		}(reqCount)
	}

	wg.Wait()
	logger.Printf("Finished experiment: RPS=%d, Duration=%dms, Dist=%s, TotalReq=%d",
		rps, durationMs, distribution, reqCount)
}

func main() {
	// Command-line flag for worker host:port
	// configure port through CLI ( go run ./loadgen/load_generator.go --worker=localhost:8080 )
	fmt.Printf("Loadgen Script running\n")
	workerAddr := flag.String("worker", "localhost:50051", "Worker gRPC host:port")
	flag.Parse()

	// Open log file
	f, _ := os.Create("load.log")
	defer f.Close()
	log.SetOutput(f)

	// Print connection attempt
	fmt.Printf("Connecting to worker at %s...\n", *workerAddr)

	// Connect to Worker
	conn, err := grpc.Dial(
		*workerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		fmt.Printf("Failed to connect: %v\n", err)
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()

	fmt.Printf("Connection Successful\n")
	client := pb.NewWorkerServiceClient(conn)

	// Sweep parameters
	rpsValues := []int{5, 10, 15, 20, 25, 30, 35, 40, 45, 50}
	distributions := []string{"uniform", "poisson"}
	durations := []int32{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000}

	// Full grid search
	for _, rps := range rpsValues {
		for _, dist := range distributions {
			for _, dur := range durations {
				RunExperiment(client, rps, dur, dist)
			}
		}
	}
}
