# Component: CheckNodeHealth Controller

## 1. What it is

This component is a Kubernetes controller that runs on-demand health checks against a single node. Each check is requested by creating a `CheckNodeHealth` (CNH) custom resource. The controller runs a checker pod on the target node, collects the results into the CR's status, reports a check that did not pass as an event on the Node, and optionally reports the result as a condition on standard Nodes.

A CNH is one-shot: once it finishes, it is not run again.

## 2. How to run it

### Command line

The binary is `/controller`.

| Flag                            | Default | Purpose |
|---------------------------------|---------|---------|
| `-metrics-bind-address`         | `:8080` | Prometheus metrics endpoint. |
| `-health-probe-bind-address`    | `:8081` | `/healthz` and `/readyz` endpoints. |
| `-leader-elect`                 | `false` | Enable leader election (ID `checknodehealth.clusterhealthmonitor.azure.com`). |
| `-enable-node-condition`        | `false` | Write the `NodeHealthy` condition on standard Nodes (see §4.2). Node events (§4.3) are emitted either way. |
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

The only condition the controller manages is `kubernetes.azure.com/NodeHealthy`.

| Node kind | Condition written |
|-----------|-------------------|
| Standard  | `NodeHealthy`, which mirrors the CNH `Healthy` condition (status, reason and message). |
| GPU       | None. A `NodeHealthy` already on the node, for example from before it was a GPU node, is removed. |

**Why GPU nodes get no condition:** existing remediation acts on `NodeHealthy` and can reboot or replace the node. It was not designed for GPU nodes, the GPU checks are still new, and GPU capacity is scarce enough that a customer may not get a replaced node back. A GPU node's results therefore never reach a Node condition; they are reported through events (§4.3) only.

Whether a node counts as a GPU node is decided when its checker pod is created (§5.4).

**Rules for writing Node conditions:**

- **Circuit breaker.** One breaker guards `NodeHealthy`. Each finished CNH on a standard node is recorded: `Healthy=False` adds a failure, and any other result (`True` or `Unknown`) clears all failures. CNHs on GPU nodes are not recorded, since they never write `NodeHealthy`. Failures older than **15m** are dropped; when **3** remain, the breaker opens for **10m**, after which it closes with a clean count. Counting happens even if the Node update itself failed. While open, the controller writes no `NodeHealthy` on standard nodes; it still removes `NodeHealthy` from GPU nodes. The breaker does not affect Node events (§4.3). This keeps a cluster-wide problem from marking every node unhealthy.

  **Why:** an unhealthy Node condition can lead other components to act on the node, for example by rebooting or replacing it. If a cluster-level problem (for example in the control plane, DNS or networking) makes many checks fail at once, writing `False` on every node would trigger that action fleet-wide for a problem the nodes don't have. Several unhealthy results in a row are treated as a sign of such a problem, so the controller stops publishing conditions until the cooldown ends.
- **Flipping back after recovery.** A component that sets a condition on a Node owns it: once the node recovers, the condition must go back to `True`. This controller only updates conditions when a CNH for the node finishes; it never re-checks a node on its own. Recovery relies on this loop:

  1. A CNH finishes unhealthy and the controller sets the condition to `False`.
  2. Another component acts on the condition, typically by rebooting the node.
  3. The NodeReboot controller sees the new boot ID and creates a new CNH.
  4. If the node has recovered, that CNH passes and this controller sets the condition back to `True`.

  As a backstop, the NodeReboot controller removes `NodeHealthy` if no check has confirmed it (`lastHeartbeatTime`) within **30m**, so a stale condition does not stay on the node indefinitely. It also removes `NodeHealthy` from GPU nodes right away. Step 3 and the backstop both need `-enable-node-reboot-check`.
- **A failed update does not block the CNH.** If updating the Node condition fails, the error is logged and the CNH still finishes. The update is not retried: the Node keeps its previous condition until the next CNH for that node finishes.

### 4.3 Node events

When a CNH finishes with `Healthy` other than `True`, the controller emits one Kubernetes event on the target Node. This applies to every node kind and does not depend on `-enable-node-condition` or the circuit breaker. A CNH that finishes `Healthy=True` emits no event.

```yaml
apiVersion: v1
kind: Event
metadata:
  namespace: default             # events on cluster-scoped objects such as Nodes go to `default`
involvedObject:
  kind: Node
  name: aks-gpunp-0
type: Warning
reason: CheckFailed              # CheckFailed | CheckUnknown
source:
  component: checknodehealth-controller
message: |-
  CheckNodeHealth check-aks-gpunp-0 results:
  PodStartup: Healthy: All containers started successfully
  PodNetwork: Healthy
  NcclAllReduce: Unhealthy [CorrectnessError]: <details>
  GpuHostBandwidth: Healthy: <details>
  GpuPeerBandwidth: Healthy: <details>
```

