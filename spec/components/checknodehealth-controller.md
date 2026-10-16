# Component: CheckNodeHealth Controller

## 1. What it is

This component is a Kubernetes controller that runs on-demand health checks against a single node. Each check is requested by creating a `CheckNodeHealth` (CNH) custom resource. The controller runs a checker pod on the target node, collects the results into the CR's status, and optionally reports them as conditions on the Node.

A CNH is one-shot: once it finishes, it is not run again.

## 2. How to run it

### Command line

The binary is `/controller`.

| Flag                            | Default | Purpose |
|---------------------------------|---------|---------|
| `-metrics-bind-address`         | `:8080` | Prometheus metrics endpoint. |
| `-health-probe-bind-address`    | `:8081` | `/healthz` and `/readyz` endpoints. |
| `-leader-elect`                 | `false` | Enable leader election (ID `checknodehealth.clusterhealthmonitor.azure.com`). |
| `-enable-node-condition`        | `false` | Write check results as conditions on the Node (see §4.2). |
| `-enable-gpu-checks`            | `false` | Run GPU checks on GPU nodes. Requires `GPU_CHECKER_POD_IMAGE`. |
| `-gpu-wait`                     | `2m`    | How long to wait for a GPU node to expose claimable GPUs before running GPU checks anyway. |
| `-v`                            | `0`     | klog verbosity. Logs are JSON. |

### Environment variables

| Variable                | Required | Purpose |
|-------------------------|----------|---------|
| `CHECKER_POD_IMAGE`     | Yes      | Image for the checker pod. The controller exits if it is unset. |
| `GPU_CHECKER_POD_IMAGE` | When `-enable-gpu-checks` is on | Image for the checker pod on GPU nodes. |

## 3. Input: the `CheckNodeHealth` resource

`clusterhealthmonitor.azure.com/v1alpha1`, cluster-scoped, short name `cnh`.

```yaml
apiVersion: clusterhealthmonitor.azure.com/v1alpha1
kind: CheckNodeHealth
metadata:
  name: check-aks-nodepool1-0
  annotations:
    # Optional. Service account for the checker pod. Default: checknodehealth-checker.
    checknodehealth.azure.com/checker-service-account: my-sa
spec:
  nodeRef:
    name: aks-nodepool1-0      # the node to check
```

Any client can create a CNH.

## 4. Output

### 4.1 CNH status

```yaml
status:
  startedAt: "2026-10-14T10:00:00Z"
  finishedAt: "2026-10-14T10:00:40Z"
  conditions:
    - type: Healthy
      status: "False"            # True | False | Unknown
      reason: <reason>
      message: <free-form text>  # human-readable only; no fixed format
  results:
    - name: PodStartup
      status: Healthy            # Healthy | Unhealthy | Unknown
      message: All containers started successfully
    - name: PodNetwork
      status: Unhealthy
      errorCode: <code>          # defined by each checker; no global list
      message: <details>
```

How `results` turn into the `Healthy` condition is described in §5.3.

The condition's `reason` and all `message` fields are free-form and have no fixed values or format. Clients must not depend on them; use `status` and `errorCode` instead.

Each result's `errorCode` is chosen by the checker that wrote it. Every checker defines its own codes; this controller adds only `CheckNotReported`. There is no fixed list across checkers.

### 4.2 Node conditions (`-enable-node-condition`)

All condition types use the prefix `kubernetes.azure.com/`.

| Node kind | Conditions written |
|-----------|--------------------|
| Standard  | `NodeHealthy`, which mirrors the CNH `Healthy` condition. |
| GPU       | Never `NodeHealthy`. Always `PodStartupHealthy` and `PodNetworkHealthy`, one per base check result. The GPU conditions below only when GPU checks reported results, and only those that apply to the SKU. |

Condition types managed by the controller that don't apply to the node's kind are removed.

**GPU Node conditions.** These are written on the Node (`node.status.conditions`, prefixed `kubernetes.azure.com/`). Each one is computed from the GPU check results listed below:

