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
	"sync"
	"time"

	pb "fyp-onboarding/workerpb"

	"google.golang.org/grpc"
)

// global mutex to enforce concurrency = 1
var mu sync.Mutex

// track active requests (should always be <= 1)
var activeRequests int32
var activeMu sync.Mutex

type server struct {
	pb.UnimplementedWorkerServiceServer
}

func (s *server) DoWork(ctx context.Context, req *pb.WorkRequest) (*pb.WorkResponse, error) {
	log.Printf("[Worker] Request received: DurationMs=%d, Timestamp=%s", req.DurationMs, time.Now().Format(time.RFC3339Nano))

	// Track waiting for lock
	log.Println("[Worker] Waiting for mutex lock...")
	mu.Lock()
	defer mu.Unlock()
	log.Println("[Worker] Acquired mutex lock!")

	// Increment active counter
	activeMu.Lock()
	activeRequests++
	if activeRequests > 1 {
		log.Printf("[ERROR] Concurrency >1! activeRequests=%d", activeRequests)
	} else {
		log.Printf("[Worker] Active requests=%d", activeRequests)
	}
	activeMu.Unlock()

	start := time.Now()
	duration := time.Duration(req.DurationMs) * time.Millisecond
	end := time.Now().Add(duration)

	timeout := duration * 5
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var count uint64
	val := 1.0

	stopCh := make(chan struct{})
	freqSamples := make([]int64, 0)
	sampleInterval := 100 * time.Millisecond

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
			case <-ctx.Done():
				return
			}
		}
	}()

	status := "done"

loop:
	for {
		select {
		case <-ctx.Done():
			status = "timeout"
			break loop
		default:
			if time.Now().After(end) {
				break loop
			}
			val = val*1.0001 + 0.9999
			val = math.Sin(val) + math.Sqrt(val)
			val = math.Log(val + 1.0)
			count++
			if val > 1e6 {
				val = math.Mod(val, 99999)
			}
		}
	}

	close(stopCh)

	var avgFreq int64
	if len(freqSamples) > 0 {
		var sum int64
		for _, f := range freqSamples {
			sum += f
		}
		avgFreq = sum / int64(len(freqSamples))
	}

	e2e := time.Since(start).Milliseconds()

	log.Printf("[Worker] Finished request: DurationMs=%d, E2ELatencyMs=%d, Iterations=%d, AvgCPUFreq=%d kHz, Status=%s",
		req.DurationMs, e2e, count, avgFreq, status)
	fmt.Printf("[Worker CLI] Request finished: DurationMs=%d, E2E=%d ms, Iterations=%d, AvgCPUFreq=%d kHz, Status=%s\n",
		req.DurationMs, e2e, count, avgFreq, status)

	// Decrement active counter
	activeMu.Lock()
	activeRequests--
	activeMu.Unlock()
	log.Println("[Worker] Released mutex lock!")

	return &pb.WorkResponse{
		Status:        status,
		E2ELatencyMs:  e2e,
		AvgCpuFreqKhz: avgFreq,
	}, nil
}

// Helper: read current CPU frequency (core 0)
func getCPUFreq() (int64, error) {
	data, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq")
	if err != nil {
		return 0, err
	}
	val := strings.TrimSpace(string(data))
	return strconv.ParseInt(val, 10, 64)
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
