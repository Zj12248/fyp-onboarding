package main

// Usage:
// Single test mode:
//   go run loadgen-dataplane/load_generator.go \
//     --worker=<worker-ip>:50051 \
//     --rps=200 \
//     --num-requests=10000 \
//     --proxy-mode=iptables-nft \
//     --service-count=10
//
// Full experiment mode:
//   go run loadgen-dataplane/load_generator.go \
//     --worker=<worker-ip>:50051 \
//     --rps=200 \
//     --num-requests=10000 \
//     --proxy-mode=iptables-nft \
//     --full-experiment
//
import (
	"context"
	"flag"
	"fmt"
	pb "fyp-onboarding/workerpb"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ---------------- Constants ----------------
const (
	WorkerPoolSize = 100 // Number of concurrent workers
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
	fmt.Printf("  Worker: %s\n", config.WorkerAddr)
	fmt.Printf("\n")

	// Create output directory and files
	timestamp := time.Now().Format("20060102_150405")
	runID := fmt.Sprintf("PM_%s_SC_%d_RPS_%d_%s",
		config.ProxyMode, config.ServiceCount, config.RPS, timestamp)

	os.MkdirAll("logs/dataplane", os.ModePerm)
	logFile := fmt.Sprintf("logs/dataplane/%s.log", runID)
	csvFile := fmt.Sprintf("logs/dataplane/%s.csv", runID)

	// Setup logging
	fmt.Printf("Creating log file: %s\n", logFile)
	f, err := os.Create(logFile)
	if err != nil {
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer f.Close()
	logger := log.New(f, "", log.LstdFlags)
	fmt.Printf("Creating CSV file: %s\n", csvFile)

	logger.Printf("Test Configuration: ProxyMode=%s, ServiceCount=%d, NumRequests=%d, RPS=%d",
		config.ProxyMode, config.ServiceCount, config.NumRequests, config.RPS)

	// Prepare CSV
	csvF, err := os.Create(csvFile)
	if err != nil {
		log.Fatalf("Failed to create CSV file: %v", err)
	}
	defer csvF.Close()
	// CSV columns: all time values in microseconds (µs)
	fmt.Fprintf(csvF, "seq,rtt_us,data_plane_latency_us,worker_processing_us\n")

	var results []requestResult
	var resultsMutex sync.Mutex
	var wg sync.WaitGroup

	// Worker pool: create channel for request IDs
	requestChan := make(chan int, WorkerPoolSize*2)

	// Start worker pool
	fmt.Printf("Starting %d workers...\n", WorkerPoolSize)
	for w := 0; w < WorkerPoolSize; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for seq := range requestChan {
				// Debug first request
				if seq == 0 {
					fmt.Printf("Sending first request (seq=%d) to worker...\n", seq)
				}

				// Capture timestamp right before gRPC call (T2)
				sendNs := time.Now().UnixNano()

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				resp, err := client.DoWork(ctx, &pb.WorkRequest{DurationMs: 0, WorkMode: "echo"})
				cancel()

				recvNs := time.Now().UnixNano()

				if err != nil {
					logger.Printf("Request %d failed: %v", seq, err)
					// Print first few errors
					if seq < 5 {
						fmt.Printf("[ERROR] Request %d failed: %v\n", seq, err)
					}
					continue // Skip failed requests
				}

				// Debug first successful response
				if seq == 0 {
					fmt.Printf("First request successful! RTT=%.2fµs\n", float64(recvNs-sendNs)/1e3)
				}

				// Calculate latencies in microseconds (convert from nanoseconds)
				// Conversion: 1 microsecond (µs) = 1000 nanoseconds (ns)
				rttUs := float64(recvNs-sendNs) / 1e3                        // Total round-trip time (T3-T2)
				workerProcessingUs := float64(resp.WorkerProcessingNs) / 1e3 // Worker processing time
				networkLatencyUs := rttUs - workerProcessingUs               // Network latency (both ways)
				dataPlaneLatencyUs := networkLatencyUs / 2.0                 // One-way data plane latency

				result := requestResult{
					sequenceNum:        seq,
					rttUs:              rttUs,
					dataPlaneLatencyUs: dataPlaneLatencyUs,
					workerProcessingUs: workerProcessingUs,
				}

				resultsMutex.Lock()
				results = append(results, result)
				resultsMutex.Unlock()
			}
		}(w)
	}

	fmt.Printf("Sending %d requests at %d RPS...\n", config.NumRequests, config.RPS)
	fmt.Printf("Press Ctrl+C to abort if needed\n")
	testStart := time.Now()

	// Send request IDs to worker pool at controlled rate
	ticker := time.NewTicker(time.Second / time.Duration(config.RPS))
	defer ticker.Stop()

	for i := 0; i < config.NumRequests; i++ {
		<-ticker.C

		// Progress indicator every 100 requests
		if (i+1)%100 == 0 {
			fmt.Printf("Sent %d/%d requests...\n", i+1, config.NumRequests)
		}

		requestChan <- i
	}

	// Close channel and wait for workers to finish
	close(requestChan)
	fmt.Printf("Waiting for all requests to complete...\n")
	wg.Wait()
	testDuration := time.Since(testStart)

	fmt.Printf("Test completed in %s\n", testDuration.Round(time.Millisecond))
	fmt.Printf("Successful requests: %d/%d (%.2f%%)\n",
		len(results), config.NumRequests,
		float64(len(results))/float64(config.NumRequests)*100.0)
	logger.Printf("Test completed. Duration=%s, SuccessfulRequests=%d/%d",
		testDuration, len(results), config.NumRequests)

	if len(results) == 0 {
		logger.Printf("ERROR: No successful requests recorded!")
		fmt.Println("\n========================================")
		fmt.Println("ERROR: No results to analyze!")
		fmt.Println("All requests failed. Check:")
		fmt.Printf("  1. Worker is running: kubectl get pod -l app=worker\n")
		fmt.Printf("  2. DNS resolves: nslookup %s\n", config.WorkerAddr)
		fmt.Printf("  3. Worker logs: kubectl logs -l app=worker\n")
		fmt.Println("  4. Network connectivity from this node to cluster")
		fmt.Println("========================================\n")
		return
	}

	// Write results to CSV
	fmt.Printf("Writing %d results to CSV...\n", len(results))
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
	WorkerAddr     string
	RPS            int
	NumRequests    int
	ProxyMode      string
	ServiceCount   int
	FullExperiment bool
}

