# Multi-stage Dockerfile for KVM Cloud Hypervisor VM Controller & Workers
# Supports cross-platform execution on Linux, macOS, and Windows.

# -------------------------------------------------------------
# Stage 1: Build static binaries using pure Go
# -------------------------------------------------------------
FROM golang:alpine AS builder

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source tree
COPY src/ ./src/

# Statically compile controller and worker binaries (zero CGO dependencies)
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w" -o /out/controller ./src/cmd/controller && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w" -o /out/worker ./src/cmd/worker

# -------------------------------------------------------------
# Stage 2: Shared Alpine runtime base
# -------------------------------------------------------------
FROM alpine:latest AS runtime-base
RUN apk add --no-cache ca-certificates curl tzdata bash coreutils procps util-linux

# -------------------------------------------------------------
# Stage 3: Controller Container
# -------------------------------------------------------------
FROM runtime-base AS controller

WORKDIR /app
COPY --from=builder /out/controller /usr/local/bin/controller

ENV CONTROLLER_ADDR="0.0.0.0:8080" \
    CONTROLLER_DATA_DIR="/data" \
    CONTROLLER_TOKEN="secret-controller-key"

EXPOSE 8080
VOLUME ["/data"]

HEALTHCHECK --interval=5s --timeout=3s --retries=5 --start-period=3s \
  CMD curl -f http://localhost:8080/health || exit 1

ENTRYPOINT ["/usr/local/bin/controller"]

# -------------------------------------------------------------
# Stage 4: Worker Container
# -------------------------------------------------------------
FROM runtime-base AS worker
ARG TARGETARCH

# Install Cloud Hypervisor v54.0 static binary for amd64 or aarch64
RUN case "${TARGETARCH}" in \
      arm64|aarch64) CH_URL="https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v54.0/cloud-hypervisor-static-aarch64" ;; \
      *) CH_URL="https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v54.0/cloud-hypervisor-static" ;; \
    esac && \
    (curl -fsSL "${CH_URL}" -o /usr/local/bin/cloud-hypervisor && chmod +x /usr/local/bin/cloud-hypervisor) || \
    echo "Warning: Cloud Hypervisor static binary could not be fetched. Will use Mock driver if /dev/kvm is missing."

WORKDIR /app
COPY --from=builder /out/worker /usr/local/bin/worker

ENV WORKER_ADDR="0.0.0.0:8081" \
    DATA_DIR="/data" \
    HYPERVISOR_DRIVER="auto" \
    CH_BIN="/usr/local/bin/cloud-hypervisor"

EXPOSE 8081 8082 2201-2220 2301-2320
VOLUME ["/data"]

HEALTHCHECK --interval=5s --timeout=3s --retries=5 --start-period=3s \
  CMD curl -f "http://localhost:${WORKER_ADDR##*:}/health" || exit 1

ENTRYPOINT ["/usr/local/bin/worker"]
