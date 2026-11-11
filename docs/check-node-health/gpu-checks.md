# GPU checks

Check Node Health can optionally run intrusive NVIDIA GPU checks on explicitly supported AKS GPU SKUs.

> [!WARNING]
> GPU checks claim all GPUs advertised as allocatable on the target node and run bandwidth and collective-compute benchmarks. Workloads that need those GPUs can prevent the checker pod from scheduling, and the checker pod can prevent other GPU workloads from scheduling while it runs.

GPU checks are in preview. They are disabled by default in the controller binary, but the supplied base Deployment explicitly enables them and configures the GPU image. To disable them, remove `-enable-gpu-checks` from the controller arguments in a Kustomize overlay.

## Current capabilities and limitations

Each request targets one node, and the controller selects checks automatically from its SKU profile. The current API does not support explicit check selection, recurring or idle-triggered GPU policies, coordinated multi-node or InfiniBand tests, or workload init-container preflight checks. The shared preflight described below is internal to the GPU benchmarks, not a workload admission or startup feature.

GPU results do not publish a dedicated Node condition or health label and do not initiate remediation. API design proposals for these capabilities are not supported configuration.

## Enable GPU checks

The controller requires:

```text
--enable-gpu-checks
GPU_CHECKER_POD_IMAGE=<gpu-image>
```

The GPU image is separate from the standard image because it includes CUDA runtime libraries, OpenMPI, `nvbandwidth`, `nccl-tests`, and topology data.

`CHECKER_POD_IMAGE` is still used for standard nodes.

The GPU image is currently built for `linux/amd64`.

### Upgrade-request exemption

A `CheckNodeHealth` whose controller owner reference is a `health.aks.io/v1alpha1` `HealthSignal` runs only `PodStartup` and `PodNetwork`, even when GPU checks are enabled. This includes requests created by the `HealthCheckRequest` integration.

These runs use `CHECKER_POD_IMAGE`, the two-minute base pod timeout, no GPU resource claim, and no GPU startup wait. GPU checks are not expected and are not added as missing Unknown results. The node is still classified as a GPU node for metrics and Node-condition handling.

This keeps experimental benchmarks out of upgrade health checks, but does not guarantee completion within an external upgrade deadline. Manual and reboot-triggered requests without this controller owner remain eligible for GPU checks.

## GPU node detection

A node is considered an NVIDIA GPU node when either:

- It advertises allocatable `nvidia.com/gpu` resources.
- It has `kubernetes.azure.com/accelerator=nvidia`.

AMD GPU nodes are not supported.

The controller reads the VM size from:

```text
node.kubernetes.io/instance-type
```

It normalizes the value to lowercase and removes a leading `Standard_` before matching a profile.

## Supported SKUs

The source profile table is authoritative. The current profiles are:

| AKS VM size | Expected GPUs | NCCL bus threshold | Host bandwidth threshold | Peer bandwidth threshold | Applicable checks |
|---|---:|---:|---:|---:|---|
| `Standard_NV72ads_A10_v5` | 2 | 1.5 GB/s | 10.0 GB/s | Not configured | GPU count, host bandwidth, NCCL all-reduce |
| `Standard_NC40ads_H100_v5` | 1 | Not applicable | 48.0 GB/s | Not applicable | GPU count, host bandwidth |
| `Standard_NC80adis_H100_v5` | 2 | 208.0 GB/s | 48.0 GB/s | 335.0 GB/s | GPU count, host bandwidth, peer bandwidth, NCCL all-reduce |
| `Standard_ND96isr_H100_v5` | 8 | 460.0 GB/s | 48.0 GB/s | 335.0 GB/s | GPU count, host bandwidth, peer bandwidth, NCCL all-reduce |

