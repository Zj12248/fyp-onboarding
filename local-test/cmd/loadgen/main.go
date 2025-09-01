package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "cloudlab-single-node/proto"
)

type result struct {
	id                 string
	clientSend         time.Time
	clientRecv         time.Time
	serverRecvUnixNano int64
	serverSendUnixNano int64
	errStr             string
}

func main() {
	target := flag.String("target", "worker-svc:50051", "host:port of worker")
	rps := flag.Float64("rps", 50, "fixed requests per second")
	workMS := flag.Int("work_ms", 50, "CPU spin time on server (ms)")
	warmupS := flag.Int("warmup_s", 2, "warmup seconds (not logged)")
	durS := flag.Int("duration_s", 30, "test duration seconds (after warmup)")
	concurrency := flag.Int("concurrency", 50, "max in-flight RPCs")
	logPath := flag.String("log_path", "/logs/results.csv", "CSV log path")
	timeoutMS := flag.Int("timeout_ms", 2000, "per-RPC deadline in ms")
	flag.Parse()

	rand.Seed(time.Now().UnixNano())

	// Dial
	conn, err := grpc.Dial(*target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("dial %s: %v", *target, err)
	}
	defer conn.Close()
	client := pb.NewWorkerClient(conn)

	// Prepare logging
	if err := os.MkdirAll(filepath.Dir(*logPath), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(*logPath)
	if err != nil {
		log.Fatalf("create log: %v", err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{
		"id","client_send_unix_nano","client_recv_unix_nano",
		"e2e_ms","server_recv_unix_nano","server_send_unix_nano",
		"server_proc_ms","status",
	})

	log.Printf("target=%s rps=%.2f work_ms=%d warmup=%ds duration=%ds concurrency=%d timeout_ms=%d",
		*target, *rps, *workMS, *warmupS, *durS, *concurrency, *timeoutMS)

	// Rate limiter ticker
	interval := time.Duration(float64(time.Second) / *rps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Concurrency limiter
	sem := make(chan struct{}, *concurrency)
	var mu sync.Mutex
	var results []result

	start := time.Now()
	testStart := start.Add(time.Duration(*warmupS) * time.Second)
	testEnd := testStart.Add(time.Duration(*durS) * time.Second)

	for {
		now := time.Now()
		if now.After(testEnd) {
			break
		}
		<-ticker.C
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			id := fmt.Sprintf("%d-%d", now.UnixNano(), rand.Int63())

			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutMS)*time.Millisecond)
			defer cancel()

			sendTS := time.Now()
			resp, err := client.Invoke(ctx, &pb.InvokeRequest{
				Id:                  id,
				WorkMs:              int64(*workMS),
				ClientSendUnixNano:  sendTS.UnixNano(),
			})
			recvTS := time.Now()

			r := result{id: id, clientSend: sendTS, clientRecv: recvTS}
			if err != nil {
				r.errStr = err.Error()
			} else {
				r.serverRecvUnixNano = resp.GetServerRecvUnixNano()
				r.serverSendUnixNano = resp.GetServerSendUnixNano()
			}

			// Only log after warmup
			if sendTS.After(testStart) {
				e2eMs := float64(r.clientRecv.Sub(r.clientSend).Microseconds()) / 1000.0
				var serverProcMs float64
				if r.serverSendUnixNano > 0 && r.serverRecvUnixNano > 0 {
					serverProcMs = float64(r.serverSendUnixNano-r.serverRecvUnixNano) / 1e6
				}
				status := "ok"
				if r.errStr != "" {
					status = r.errStr
				}
				_ = w.Write([]string{
					r.id,
					fmt.Sprintf("%d", r.clientSend.UnixNano()),
					fmt.Sprintf("%d", r.clientRecv.UnixNano()),
					fmt.Sprintf("%.3f", e2eMs),
					fmt.Sprintf("%d", r.serverRecvUnixNano),
					fmt.Sprintf("%d", r.serverSendUnixNano),
					fmt.Sprintf("%.3f", serverProcMs),
					status,
				})
			}

			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}()
	}

	// Drain in-flight requests
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}

	// Summaries
	var latencies []float64
	var okCount, errCount int
	for _, r := range results {
		if r.clientSend.Before(testStart) { // exclude warmup
			continue
		}
		if r.errStr != "" {
			errCount++
			continue
		}
		okCount++
		lat := float64(r.clientRecv.Sub(r.clientSend).Microseconds()) / 1000.0
		latencies = append(latencies, lat)
	}
	summary(latencies, okCount, errCount, *rps, *workMS, *durS, testStart, testEnd, *logPath)
}

func summary(lats []float64, ok, err int, rps float64, workMS, durS int, start, end time.Time, logPath string) {
	fmt.Println("========== SUMMARY ==========")
	fmt.Printf("Window: %s .. %s (%ds)\n", start.Format(time.RFC3339), end.Format(time.RFC3339), durS)
	fmt.Printf("Target RPS: %.2f   Work(ms): %d\n", rps, workMS)
	fmt.Printf("Requests: %d OK, %d ERR\n", ok, err)
	if len(lats) == 0 {
		fmt.Println("No successful requests to summarize.")
		fmt.Printf("CSV: %s\n", logPath)
		return
	}
	// Sort
	for i := 1; i < len(lats); i++ {
		j := i
		for j > 0 && lats[j-1] > lats[j] {
			lats[j-1], lats[j] = lats[j], lats[j-1]
			j--
		}
	}
	p := func(q float64) float64 {
		if len(lats) == 0 {
			return math.NaN()
		}
		idx := int(math.Ceil(q*float64(len(lats)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(lats) {
			idx = len(lats) - 1
		}
		return lats[idx]
	}
	mean := 0.0
	for _, v := range lats {
		mean += v
	}
	mean /= float64(len(lats))

	fmt.Printf("E2E latency ms: p50=%.2f  p90=%.2f  p95=%.2f  p99=%.2f  max=%.2f  avg=%.2f\n",
		p(0.50), p(0.90), p(0.95), p(0.99), lats[len(lats)-1], mean)
	fmt.Printf("CSV log: %s (columns: id,client_send_unix_nano,client_recv_unix_nano,e2e_ms,server_recv_unix_nano,server_send_unix_nano,server_proc_ms,status)\n", logPath)
}
