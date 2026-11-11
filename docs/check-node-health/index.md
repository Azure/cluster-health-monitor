# Check Node Health

- [Overview](#overview)
- [How to run checks](#how-to-run-checks)
- [How to consume results](#how-to-consume-results)
- [What checks run](#what-checks-run)

> [!IMPORTANT]
> Configuration, CR fields, metrics, labels, error codes, and supported checks are in preview and may change between versions.

## Overview

Check Node Health evaluates the health of a specific Linux AKS node. Each cluster-scoped `CheckNodeHealth` custom resource requests one run.

```text
Manual request, HealthCheckRequest, or node boot
   |
   v
CheckNodeHealth CR
   |
   v
Controller creates a checker pod on the target node
   |
   v
Results
   +-- CR status
   +-- Prometheus metrics
   +-- Node Event for Unhealthy or Unknown results
   +-- NodeHealthy condition on non-GPU nodes
```

## How to run checks

The controller automatically selects the relevant checks for the target node: base checks on all nodes, plus applicable SKU-based GPU checks when enabled. `HealthCheckRequest`-triggered runs use only non-GPU checks.

### Installation and configuration

See [Getting started](../getting-started.md#install-only-check-node-health) for installation, verification, and removal commands. The [base Deployment](../../manifests/base/checknodehealth-controller/deployment.yaml) enables automatic new-node/reboot checks, standard-node condition publication, and intrusive GPU checks.

> [!IMPORTANT]
> Automatic checks on GPU nodes can claim all advertised GPUs while benchmarks run. Standard-node conditions can trigger external remediation. Review the settings before deploying.

#### Controller settings

| Setting | Binary default / base manifest | Effect |
|---|---|---|
| `CHECKER_POD_IMAGE` | Required / set in base | Standard checker image. |
| `GPU_CHECKER_POD_IMAGE` | Unset / set in base | Required when GPU checks are enabled. |
| `--enable-node-reboot-check` | `false` / enabled | Create checks for eligible new and rebooted nodes; clean up stale or inapplicable `NodeHealthy` conditions. |
| `--enable-node-condition` | `false` / enabled | Publish completed results as `NodeHealthy` on non-GPU nodes. |
| `--enable-gpu-checks` | `false` / enabled | Run intrusive GPU checks, except for `HealthSignal`-owned requests. |
| `--enable-health-check-request` | `false` / disabled | Bridge `HealthCheckRequest` through `HealthSignal` to a node check. |
| `--gpu-wait` | `2m` | Shared startup budget for GPU SKU metadata and claimable GPUs, measured from request creation. |
| `--metrics-bind-address` | `:8080` | Prometheus metrics address. |
| `--health-probe-bind-address` | `:8081` | Health and readiness probe address. |
| `--leader-elect` | `false` | Enable leader election for multiple replicas. |

Adjust the controller Deployment's arguments and environment variables as needed. Removing an enable flag restores its binary default. For example, removing all three enabled capability flags makes runs manual-only, without Node-condition publication or GPU benchmarks.

### Manual request

List Linux nodes:

```bash
kubectl get nodes -l kubernetes.io/os=linux
```

Replace `<node-name>` and create a request:

```bash
kubectl apply -f - <<EOF
apiVersion: clusterhealthmonitor.azure.com/v1alpha1
kind: CheckNodeHealth
metadata:
  name: manual-node-check
spec:
  nodeRef:
    name: <node-name>
EOF
```

Watch the run, wait for completion, and inspect its results:

```bash
kubectl get checknodehealth manual-node-check --watch
```

```bash
kubectl wait --for=jsonpath='{.status.finishedAt}' \
  checknodehealth/manual-node-check --timeout=20m
kubectl get checknodehealth manual-node-check -o yaml
```

Each resource is one-shot; applying the same resource again does not rerun it. Create a new request for another run.

### `HealthCheckRequest` integration

This integrates with the AKS [Health Signal API](https://github.com/Azure/aks-health-signal) (`health.aks.io/v1alpha1`). During node upgrades, AKS can request health checks and use the returned signals to pause upgrades of subsequent nodes if an unhealthy result is detected. This controller reports health; AKS decides whether to continue the upgrade.

Enable `--enable-health-check-request` and install the external CRDs if absent:

```bash
kubectl apply -k manifests/base/healthcheckrequest-controller
```

The integration creates an ownership chain of `HealthCheckRequest` → `HealthSignal` → `CheckNodeHealth`, then copies completed CR conditions into `HealthSignal.status.conditions`. Individual results remain on the `CheckNodeHealth`. Deleting the request allows garbage collection of the owned resources.

```yaml
apiVersion: health.aks.io/v1alpha1
kind: HealthCheckRequest
metadata:
  name: node-health-request
spec:
  scope: Node
  targetRef:
    name: <node-name>
```

The integration currently expects a Node target, even though the external API defines broader scopes.

On GPU nodes, `HealthCheckRequest`-triggered runs perform only the non-GPU checks: `PodStartup` and `PodNetwork`.

### Automatic triggers

With `--enable-node-reboot-check`, Linux nodes are checked when newly created or when their boot ID changes:

See the [Node controller specification](../../spec/components/node-controller.md) for the full readiness and cleanup contract. 

### Execution and cleanup

The controller creates a temporary pod in `kube-system`, pinned to the target node. Checkers run sequentially and write results as they complete. The controller adds missing expected results as Unknown, computes the aggregate result, records metrics and applicable Node output, then deletes the pod.

Requests have a six-hour TTL and are deleted on the next hourly cleanup, normally six to seven hours after creation.

#### GPU resource requirements

GPU checks require SKU metadata and healthy GPUs advertised by the NVIDIA device plugin. The controller waits up to `--gpu-wait` (default two minutes) for these prerequisites; if they remain unavailable, GPU results are Unknown while base checks still run.

The GPU image includes CUDA, OpenMPI, `nvbandwidth`, `nccl-tests`, and topology data, and is built for `linux/amd64`. Keep it aligned with the controller version.

The checker pod requires all allocatable GPUs, so existing GPU workloads must finish or be stopped before GPU checks can run.

## How to consume results

### CheckNodeHealth Status

CheckNodeHealth CR status is authoritative for an individual run. Use a non-empty `status.finishedAt` to identify a terminal run.

| Individual results | Aggregate `Healthy` condition | Current reason |
|---|---|---|
| Any Unhealthy | `False` | `CheckFailed` |
| No Unhealthy, any Unknown | `Unknown` | `CheckUnknown` |
| All Healthy | `True` | `CheckPassed` |

Missing expected results become Unknown with `CheckNotReported`. While running, the condition is Unknown with reason `CheckStarted`. Unsupported targets finish Unknown with `CheckUnsupported`.

Condition reasons and messages are human-readable, not stable machine-readable contracts. Use timestamps, condition status, and individual result status/error codes for automation.

A sample healthy result looks like:

```yaml
status:
  startedAt: "2026-10-15T18:00:00Z"
  finishedAt: "2026-10-15T18:00:09Z"
  conditions:
    - type: Healthy
      status: "True"
      reason: CheckPassed
      lastTransitionTime: "2026-10-15T18:00:10Z"
  results:
    - name: PodStartup
      status: Healthy
      message: All containers started successfully
    - name: PodNetwork
      status: Healthy
```

A sample unhealthy result looks like:

```yaml
status:
  startedAt: "2026-10-15T18:00:00Z"
  finishedAt: "2026-10-15T18:00:09Z"
  conditions:
    - type: Healthy
      status: "False"
      reason: CheckFailed
      lastTransitionTime: "2026-10-15T18:00:10Z"
  results:
    - name: PodStartup
      status: Healthy
      message: All containers started successfully
    - name: PodNetwork
      status: Unhealthy
      errorCode: NetworkConnectivityFailed
      message: Cluster DNS service connectivity failure detected
```

### Node Events

A completed Unhealthy or Unknown run emits a Warning Event on the target Node with reason `CheckFailed` or `CheckUnknown` and individual check results. This applies to standard and GPU nodes. Healthy and unsupported-node runs emit no Event.

Example Event:

```yaml
apiVersion: v1
kind: Event
metadata:
  namespace: default
involvedObject:
  kind: Node
  name: aks-nodepool1-0
type: Warning
reason: CheckFailed
source:
  component: checknodehealth-controller
message: |-
  CheckNodeHealth manual-node-check results:
  PodStartup: Healthy: All containers started successfully
  PodNetwork: Unhealthy [NetworkConnectivityFailed]: dial timeout
```

View Events for a node:

```bash
kubectl events --for node/<node-name> --types=Warning
```

### Standard-node condition

With `--enable-node-condition`, non-GPU nodes receive `kubernetes.azure.com/NodeHealthy`, mirroring the completed CR's aggregate status, reason, and message. AKS and other scheduling or remediation systems may consume it.

Example condition on a healthy standard Node:

```yaml
status:
  conditions:
    - type: kubernetes.azure.com/NodeHealthy
      status: "True"
      reason: CheckPassed
      message: |-
        PodStartup: Healthy
        PodNetwork: Healthy
      lastHeartbeatTime: "2026-10-15T18:00:10Z"
      lastTransitionTime: "2026-10-15T18:00:10Z"
```

A circuit breaker pauses condition updates for ten minutes after three consecutive unhealthy results within 15 minutes. Healthy or Unknown results reset the failure count.

With `--enable-node-reboot-check`, conditions not confirmed for more than 30 minutes are removed when the Node is next reconciled.

### Prometheus metrics

Controller metrics are exposed on port 8080:

```bash
kubectl -n kube-system port-forward deployment/checknodehealth-controller 8080:8080
```

| Metric | Meaning | Labels |
|---|---|---|
| `cluster_health_monitor_node_health_check_total` | Once per terminal run. | `node_kind`, `sku`, `status`, `reason` |
| `cluster_health_monitor_node_checker_result_total` | Once per recorded result in a completed run. | `node_kind`, `sku`, `checker_name`, `status`, `error_code` |

| Label | Values / meaning |
|---|---|
| `node_kind` | `standard` or `gpu`. |
| `sku` | Normalized profiled GPU SKU, `other` for unprofiled GPU nodes, or empty for standard nodes. |
| `status` | `Healthy`, `Unhealthy`, or `Unknown`. |
| `reason` | Aggregate condition reason, such as `CheckPassed` or `CheckFailed`. |
| `checker_name` | Individual check name, such as `PodStartup` or `PodNetwork`. |
| `error_code` | Recorded error code; `Healthy` or `Unknown` when no code is recorded. |

Node names are omitted to bound cardinality. Use CR status and controller logs to investigate individual runs.

### Controller logs

Inspect controller logs by CR and node name:

```bash
kubectl -n kube-system logs deployment/checknodehealth-controller
```

Completion logs include CR name, node name/kind, original SKU, aggregate status/reason, and duration when available.

## What checks run

### `PodStartup`

The controller records whether all checker-pod containers started. A running container or one that terminated after starting passes; a later application failure does not invalidate startup. A pod still Pending at timeout is Unhealthy unless checker-reported results prove it ran.

### `PodNetwork`

The checker queries ready, running CoreDNS pods on other nodes directly and through the `kube-dns` Service IP. Healthy requires both Service connectivity and at least one successful direct Pod path. Failure of either path is Unhealthy; insufficient eligible off-node CoreDNS pods produces Unknown.

### GPU checks

The controller detects NVIDIA GPU nodes through allocatable `nvidia.com/gpu` or `kubernetes.azure.com/accelerator=nvidia`. AMD GPUs are not supported. It selects applicable GPU checks using the `node.kubernetes.io/instance-type` label.

#### Supported SKUs

The [source profile table](../../pkg/nodecheckerrunner/checkers/gpu/sku.go) is authoritative for supported SKUs.

- `Standard_NV72ads_A10_v5`
- `Standard_NC40ads_H100_v5`
- `Standard_NC80adis_H100_v5`
- `Standard_ND96isr_H100_v5`

#### Benchmarks and error codes

Each applicable benchmark checks SKU metadata/profile, controller skip reasons, and the GPU count from `nvidia-smi -L` before running tools.

| Result | Healthy criterion |
|---|---|
| `GpuHostBandwidth` | Minimum `nvbandwidth` measurement across GPUs and both host/device directions meets the profile floor. |
| `GpuPeerBandwidth` | Every measured GPU pair meets the peer-bandwidth floor; omitted for profiles without one. |
| `NcclAllReduce` | `nccl-tests` through OpenMPI reports correct output and average bus bandwidth at or above the profile floor. |

| Error code | Status | Meaning |
|---|---|---|
| `UnexpectedGPUCount` | Unhealthy | Detected GPU count differs from the profile. |
| `CorrectnessError` | Unhealthy | NCCL reported out-of-bounds values. |
| `BandwidthBelowThreshold` | Unhealthy | A trusted measurement is below its floor. |
| `ToolFailed` | Unknown | Tool failure, missing/malformed/incomplete output, or an untrustworthy GPU count. |
| `UnknownSKU` | Unknown | No applicable profile exists; unprofiled SKUs report all three GPU results without benchmarks. |
| `NodeSKUNotAvailable` | Unknown | VM-size metadata was missing after the wait. |
| `GPUsNotClaimable` | Unknown | No GPUs could be exclusively assigned after the wait. |
| `InsufficientGPUs` | Unknown | NCCL requires at least two GPUs. |

Treat Unknown as inability to judge, not evidence that hardware is healthy/unhealthy.
