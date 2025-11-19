# ----------------------
# Stage 1: Build the binary
# ----------------------
FROM golang:1.25 AS builder

# Set the working directory
WORKDIR /app

# Copy go modules manifests first
COPY go.mod go.sum ./
RUN go mod download

# Then copy the source code
COPY . .

# Build the Go binary (worker only)
RUN go build -o /app/bin/worker ./worker

# ----------------------
# Stage 2: Ubuntu runtime
# ----------------------
FROM ubuntu:22.04

WORKDIR /root/

# --- CRITICAL FIX START ---
# Firecracker-containerd fails if the image has NO environment variables.
# We add a dummy variable to ensure the list is populated.
ENV WORKER_TYPE=firecracker
# --- CRITICAL FIX END ---

# Copy only the binary from the builder stage to root
COPY --from=builder /app/bin/worker .

# Expose gRPC port
EXPOSE 50051 9090

# Run the worker binary (Absolute path is safer for Firecracker)
CMD ["/root/worker"]
