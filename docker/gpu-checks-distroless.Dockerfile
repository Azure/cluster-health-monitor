# Experimental variant of gpu-checks.Dockerfile that ships nccl-tests + nvbandwidth on the
# same Azure Linux distroless base as the cluster-health-monitor image instead of the NVIDIA
# CUDA runtime image. The build stage is unchanged; only the runtime stage differs.
#
# The distroless base has glibc, ldconfig and libgcc_s but no shell, no libstdc++ and no CUDA
# runtime, so the pieces the binaries actually need are copied in explicitly. libcuda,
# libnvidia-ml and nvidia-smi are not copied: those come from the host driver, injected at run
# time by the NVIDIA container runtime.
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

# Source the redistributable runtime libraries from the CUDA runtime image so the versions match
# what the known-good image links against.
FROM nvcr.io/nvidia/cuda:${CUDA_VERSION}-runtime-ubuntu${UBUNTU_VERSION} AS cudart

FROM mcr.microsoft.com/azurelinux/distroless/base:3.0

# Azure Linux resolves libraries from /usr/lib, so the copied .so files need no extra config.
COPY --from=cudart /usr/local/cuda/targets/x86_64-linux/lib/libcudart.so.12* /usr/lib/
COPY --from=cudart /usr/lib/x86_64-linux-gnu/libnccl.so.2* /usr/lib/
COPY --from=cudart /usr/lib/x86_64-linux-gnu/libstdc++.so.6* /usr/lib/

# The entrypoint is a bash script and the base has no shell at all.
COPY --from=cudart /usr/bin/bash /usr/bin/bash
COPY --from=cudart /usr/lib/x86_64-linux-gnu/libtinfo.so.6* /usr/lib/

COPY --from=builder /nccl-tests/build/ /usr/local/bin/
COPY --from=builder /nvbandwidth/build/nvbandwidth /usr/local/bin/nvbandwidth
# No shell means no RUN chmod, so set the mode during the copy.
COPY --chmod=755 docker/run-gpu-checks.sh /usr/local/bin/run-gpu-checks.sh

# The CUDA base images set this for us; on a neutral base the container runtime would otherwise
# inject a minimal driver set without nvidia-smi or libnvidia-ml.
ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility
# Where the NVIDIA container runtime injects the host driver libraries.
ENV LD_LIBRARY_PATH=/usr/lib/x86_64-linux-gnu:/usr/lib64:/usr/lib

# The checks only need the GPUs handed to them by the device plugin, so run unprivileged.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/run-gpu-checks.sh"]
