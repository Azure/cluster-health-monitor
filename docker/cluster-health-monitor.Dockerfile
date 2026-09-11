# This file defines two shippable images:
#   --target default  the image every node pulls
#   --target gpu      adds the CUDA payload and GPU benchmarks, amd64 only
# Docker builds the last stage in the file when no --target is given, so `default` is defined
# last to keep that as the fallback.

ARG CUDA_VERSION=12.6.3
ARG UBUNTU_VERSION=22.04

# Build the clusterhealthmonitor binary on the native runner architecture.
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.26.6 AS builder

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

# ---------------------------------------------------------------------------------------------
# GPU image variant (--target gpu). Same distroless base as the default image, so it keeps
# SymCrypt. Separate because it is amd64-only and carries CUDA libraries and benchmark binaries
# that non-GPU nodes should not pull.
# ---------------------------------------------------------------------------------------------

# NVIDIA publishes nccl-tests and nvbandwidth as source only, so they are built here.
FROM nvcr.io/nvidia/cuda:${CUDA_VERSION}-devel-ubuntu${UBUNTU_VERSION} AS gpu-tools-builder

# Do not install NCCL. It is already in the cuda image, and the shipped version must match what the
# tools were built against.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git ca-certificates build-essential cmake libboost-program-options-dev \
    && rm -rf /var/lib/apt/lists/*

ARG NCCL_TESTS_REF=v2.19.7
RUN git clone --depth 1 --branch ${NCCL_TESTS_REF} https://github.com/NVIDIA/nccl-tests.git /nccl-tests \
    && make -C /nccl-tests -j"$(nproc)"

ARG NVBANDWIDTH_REF=v0.10.0
# nvbandwidth only: CMake cannot detect an arch without a GPU present and falls back to a list
# including sm_100, which CUDA 12.6 cannot compile. Covers V100/T4/A100/A10/L40S/H100/H200.
# nccl-tests ignores this and uses its own defaults, sm_60/61/70/80/90, so no T4, A10 or L40S.
ARG CUDA_ARCHS="70;75;80;86;89;90"
RUN git clone --depth 1 --branch ${NVBANDWIDTH_REF} https://github.com/NVIDIA/nvbandwidth.git /nvbandwidth \
    && cmake -S /nvbandwidth -B /nvbandwidth/build -DCMAKE_CUDA_ARCHITECTURES="${CUDA_ARCHS}" \
    && cmake --build /nvbandwidth/build -j"$(nproc)"

# Source the runtime libraries from the same CUDA release the tools were compiled against, so
# their versions cannot drift from each other.
FROM nvcr.io/nvidia/cuda:${CUDA_VERSION}-runtime-ubuntu${UBUNTU_VERSION} AS cudart

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0 AS gpu
WORKDIR /

# Copy required libraries. Azure Linux resolves libraries from /usr/lib, so the copied .so 
# files need no extra config. libcuda, libnvidia-ml and nvidia-smi are intentionally absent
# because the NVIDIA container runtime injects them from the host driver.
#
# Not a glob: these are symlinks and COPY dereferences them, so libnccl.so.2* would ship it twice.
COPY --from=cudart /usr/local/cuda/targets/x86_64-linux/lib/libcudart.so.12 /usr/lib/
COPY --from=cudart /usr/lib/x86_64-linux-gnu/libnccl.so.2 /usr/lib/
COPY --from=cudart /usr/lib/x86_64-linux-gnu/libstdc++.so.6 /usr/lib/

# Copy the binaries into the final image
COPY --from=gpu-tools-builder /nccl-tests/build/all_reduce_perf /usr/local/bin/all_reduce_perf
COPY --from=gpu-tools-builder /nvbandwidth/build/nvbandwidth /usr/local/bin/nvbandwidth
 
COPY --from=builder /workspace/nodechecker .

# `compute` is required for running CUDA kernels, and `utility` brings in nvidia-smi so we can 
# query GPU information.
ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility
# Where the NVIDIA container runtime injects the host driver libraries.
ENV LD_LIBRARY_PATH=/usr/lib/x86_64-linux-gnu:/usr/lib64:/usr/lib

USER 65532:65532
ENTRYPOINT ["/nodechecker"]

# ---------------------------------------------------------------------------------------------
# Default image variant (--target default).
# ---------------------------------------------------------------------------------------------

# Use distroless as minimal base image to package the clusterhealthmonitor binary. Using distroless/base 
# instead of distroless/minimal because it comes with SymCrypt and SymCrypt-OpenSSL which are required for 
# FIPS/Azure compliance. Refer to https://mcr.microsoft.com/en-us/artifact/mar/azurelinux/distroless/base/about 
# for more details.
#
# Kept as last stage on purpose so that a build with no `--target` defaults to this instead of the GPU image.
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0 AS default
WORKDIR /
COPY --from=builder /workspace/clusterhealthmonitor .
COPY --from=builder /workspace/controller .
COPY --from=builder /workspace/nodechecker .
USER 65532:65532

# TODO: remove the ENTRYPOINT since all component builds are in the same image now. This needs changes in existing deployment manifests as well.
ENTRYPOINT ["/clusterhealthmonitor"]
