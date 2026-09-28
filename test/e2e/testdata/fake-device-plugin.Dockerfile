FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.26.6 AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /workspace
COPY test/e2e/testdata/fake-device-plugin/go.mod test/e2e/testdata/fake-device-plugin/go.sum ./
RUN go mod download
COPY test/e2e/testdata/fake-device-plugin/main.go ./main.go
RUN CGO_ENABLED=0 GOEXPERIMENT=ms_nocgo_opensslcrypto GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /fake-device-plugin ./main.go

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0
COPY --from=builder /fake-device-plugin /fake-device-plugin
USER 65532:65532
ENTRYPOINT ["/fake-device-plugin"]