| Field     | Value |
|-----------|-------|
| `type`    | Always `Warning`. |
| `reason`  | `CheckFailed` when `Healthy=False`; `CheckUnknown` when `Healthy=Unknown`. |
| `message` | The first line is `CheckNodeHealth <name> results:`. It is followed by one line per entry in `status.results`, in the same order, as `<name>: <status>`, with ` [<errorCode>]` appended when the code is set and `: <message>` appended when the message is set. |

Unlike the CNH condition's `reason` (§4.1), the event's `reason` has fixed values. The first message line is stable, so the event can be matched to its CNH. The result lines are for people and repeat the free-form result messages, so clients must not parse them. Read the CNH for structured results while it exists (§5.1).

**Why an event:** it puts every detail of a failed or inconclusive check on the node itself (`kubectl describe node`, event pipelines) without setting a condition that other components act on. That makes it safe on GPU nodes. It also adds detail on standard nodes, where the `NodeHealthy` message lists only each check's status.

**Rules for emitting Node events:**

- **At most one event per CNH.** The event is emitted once, when the CNH finishes (§5.2). Later processing of a finished CNH never emits another.
- **Best effort.** An event can be lost, for example if the controller restarts right after the CNH finishes or the API server is unavailable. Kubernetes may also rate-limit or aggregate events for a node, and expires them after the cluster's event TTL (1h by default). The CNH status is the source of truth; the event is for visibility.
- **No event without a Node.** CNHs for nodes that are not Linux (§5.1) and CNHs whose target node does not exist emit no event.

### 4.4 Metrics

The controller serves Prometheus metrics on `-metrics-bind-address`, alongside the standard controller-runtime metrics. Each metric is recorded once per finished CNH.

| Metric | Type | Labels |
|--------|------|--------|
| `cluster_health_monitor_node_health_check_total`            | Counter   | `node_kind`, `sku`, `status`, `reason` |
| `cluster_health_monitor_node_checker_result_total`          | Counter   | `node_kind`, `sku`, `checker_name`, `status`, `error_code` (one increment per result) |
| `cluster_health_monitor_node_health_check_duration_seconds` | Histogram | `node_kind`, `sku`, `status` |

Labels:

- `node_kind`: `standard` or `gpu`.
- `sku`: empty on standard nodes. On GPU nodes, the SKU if the GPU checks have a profile for it, otherwise `other`. This keeps the number of values bounded.
- `status`: `Healthy`, `Unhealthy` or `Unknown`. On the overall metrics it maps the `Healthy` condition (`True`/`False`/`Unknown`); on the per-check counter it is the result's status.
- `reason`: the condition's reason. It has no fixed values (§4.1), except `CheckUnsupported` for nodes that are not Linux.
- `error_code`: the result's `errorCode`; when it is empty, `Healthy` for healthy results and `Unknown` otherwise.

The duration runs from CNH creation to the moment the final result is written, which is when consumers of the CNH get their answer. It does not include emitting the Node event, updating the Node condition or deleting the pod. It is not recorded for nodes that are not Linux.

## 5. Behavior

### 5.1 Watching CNHs and deciding whether to run

The controller watches `CheckNodeHealth` resources and the checker pods they own. A CNH is processed when it changes, when its checker pod changes, every 30s until its pod finishes or times out, and on the periodic resync (every 1h). Each time, the controller decides what to do, in this order:

1. **Not handled:**
   - **Being deleted:** delete the checker pod, then remove the finalizer (§5.2).
   - **Older than 6h:** delete the CNH. The controller owns CNH cleanup; whoever creates a CNH does not need to delete it. Finished CNHs are not requeued; they are re-processed by the controller's periodic resync (every 1h) and deleted on the first resync after they are 6h old, so a CNH is removed 6–7h after creation.
   - **Already finished** (`status.finishedAt` is set): only clean up any remaining checker pod. A CNH is never run twice.
2. **Finished right away:**
   - **Node is not Linux:** no checker pod is created; the CNH finishes with `Healthy=Unknown`, reason `CheckUnsupported`, and no results. Metrics are recorded (with `reason=CheckUnsupported`), but Node conditions are not touched, no Node event is emitted, and the circuit breaker does not count it. The OS comes from the `kubernetes.io/os` label, falling back to `status.nodeInfo.operatingSystem`; an empty OS also counts as not Linux.
