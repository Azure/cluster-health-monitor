# Shared by the GPU tool builder and the runtime libraries copied into the gpu stage; the CUDA
# runtime image must match the one nccl-tests was linked against.
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

# Use distroless as minimal base image to package the clusterhealthmonitor binary
# Using distroless/base instead of distroless/minimal because it comes with SymCrypt and SymCrypt-OpenSSL which are required FIPS/Azure compliance
# Refer to https://mcr.microsoft.com/en-us/artifact/mar/azurelinux/distroless/base/about for more details
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0 AS runtime
WORKDIR /
COPY --from=builder /workspace/clusterhealthmonitor .
COPY --from=builder /workspace/controller .
COPY --from=builder /workspace/nodechecker .
USER 65532:65532

# TODO: remove the ENTRYPOINT since all component builds are in the same image now. This needs changes in existing deployment manifests as well.
ENTRYPOINT ["/clusterhealthmonitor"]

# ---------------------------------------------------------------------------------------------
# GPU image variant (--target gpu). Same Go binaries as the runtime stage, on the NVIDIA CUDA
# runtime base so the benchmarks get libcudart/libnccl/libstdc++ without hand-copying them.
# Kept separate from the default image because every node pulls that one, it must stay
# multi-arch, and this payload is neither FIPS-built nor scanned alongside the Go binaries.
# amd64 only.
# ---------------------------------------------------------------------------------------------

# NVIDIA publishes nccl-tests and nvbandwidth as source only, so they are built here.
FROM nvcr.io/nvidia/cuda:${CUDA_VERSION}-devel-ubuntu${UBUNTU_VERSION} AS gpu-tools-builder

# Both CUDA images ship NCCL pinned via apt holds. Do NOT install/upgrade libnccl here: doing so
# links nccl-tests against a newer NCCL than the runtime image provides, which fails at run time
# with "undefined symbol: ncclCommQueryProperties".
RUN apt-get update && apt-get install -y --no-install-recommends \
        git ca-certificates build-essential cmake libboost-program-options-dev \
    && rm -rf /var/lib/apt/lists/*

# Pinned for reproducible builds; bump deliberately.
ARG NCCL_TESTS_REF=v2.19.7
RUN git clone --depth 1 --branch ${NCCL_TESTS_REF} https://github.com/NVIDIA/nccl-tests.git /nccl-tests \
    && make -C /nccl-tests -j"$(nproc)"

ARG NVBANDWIDTH_REF=v0.10.0
# CMake cannot auto-detect an arch without a GPU present and falls back to a list including
# sm_100, which CUDA 12.6 cannot compile. Pin the architectures AKS GPU SKUs actually use
# (V100/T4/A100/A10/L40S/H100/H200).
ARG CUDA_ARCHS="70;75;80;86;89;90"
RUN git clone --depth 1 --branch ${NVBANDWIDTH_REF} https://github.com/NVIDIA/nvbandwidth.git /nvbandwidth \
    && cmake -S /nvbandwidth -B /nvbandwidth/build -DCMAKE_CUDA_ARCHITECTURES="${CUDA_ARCHS}" \
    && cmake --build /nvbandwidth/build -j"$(nproc)"

FROM nvcr.io/nvidia/cuda:${CUDA_VERSION}-runtime-ubuntu${UBUNTU_VERSION} AS gpu
WORKDIR /
COPY --from=builder /workspace/clusterhealthmonitor .
COPY --from=builder /workspace/controller .
COPY --from=builder /workspace/nodechecker .

# Only the all-reduce test is copied; the other nccl-tests binaries each carry fat CUDA kernels
# for every pinned arch.
COPY --from=gpu-tools-builder /nccl-tests/build/all_reduce_perf /usr/local/bin/all_reduce_perf
COPY --from=gpu-tools-builder /nvbandwidth/build/nvbandwidth /usr/local/bin/nvbandwidth
# Keep the NCCL redistribution terms with the image that carries the NCCL benchmark binary.
COPY licenses/LICENSE-NCCL.txt /licenses/LICENSE-NCCL.txt

USER 65532:65532
ENTRYPOINT ["/nodechecker"]
