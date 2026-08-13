# GPU intrusive health check image: NVIDIA nccl-tests + nvbandwidth.
# Both tools are published by NVIDIA as source only, so they are built here on top of the
# official CUDA images and shipped as a slim runtime image.
ARG CUDA_VERSION=12.6.3
ARG UBUNTU_VERSION=22.04

FROM nvcr.io/nvidia/cuda:${CUDA_VERSION}-devel-ubuntu${UBUNTU_VERSION} AS builder

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
# (V100/T4/A100/A10/L40S/H100).
ARG CUDA_ARCHS="70;75;80;86;89;90"
RUN git clone --depth 1 --branch ${NVBANDWIDTH_REF} https://github.com/NVIDIA/nvbandwidth.git /nvbandwidth \
    && cmake -S /nvbandwidth -B /nvbandwidth/build -DCMAKE_CUDA_ARCHITECTURES="${CUDA_ARCHS}" \
    && cmake --build /nvbandwidth/build -j"$(nproc)"

FROM nvcr.io/nvidia/cuda:${CUDA_VERSION}-runtime-ubuntu${UBUNTU_VERSION}

# The CUDA runtime image already contains libnccl2; nothing further to install.
COPY --from=builder /nccl-tests/build/ /usr/local/bin/
COPY --from=builder /nvbandwidth/build/nvbandwidth /usr/local/bin/nvbandwidth
COPY docker/run-gpu-checks.sh /usr/local/bin/run-gpu-checks.sh
RUN chmod +x /usr/local/bin/run-gpu-checks.sh

# The checks only need the GPUs handed to them by the device plugin, so run unprivileged.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/run-gpu-checks.sh"]
