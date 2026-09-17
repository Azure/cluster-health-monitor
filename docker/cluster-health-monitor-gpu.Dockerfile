# Builds the GPU checker image. Separate from the default image because it is amd64-only and
# carries CUDA libraries and benchmark binaries that non-GPU nodes should not pull.

ARG CUDA_VERSION=12.6.3
ARG UBUNTU_VERSION=22.04

# Only nodechecker runs in this image, so the other components are not built.
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
RUN CGO_ENABLED=0 GOEXPERIMENT=ms_nocgo_opensslcrypto GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o nodechecker cmd/nodechecker/main.go

# NVIDIA publishes nccl-tests and nvbandwidth as source only, so they are built here.
FROM nvcr.io/nvidia/cuda:${CUDA_VERSION}-devel-ubuntu${UBUNTU_VERSION} AS gpu-tools-builder

# Do not install NCCL. It is already in the cuda image, and the shipped version must match what the
# tools were built against.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git ca-certificates build-essential cmake libboost-program-options-dev wget bzip2 \
    && rm -rf /var/lib/apt/lists/*

# Needed to build nccl-tests with MPI, so the all-reduce runs one rank per GPU as AzNHC does.
# MPI bootstraps the test rather than transferring its data, so the RDMA transports are excluded.
ARG OPENMPI_VERSION=4.1.5
# Digest published upstream at https://www.open-mpi.org/software/ompi/v4.1/.
ARG OPENMPI_SHA256=a640986bc257389dd379886fdae6264c8cfa56bc98b71ce3ae3dfbd8ce61dbe3
RUN set -eu; \
    wget -q -O /tmp/openmpi.tar.bz2 \
        https://download.open-mpi.org/release/open-mpi/v4.1/openmpi-${OPENMPI_VERSION}.tar.bz2; \
    echo "${OPENMPI_SHA256}  /tmp/openmpi.tar.bz2" | sha256sum -c -; \
    tar -xjf /tmp/openmpi.tar.bz2 -C /tmp; \
    cd /tmp/openmpi-${OPENMPI_VERSION}; \
    ./configure --prefix=/opt/openmpi \
        --enable-mpirun-prefix-by-default \
        --disable-dlopen \
        --disable-mpi-fortran \
        --without-verbs --without-ucx --without-libfabric > /dev/null; \
    make -j"$(nproc)" > /dev/null; \
    make install > /dev/null; \
    rm -rf /tmp/openmpi-${OPENMPI_VERSION} /tmp/openmpi.tar.bz2; \
    rm -f /opt/openmpi/lib/*.a /opt/openmpi/lib/*.la; \
    strip /opt/openmpi/bin/* /opt/openmpi/lib/*.so* 2>/dev/null || true

# Both tools are built for this arch list so their GPU coverage cannot diverge. nvbandwidth also
# needs it because CMake cannot detect an arch without a GPU present and falls back to a list
# including sm_100, which CUDA 12.6 cannot compile. Covers V100/T4/A100/A10/L40S/H100/H200.
ARG CUDA_ARCHS="70;75;80;86;89;90"

# v2.19.7
ARG NCCL_TESTS_REF=1a65d7f0514b8da6a61ae235d1c5f38549478e29
RUN set -eu; \
    git clone -q https://github.com/NVIDIA/nccl-tests.git /nccl-tests; \
    git -C /nccl-tests checkout -q ${NCCL_TESTS_REF}; \
    gencode=""; \
    for arch in $(echo "${CUDA_ARCHS}" | tr ';' ' '); do \
        gencode="$gencode -gencode=arch=compute_$arch,code=sm_$arch"; \
    done; \
    make -C /nccl-tests -j"$(nproc)" MPI=1 MPI_HOME=/opt/openmpi NVCC_GENCODE="$gencode"

# v0.10.0
ARG NVBANDWIDTH_REF=82fc4e8c6afa0babb8687793678f615b3b8d793e
RUN git clone -q https://github.com/NVIDIA/nvbandwidth.git /nvbandwidth \
    && git -C /nvbandwidth checkout -q ${NVBANDWIDTH_REF} \
    && cmake -S /nvbandwidth -B /nvbandwidth/build -DCMAKE_CUDA_ARCHITECTURES="${CUDA_ARCHS}" \
    && cmake --build /nvbandwidth/build -j"$(nproc)"

ARG AZHPC_IMAGES_REF=master
RUN set -eu; \
    git init -q /tmp/azhpc-images; \
    git -C /tmp/azhpc-images remote add origin https://github.com/Azure/azhpc-images.git; \
    git -C /tmp/azhpc-images fetch -q --depth=1 origin ${AZHPC_IMAGES_REF}; \
    git -C /tmp/azhpc-images checkout -q FETCH_HEAD; \
    mkdir -p /topofiles; \
    cp /tmp/azhpc-images/topology/* /topofiles/; \
    rm -rf /tmp/azhpc-images

# Source the runtime libraries from the same CUDA release the tools were compiled against, so
# their versions cannot drift from each other.
FROM nvcr.io/nvidia/cuda:${CUDA_VERSION}-runtime-ubuntu${UBUNTU_VERSION} AS cudart

# Same distroless base as the default image, so this image keeps SymCrypt.
FROM mcr.microsoft.com/azurelinux/distroless/base:3.0
WORKDIR /

# CUDA runtime libraries. Azure Linux resolves libraries from /usr/lib, so the copied .so files
# need no extra config. libcuda, libnvidia-ml and nvidia-smi are intentionally absent because the
# NVIDIA container runtime injects them from the host driver.
#
# Not a glob: these are symlinks and COPY dereferences them, so libnccl.so.2* would ship it twice.
COPY --from=cudart /usr/local/cuda/targets/x86_64-linux/lib/libcudart.so.12 /usr/lib/
COPY --from=cudart /usr/lib/x86_64-linux-gnu/libnccl.so.2 /usr/lib/
COPY --from=cudart /usr/lib/x86_64-linux-gnu/libstdc++.so.6 /usr/lib/

COPY --from=gpu-tools-builder /nccl-tests/build/all_reduce_perf /usr/local/bin/all_reduce_perf
COPY --from=gpu-tools-builder /nvbandwidth/build/nvbandwidth /usr/local/bin/nvbandwidth
# Keep the NCCL redistribution terms with the image that carries the NCCL benchmark binary.
COPY licenses/LICENSE-NCCL.txt /licenses/LICENSE-NCCL.txt

# OpenMPI, copied as directories so the library symlinks survive instead of being dereferenced into
# duplicates.
COPY --from=gpu-tools-builder /opt/openmpi/bin/ /opt/openmpi/bin/
COPY --from=gpu-tools-builder /opt/openmpi/lib/ /opt/openmpi/lib/
COPY --from=gpu-tools-builder /opt/openmpi/share/openmpi/ /opt/openmpi/share/openmpi/

# Topology files for every SKU; the checker passes the one matching the node to NCCL.
COPY --from=gpu-tools-builder /topofiles/ /usr/local/share/topofiles/

COPY --from=builder /workspace/nodechecker /nodechecker

# `compute` is required for running CUDA kernels, and `utility` brings in nvidia-smi so we can 
# query GPU information.
ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility
# Where the NVIDIA container runtime injects the host driver libraries.
ENV LD_LIBRARY_PATH=/usr/lib/x86_64-linux-gnu:/usr/lib64:/usr/lib:/opt/openmpi/lib

USER 65532:65532
ENTRYPOINT ["/nodechecker"]
