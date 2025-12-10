# Build the clusterhealthmonitor binary
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.24.9 AS builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY apis/ apis/

# Build
RUN CGO_ENABLED=1 GOEXPERIMENT=systemcrypto go build -o clusterhealthmonitor cmd/clusterhealthmonitor/main.go
RUN CGO_ENABLED=1 GOEXPERIMENT=systemcrypto go build -o controller cmd/controller/checknodehealth/main.go
RUN CGO_ENABLED=1 GOEXPERIMENT=systemcrypto go build -o nodechecker cmd/nodechecker/main.go

# Use distroless as minimal base image to package the clusterhealthmonitor binary
# Using distroless/base instead of distroless/minimal because it comes with SymCrypt and SymCrypt-OpenSSL which are required FIPS/Azure compliance
# Refer to https://mcr.microsoft.com/en-us/artifact/mar/azurelinux/distroless/base/about for more details
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0
WORKDIR /
COPY --from=builder /workspace/clusterhealthmonitor .
COPY --from=builder /workspace/controller .
COPY --from=builder /workspace/nodechecker .
USER 65532:65532

# TODO: remove the ENTRYPOINT since all component builds are in the same image now. This needs changes in existing deployment manifests as well.
ENTRYPOINT ["/clusterhealthmonitor"]
