# Build the controller binary
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.24.6 AS builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/controller/ cmd/controller/
COPY pkg/ pkg/
COPY apis/ apis/

# Build
RUN CGO_ENABLED=1 GOEXPERIMENT=systemcrypto go build -o controller cmd/controller/main.go

# Use distroless as minimal base image to package the controller binary
# Using distroless/base instead of distroless/minimal because it comes with SymCrypt and SymCrypt-OpenSSL which are required FIPS/Azure compliance
# Refer to https://mcr.microsoft.com/en-us/artifact/mar/azurelinux/distroless/base/about for more details
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0
WORKDIR /
COPY --from=builder /workspace/controller .
USER 65532:65532

ENTRYPOINT ["/controller"]