| Condition | From checks | `False` when | Notes |
|-----------|-------------|--------------|-------|
| `GPUCountHealthy` | all GPU checks | any reports `UnexpectedGPUCount` | Every check verifies the GPU count first, so a check that is `Unhealthy` with any other code counts as `True` here. An `Unknown` result stays `Unknown`, since the check may have stopped before verifying the count. |
| `GPUHostBandwidthHealthy` | `GpuHostBandwidth` | it reports `BandwidthBelowThreshold` | Any other non-healthy result counts as `Unknown`. |
| `GPUPeerBandwidthHealthy` | `GpuPeerBandwidth` | it reports `BandwidthBelowThreshold` | Any other non-healthy result counts as `Unknown`. Absent on SKUs that don't run peer bandwidth. |
| `GPUAllReduceBandwidthHealthy` | `NcclAllReduce` | it reports `BandwidthBelowThreshold` | Any other non-healthy result counts as `Unknown`. |
| `GPUCorrectnessHealthy` | `NcclAllReduce` | it reports `CorrectnessError` | `BandwidthBelowThreshold` counts as `True`: NCCL checks correctness before bandwidth, so low bandwidth means correctness passed. Any other non-healthy result counts as `Unknown`. |

For each condition, a `Healthy` result counts as `True`, a listed code as `False`, and anything else (including a missing result) as `Unknown`, except where noted. Across its checks: any `False` → `False`; otherwise any `Unknown` → `Unknown`; otherwise `True`. The condition's `reason` is the failing `errorCode` when `False`.

The base conditions (`PodStartupHealthy`, `PodNetworkHealthy`) map the result status directly (`Healthy`→`True`, `Unhealthy`→`False`, else `Unknown`), with the result's `errorCode` as the reason when set.

**Rules for writing Node conditions:**

- **Circuit breaker.** There is one breaker for standard nodes and one for GPU nodes. Each finished CNH is recorded: `Healthy=False` adds an unhealthy event, and any other result (`True` or `Unknown`) clears all events. Events older than **15m** are dropped; when **3** remain, the breaker opens for **10m**, after which it closes with a clean count. Counting happens even if the Node update itself failed. While open, the controller writes no new conditions; it only removes conditions that no longer apply. This keeps a cluster-wide problem from marking every node unhealthy.

  **Why:** an unhealthy Node condition can lead other components to act on the node, for example by rebooting or replacing it. If a cluster-level problem (for example in the control plane, DNS or networking) makes many checks fail at once, writing `False` on every node would trigger that action fleet-wide for a problem the nodes don't have. Several unhealthy results in a row are treated as a sign of such a problem, so the controller stops publishing conditions until the cooldown ends.
- **Flipping back after recovery.** A component that sets a condition on a Node owns it: once the node recovers, the condition must go back to `True`. This controller only updates conditions when a CNH for the node finishes; it never re-checks a node on its own. Recovery relies on this loop:

  1. A CNH finishes unhealthy and the controller sets the condition to `False`.
  2. Another component acts on the condition, typically by rebooting the node.
  3. The NodeReboot controller sees the new boot ID and creates a new CNH.
  4. If the node has recovered, that CNH passes and this controller sets the condition back to `True`.

  As a backstop, the NodeReboot controller removes any managed condition not confirmed by a check (`lastHeartbeatTime`) within **30m**, so a stale condition does not stay on the node indefinitely. Step 3 and the backstop both need `-enable-node-reboot-check`.
- **A failed update does not block the CNH.** If updating the Node conditions fails, the error is logged and the CNH still finishes. The update is not retried: the Node keeps its previous conditions until the next CNH for that node finishes.

### 4.3 Metrics

The controller serves Prometheus metrics on `-metrics-bind-address`, alongside the standard controller-runtime metrics. Each counter is incremented once per finished CNH.

