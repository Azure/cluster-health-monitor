# Check Node Health

- [Overview](#overview)
- [Installation and configuration](#installation-and-configuration)
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

### Execution and cleanup

The temporary checker pod runs in `kube-system` on the target node and is deleted after results are collected.

CheckNodeHealth CRs have a six-hour TTL and are deleted on the next hourly cleanup, normally six to seven hours after creation.

### GPU resource requirements

GPU checks require SKU metadata and healthy GPUs advertised by the NVIDIA device plugin. The controller waits up to `--gpu-wait` (default two minutes) for these prerequisites; if they remain unavailable, GPU results are Unknown while base checks still run.

The checker pod requires all allocatable GPUs, so existing GPU workloads must finish or be stopped before GPU checks can run.

## Installation and configuration

See [Getting started](../getting-started.md#install-only-check-node-health) for installation, verification, and removal commands. The [base Deployment](../../manifests/base/checknodehealth-controller/deployment.yaml) enables automatic new-node/reboot checks, standard-node condition publication, and intrusive GPU checks.

> [!IMPORTANT]
> Automatic checks on GPU nodes can claim all advertised GPUs while benchmarks run. Standard-node conditions can trigger external remediation. Review the settings before deploying.

### Controller settings

| Setting | Binary default / base manifest | Effect |
|---|---|---|
| `--enable-node-reboot-check` | `false` / enabled | Create checks for eligible new and rebooted nodes; clean up stale or inapplicable `NodeHealthy` conditions. |
| `--enable-node-condition` | `false` / enabled | Publish completed results as `NodeHealthy` on non-GPU nodes. |
| `--enable-gpu-checks` | `false` / enabled | Run intrusive GPU checks, except for `HealthSignal`-owned requests. |
| `--enable-health-check-request` | `false` / disabled | Bridge `HealthCheckRequest` through `HealthSignal` to a node check. |
| `--gpu-wait` | `2m` | Shared startup budget for GPU SKU metadata and claimable GPUs, measured from request creation. |

Remove an enable flag from the Deployment to disable that capability. See the [controller specification](../../spec/components/checknodehealth-controller.md) for all settings.

## How to run checks

The controller automatically selects the relevant checks for the target node: base checks on all nodes, plus applicable SKU-based GPU checks when enabled. `HealthCheckRequest`-triggered runs use only non-GPU checks.

### Manual request

To run health checks on a node, create a CheckNodeHealth CR that specfies that node in `spec.nodeRef`. Example of how to do this via kubectl below. Replace `<node-name>` and create a request:

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

Wait for completion and inspect the results:

```bash
kubectl wait --for=jsonpath='{.status.finishedAt}' \
  checknodehealth/manual-node-check --timeout=20m
kubectl get checknodehealth manual-node-check -o yaml
```

Each resource is one-shot; applying the same resource again does not rerun it. Create a new request for another run.

### `HealthCheckRequest` integration

The AKS [Health Signal API](https://github.com/Azure/aks-health-signal) (`health.aks.io/v1alpha1`) lets AKS request checks during upgrades and pause upgrades of subsequent nodes if unhealthy results are detected. This controller reports health; AKS controls upgrade progression.

Enable `--enable-health-check-request` and install the external CRDs if absent:

```bash
kubectl apply -k manifests/base/healthcheckrequest-controller
```

Each request creates a `CheckNodeHealth` and returns its completed conditions through `HealthSignal.status.conditions`. Individual results remain on the `CheckNodeHealth`.

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

On GPU nodes, `HealthCheckRequest`-triggered runs perform only the non-GPU checks: `PodStartup` and `PodNetwork`.

### Automatically on node boot

With `--enable-node-reboot-check`, eligible new Linux nodes and nodes whose boot ID changes are checked automatically. See the [Node controller specification](../../spec/components/node-controller.md) for readiness and cleanup rules.

## How to consume results

### CheckNodeHealth Status

CheckNodeHealth CR status is authoritative for an individual run. Use a non-empty `status.finishedAt` to identify a terminal run.

| Individual results | Aggregate `Healthy` condition | Current reason |
|---|---|---|
| Any Unhealthy | `False` | `CheckFailed` |
| No Unhealthy, any Unknown | `Unknown` | `CheckUnknown` |
| All Healthy | `True` | `CheckPassed` |

Missing expected results become Unknown with `CheckNotReported`. Running checks also have an Unknown condition.

For automation, use completion timestamps, statuses, and error codes rather than parsing reasons or messages.

Healthy result:

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

Unhealthy result:

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

With `--enable-node-condition`, non-GPU nodes receive `kubernetes.azure.com/NodeHealthy`, mirroring the completed CR's aggregate status, reason, and message. AKS may consume this condition for node remediation.

Example healthy Node condition:

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

A circuit breaker pauses condition updates for ten minutes after three consecutive unhealthy results within 15 minutes. This helps prevent a cluster-wide issue, such as a DNS outage, from marking many nodes unhealthy and triggering widespread remediation. Healthy or Unknown results reset the failure count.

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

## What checks run

### `PodStartup`

Checks whether all checker-pod containers started. Later application failures do not invalidate startup; a pod that never starts before timeout is Unhealthy.

### `PodNetwork`

Queries off-node CoreDNS pods directly and through the `kube-dns` Service IP. Healthy requires both paths to succeed. Insufficient eligible CoreDNS pods produces Unknown.

### GPU checks

The controller detects NVIDIA GPU nodes through allocatable `nvidia.com/gpu` or `kubernetes.azure.com/accelerator=nvidia`. AMD GPUs are not supported. It selects applicable GPU checks using the `node.kubernetes.io/instance-type` label.

#### Supported SKUs

The [source profile table](../../pkg/nodecheckerrunner/checkers/gpu/sku.go) is authoritative for supported SKUs.

- `Standard_NV72ads_A10_v5`
- `Standard_ND96isr_H100_v5`

#### Benchmarks

Applicable benchmarks validate GPU count before running:

| Result | Healthy criterion |
|---|---|
| `GpuHostBandwidth` | Minimum `nvbandwidth` measurement across GPUs and both host/device directions meets the profile floor. |
| `GpuPeerBandwidth` | Every measured GPU pair meets the peer-bandwidth floor; omitted for profiles without one. |
| `NcclAllReduce` | `nccl-tests` through OpenMPI reports correct output and average bus bandwidth at or above the profile floor. |

GPU count mismatches, incorrect output, or bandwidth below the profile floor produce Unhealthy. Missing prerequisites or unreliable measurements produce Unknown, not evidence of healthy or unhealthy hardware. See the [GPU error-code definitions](../../pkg/nodecheckerrunner/checkers/gpu/errors.go) for details.
