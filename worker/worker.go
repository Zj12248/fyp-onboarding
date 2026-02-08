package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	pb "fyp-onboarding/workerpb"

	"google.golang.org/grpc"
)

type server struct {
	pb.UnimplementedWorkerServiceServer
}

func (s *server) DoWork(ctx context.Context, req *pb.WorkRequest) (*pb.WorkResponse, error) {
	// Capture arrival timestamp immediately for data plane latency analysis
	arrivalTime := time.Now()
	arrivalNs := arrivalTime.UnixNano()

	log.Printf("[Worker] Request received: DurationMs=%d, WorkMode=%s, Timestamp=%s",
		req.DurationMs, req.WorkMode, arrivalTime.Format(time.RFC3339Nano))

	start := time.Now()
	duration := time.Duration(req.DurationMs) * time.Millisecond
	end := time.Now().Add(duration)

	var count int64
	val := 1.0

	// Capture timestamp before busy work
	preBusyTime := time.Now()
	preBusyNs := preBusyTime.UnixNano()

	// Determine work mode (default to "full" for backward compatibility)
	workMode := req.WorkMode
	if workMode == "" {
		workMode = "full"
	}

	stopCh := make(chan struct{})
	freqSamples := make([]int64, 0)
	sampleInterval := 100 * time.Millisecond // cpu sampling rate

	// Start CPU frequency sampler
	go func() {
		ticker := time.NewTicker(sampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if freq, err := getCPUFreq(); err == nil {
					freqSamples = append(freqSamples, freq)
				}
			case <-stopCh:
				return
			case <-ctx.Done(): // cancel if client disconnects
				return
			}
		}
	}()

	// Busy spin loop for requested duration (skip if echo mode)
	if workMode == "echo" {
		// Echo mode: No busy work, just timestamps
		log.Printf("[Worker] Echo mode - skipping busy work")
	} else {
		// Full mode: Complete CPU-intensive work
		for time.Now().Before(end) {
			val = val*1.0001 + 0.9999
			val = math.Sin(val) + math.Sqrt(val)
			val = math.Log(val+1.0) + math.Tan(val) + math.Exp(val)
			val = math.Atan(val) + math.Cosh(val) + math.Sinh(val)
			count++
			if val > 1e6 {
				val = math.Mod(val, 99999)
			}
		}
	}

	// Capture timestamp after busy work
	postBusyTime := time.Now()
	postBusyNs := postBusyTime.UnixNano()

	status := "done"

	close(stopCh)

	// Compute average CPU frequency
	var avgFreq int64
	if len(freqSamples) > 0 {
		var sum int64
		for _, f := range freqSamples {
			sum += f
		}
		avgFreq = sum / int64(len(freqSamples))
	}

	// Capture response timestamp
	responseTime := time.Now()
	responseNs := responseTime.UnixNano()

	e2e := time.Since(start).Milliseconds()
	workerProcessingNs := postBusyNs - preBusyNs
	workerProcessingMs := float64(workerProcessingNs) / 1e6
	totalLatencyNs := responseNs - arrivalNs
	totalLatencyMs := float64(totalLatencyNs) / 1e6

	log.Printf("[Worker] Finished request: WorkMode=%s, DurationMs=%d, E2ELatencyMs=%d, TotalLatency=%.3fms, WorkerProcessing=%.3fms, Iterations=%d, AvgCPUFreq=%d kHz, Status=%s",
		workMode, req.DurationMs, e2e, totalLatencyMs, workerProcessingMs, count, avgFreq, status)
	fmt.Printf("[Worker CLI] Request finished: WorkMode=%s, DurationMs=%d, E2E=%d ms, TotalLatency=%.3fms, Processing=%.3fms, Iterations=%d, AvgCPUFreq=%d kHz, Status=%s\n",
		workMode, req.DurationMs, e2e, totalLatencyMs, workerProcessingMs, count, avgFreq, status)

	// Return comprehensive response with high-precision timestamps
	return &pb.WorkResponse{
		Status:              status,
		E2ELatencyMs:        e2e,
		AvgCpuFreqKhz:       avgFreq,
		Iterations:          count,
		ArrivalTimestampNs:  arrivalNs,
		PreBusyTimestampNs:  preBusyNs,
		PostBusyTimestampNs: postBusyNs,
		ResponseTimestampNs: responseNs,
		WorkerProcessingNs:  workerProcessingNs,
	}, nil
}

func getCPUFreq() (int64, error) {
	const numCores = 20
	var sum int64
	var valid int64

	for i := range numCores {
		path := fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq/scaling_cur_freq", i)
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[Worker] Failed to read CPU%d freq: %v", i, err)
			continue
		}

		val := strings.TrimSpace(string(data))
		freq, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			log.Printf("[Worker] Failed to parse CPU%d freq (%q): %v", i, val, err)
			continue
		}

		sum += freq
		valid++
	}

	if valid == 0 {
		return 0, fmt.Errorf("no CPU frequencies could be read")
	}

	avg := sum / valid
	return avg, nil
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
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