| Metric | Labels |
|--------|--------|
| `cluster_health_monitor_node_check_total`        | `node_kind` (`standard`/`gpu`), `result` (`Healthy`/`Unhealthy`/`Unknown`), `reason` (the condition's reason; no fixed values, see §4.1) |
| `cluster_health_monitor_node_check_result_total` | `node_kind`, `checker_name`, `status`, `error_code` (one increment per result) |

`error_code` is the result's `errorCode`; when it is empty, it is `Healthy` for healthy results and `Unknown` otherwise.

## 5. Behavior

### 5.1 Watching CNHs and deciding whether to run

The controller watches `CheckNodeHealth` resources and the checker pods they own. A CNH is processed when it changes, when its checker pod changes, every 30s until its pod finishes or times out, and on the periodic resync (every 1h). Each time, the controller decides what to do, in this order:

1. **Not handled:**
   - **Being deleted:** delete the checker pod, then remove the finalizer (§5.2).
   - **Older than 6h:** delete the CNH. The controller owns CNH cleanup; whoever creates a CNH does not need to delete it. Finished CNHs are not requeued; they are re-processed by the controller's periodic resync (every 1h) and deleted on the first resync after they are 6h old, so a CNH is removed 6–7h after creation.
   - **Already finished** (`status.finishedAt` is set): only clean up any remaining checker pod. A CNH is never run twice.
2. **Finished right away:**
   - **Node is not Linux:** no checker pod is created; the CNH finishes with `Healthy=Unknown`, reason `CheckUnsupported`, and no results. Metrics are recorded (with `reason=CheckUnsupported`), but Node conditions are not touched and the circuit breaker does not count it. The OS comes from the `kubernetes.io/os` label, falling back to `status.nodeInfo.operatingSystem`; an empty OS also counts as not Linux.
3. **Delayed:**
   - **GPU node with no claimable GPUs yet** (only with `-enable-gpu-checks`): start is delayed up to `-gpu-wait` after CNH creation (§5.4).
4. **Otherwise, run the check (§5.2).**

A missing node is not rejected: the checker pod is created but stays `Pending`, and the run ends by pod timeout with `PodStartup=Unhealthy`.

### 5.2 Running the check

1. **Create the checker pod** and set `status.startedAt` and `Healthy=Unknown`. The pod (`check-node-health-<cnh name>` in `kube-system`, owned by the CNH) is pinned to the node and runs the node-level checks, writing each result into `status.results` itself.
2. **When the pod has started** (every container is running or has run), record the `PodStartup` result as `Healthy`. If it is still `Pending` after the pod timeout, record `Unhealthy`, unless the pod has already reported other results (then `Healthy`).
3. **When the pod has finished** (or the pod timeout passes), set `status.finishedAt` and the `Healthy` condition from the results (§5.3), update the Node conditions if enabled (§4.2), and delete the pod.

**Keeping the CNH and its pod consistent:**

1. **The CNH does not disappear while its checker is running.** Before creating the pod, the controller adds the finalizer `checknodehealth.clusterhealthmonitor.azure.com/finalizer` to the CNH. A CNH deleted mid-run then stays (with `deletionTimestamp` set) until the controller has deleted its pod and removed the finalizer. Because pods are matched to CNHs by name, this also stops a CNH re-created with the same name from picking up the old pod. It guards against ordinary deletes only; removing the finalizer by hand bypasses it.
2. **The checker pod never becomes an orphan.**
   - **Owner reference:** the pod is owned by the CNH, so if the CNH is gone, Kubernetes garbage collection deletes the pod.
   - **Label:** the pod carries `clusterhealthmonitor.azure.com/checknodehealth=<cnh name>`. When a run finishes, and when a CNH is deleted, the controller lists pods by this label and deletes all of them. The controller's cache also only watches `kube-system` pods with this label.

Pod timeout, counted from pod creation (not CNH creation): **2m** for standard nodes, **15m** for GPU nodes running GPU checks.

Expected checks: `PodStartup` and `PodNetwork`, plus the GPU checks for the node's SKU when GPU checks run. The GPU checks are `NcclAllReduce`, `GpuHostBandwidth` and `GpuPeerBandwidth`; a known SKU may run only some of them (for example, no peer bandwidth without NVLink). An unknown SKU lists all three, but none of them runs: each reports `Unknown` with `errorCode: UnknownSKU`.

### 5.3 From results to the `Healthy` condition

The checker pod is expected to write a result for every check it runs before it exits. The controller writes only `PodStartup` itself (§5.2).

When the run finishes, the controller:

1. **Fills in missing results.** Every expected check with no result gets `status: Unknown`, `errorCode: CheckNotReported`. A pod that exits early or times out therefore cannot produce `True`.
2. **Computes `Healthy` from all results**, in this order:

   | Results | `Healthy` |
   |---------|-----------|
   | Any result is `Unhealthy` | `False` |
   | Otherwise, any result is `Unknown`, or there are no results | `Unknown` |
   | All results are `Healthy` | `True` |

   `Unhealthy` wins over `Unknown`: one failed check makes the node unhealthy even if other checks are inconclusive.

Before the run finishes, `Healthy` is `Unknown`. A node that is not Linux also gets `Unknown` without any results (§5.1).

### 5.4 GPU nodes

A node is a GPU node if it has allocatable `nvidia.com/gpu` or the label `kubernetes.azure.com/accelerator=nvidia`.

GPU checks run only when `-enable-gpu-checks` is on. In that case the checker pod:
- uses `GPU_CHECKER_POD_IMAGE`;
- gets `--enable-gpu-checks --sku=<node.kubernetes.io/instance-type>`;
- requests all claimable GPUs.

GPU checks report `Unhealthy` only for a clear hardware problem: a wrong GPU count (`UnexpectedGPUCount`), incorrect results (`CorrectnessError`) or low bandwidth (`BandwidthBelowThreshold`). Anything that stops a check from producing a trusted measurement is `Unknown`, so a healthy node is not acted on by mistake: an unknown SKU (`UnknownSKU`), no claimable GPUs (`GPUsNotClaimable`), too few GPUs for NCCL (`InsufficientGPUs`), or a benchmark tool that produced no usable output (`ToolFailed`).

If the node has no claimable GPUs yet, the controller waits up to `-gpu-wait` from CNH creation, polling every 30s. After that, it runs the pod with `--gpu-skip-reason=GPUsNotClaimable`: every GPU check that applies to a known SKU then reports `Unknown` with `errorCode: GPUsNotClaimable` without running, so the CNH ends `Healthy=Unknown` (unless a base check is `Unhealthy`).

The decision is recorded on the pod as annotations (`clusterhealthmonitor.azure.com/node-kind`, `.../gpu-checks-sku`). Later reconciles read these annotations, so a node that changes mid-run doesn't change the expected checks.

## 6. E2E

The E2E suite runs on a Kind cluster against the deployed controller (`make kind-test-local`, or `make test-e2e` on an existing cluster). Cases live in `test/e2e/checknodehealth_test.go` and `test/e2e/gpu_test.go`. Each case below is a behavior the spec requires.

### 6.1 Standard nodes

The suite targets a real node that does not run CoreDNS. Some cases use a fake Node object, so the checker pod stays `Pending`.

| # | Case | Setup | Expected |
|---|------|-------|----------|
| 1 | Healthy run | CNH on a real node | `finishedAt` set; `Healthy=True`; results `PodStartup` and `PodNetwork` both `Healthy`; checker pod deleted; Node has `NodeHealthy=True`. |
| 2 | Pod timeout | CNH on a fake node | Pod stays `Pending`. After the 2m pod timeout: `Healthy=False`, `PodStartup=Unhealthy`, `startedAt` set, pod deleted. |
| 3 | Unhealthy Node condition | CNH on a fake node, run to timeout | `Healthy=False`; Node has `NodeHealthy=False` with a non-empty reason. |
| 4 | Unsupported OS | CNH on a fake Windows node | No checker pod is created; `NodeHealthy` is never set; CNH finishes with `Healthy=Unknown`. |
| 5 | Deletion cleanup | CNH on a missing node; delete it while the pod is `Pending` | Checker pod is deleted. |
| 6 | Finalizer | Create a CNH | Exactly one finalizer is added. |
| 7 | Checker reports nothing | CNH annotated to use the `default` service account (no write permission) | `Healthy=Unknown`; `PodStartup=Healthy`; `PodNetwork=Unknown` with error code `CheckNotReported`. |

### 6.2 GPU nodes

These cases run with `-enable-gpu-checks`. Kind has no real GPUs, so a node is made to look like an NVIDIA node: a label, and for "fully managed" nodes a fake device plugin that exposes `nvidia.com/gpu`. A GPU test image replaces `nvidia-smi`, NCCL and `nvbandwidth` with fixed outputs.

| # | Case | Setup | Expected |
|---|------|-------|----------|
| 8  | Driver-only GPU node | NVIDIA label, no allocatable GPUs | GPU checks are reported as not runnable (`GPUsNotClaimable`). |
| 9  | Fully managed GPU node | Fake device plugin exposes GPUs | GPU checks run and report `Healthy`. |
| 10 | GPU node conditions | Run GPU checks to completion | Every base and GPU node condition is `True`; `NodeHealthy` is never set. |
| 11 | Node stops being a GPU node | Run on a simulated GPU node, restore the node, run again | Node now has `NodeHealthy`; the GPU node conditions are removed. |

