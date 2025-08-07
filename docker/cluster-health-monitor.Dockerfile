# Build the clusterhealthmonitor binary
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.24.4 AS builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/clusterhealthmonitor/ cmd/clusterhealthmonitor/
COPY pkg/ pkg/

# Install build dependencies for cross-compilation
RUN apt-get update && apt-get install -y \
    gcc \
    gcc-aarch64-linux-gnu \
    libssl-dev \
    libssl-dev:arm64 \
    pkg-config

# Build
ARG TARGETPLATFORM
RUN case "$TARGETPLATFORM" in \
    "linux/arm64") \
        CC=aarch64-linux-gnu-gcc \
        PKG_CONFIG_PATH=/usr/lib/aarch64-linux-gnu/pkgconfig \
        CGO_ENABLED=1 \
        GOOS=linux \
        GOARCH=arm64 \
        GOEXPERIMENT=systemcrypto \
        go build -o clusterhealthmonitor cmd/clusterhealthmonitor/main.go \
        ;; \
    "linux/amd64"|*) \
        CC=gcc \
        CGO_ENABLED=1 \
        GOOS=linux \
        GOARCH=amd64 \
        GOEXPERIMENT=systemcrypto \
        go build -o clusterhealthmonitor cmd/clusterhealthmonitor/main.go \
        ;; \
    esac

# Use distroless as minimal base image to package the clusterhealthmonitor binary
# Using distroless/base instead of distroless/minimal because it comes with SymCrypt and SymCrypt-OpenSSL which are required FIPS/Azure compliance
# Refer to https://mcr.microsoft.com/en-us/artifact/mar/azurelinux/distroless/base/about for more details
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0
WORKDIR /
COPY --from=builder /workspace/clusterhealthmonitor .
USER 65532:65532

ENTRYPOINT ["/clusterhealthmonitor"]