The NC H100 profiles use the thresholds from the upstream [NC40 configuration](https://github.com/Azure/azurehpc-health-checks/blob/main/conf/nc40ads_h100_v5.conf) and [NC80 configuration](https://github.com/Azure/azurehpc-health-checks/blob/main/conf/nc80adis_h100_v5.conf). NC80 uses 16G NCCL messages without a topology override. NC40 omits peer bandwidth and NCCL all-reduce because it has only one GPU.

Thresholds, supported SKUs, and benchmark settings are preview contracts and may change between versions.

An unprofiled SKU runs no benchmark to a health verdict. Its expected GPU results are Unknown with `UnknownSKU`.

## Startup and resource selection

When GPU checks are enabled for a detected GPU node and the request is not exempt:

1. The controller waits up to `--gpu-wait` from request creation for both the `node.kubernetes.io/instance-type` label and claimable GPUs. The default is two minutes.
2. It polls the Node every 30 seconds during this wait.
3. Once both prerequisites are available, the checker pod requests and limits exactly the full allocatable `nvidia.com/gpu` count.
4. The pod is pinned directly to the target node.
5. The GPU run receives a 15-minute pod timeout.

Cloud metadata and NVIDIA device-plugin registration can arrive independently, even after the node becomes Ready. No checker pod is created during the wait. The wait budget is shared and does not restart when one prerequisite arrives.

At the deadline:

- If the SKU label is missing, the pod claims no GPUs and all GPU checkers report Unknown with `NodeSKUNotAvailable`. No GPU tools run.
- If the SKU is present but no GPUs are claimable, the pod claims no GPUs and every applicable GPU checker reports Unknown with `GPUsNotClaimable`.
- A non-empty but unsupported SKU still reports `UnknownSKU`; it is not missing metadata.

Base pod checks still run in these diagnostic pods. An Unhealthy base result takes precedence over Unknown GPU results in the overall verdict. `--gpu-wait=0` reports missing prerequisites immediately without waiting.

The run's node kind and GPU SKU are recorded on the checker pod. Later Node metadata changes do not change an already-started run's expected checks or condition handling.

## Shared preflight

Every GPU benchmark performs preflight checks before running its tool:

1. Report `NodeSKUNotAvailable` if SKU metadata is empty.
2. Confirm that the SKU has a profile and the benchmark applies to it.
3. Honor a controller-provided skip reason such as `GPUsNotClaimable`.
4. Run `nvidia-smi -L`.
5. Compare the detected count with the SKU profile.

If the count differs, the result is Unhealthy with `UnexpectedGPUCount`.

If `nvidia-smi` cannot produce a trustworthy count, the result is Unknown with `ToolFailed`.

## `GpuHostBandwidth`

This checker runs `nvbandwidth` for:

- Host-to-device copies
- Device-to-host copies

It finds the minimum reported bandwidth across GPUs and both directions. Any value below the SKU's PCIe threshold produces Unhealthy with `BandwidthBelowThreshold`.

Missing, malformed, or incomplete measurements produce Unknown with `ToolFailed`.

Bandwidth checkers parse JSON from `nvbandwidth` stdout separately from stderr, so diagnostic warnings on stderr alone do not invalidate a valid measurement. For Unknown results, available stderr is included in the bounded diagnostic message.

## `GpuPeerBandwidth`

This checker runs the `nvbandwidth` device-to-device copy test across GPU pairs.

It applies only to profiles with a peer-bandwidth threshold. Any measured pair below the threshold produces Unhealthy with `BandwidthBelowThreshold`.

The current A10 profile does not have a peer threshold, so this checker is omitted for `Standard_NV72ads_A10_v5`.

## `NcclAllReduce`

This checker runs `nccl-tests` through OpenMPI across all GPUs assigned to the pod.

It evaluates:

1. Correctness, using the reported out-of-bounds count.
2. Average all-reduce bus bandwidth against the SKU profile.

Out-of-bounds values produce Unhealthy with `CorrectnessError`.

If correctness passes but bus bandwidth is below the threshold, the result is Unhealthy with `BandwidthBelowThreshold`.

Missing reports, malformed reports, and tool execution failures produce Unknown with `ToolFailed`.

NCCL requires at least two GPUs. A profile with fewer than two produces Unknown with `InsufficientGPUs`.

## Result names and error codes

| Checker result | Healthy criterion |
|---|---|
| `GpuHostBandwidth` | Every host/device measurement meets the SKU threshold. |
| `GpuPeerBandwidth` | Every measured GPU pair meets the SKU threshold. |
| `NcclAllReduce` | Correct output and average bus bandwidth at or above the SKU threshold. |

| Error code | Status | Meaning |
|---|---|---|
| `UnexpectedGPUCount` | Unhealthy | Detected GPU count differs from the SKU profile. |
| `CorrectnessError` | Unhealthy | NCCL reported incorrect values. |
| `BandwidthBelowThreshold` | Unhealthy | A trusted measurement is below the SKU threshold. |
| `ToolFailed` | Unknown | A benchmark or prerequisite tool did not produce a trustworthy result. |
| `UnknownSKU` | Unknown | No source profile exists for the VM size. |
| `NodeSKUNotAvailable` | Unknown | The node's VM-size label was still missing after the startup wait; no GPU tools ran. |
| `GPUsNotClaimable` | Unknown | No GPUs could be exclusively assigned after the wait period. |
| `InsufficientGPUs` | Unknown | The benchmark requires more GPUs than the profile provides. |

## Node Events and conditions

GPU nodes never receive `kubernetes.azure.com/NodeHealthy` or any other controller-managed Node condition. This is true whether intrusive GPU checks are enabled or disabled.

When a GPU-node run completes Unhealthy or Unknown, the controller emits one Warning Event on the target Node. The Event includes every base and GPU check result, including each status, error code, and message. A healthy GPU-node run emits no Event.

For example, a failed NCCL result appears in the Event message as:

```text
CheckNodeHealth <name> results:
PodStartup: Healthy: All containers started successfully
PodNetwork: Healthy
NcclAllReduce: Unhealthy [CorrectnessError]: NCCL all-reduce reported 3 out-of-bounds values
GpuHostBandwidth: Healthy: <measurement>
GpuPeerBandwidth: Healthy: <measurement>
```

If `--enable-node-condition` is enabled and a GPU node already carries `NodeHealthy`, the controller removes that stale or inapplicable condition. GPU results do not participate in the `NodeHealthy` circuit breaker.

See [Check Node Health results](results.md#node-events) for Event fields, message limits, and query commands.

## Operational guidance

- Run checks when the node can release all GPUs to the checker pod.
- Ensure the NVIDIA device plugin is installed and advertises healthy allocatable resources.
- Ensure the GPU image matches the controller version.
- Expect cold image pulls to contribute to total run duration; the 15-minute pod timeout includes image startup and benchmarks.
- Use CR results for an individual run and metrics for aggregate fleet trends.
- Treat Unknown as an inability to judge, not as evidence that GPU hardware is healthy.

Inspect an active GPU checker pod:

```bash
kubectl -n kube-system get pod \
  -l clusterhealthmonitor.azure.com/checknodehealth
```

Inspect the completed result:

```bash
kubectl get checknodehealth <name> -o yaml
```
