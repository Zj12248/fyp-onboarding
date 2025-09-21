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
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type batchResult struct {
	workerE2E     int64
	clientE2E     int64
	avgCpuFreqKhz int64
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

	// Warmup & experiment durations
	warmupMin := 0
	expMin := 2

	var wg sync.WaitGroup
	var ticker *time.Ticker
	if distribution == "uniform" {
		ticker = time.NewTicker(time.Second / time.Duration(rps))
		defer ticker.Stop()
	}

	reqCount := int64(0)
	timeoutCount := int64(0)

	batchResults := []batchResult{}
	var batchMutex sync.Mutex

	// Time-based batch ticker (30s)
	batchTicker := time.NewTicker(30 * time.Second)
	defer batchTicker.Stop()

	done := make(chan struct{})

	// Goroutine to log batch averages every 30s (only during experiment phase)
	go func() {
		for {
			select {
			case <-batchTicker.C:
				batchMutex.Lock()
				if len(batchResults) > 0 {
					var sumWorker, sumClient, sumFreq int64
					for _, r := range batchResults {
						sumWorker += r.workerE2E
						sumClient += r.clientE2E
						sumFreq += r.avgCpuFreqKhz
					}
					avgWorker := float64(sumWorker) / float64(len(batchResults))
					avgClient := float64(sumClient) / float64(len(batchResults))
					avgFreq := float64(sumFreq) / float64(len(batchResults))
					logger.Printf("30s Batch Avg (last %d reqs): WorkerE2E=%.2f ms, ClientE2E=%.2f ms, AvgCPUFreq=%.2f kHz",
						len(batchResults), avgWorker, avgClient, avgFreq)
					batchResults = []batchResult{} // reset
				}
				batchMutex.Unlock()
			case <-done:
				return
			}
		}
	}()

	// --- Warmup Phase ---
	fmt.Printf("Warmup for %d minutes (discarding results)...\n", warmupMin)
	warmupEnd := time.Now().Add(time.Duration(warmupMin) * time.Minute)
	for time.Now().Before(warmupEnd) {
		if distribution == "uniform" {
			<-ticker.C
		} else {
			meanInterval := float64(time.Second) / float64(rps)
			delay := time.Duration(rand.ExpFloat64() * meanInterval)
			time.Sleep(delay)
		}

		go func() {
			_, _ = client.DoWork(
				context.Background(),
				&pb.WorkRequest{DurationMs: durationMs},
			)
		}()
	}

	// --- Experiment Phase ---
	fmt.Printf("Running experiment for %d minutes...\n", expMin)
	expEnd := time.Now().Add(time.Duration(expMin) * time.Minute)

	expCtx, expCancel := context.WithCancel(context.Background())
	defer expCancel()

	for time.Now().Before(expEnd) {
		select {
		case <-expCtx.Done():
			fmt.Println("Experiment stopped early due to high timeout rate.")
			break
		default:
		}

		if distribution == "uniform" {
			<-ticker.C
		} else {
			meanInterval := float64(time.Second) / float64(rps)
			delay := time.Duration(rand.ExpFloat64() * meanInterval)
			time.Sleep(delay)
		}

		newReqID := atomic.AddInt64(&reqCount, 1)
		wg.Add(1)

		go func(idx int64) {
			defer wg.Done()
			start := time.Now()

			// Timeout = 10x requested spin duration
			timeout := time.Duration(durationMs) * 20 * time.Millisecond
			ctx, cancel := context.WithTimeout(expCtx, timeout)
			defer cancel()

			resp, err := client.DoWork(
				ctx, // for timeout
				&pb.WorkRequest{DurationMs: durationMs},
			)
			e2e := time.Since(start).Milliseconds()

			if err != nil {
				logger.Printf("Error (req %d): %v", idx, err)
				if ctx.Err() == context.DeadlineExceeded { //if timeout
					atomic.AddInt64(&timeoutCount, 1)
				}
				// Check threshold (10% timeout)
				total := atomic.LoadInt64(&reqCount)
				timeouts := atomic.LoadInt64(&timeoutCount)
				if total > 50 && float64(timeouts)/float64(total) > 0.10 {
					logger.Printf("Timeout rate exceeded 10%% (timeouts=%d, total=%d). Stopping experiment.", timeouts, total)
					expCancel()
				}
				return
			}

			// Store result for 30s batch
			batchMutex.Lock()
			batchResults = append(batchResults, batchResult{
				workerE2E:     resp.E2ELatencyMs,
				clientE2E:     e2e,
				avgCpuFreqKhz: resp.AvgCpuFreqKhz,
			})
			batchMutex.Unlock()
		}(newReqID)
	}

	wg.Wait()
	close(done)

	// log remaining batch if any
	batchMutex.Lock()
	if len(batchResults) > 0 {
		var sumWorker, sumClient, sumFreq int64
		for _, r := range batchResults {
			sumWorker += r.workerE2E
			sumClient += r.clientE2E
			sumFreq += r.avgCpuFreqKhz
		}
		avgWorker := float64(sumWorker) / float64(len(batchResults))
		avgClient := float64(sumClient) / float64(len(batchResults))
		avgFreq := float64(sumFreq) / float64(len(batchResults))
		logger.Printf("Final Batch Avg (last %d reqs): WorkerE2E=%.2f ms, ClientE2E=%.2f ms, AvgCPUFreq=%.2f kHz",
			len(batchResults), avgWorker, avgClient, avgFreq)
	}
	batchMutex.Unlock()

	total := atomic.LoadInt64(&reqCount)
	timeouts := atomic.LoadInt64(&timeoutCount)
	logger.Printf("Finished experiment: RPS=%d, Duration=%dms, Dist=%s, TotalReq=%d, Timeouts=%d (%.2f%%)",
		rps, durationMs, distribution, total, timeouts, 100*float64(timeouts)/float64(total))
}

func main() {
	fmt.Printf("Loadgen Script running\n")
	workerAddr := flag.String("worker", "localhost:50051", "Worker gRPC host:port")
	flag.Parse()

	// Open log file
	f, _ := os.Create("load.log")
	defer f.Close()
	log.SetOutput(f)

	fmt.Printf("Connecting to worker at %s...\n", *workerAddr)

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

	// Sweep parameters (reduced for testing)
	rpsValues := []int{25}
	distributions := []string{"uniform"}
	durations := []int32{1000}

	fmt.Printf("Performing Grid Search\n")
	for _, rps := range rpsValues {
		for _, dist := range distributions {
			for _, dur := range durations {
				RunExperiment(client, rps, dur, dist)
			}
		}
	}
}
