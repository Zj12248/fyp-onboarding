package main

// Usage:
// go run loadgen-dataplane/load_generator.go \
//  --worker=<worker-url>:80 \
//  --rps=50 \
//  --num-requests=1000 \
//  --proxy-mode=iptables-nft \
//  --service-count=10
//
// service-count, proxy-mode are used for logging purposes only, they are not defined here.
import (
	"context"
	"flag"
	"fmt"
	pb "fyp-onboarding/workerpb"
	"log"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ---------------- Result Struct for Individual Requests ----------------
type requestResult struct {
	sequenceNum        int
	rttUs              float64 // Round trip time in microseconds
	dataPlaneLatencyUs float64 // One-way data plane latency estimate
	workerProcessingUs float64 // Worker processing time
}

// ---------------- Main Test Runner ----------------
func RunDataPlaneTest(client pb.WorkerServiceClient, config TestConfig) {
	fmt.Printf("\n=== Data Plane Latency Test ===\n")
	fmt.Printf("  Proxy Mode: %s\n", config.ProxyMode)
	fmt.Printf("  Service Count: %d\n", config.ServiceCount)
	fmt.Printf("  Total Requests: %d\n", config.NumRequests)
	fmt.Printf("  RPS: %d\n", config.RPS)
	fmt.Printf("\n")

	// Create output directory and files
	timestamp := time.Now().Format("20060102_150405")
	runID := fmt.Sprintf("PM_%s_SC_%d_RPS_%d_%s",
		config.ProxyMode, config.ServiceCount, config.RPS, timestamp)

	os.MkdirAll("logs/dataplane", os.ModePerm)
	logFile := fmt.Sprintf("logs/dataplane/%s.log", runID)
	csvFile := fmt.Sprintf("logs/dataplane/%s.csv", runID)

	// Setup logging
	f, err := os.Create(logFile)
	if err != nil {
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer f.Close()
	logger := log.New(f, "", log.LstdFlags)

	logger.Printf("Test Configuration: ProxyMode=%s, ServiceCount=%d, NumRequests=%d, RPS=%d",
		config.ProxyMode, config.ServiceCount, config.NumRequests, config.RPS)

	// Prepare CSV
	csvF, err := os.Create(csvFile)
	if err != nil {
		log.Fatalf("Failed to create CSV file: %v", err)
	}
	defer csvF.Close()
	fmt.Fprintf(csvF, "seq,rtt_us,data_plane_latency_us,worker_processing_us\n")

	var results []requestResult
	var resultsMutex sync.Mutex
	var wg sync.WaitGroup

	ticker := time.NewTicker(time.Second / time.Duration(config.RPS))
	defer ticker.Stop()

	fmt.Printf("Sending %d requests at %d RPS...\n", config.NumRequests, config.RPS)
	testStart := time.Now()

	// Send requests
	for i := 0; i < config.NumRequests; i++ {
		<-ticker.C

		wg.Add(1)
		go func(seq int) {
			defer wg.Done()

			// Measure RTT
			sendNs := time.Now().UnixNano()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			resp, err := client.DoWork(ctx, &pb.WorkRequest{DurationMs: 0, WorkMode: "echo"})

			recvNs := time.Now().UnixNano()

			if err != nil {
				return // Skip failed requests
			}

			// Calculate latencies in microseconds
			rttUs := float64(recvNs-sendNs) / 1e3
			workerProcessingUs := float64(resp.WorkerProcessingNs) / 1e3
			networkLatencyUs := rttUs - workerProcessingUs
			dataPlaneLatencyUs := networkLatencyUs / 2.0 // One-way estimate

			result := requestResult{
				sequenceNum:        seq,
				rttUs:              rttUs,
				dataPlaneLatencyUs: dataPlaneLatencyUs,
				workerProcessingUs: workerProcessingUs,
			}

			resultsMutex.Lock()
			results = append(results, result)
			resultsMutex.Unlock()
		}(i)
	}

	wg.Wait()
	testDuration := time.Since(testStart)

	fmt.Printf("Test completed in %s\n", testDuration.Round(time.Millisecond))
	logger.Printf("Test completed. Duration=%s, SuccessfulRequests=%d/%d",
		testDuration, len(results), config.NumRequests)

	if len(results) == 0 {
		logger.Printf("ERROR: No successful requests recorded!")
		fmt.Println("ERROR: No results to analyze!")
		return
	}

	// Write results to CSV
	for _, r := range results {
		fmt.Fprintf(csvF, "%d,%.2f,%.2f,%.2f\n",
			r.sequenceNum, r.rttUs, r.dataPlaneLatencyUs, r.workerProcessingUs)
	}

	// Calculate statistics
	stats := calculateStatistics(results)
	successRate := float64(len(results)) / float64(config.NumRequests) * 100.0

	// Log summary
	logger.Printf("\n=== RESULTS ===")
	logger.Printf("Total Requests: %d", config.NumRequests)
	logger.Printf("Successful: %d (%.2f%%)", len(results), successRate)
	logger.Printf("Data Plane Latency (µs): Mean=%.2f, Median=%.2f, P95=%.2f, P99=%.2f, StdDev=%.2f",
		stats.Mean, stats.P50, stats.P95, stats.P99, stats.StdDev)
	logger.Printf("RTT (µs): Mean=%.2f, P95=%.2f, P99=%.2f",
		stats.RTTMean, stats.RTTP95, stats.RTTP99)

	// Print to console
	fmt.Printf("\n=== RESULTS ===\n")
	fmt.Printf("Successful: %d/%d (%.2f%%)\n", len(results), config.NumRequests, successRate)
	fmt.Printf("\nData Plane Latency (one-way):\n")
	fmt.Printf("  Mean:   %.2f µs\n", stats.Mean)
	fmt.Printf("  Median: %.2f µs\n", stats.P50)
	fmt.Printf("  P95:    %.2f µs\n", stats.P95)
	fmt.Printf("  P99:    %.2f µs\n", stats.P99)
	fmt.Printf("  StdDev: %.2f µs\n", stats.StdDev)
	fmt.Printf("\nRound Trip Time:\n")
	fmt.Printf("  Mean: %.2f µs\n", stats.RTTMean)
	fmt.Printf("  P95:  %.2f µs\n", stats.RTTP95)
	fmt.Printf("  P99:  %.2f µs\n", stats.RTTP99)
	fmt.Printf("\nResults saved to:\n")
	fmt.Printf("  %s\n", logFile)
	fmt.Printf("  %s\n", csvFile)
}

// ---------------- Statistics ----------------
type Stats struct {
	Mean    float64
	P50     float64
	P95     float64
	P99     float64
	Min     float64
	Max     float64
	StdDev  float64
	RTTMean float64
	RTTP95  float64
	RTTP99  float64
}

func calculateStatistics(results []requestResult) Stats {
	n := len(results)
	dataPlane := make([]float64, n)
	rttValues := make([]float64, n)

	sum := 0.0
	rttSum := 0.0
	for i, r := range results {
		dataPlane[i] = r.dataPlaneLatencyUs
		rttValues[i] = r.rttUs
		sum += r.dataPlaneLatencyUs
		rttSum += r.rttUs
	}

	mean := sum / float64(n)
	rttMean := rttSum / float64(n)

	// Standard deviation
	sumSqDiff := 0.0
	for _, v := range dataPlane {
		diff := v - mean
		sumSqDiff += diff * diff
	}
	stdDev := math.Sqrt(sumSqDiff / float64(n))

	// Sort for percentiles
	sort.Float64s(dataPlane)
	sort.Float64s(rttValues)

	return Stats{
		Mean:    mean,
		P50:     percentile(dataPlane, 50),
		P95:     percentile(dataPlane, 95),
		P99:     percentile(dataPlane, 99),
		Min:     dataPlane[0],
		Max:     dataPlane[n-1],
		StdDev:  stdDev,
		RTTMean: rttMean,
		RTTP95:  percentile(rttValues, 95),
		RTTP99:  percentile(rttValues, 99),
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)) * p / 100.0)
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

// ---------------- Configuration ----------------
type TestConfig struct {
	WorkerAddr   string
	RPS          int
	NumRequests  int
	ProxyMode    string
	ServiceCount int
}

// ---------------- Main ----------------
func main() {
	fmt.Println("Data Plane Latency Test - Simple Packet Sender")

	workerAddr := flag.String("worker", "localhost:50051", "Worker gRPC host:port")
	rps := flag.Int("rps", 50, "Requests per second")
	numRequests := flag.Int("num-requests", 1000, "Total number of requests to send")
	proxyMode := flag.String("proxy-mode", "unknown", "Kube-proxy mode: iptables-nft or nftables")
	serviceCount := flag.Int("service-count", 1, "Number of services in cluster")
	flag.Parse()

	if *proxyMode == "unknown" {
		fmt.Println("WARNING: --proxy-mode not specified. Use --proxy-mode=iptables-nft or --proxy-mode=nftables")
	}

	config := TestConfig{
		WorkerAddr:   *workerAddr,
		RPS:          *rps,
		NumRequests:  *numRequests,
		ProxyMode:    *proxyMode,
		ServiceCount: *serviceCount,
	}

	// Connect to worker
	fmt.Printf("Connecting to worker at %s...\n", config.WorkerAddr)
	conn, err := grpc.Dial(config.WorkerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	client := pb.NewWorkerServiceClient(conn)
	fmt.Println("Connected successfully")

	// Run test
	RunDataPlaneTest(client, config)

	fmt.Println("\nTest complete!")
}
