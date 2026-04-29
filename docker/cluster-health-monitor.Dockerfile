# Build the clusterhealthmonitor binary
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.26.2 AS builder

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
RUN go build -o clusterhealthmonitor cmd/clusterhealthmonitor/main.go
RUN go build -o controller cmd/controller/checknodehealth/main.go
RUN go build -o nodechecker cmd/nodechecker/main.go

# Patch the distroless base image with updated openssl packages
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0 AS distroless-base
FROM mcr.microsoft.com/azurelinux/base/core:3.0 AS patcher
RUN tdnf update -y openssl openssl-libs && tdnf clean all
# Generate updated RPM manifest from distroless base with patched openssl version
COPY --from=distroless-base /var/lib/rpmmanifest/ /var/lib/rpmmanifest/
RUN OPENSSL_VERSION=$(rpm -q openssl --qf '%{VERSION}-%{RELEASE}') && \
    sed -i "s/^openssl\t[^\t]*/openssl\t${OPENSSL_VERSION}/" /var/lib/rpmmanifest/container-manifest-2 && \
    sed -i "s/^openssl-libs\t[^\t]*/openssl-libs\t${OPENSSL_VERSION}/" /var/lib/rpmmanifest/container-manifest-2 && \
    sed -i "s/openssl-3\.3\.5-4\.azl3/openssl-${OPENSSL_VERSION}/g" /var/lib/rpmmanifest/container-manifest-2 && \
    sed -i "s/openssl-3\.3\.5-4\.azl3/openssl-${OPENSSL_VERSION}/g" /var/lib/rpmmanifest/container-manifest-1

# Use distroless as minimal base image to package the clusterhealthmonitor binary
# Using distroless/base instead of distroless/minimal because it comes with SymCrypt and SymCrypt-OpenSSL which are required FIPS/Azure compliance
# Refer to https://mcr.microsoft.com/en-us/artifact/mar/azurelinux/distroless/base/about for more details
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0
COPY --from=patcher /usr/lib64/libssl.so* /usr/lib64/
COPY --from=patcher /usr/lib64/libcrypto.so* /usr/lib64/
COPY --from=patcher /usr/lib64/engines-3/ /usr/lib64/engines-3/
COPY --from=patcher /usr/lib64/ossl-modules/ /usr/lib64/ossl-modules/
COPY --from=patcher /var/lib/rpmmanifest/ /var/lib/rpmmanifest/
WORKDIR /
COPY --from=builder /workspace/clusterhealthmonitor .
COPY --from=builder /workspace/controller .
COPY --from=builder /workspace/nodechecker .
USER 65532:65532

# TODO: remove the ENTRYPOINT since all component builds are in the same image now. This needs changes in existing deployment manifests as well.
ENTRYPOINT ["/clusterhealthmonitor"]
