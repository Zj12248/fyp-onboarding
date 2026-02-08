package main

import (
	"context"
	"flag"
	"fmt"
	pb "fyp-onboarding/workerpb"
	"log"
	"math"
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
	// High-precision network metrics
	clientSendNs       int64 // Client send timestamp (ns)
	clientRecvNs       int64 // Client receive timestamp (ns)
	networkLatencyNs   int64 // Pure network latency (total - worker processing)
	workerProcessingNs int64 // Worker-reported processing time
	dataPlaneLatencyNs int64 // Estimated one-way data plane latency
}

const WARMUPMIN = 1
const EXPMIN = 2

// ---------------- Experiment Runner ----------------
func RunExperiment(client pb.WorkerServiceClient, rps int, durationMs int32, distribution string, workMode string, proxyMode string, experimentName string) {
	fmt.Printf("Running Experiment with RPS=%d, DUR=%d, WorkMode=%s, ProxyMode=%s\n", rps, durationMs, workMode, proxyMode)

	runStart := time.Now()
	runID := fmt.Sprintf("RPS%d_Dur%d_%s_WM-%s_PM-%s_%s", rps, durationMs, distribution, workMode, proxyMode, time.Now().Format("150405"))
	if experimentName != "" {
		runID = fmt.Sprintf("%s_%s", experimentName, runID)
	}
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
					var sumNetworkLatency, sumDataPlane, sumWorkerProcessing int64
					var networkLatencies, dataPlaneLatencies []int64

					for _, r := range batchResults {
						sumWorker += r.workerE2E
						sumClient += r.clientE2E
						sumFreq += r.avgCpuFreqKhz
						sumIter += r.iterations
						sumNetworkLatency += r.networkLatencyNs
						sumDataPlane += r.dataPlaneLatencyNs
						sumWorkerProcessing += r.workerProcessingNs
						networkLatencies = append(networkLatencies, r.networkLatencyNs)
						dataPlaneLatencies = append(dataPlaneLatencies, r.dataPlaneLatencyNs)
					}

					n := float64(len(batchResults))
					avgWorker := float64(sumWorker) / n
					avgClient := float64(sumClient) / n
					avgFreq := float64(sumFreq) / n
					avgIter := float64(sumIter) / n
					avgNetworkLatencyUs := float64(sumNetworkLatency) / n / 1000.0
					avgDataPlaneUs := float64(sumDataPlane) / n / 1000.0
					avgWorkerProcessingMs := float64(sumWorkerProcessing) / n / 1e6

					// Calculate jitter (standard deviation)
					var sumSqDiff float64
					meanDataPlane := float64(sumDataPlane) / n
					for _, val := range dataPlaneLatencies {
						diff := float64(val) - meanDataPlane
						sumSqDiff += diff * diff
					}
					jitterUs := 0.0
					if len(dataPlaneLatencies) > 1 {
						jitterUs = math.Sqrt(sumSqDiff/float64(len(dataPlaneLatencies))) / 1000.0
					}

					logger.Printf("20s Batch Avg (last %d reqs): WorkerE2E=%.2f ms, ClientE2E=%.2f ms, NetworkLatency=%.2f µs, DataPlaneLatency=%.2f µs, Jitter=%.2f µs, WorkerProcessing=%.3f ms, AvgCPUFreq=%.2f kHz, AvgIterations=%.0f",
						len(batchResults), avgWorker, avgClient, avgNetworkLatencyUs, avgDataPlaneUs, jitterUs, avgWorkerProcessingMs, avgFreq, avgIter)
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
			_, _ = client.DoWork(context.Background(), &pb.WorkRequest{DurationMs: durationMs, WorkMode: workMode})
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

			// High-precision timing: capture send timestamp
			sendTime := time.Now()
			sendNs := sendTime.UnixNano()

			timeout := time.Duration(durationMs) * 20 * time.Millisecond
			ctx, cancel := context.WithTimeout(expCtx, timeout)
			defer cancel()

			resp, err := client.DoWork(ctx, &pb.WorkRequest{DurationMs: durationMs, WorkMode: workMode})

			// High-precision timing: capture receive timestamp
			recvTime := time.Now()
			recvNs := recvTime.UnixNano()
			e2e := time.Since(sendTime).Milliseconds()

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

			// Calculate network-specific metrics
			clientRoundTripNs := recvNs - sendNs
			workerProcessingNs := resp.WorkerProcessingNs
			networkLatencyNs := clientRoundTripNs - workerProcessingNs
			// Approximate one-way data plane latency (divide by 2 for request + response path)
			dataPlaneLatencyNs := networkLatencyNs / 2

			batchMutex.Lock()
			batchResults = append(batchResults, batchResult{
				workerE2E:          resp.E2ELatencyMs,
				clientE2E:          e2e,
				avgCpuFreqKhz:      resp.AvgCpuFreqKhz,
				iterations:         resp.Iterations,
				clientSendNs:       sendNs,
				clientRecvNs:       recvNs,
				networkLatencyNs:   networkLatencyNs,
				workerProcessingNs: workerProcessingNs,
				dataPlaneLatencyNs: dataPlaneLatencyNs,
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
		var sumNetworkLatency, sumDataPlane, sumWorkerProcessing int64
		var dataPlaneLatencies []int64

		for _, r := range batchResults {
			sumWorker += r.workerE2E
			sumClient += r.clientE2E
			sumFreq += r.avgCpuFreqKhz
			sumIter += r.iterations
			sumNetworkLatency += r.networkLatencyNs
			sumDataPlane += r.dataPlaneLatencyNs
			sumWorkerProcessing += r.workerProcessingNs
			dataPlaneLatencies = append(dataPlaneLatencies, r.dataPlaneLatencyNs)
		}

		n := float64(len(batchResults))
		avgWorker := float64(sumWorker) / n
		avgClient := float64(sumClient) / n
		avgFreq := float64(sumFreq) / n
		avgIter := float64(sumIter) / n
		avgNetworkLatencyUs := float64(sumNetworkLatency) / n / 1000.0
		avgDataPlaneUs := float64(sumDataPlane) / n / 1000.0
		avgWorkerProcessingMs := float64(sumWorkerProcessing) / n / 1e6

		// Calculate jitter
		var sumSqDiff float64
		meanDataPlane := float64(sumDataPlane) / n
		for _, val := range dataPlaneLatencies {
			diff := float64(val) - meanDataPlane
			sumSqDiff += diff * diff
		}
		jitterUs := 0.0
		if len(dataPlaneLatencies) > 1 {
			jitterUs = math.Sqrt(sumSqDiff/float64(len(dataPlaneLatencies))) / 1000.0
		}

		logger.Printf("Final Batch Avg (last %d reqs): WorkerE2E=%.2f ms, ClientE2E=%.2f ms, NetworkLatency=%.2f µs, DataPlaneLatency=%.2f µs, Jitter=%.2f µs, WorkerProcessing=%.3f ms, AvgCPUFreq=%.2f kHz, AvgIterations=%.0f",
			len(batchResults), avgWorker, avgClient, avgNetworkLatencyUs, avgDataPlaneUs, jitterUs, avgWorkerProcessingMs, avgFreq, avgIter)
	}
	batchMutex.Unlock()

	total := atomic.LoadInt64(&reqCount)
	timeouts := atomic.LoadInt64(&timeoutCount)
	timeoutRate := 0.0
	if total > 0 {
		timeoutRate = 100 * float64(timeouts) / float64(total)
	}

	runDuration := time.Since(runStart)
	logger.Printf("Finished experiment: RPS=%d, Duration=%dms, Dist=%s, WorkMode=%s, ProxyMode=%s, TotalReq=%d, Timeouts=%d (%.2f%%), RunTime=%s",
		rps, durationMs, distribution, workMode, proxyMode, total, timeouts, timeoutRate, runDuration)
	fmt.Printf("Timeout rate: %.2f%%, Total run duration: %s\n", timeoutRate, runDuration)
}

// ---------------- Main Function ----------------
func main() {
	fmt.Println("Loadgen Script running")

	workerAddr := flag.String("worker", "localhost:50051", "Worker gRPC host:port")
	workMode := flag.String("work-mode", "full", "Work mode: full or echo")
	proxyMode := flag.String("proxy-mode", "unknown", "Kube-proxy mode: iptables-nft or nftables")
	experimentName := flag.String("experiment-name", "", "Custom experiment name for logs")
	flag.Parse()

	// Logging
	f, _ := os.Create("load.log")
	defer f.Close()
	log.SetOutput(f)

	// Start Prometheus metrics server
	prometheus.MustRegister(totalRequests)
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		fmt.Println("Inactive! -- Prometheus metrics")
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
	rpsValues := []int{10, 20, 30} //{15, 20, 25, 30, 35, 40}
	distributions := []string{"uniform"}
	durations := []int32{600, 900} //{300, 400, 500, 600, 700, 800, 900, 1000}

	fmt.Println("Performing Grid Search")
	fmt.Printf("Configuration: WorkMode=%s, ProxyMode=%s\n", *workMode, *proxyMode)
	for _, rps := range rpsValues {
		for _, dist := range distributions {
			for _, dur := range durations {
				RunExperiment(client, rps, dur, dist, *workMode, *proxyMode, *experimentName)
				time.Sleep(5 * time.Second) // sleep between runs
			}
		}
	}
}
