package main

import (
	"context"
	"fmt"
	"io/ioutil"
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
	start := time.Now()
	log.Printf("[Worker] Received request: DurationMs=%d", req.DurationMs)
	fmt.Printf("[Worker CLI] Request received: DurationMs=%d\n", req.DurationMs)

	duration := time.Duration(req.DurationMs) * time.Millisecond
	end := time.Now().Add(duration)

	var count uint64
	val := 1.0

	// Channel to stop sampling goroutine
	stopCh := make(chan struct{})
	freqSamples := make([]int64, 0)
	sampleInterval := 200 * time.Millisecond

	// Start concurrent CPU frequency sampler
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
			}
		}
	}()

	// Busy spin loop
	for time.Now().Before(end) {
		for i := 0; i < 1000; i++ {
			val = val*1.0001 + 1.0
			val = val/1.0001 + 0.9999
			val = val*val - val/2 + 3.14159
			val = val / (val + 1.0)

			if val > 1e6 {
				val = math.Mod(val, 99999)
			}
			count++
		}
	}

	// Stop sampler
	close(stopCh)

	// Compute average freq
	var avgFreq int64
	if len(freqSamples) > 0 {
		var sum int64
		for _, f := range freqSamples {
			sum += f
		}
		avgFreq = sum / int64(len(freqSamples))
	}

	e2e := time.Since(start).Milliseconds()
	log.Printf("[Worker] Finished request: DurationMs=%d, E2ELatencyMs=%d, Iterations=%d, AvgCPUFreq=%d kHz",
		req.DurationMs, e2e, count, avgFreq)
	fmt.Printf("[Worker CLI] Request finished: DurationMs=%d, E2E=%d ms, Iterations=%d, AvgCPUFreq=%d kHz\n",
		req.DurationMs, e2e, count, avgFreq)

	return &pb.WorkResponse{
		Status:        "done",
		E2ELatencyMs:  e2e,
		AvgCpuFreqKhz: avgFreq,
	}, nil
}

// Helper: read current CPU frequency (core 0)
func getCPUFreq() (int64, error) {
	data, err := ioutil.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq")
	if err != nil {
		return 0, err
	}
	val := strings.TrimSpace(string(data))
	return strconv.ParseInt(val, 10, 64)
}

func main() {
	// Read port from environment variable (Knative sets port dynamically)
	port := os.Getenv("PORT")
	if port == "" { // local testing
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
