package main

import (
	"context"
	"flag"
	"fmt"
	pb "fyp-onboarding/workerpb"
	"log"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ---------------- Prometheus Metric ----------------
var totalRequests = prometheus.NewCounter(
	prometheus.CounterOpts{
		Name: "loadgen_total_requests",
		Help: "Total number of requests sent by loadgen",
	},
)

// ---------------- Batch Result Struct ----------------
type batchResult struct {
	workerE2E     int64
	clientE2E     int64
	avgCpuFreqKhz int64
	iterations    int64
}

const WARMUPMIN = 1
const EXPMIN = 2

// ---------------- Experiment Runner ----------------
func RunExperiment(client pb.WorkerServiceClient, rps int, durationMs int32, distribution string) {
	fmt.Printf("Running Experiment with RPS=%d, DUR=%d\n", rps, durationMs)

	runStart := time.Now()
	runID := fmt.Sprintf("RPS%d_Dur%d_%s_%s", rps, durationMs, distribution, time.Now().Format("150405"))
	logFile := fmt.Sprintf("logs/%s.log", runID)
	os.MkdirAll("logs", os.ModePerm)
	f, err := os.Create(logFile)
	if err != nil {
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer f.Close()
	logger := log.New(f, "", log.LstdFlags)

	var wg sync.WaitGroup
	var ticker *time.Ticker
	if distribution == "uniform" {
		ticker = time.NewTicker(time.Second / time.Duration(rps))
		defer ticker.Stop()
	}

	var reqCount int64
	var timeoutCount int64
	batchResults := []batchResult{}
	var batchMutex sync.Mutex

	batchTicker := time.NewTicker(20 * time.Second)
	defer batchTicker.Stop()
	done := make(chan struct{})

	// Log batch averages every 20s
	go func() {
		for {
			select {
			case <-batchTicker.C:
				batchMutex.Lock()
				if len(batchResults) > 0 {
					var sumWorker, sumClient, sumFreq, sumIter int64
					for _, r := range batchResults {
						sumWorker += r.workerE2E
						sumClient += r.clientE2E
						sumFreq += r.avgCpuFreqKhz
						sumIter += r.iterations
					}
					avgWorker := float64(sumWorker) / float64(len(batchResults))
					avgClient := float64(sumClient) / float64(len(batchResults))
					avgFreq := float64(sumFreq) / float64(len(batchResults))
					avgIter := float64(sumIter) / float64(len(batchResults))
					logger.Printf("20s Batch Avg (last %d reqs): WorkerE2E=%.2f ms, ClientE2E=%.2f ms, AvgCPUFreq=%.2f kHz, AvgIterations=%.0f",
						len(batchResults), avgWorker, avgClient, avgFreq, avgIter)
					batchResults = []batchResult{}
				}
				batchMutex.Unlock()
			case <-done:
				return
			}
		}
	}()

	// --- Warmup Phase ---
	fmt.Printf("Warmup for %d minutes (discarding results)...\n", WARMUPMIN)
	warmupEnd := time.Now().Add(time.Duration(WARMUPMIN) * time.Minute)
	for time.Now().Before(warmupEnd) {
		if distribution == "uniform" {
			<-ticker.C
		} else {
			meanInterval := float64(time.Second) / float64(rps)
			time.Sleep(time.Duration(rand.ExpFloat64() * meanInterval))
		}
		go func() {
			_, _ = client.DoWork(context.Background(), &pb.WorkRequest{DurationMs: durationMs})
		}()
	}

	// --- Experiment Phase ---
	fmt.Printf("Running experiment for %d minutes...\n", EXPMIN)
	expEnd := time.Now().Add(time.Duration(EXPMIN) * time.Minute)
	expCtx, expCancel := context.WithCancel(context.Background())
	defer expCancel()

	stopEarly := int32(0)

	for time.Now().Before(expEnd) && atomic.LoadInt32(&stopEarly) == 0 {
		if distribution == "uniform" {
			<-ticker.C
		} else {
			meanInterval := float64(time.Second) / float64(rps)
			time.Sleep(time.Duration(rand.ExpFloat64() * meanInterval))
		}

		newReqID := atomic.AddInt64(&reqCount, 1)
		totalRequests.Inc() // Prometheus metric

		wg.Add(1)
		go func(idx int64) {
			defer wg.Done()
			start := time.Now()

			timeout := time.Duration(durationMs) * 20 * time.Millisecond
			ctx, cancel := context.WithTimeout(expCtx, timeout)
			defer cancel()

			resp, err := client.DoWork(ctx, &pb.WorkRequest{DurationMs: durationMs})
			e2e := time.Since(start).Milliseconds()

			if err != nil {
				if ctx.Err() == context.DeadlineExceeded {
					atomic.AddInt64(&timeoutCount, 1)
				}
				total := atomic.LoadInt64(&reqCount)
				timeouts := atomic.LoadInt64(&timeoutCount)
				if total > 50 && float64(timeouts)/float64(total) > 0.10 {
					atomic.StoreInt32(&stopEarly, 1)
					expCancel()
				}
				return
			}

			batchMutex.Lock()
			batchResults = append(batchResults, batchResult{
				workerE2E:     resp.E2ELatencyMs,
				clientE2E:     e2e,
				avgCpuFreqKhz: resp.AvgCpuFreqKhz,
				iterations:    resp.Iterations,
			})
			batchMutex.Unlock()
		}(newReqID)
	}

	wg.Wait()
	close(done)

	// Log final batch
	batchMutex.Lock()
	if len(batchResults) > 0 {
		var sumWorker, sumClient, sumFreq, sumIter int64
		for _, r := range batchResults {
			sumWorker += r.workerE2E
			sumClient += r.clientE2E
			sumFreq += r.avgCpuFreqKhz
			sumIter += r.iterations
		}
		avgWorker := float64(sumWorker) / float64(len(batchResults))
		avgClient := float64(sumClient) / float64(len(batchResults))
		avgFreq := float64(sumFreq) / float64(len(batchResults))
		avgIter := float64(sumIter) / float64(len(batchResults))
		logger.Printf("Final Batch Avg (last %d reqs): WorkerE2E=%.2f ms, ClientE2E=%.2f ms, AvgCPUFreq=%.2f kHz, AvgIterations=%.0f",
			len(batchResults), avgWorker, avgClient, avgFreq, avgIter)
	}
	batchMutex.Unlock()

	total := atomic.LoadInt64(&reqCount)
	timeouts := atomic.LoadInt64(&timeoutCount)
	timeoutRate := 0.0
	if total > 0 {
		timeoutRate = 100 * float64(timeouts) / float64(total)
	}

	runDuration := time.Since(runStart)
	logger.Printf("Finished experiment: RPS=%d, Duration=%dms, Dist=%s, TotalReq=%d, Timeouts=%d (%.2f%%), RunTime=%s",
		rps, durationMs, distribution, total, timeouts, timeoutRate, runDuration)
	fmt.Printf("Timeout rate: %.2f%%, Total run duration: %s\n", timeoutRate, runDuration)
}

// ---------------- Main Function ----------------
func main() {
	fmt.Println("Loadgen Script running")

	workerAddr := flag.String("worker", "localhost:50051", "Worker gRPC host:port")
	flag.Parse()

	// Logging
	f, _ := os.Create("load.log")
	defer f.Close()
	log.SetOutput(f)

	// Start Prometheus metrics server
	prometheus.MustRegister(totalRequests)
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		fmt.Println("Prometheus metrics available at :9090/metrics")
		http.ListenAndServe(":9090", nil)
	}()

	// Connect to gRPC worker
	fmt.Printf("Connecting to worker at %s...\n", *workerAddr)
	conn, err := grpc.Dial(*workerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()
	client := pb.NewWorkerServiceClient(conn)
	fmt.Println("Connection successful")

	// Grid search values
	rpsValues := []int{20} //{15, 20, 25, 30, 35, 40}
	distributions := []string{"uniform"}
	durations := []int32{910} //{300, 400, 500, 600, 700, 800, 900, 1000}

	fmt.Println("Performing Grid Search")
	for _, rps := range rpsValues {
		for _, dist := range distributions {
			for _, dur := range durations {
				RunExperiment(client, rps, dur, dist)
				time.Sleep(5 * time.Second) // sleep between runs
			}
		}
	}
}
