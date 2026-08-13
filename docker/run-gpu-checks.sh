#!/usr/bin/env bash
# Entrypoint for the GPU intrusive health check image. Runs the NCCL all-reduce and
# nvbandwidth benchmarks, wrapping each in delimiters the controller parses into sub-results.
# Each section is always emitted, even on failure, so one failing tool never hides the other.
set -uo pipefail

if [[ -z "${NGPUS:-}" ]]; then
    NGPUS=$(nvidia-smi -L 2>/dev/null | grep -c '^GPU')
fi
[[ "${NGPUS}" -lt 1 ]] && NGPUS=1
echo "=== detected GPUs: ${NGPUS} ==="

# Measure at a single large message size, as AzNHC does (check_nccl_allreduce ... 16G).
# Sweeping up from tiny sizes drags nccl-tests' reported average bus bandwidth far below the
# achievable peak, making it incomparable to the SKU threshold.
NCCL_ARGS=${NCCL_ARGS:-"-b 16G -e 16G -f 2 -g ${NGPUS}"}
# Mirrors AzNHC check_gpu_bw: host<->device PCIe plus device-to-device NVLink reads.
NVBW_ARGS=${NVBW_ARGS:-"-t host_to_device_memcpy_ce device_to_host_memcpy_ce device_to_device_memcpy_read_ce -i 10"}

run_section() {
    local name=$1
    shift
    echo "=== CHM CHECK: ${name} ==="
    "$@"
    local rc=$?
    echo "=== CHM CHECK END: ${name} rc=${rc} ==="
    return 0
}

# Split into arrays so each flag/value becomes its own argv entry; passing the raw
# string would hand the tool a single argument.
read -r -a nccl_args <<< "${NCCL_ARGS}"
read -r -a nvbw_args <<< "${NVBW_ARGS}"

run_section nccl_all_reduce /usr/local/bin/all_reduce_perf "${nccl_args[@]}"
run_section nvbandwidth /usr/local/bin/nvbandwidth "${nvbw_args[@]}"
