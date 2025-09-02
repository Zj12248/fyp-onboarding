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
# ! Best practice for docker thus no need to rebuild above layer. Layering optimisation.
COPY . .

# Build the Go binary (worker only)
RUN go build -o /app/bin/worker ./worker

# ----------------------
# Stage 2: Ubuntu runtime
# ----------------------
FROM ubuntu:22.04

WORKDIR /root/

# Install necessary libraries (e.g., ca-certificates)
# RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

# Copy only the binary from the builder stage to root
COPY --from=builder /app/bin/worker .

# Expose gRPC port
EXPOSE 50051

# Run the worker binary
CMD ["./worker"]
