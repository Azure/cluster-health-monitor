# Build the clusterhealthmonitor binary on the native runner architecture.
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.26.10 AS builder

ARG TARGETOS
ARG TARGETARCH

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
# According to https://github.com/microsoft/go/tree/microsoft/main/eng/doc/fips#usage-common-configurations
# CGO_ENABLED=0 GOEXPERIMENT=ms_nocgo_opensslcrypto is FIPS compliant with Go 1.26
RUN CGO_ENABLED=0 GOEXPERIMENT=ms_nocgo_opensslcrypto GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o clusterhealthmonitor cmd/clusterhealthmonitor/main.go
RUN CGO_ENABLED=0 GOEXPERIMENT=ms_nocgo_opensslcrypto GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o controller cmd/controller/checknodehealth/main.go
RUN CGO_ENABLED=0 GOEXPERIMENT=ms_nocgo_opensslcrypto GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o nodechecker cmd/nodechecker/main.go

# Use distroless as minimal base image to package the clusterhealthmonitor binary
# Using distroless/base instead of distroless/minimal because it comes with SymCrypt and SymCrypt-OpenSSL which are required FIPS/Azure compliance
# Refer to https://mcr.microsoft.com/en-us/artifact/mar/azurelinux/distroless/base/about for more details
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0 AS runtime-base

FROM --platform=$BUILDPLATFORM mcr.microsoft.com/azurelinux/base/core:3.0 AS runtime-rootfs
RUN tdnf install -y gawk
COPY --from=runtime-base / /rootfs/
# Preserve other CAs' OpenSSL trust attributes and regenerate every certificate-store format.
# AAA Certificate Services also signs the compromised intermediates (Mozilla bug 2083449).
RUN set -eu; \
    destination=/rootfs/etc/pki/ca-trust/extracted; \
    source_bundle="$destination/openssl/ca-bundle.trust.crt"; \
    aaa_root_sha256=d7a7a0fb5d7e2731d771e9484ebcdef71d5f0c3e0a2948782bc83ee0ea699ef4; \
    temporary="$(mktemp -d)"; \
    trap 'rm -rf "$temporary"' EXIT; \
    if ! awk -v directory="$temporary" ' \
        /^-----BEGIN TRUSTED CERTIFICATE-----$/ { \
            if (output != "") exit 1; output = sprintf("%s/%06d.pem", directory, ++count) \
        } \
        output != "" { print > output } \
        /^-----END TRUSTED CERTIFICATE-----$/ { \
            if (output == "") exit 1; close(output); output = "" \
        } \
        END { if (count == 0 || output != "") exit 1 } \
    ' "$source_bundle"; then \
        echo "Invalid or empty runtime OpenSSL CA bundle: $source_bundle" >&2; \
        exit 1; \
    fi; \
    find /etc/pki/ca-trust/source /usr/share/pki/ca-trust-source -type f -delete; \
    filtered=/etc/pki/ca-trust/source/filtered.trust.crt; \
    : > "$filtered"; \
    removed=0; retained=0; \
    for certificate in "$temporary"/*.pem; do \
        subject="$(openssl x509 -in "$certificate" -noout -subject -nameopt RFC2253)"; \
        subject="$(printf '%s' "$subject" | tr '[:upper:]' '[:lower:]')"; \
        fingerprint="$(openssl x509 -in "$certificate" -noout -fingerprint -sha256)"; \
        fingerprint="$(printf '%s' "${fingerprint#*=}" | tr -d ':' | tr '[:upper:]' '[:lower:]')"; \
        case "$subject:$fingerprint" in \
            *emudhra*|*emsign*|*:"$aaa_root_sha256") removed=$((removed + 1)) ;; \
            *) cat "$certificate" >> "$filtered"; retained=$((retained + 1)) ;; \
        esac; \
    done; \
    if [ "$retained" -eq 0 ]; then \
        echo "No CA certificates remain after filtering" >&2; \
        exit 1; \
    fi; \
    export P11_KIT_NO_USER_CONFIG=1; \
    cp "$filtered" "$destination/openssl/ca-bundle.trust.crt"; \
    trust extract --format=pem-bundle --filter=ca-anchors --overwrite --comment --purpose=server-auth \
        "$destination/pem/tls-ca-bundle.pem"; \
    trust extract --format=pem-bundle --filter=ca-anchors --overwrite --comment --purpose=email \
        "$destination/pem/email-ca-bundle.pem"; \
    trust extract --format=pem-bundle --filter=ca-anchors --overwrite --comment --purpose=code-signing \
        "$destination/pem/objsign-ca-bundle.pem"; \
    trust extract --format=java-cacerts --filter=ca-anchors --overwrite --purpose=server-auth \
        "$destination/java/cacerts"; \
    trust extract --format=edk2-cacerts --filter=ca-anchors --overwrite --purpose=server-auth \
        "$destination/edk2/cacerts.bin"; \
    cp "$destination/pem/tls-ca-bundle.pem" /rootfs/etc/pki/tls/cert.pem; \
    cp "$destination/java/cacerts" /rootfs/etc/pki/java/cacerts; \
    echo "Removed $removed eMudhra/emSign or AAA CA certificates; retained $retained other certificates"

# Copy only the sanitized filesystem so excluded certificates cannot survive in lower layers.
FROM scratch AS runtime
COPY --from=runtime-rootfs /rootfs/ /
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

FROM runtime
WORKDIR /
COPY --from=builder /workspace/clusterhealthmonitor .
COPY --from=builder /workspace/controller .
COPY --from=builder /workspace/nodechecker .
USER 65532:65532

# TODO: remove the ENTRYPOINT since all component builds are in the same image now. This needs changes in existing deployment manifests as well.
ENTRYPOINT ["/clusterhealthmonitor"]
