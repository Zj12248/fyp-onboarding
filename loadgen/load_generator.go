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

type batchResult struct {
	workerE2E int64
	clientE2E int64
}

func RunExperiment(client pb.WorkerServiceClient, rps int, durationMs int32, distribution string) {
	fmt.Printf("Running Experiment with RPS=%d, DUR=%d\n", rps, durationMs)
	runID := fmt.Sprintf("RPS%d_Dur%d_%s_%s",
		rps, durationMs, distribution, time.Now().Format("150405"))
	logFile := fmt.Sprintf("logs/%s.log", runID)

	os.MkdirAll("logs", os.ModePerm)
	f, err := os.Create(logFile)
	if err != nil {
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer f.Close()
	logger := log.New(f, "", log.LstdFlags)

	runMin := 2
	endTime := time.Now().Add(time.Duration(runMin) * time.Minute)

	var wg sync.WaitGroup
	var ticker *time.Ticker
	if distribution == "uniform" {
		ticker = time.NewTicker(time.Second / time.Duration(rps))
		defer ticker.Stop()
	}

	reqCount := 0
	batchResults := []batchResult{}
	var batchMutex sync.Mutex

	// Time-based batch ticker (30s)
	batchTicker := time.NewTicker(30 * time.Second)
	defer batchTicker.Stop()

	done := make(chan struct{})

	// Goroutine to log batch averages every 30s
	go func() {
		for {
			select {
			case <-batchTicker.C:
				batchMutex.Lock()
				if len(batchResults) > 0 {
					var sumWorker, sumClient int64
					for _, r := range batchResults {
						sumWorker += r.workerE2E
						sumClient += r.clientE2E
					}
					avgWorker := float64(sumWorker) / float64(len(batchResults))
					avgClient := float64(sumClient) / float64(len(batchResults))
					logger.Printf("30s Batch Avg (last %d reqs): WorkerE2E=%.2f ms, ClientE2E=%.2f ms",
						len(batchResults), avgWorker, avgClient)
					batchResults = []batchResult{} // reset for next interval
				}
				batchMutex.Unlock()
			case <-done:
				return
			}
		}
	}()

	for time.Now().Before(endTime) {
		if distribution == "uniform" {
			<-ticker.C
		} else { // poisson
			meanInterval := float64(time.Second) / float64(rps)
			delay := time.Duration(rand.ExpFloat64() * meanInterval)
			time.Sleep(delay)
		}

		reqCount++
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()
			start := time.Now()
			resp, err := client.DoWork(
				context.Background(),
				&pb.WorkRequest{DurationMs: durationMs},
			)
			e2e := time.Since(start).Milliseconds()

			if err != nil {
				logger.Printf("Error (req %d): %v", idx, err)
				return
			}

			// Store result for 30s batch
			batchMutex.Lock()
			batchResults = append(batchResults, batchResult{
				workerE2E: resp.E2ELatencyMs,
				clientE2E: e2e,
			})
			batchMutex.Unlock()
		}(reqCount)
	}

	wg.Wait()
	close(done)

	// log remaining batch if any
	batchMutex.Lock()
	if len(batchResults) > 0 {
		var sumWorker, sumClient int64
		for _, r := range batchResults {
			sumWorker += r.workerE2E
			sumClient += r.clientE2E
		}
		avgWorker := float64(sumWorker) / float64(len(batchResults))
		avgClient := float64(sumClient) / float64(len(batchResults))
		logger.Printf("Final Batch Avg (last %d reqs): WorkerE2E=%.2f ms, ClientE2E=%.2f ms",
			len(batchResults), avgWorker, avgClient)
	}
	batchMutex.Unlock()

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

	// Sweep parameters(reduced for testing)
	rpsValues := []int{50, 100}          //, 15, 20, 25, 30, 35, 40, 45, 50}
	distributions := []string{"uniform"} // , "poisson"}
	durations := []int32{500, 1000}      // 300, 400, 500, 600, 700, 800, 900, 1000}

	fmt.Printf("Performing Grid Search\n")
	// Full grid search
	for _, rps := range rpsValues {
		for _, dist := range distributions {
			for _, dur := range durations {
				RunExperiment(client, rps, dur, dist)
			}
		}
	}
}