3. **Delayed:**
   - **GPU node with no claimable GPUs yet** (only with `-enable-gpu-checks`): start is delayed up to `-gpu-wait` after CNH creation (§5.4).
4. **Otherwise, run the check (§5.2).**

A missing node is not rejected: the checker pod is created but stays `Pending`, and the run ends by pod timeout with `PodStartup=Unhealthy`. No Node event is emitted, since there is no Node to attach it to.

### 5.2 Running the check

1. **Create the checker pod** and set `status.startedAt` (the pod's creation time) and `Healthy=Unknown`. The pod (`check-node-health-<cnh name>` in `kube-system`, owned by the CNH) is pinned to the node and runs the node-level checks, writing each result into `status.results` itself.
2. **When the pod has started** (every container is running or has run), record the `PodStartup` result as `Healthy`. If it is still `Pending` after the pod timeout, record `Unhealthy`, unless the pod has already reported other results (then `Healthy`).
3. **When the pod has finished** (or the pod timeout passes), set `status.finishedAt` and the `Healthy` condition from the results (§5.3), emit the Node event if the check did not pass (§4.3), update the Node condition if enabled (§4.2), and delete the pod.
   `status.finishedAt` is when the checks actually ended: the time the last container terminated if the pod finished, or pod creation plus the pod timeout if it timed out. It is never later than now or earlier than `startedAt`.

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

The decision is recorded on the pod as annotations (`clusterhealthmonitor.azure.com/node-kind`, `.../gpu-checks-sku`). Later reconciles read these annotations, so a node that changes mid-run changes neither the expected checks nor whether the result can reach `NodeHealthy` (§4.2).

## 6. E2E

The E2E suite runs on a Kind cluster against the deployed controller (`make kind-test-local`, or `make test-e2e` on an existing cluster). Cases live in `test/e2e/checknodehealth_test.go` and `test/e2e/gpu_test.go`. Each case below is a behavior the spec requires.

### 6.1 Standard nodes

The suite targets a real node that does not run CoreDNS. Some cases use a fake Node object, so the checker pod stays `Pending`.

| # | Case | Setup | Expected |
|---|------|-------|----------|
| 1 | Healthy run | CNH on a real node | `finishedAt` set; `Healthy=True`; results `PodStartup` and `PodNetwork` both `Healthy`; checker pod deleted; Node has `NodeHealthy=True`. |
| 2 | Pod timeout | CNH on a fake node | Pod stays `Pending`. After the 2m pod timeout: `Healthy=False`, `PodStartup=Unhealthy`, `startedAt` set, pod deleted. |
| 3 | Unhealthy Node condition and event | CNH on a fake node, run to timeout | `Healthy=False`; Node has `NodeHealthy=False` with a non-empty reason; the Node has a `Warning` event with reason `CheckFailed` that names the CNH and contains `PodStartup: Unhealthy`. |
| 4 | Unsupported OS | CNH on a fake Windows node | No checker pod is created; `NodeHealthy` is never set; CNH finishes with `Healthy=Unknown`. |
| 5 | Deletion cleanup | CNH on a missing node; delete it while the pod is `Pending` | Checker pod is deleted. |
| 6 | Finalizer | Create a CNH | Exactly one finalizer is added. |
| 7 | Checker reports nothing | CNH annotated to use the `default` service account (no write permission) | `Healthy=Unknown`; `PodStartup=Healthy`; `PodNetwork=Unknown` with error code `CheckNotReported`. |

### 6.2 GPU nodes

These cases run with `-enable-gpu-checks`. Kind has no real GPUs, so a node is made to look like an NVIDIA node: a label, and for "fully managed" nodes a fake device plugin that exposes `nvidia.com/gpu`. A GPU test image replaces `nvidia-smi`, NCCL and `nvbandwidth` with fixed outputs.

| # | Case | Setup | Expected |
|---|------|-------|----------|
| 8  | Driver-only GPU node | NVIDIA label, no allocatable GPUs | GPU checks are reported as not runnable (`GPUsNotClaimable`); `Healthy=Unknown`; the Node has a `Warning` event with reason `CheckUnknown` that lists each GPU check as `Unknown [GPUsNotClaimable]`; `NodeHealthy` is never set. |
| 9  | Fully managed GPU node | Fake device plugin exposes GPUs | GPU checks run and report `Healthy`. |
| 10 | Healthy GPU node publishes nothing | Run GPU checks to completion | `NodeHealthy` is never set, and the CNH emits no event. |
| 11 | Node becomes a GPU node | Run on a standard node, then simulate a driver-only GPU node and run again | The first run sets `NodeHealthy`; the second removes it. |