// ---------------- Service Management ----------------
func createDummyServices(count int, projectRoot string) error {
	fmt.Printf("Creating %d dummy services...\n", count)
	scriptDir := filepath.Join(projectRoot, "scripts/create-dummy-services")
	cmd := exec.Command("go", "run", "main.go", "-count", strconv.Itoa(count))
	cmd.Dir = scriptDir // Run from the script directory so go.mod is found
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func deleteDummyServices(projectRoot string) error {
	fmt.Println("Deleting all dummy services...")
	scriptDir := filepath.Join(projectRoot, "scripts/delete-dummy-services")
	cmd := exec.Command("go", "run", "main.go")
	cmd.Dir = scriptDir // Run from the script directory so go.mod is found
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func waitForKubeproxySync(seconds int) {
	fmt.Printf("Waiting %ds for kube-proxy to sync rules...\n", seconds)
	time.Sleep(time.Duration(seconds) * time.Second)
}

func getWorkerPosition(workerIP string, proxyMode string) (position int, totalRules int) {
	// Get worker position using same commands as run-experiment.sh
	posCmd := fmt.Sprintf("sudo iptables -t nat -L KUBE-SERVICES --line-numbers -n | grep '%s' | grep 'dpt:50051' | head -1 | awk '{print $1}'", workerIP)
	posOutput, err := exec.Command("bash", "-c", posCmd).Output()
	if err == nil && len(posOutput) > 0 {
		fmt.Sscanf(strings.TrimSpace(string(posOutput)), "%d", &position)
	}

	// Get total rules count
	totalCmd := "sudo iptables -t nat -L KUBE-SERVICES --line-numbers -n | tail -n +3 | wc -l"
	totalOutput, err := exec.Command("bash", "-c", totalCmd).Output()
	if err == nil && len(totalOutput) > 0 {
		fmt.Sscanf(strings.TrimSpace(string(totalOutput)), "%d", &totalRules)
	}

	return position, totalRules
}

// ---------------- Full Experiment ----------------
func RunFullExperiment(config TestConfig) {
	serviceCounts := []int{100, 1000, 5000, 10000, 20000}
	projectRoot := "."

	fmt.Printf("\n=== Full Data Plane Experiment Suite ===\n")
	fmt.Printf("  Service counts: %v\n", serviceCounts)
	fmt.Printf("  RPS: %d\n", config.RPS)
	fmt.Printf("  Requests per test: %d\n", config.NumRequests)
	fmt.Printf("  Proxy mode: %s\n", config.ProxyMode)
	fmt.Printf("  Worker: %s\n", config.WorkerAddr)
	fmt.Printf("\n")

	// Create summary CSV
	timestamp := time.Now().Format("20060102_150405")
	os.MkdirAll("logs/dataplane", os.ModePerm)
	summaryFile := fmt.Sprintf("logs/dataplane/experiment_summary_%s.csv", timestamp)

	f, err := os.Create(summaryFile)
	if err != nil {
		log.Fatalf("Failed to create summary file: %v", err)
	}
	defer f.Close()

	// Write CSV header
	fmt.Fprintf(f, "ServiceCount,ProxyMode,WorkerPosition,TotalRules,NumRequests,SuccessRate,MeanLatency_us,P50_us,P95_us,P99_us,RTTMean_us,RTTP95_us,RTTP99_us,LogFile\n")
	fmt.Printf("Results will be saved to: %s\n\n", summaryFile)

	// Connect to worker once
	fmt.Printf("Connecting to worker at %s...\n", config.WorkerAddr)
	conn, err := grpc.Dial(config.WorkerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()
	client := pb.NewWorkerServiceClient(conn)
	fmt.Println("✓ Connected successfully\n")

	// Detect worker position in iptables chain
	fmt.Println("Detecting worker position in KUBE-SERVICES chain...")
	workerIP := strings.Split(config.WorkerAddr, ":")[0]
	workerPosition, totalRules := getWorkerPosition(workerIP, config.ProxyMode)

	if workerPosition > 0 && totalRules > 0 {
		if config.ProxyMode == "nftables" {
			fmt.Printf("✓ Worker position: %d / %d [nftables uses hash tables - position irrelevant]\n\n", workerPosition, totalRules)
		} else {
			fmt.Printf("✓ Worker position: %d / %d\n\n", workerPosition, totalRules)
		}
	} else {
		fmt.Println("⚠ Could not determine worker position in iptables chain\n")
		workerPosition = 0
		totalRules = 0
	}

	// Initial cleanup
	fmt.Println("=== Initial Cleanup ===")
	if err := deleteDummyServices(projectRoot); err != nil {
		fmt.Printf("Warning: cleanup failed: %v\n", err)
	}
	time.Sleep(30 * time.Second)
	fmt.Println("✓ Starting with clean state\n")

	currentServiceCount := 0

	// Main experiment loop
	for _, serviceCount := range serviceCounts {
		fmt.Printf("\n========================================\n")
		fmt.Printf("Testing with %d services\n", serviceCount)
		fmt.Printf("========================================\n\n")

		// Calculate services to add
		servicesToAdd := serviceCount - currentServiceCount

		if servicesToAdd > 0 {
			fmt.Printf("[1/3] Creating %d additional services (total: %d)...\n", servicesToAdd, serviceCount)
			startTime := time.Now()
			if err := createDummyServices(servicesToAdd, projectRoot); err != nil {
				fmt.Printf("ERROR: Failed to create services: %v\n", err)
				continue
			}
			createDuration := time.Since(startTime)
			fmt.Printf("✓ Created %d services in %s\n\n", servicesToAdd, createDuration.Round(time.Second))
			currentServiceCount = serviceCount

			fmt.Printf("[2/3] Waiting for kube-proxy sync (120s)...\n")
			waitForKubeproxySync(120)
			fmt.Println()
		} else {
			fmt.Printf("Already at %d services, skipping creation\n\n", serviceCount)
		}

		// Run test
		fmt.Printf("[3/3] Running load test...\n")
		testConfig := config
		testConfig.ServiceCount = serviceCount

		// Get current worker position
		workerIP := strings.Split(config.WorkerAddr, ":")[0]
		currentWorkerPosition, currentTotalRules := getWorkerPosition(workerIP, config.ProxyMode)
		if currentWorkerPosition > 0 {
			fmt.Printf("Worker position in rules: %d / %d\n", currentWorkerPosition, currentTotalRules)
		}

		// Run test and capture results
		results := runTestAndGetResults(client, testConfig, currentWorkerPosition, currentTotalRules)

		if len(results) == 0 {
			fmt.Printf("ERROR: No results for %d services\n", serviceCount)
			fmt.Fprintf(f, "%d,%s,%d,%d,%d,0.00,N/A,N/A,N/A,N/A,N/A,N/A,N/A,N/A\n",
				serviceCount, config.ProxyMode, currentWorkerPosition, currentTotalRules, config.NumRequests)
			continue
		}

		// Calculate statistics
		stats := calculateStatistics(results)
		successRate := float64(len(results)) / float64(config.NumRequests) * 100.0

		// Find the log file (most recent matching this service count)
		logPattern := fmt.Sprintf("logs/dataplane/PM_%s_SC_%d_*.log", config.ProxyMode, serviceCount)
		logFile := "N/A"
		if matches, _ := filepath.Glob(logPattern); len(matches) > 0 {
			logFile = matches[len(matches)-1]
		}

		// Write to summary CSV
		fmt.Fprintf(f, "%d,%s,%d,%d,%d,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%s\n",
			serviceCount, config.ProxyMode, currentWorkerPosition, currentTotalRules, config.NumRequests, successRate,
			stats.Mean, stats.P50, stats.P95, stats.P99,
			stats.RTTMean, stats.RTTP95, stats.RTTP99, logFile)

		// Quick summary
		fmt.Printf("\nQuick Summary:\n")
		fmt.Printf("  Service count: %d\n", serviceCount)
		fmt.Printf("  Worker position: %d / %d\n", currentWorkerPosition, currentTotalRules)
		fmt.Printf("  Success rate: %.2f%%\n", successRate)
		fmt.Printf("  Mean latency: %.2f µs\n", stats.Mean)
		fmt.Printf("  P95 latency: %.2f µs\n", stats.P95)
		fmt.Printf("  P99 latency: %.2f µs\n", stats.P99)

		// Pause between tests
		if serviceCount != serviceCounts[len(serviceCounts)-1] {
			fmt.Printf("\nPausing 30 seconds before next test...\n")
			time.Sleep(30 * time.Second)
		}
	}

	// Final cleanup
	fmt.Printf("\n========================================\n")
	fmt.Printf("ALL EXPERIMENTS COMPLETE\n")
	fmt.Printf("========================================\n\n")

	fmt.Println("Cleaning up dummy services...")
	if err := deleteDummyServices(projectRoot); err != nil {
		fmt.Printf("Warning: cleanup failed: %v\n", err)
	}

	fmt.Printf("\n✓ Full experiment suite finished!\n")
	fmt.Printf("\nResults saved to: %s\n", summaryFile)

	// Display summary table
	fmt.Println("\nResults Summary:")
	fmt.Println("=================")
	data, _ := os.ReadFile(summaryFile)
	fmt.Println(string(data))
}

func runTestAndGetResults(client pb.WorkerServiceClient, config TestConfig, workerPosition int, totalRules int) []requestResult {
	// Create individual test log/CSV
	timestamp := time.Now().Format("20060102_150405")
	runID := fmt.Sprintf("PM_%s_SC_%d_RPS_%d_%s",
		config.ProxyMode, config.ServiceCount, config.RPS, timestamp)

	logFile := fmt.Sprintf("logs/dataplane/%s.log", runID)
	csvFile := fmt.Sprintf("logs/dataplane/%s.csv", runID)

	f, err := os.Create(logFile)
	if err != nil {
		log.Printf("Failed to create log file: %v", err)
		return nil
	}
	defer f.Close()
	logger := log.New(f, "", log.LstdFlags)

	logger.Printf("Test Configuration: ProxyMode=%s, ServiceCount=%d, NumRequests=%d, RPS=%d",
		config.ProxyMode, config.ServiceCount, config.NumRequests, config.RPS)
	logger.Printf("Worker Position: %d / %d", workerPosition, totalRules)

	csvF, err := os.Create(csvFile)
	if err != nil {
		log.Printf("Failed to create CSV file: %v", err)
		return nil
	}
	defer csvF.Close()
	// CSV header with metadata as comments
	fmt.Fprintf(csvF, "# ProxyMode: %s\n", config.ProxyMode)
	fmt.Fprintf(csvF, "# ServiceCount: %d\n", config.ServiceCount)
	fmt.Fprintf(csvF, "# WorkerPosition: %d\n", workerPosition)
	fmt.Fprintf(csvF, "# TotalRules: %d\n", totalRules)
	fmt.Fprintf(csvF, "# RPS: %d\n", config.RPS)
	fmt.Fprintf(csvF, "# NumRequests: %d\n", config.NumRequests)
	fmt.Fprintf(csvF, "seq,rtt_us,data_plane_latency_us,worker_processing_us\n")

	var results []requestResult
	var resultsMutex sync.Mutex
	var wg sync.WaitGroup

	// Worker pool: create channel for request IDs
	requestChan := make(chan int, WorkerPoolSize*2)

	// Start worker pool
	for w := 0; w < WorkerPoolSize; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for seq := range requestChan {
				// Capture timestamp right before gRPC call (T2)
				sendNs := time.Now().UnixNano()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				resp, err := client.DoWork(ctx, &pb.WorkRequest{DurationMs: 0, WorkMode: "echo"})
				cancel()
				recvNs := time.Now().UnixNano()

				if err != nil {
					logger.Printf("Request %d failed: %v", seq, err)
					continue
				}

				rttUs := float64(recvNs-sendNs) / 1e3
				workerProcessingUs := float64(resp.WorkerProcessingNs) / 1e3
				networkLatencyUs := rttUs - workerProcessingUs
				dataPlaneLatencyUs := networkLatencyUs / 2.0

				result := requestResult{
					sequenceNum:        seq,
					rttUs:              rttUs,
					dataPlaneLatencyUs: dataPlaneLatencyUs,
					workerProcessingUs: workerProcessingUs,
				}

				resultsMutex.Lock()
				results = append(results, result)
				resultsMutex.Unlock()
			}
		}()
	}

	fmt.Printf("Sending %d requests at %d RPS...\n", config.NumRequests, config.RPS)
	testStart := time.Now()

	// Send request IDs to worker pool at controlled rate
	ticker := time.NewTicker(time.Second / time.Duration(config.RPS))
	defer ticker.Stop()

	for i := 0; i < config.NumRequests; i++ {
		<-ticker.C

		if (i+1)%100 == 0 {
			fmt.Printf("Sent %d/%d requests...\n", i+1, config.NumRequests)
		}

		requestChan <- i
	}

	// Close channel and wait for workers to finish
	close(requestChan)
	wg.Wait()
	testDuration := time.Since(testStart)

	fmt.Printf("Test completed in %s\n", testDuration.Round(time.Millisecond))
	fmt.Printf("Successful requests: %d/%d (%.2f%%)\n",
		len(results), config.NumRequests,
		float64(len(results))/float64(config.NumRequests)*100.0)

	// Write to CSV
	for _, r := range results {
		fmt.Fprintf(csvF, "%d,%.2f,%.2f,%.2f\n",
			r.sequenceNum, r.rttUs, r.dataPlaneLatencyUs, r.workerProcessingUs)
	}

	logger.Printf("Test completed. Duration=%s, SuccessfulRequests=%d/%d",
		testDuration, len(results), config.NumRequests)

	if len(results) > 0 {
		stats := calculateStatistics(results)
		logger.Printf("Data Plane Latency (µs): Mean=%.2f, P50=%.2f, P95=%.2f, P99=%.2f",
			stats.Mean, stats.P50, stats.P95, stats.P99)
	}

	return results
}

// ---------------- Main ----------------
func main() {
	fmt.Println("Data Plane Latency Test - gRPC Load Generator")

	workerAddr := flag.String("worker", "localhost:50051", "Worker gRPC host:port")
	rps := flag.Int("rps", 200, "Requests per second")
	numRequests := flag.Int("num-requests", 10000, "Total number of requests to send")
	proxyMode := flag.String("proxy-mode", "unknown", "Kube-proxy mode: iptables-nft or nftables")
	serviceCount := flag.Int("service-count", 1, "Number of services in cluster (single test mode)")
	fullExperiment := flag.Bool("full-experiment", false, "Run full experiment at multiple service counts (100/1k/5k/10k/20k)")
	flag.Parse()

	if *proxyMode == "unknown" {
		fmt.Println("WARNING: --proxy-mode not specified. Use --proxy-mode=iptables-nft or --proxy-mode=nftables")
	}

	config := TestConfig{
		WorkerAddr:     *workerAddr,
		RPS:            *rps,
		NumRequests:    *numRequests,
		ProxyMode:      *proxyMode,
		ServiceCount:   *serviceCount,
		FullExperiment: *fullExperiment,
	}

	if config.FullExperiment {
		// Full experiment mode - automated testing at multiple scales
		RunFullExperiment(config)
	} else {
		// Single test mode - original behavior
		fmt.Printf("\nConnecting to worker at %s...\n", config.WorkerAddr)
		conn, err := grpc.Dial(config.WorkerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatalf("Failed to connect: %v\nCheck DNS and network connectivity", err)
		}
		defer conn.Close()

		client := pb.NewWorkerServiceClient(conn)
		fmt.Println("✓ Connected successfully")
		fmt.Println("Ready to send requests...\n")

		RunDataPlaneTest(client, config)
		fmt.Println("\nTest complete!")
	}
}
