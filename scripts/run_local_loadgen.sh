#!/usr/bin/env bash
set -euo pipefail
go run ./cmd/loadgen --target=127.0.0.1:50051 --rps=100 --work_ms=50 --warmup_s=2 --duration_s=20 --concurrency=100 --timeout_ms=2000 --log_path=./results.csv
