# Consuming Check Node Health results

The `CheckNodeHealth` status is the authoritative result for an individual run. Prometheus metrics provide aggregate views across runs. A supported-node run that executes checks and finishes non-passing emits a Warning Event on its target Node, and standard nodes can optionally expose the latest result through `NodeHealthy`.

> [!IMPORTANT]
> CR fields, metrics, labels, error codes, and Node-condition behavior are in preview and may change between versions.

Condition reasons and result messages are human-readable, not stable machine-readable contracts. Reason strings below describe current behavior. Use `status.finishedAt`, condition status, and individual result status/error codes for automation.

## List runs

The CRD adds `NODE`, `HEALTHY`, and `AGE` printer columns:

```bash
kubectl get checknodehealth
```

Example:

```text
NAME                NODE                               HEALTHY   AGE
manual-node-check   aks-nodepool1-12345678-vmss0000a   True      2m
```

The `HEALTHY` column comes from the `Healthy` condition:

- `True`: every expected check was Healthy.
- `False`: at least one check was Unhealthy.
- `Unknown`: the run is still starting or at least one result could not produce a verdict.

Use `status.finishedAt`, not the condition alone, to determine whether a run is terminal. A running check also has `Healthy=Unknown`.

## Status lifecycle

While a checker pod is running:

```yaml
status:
  startedAt: "2026-10-15T18:00:00Z"
  conditions:
    - type: Healthy
      status: Unknown
      reason: CheckStarted
      lastTransitionTime: "2026-10-15T18:00:01Z"
```

A completed healthy run resembles:

```yaml
status:
  startedAt: "2026-10-15T18:00:00Z"
  finishedAt: "2026-10-15T18:00:09Z"
  conditions:
    - type: Healthy
      status: "True"
      reason: CheckPassed
      message: |-
        PodStartup: Healthy
        PodNetwork: Healthy
      lastTransitionTime: "2026-10-15T18:00:10Z"
  results:
    - name: PodStartup
      status: Healthy
      message: All containers started successfully
    - name: PodNetwork
      status: Healthy
```

A completed failed or inconclusive run includes per-check details:

```yaml
status:
  startedAt: "2026-10-15T18:00:00Z"
  finishedAt: "2026-10-15T18:02:00Z"
  conditions:
    - type: Healthy
      status: "False"
      reason: CheckFailed
      message: |-
        PodStartup: Healthy
        PodNetwork: Unhealthy
  results:
    - name: PodStartup
      status: Healthy
      message: All containers started successfully
    - name: PodNetwork
      status: Unhealthy
      errorCode: NetworkConnectivityFailed
      message: Cluster DNS service connectivity failure detected; pod-to-pod network connectivity is functioning
```

## Overall aggregation

Individual results use `Healthy`, `Unhealthy`, or `Unknown`. The overall Kubernetes condition is reduced in this order:

| Individual results | Overall condition | Reason |
|---|---|---|
| Any Unhealthy | `Healthy=False` | `CheckFailed` |
| No Unhealthy, any Unknown | `Healthy=Unknown` | `CheckUnknown` |
| All Healthy | `Healthy=True` | `CheckPassed` |

Unhealthy takes precedence over Unknown so a known failure is not hidden by an inconclusive secondary result.

Other terminal reasons include:

| Reason | Meaning |
|---|---|
| `CheckUnsupported` | The target is not a supported Linux node. No checker pod is run and no Node condition is published. |

An expected checker that never writes a result is added as Unknown with error code `CheckNotReported`.

## Useful commands

Read the overall condition:

```bash
kubectl get checknodehealth manual-node-check \
  -o jsonpath='{.status.conditions[?(@.type=="Healthy")]}{"\n"}'
```

Read completion timestamps:

```bash
kubectl get checknodehealth manual-node-check \
  -o jsonpath='started={.status.startedAt} finished={.status.finishedAt}{"\n"}'
```

Read individual results as JSON:

```bash
kubectl get checknodehealth manual-node-check \
  -o jsonpath='{range .status.results[*]}{.name}{"\t"}{.status}{"\t"}{.errorCode}{"\t"}{.message}{"\n"}{end}'
```

Wait for completion:

```bash
kubectl wait \
  --for=jsonpath='{.status.finishedAt}' \
  checknodehealth/manual-node-check \
  --timeout=20m
```

## Prometheus metrics

Controller metrics are exposed on port 8080:

```bash
kubectl -n kube-system port-forward deployment/checknodehealth-controller 8080:8080
```

### Completed-run counter

```text
cluster_health_monitor_node_health_check_total
```

Incremented once per terminal `CheckNodeHealth`.

| Label | Meaning |
|---|---|
| `node_kind` | `standard` or `gpu`. |
| `sku` | Normalized profiled GPU SKU, `other` for an unprofiled GPU SKU, or empty for standard nodes. |
| `status` | `Healthy`, `Unhealthy`, or `Unknown`. |
| `reason` | Overall condition reason such as `CheckPassed`, `CheckFailed`, `CheckUnknown`, or `CheckUnsupported`. |

### Individual checker counter

```text
cluster_health_monitor_node_checker_result_total
```

Incremented once for every result recorded in a completed run.

| Label | Meaning |
|---|---|
| `node_kind` | `standard` or `gpu`. |
| `sku` | Bounded GPU SKU label or empty for standard nodes. |
| `checker_name` | `PodStartup`, `PodNetwork`, or an applicable GPU checker name. |
| `status` | `Healthy`, `Unhealthy`, or `Unknown`. |
| `error_code` | Checker error code, `Healthy`, or `Unknown`. |

### Time-to-verdict histogram

```text
cluster_health_monitor_node_health_check_duration_seconds
```

This histogram measures time from CR creation until the terminal result is written. It does not include the later Node-condition update or checker-pod deletion.

The duration includes GPU startup waiting when applicable. Unsupported non-Linux requests increment the completed-run counter but do not record a duration.

Labels:

- `node_kind`
- `sku`
- `status`

### PromQL examples

Completed runs by outcome:

```promql
sum by (node_kind, status, reason) (
  increase(cluster_health_monitor_node_health_check_total[1h])
)
```

Individual failed checks:

```promql
sum by (node_kind, sku, checker_name, error_code) (
  increase(cluster_health_monitor_node_checker_result_total{status="Unhealthy"}[1h])
)
```

95th percentile time to verdict:

```promql
histogram_quantile(
  0.95,
  sum by (le, node_kind, status) (
    rate(cluster_health_monitor_node_health_check_duration_seconds_bucket[1h])
  )
)
```

The metrics intentionally omit node names to keep label cardinality bounded. Use CRs and controller logs to investigate individual runs.

## Node Events

When a supported-node run executes checks and finishes with `Healthy=False` or `Healthy=Unknown`, the controller emits one Kubernetes Warning Event on the target Node. Event reporting:

- Applies to standard and GPU nodes.
- Is always enabled.
- Does not depend on `--enable-node-condition`.
- Is not suppressed by the `NodeHealthy` circuit breaker.
- Emits nothing for a run that finishes `Healthy=True`.

An unsupported-node request completes as Unknown without executing checks and does not emit an Event. If the target Node no longer exists when a run finishes, there is no object on which to record an Event.

The Event has:

| Field | Value |
|---|---|
| Involved object | The target Node |
| Namespace | `default`, because Nodes are cluster-scoped |
| Type | `Warning` |
| Reason | `CheckFailed` for `Healthy=False` or `CheckUnknown` for `Healthy=Unknown` |
| Reporting component | `checknodehealth-controller` |
| Message | The CR name followed by every individual check result |

Example:

```text
CheckNodeHealth manual-node-check results:
PodStartup: Healthy: All containers started successfully
PodNetwork: Unhealthy [NetworkConnectivityFailed]: dial timeout
```

Each result line has this shape:

```text
<name>: <status> [<errorCode>]: <message>
```

The error code and message suffixes are omitted when empty. Line breaks inside a result message become `; `. An individual result message is truncated after 1024 bytes, and the complete Event message is bounded to 8 KiB. If not every result fits, the final line identifies how many were omitted and points to the complete `CheckNodeHealth`.

View recent Events for a node:

```bash
kubectl events --for node/<node-name> --types=Warning
```

For kubectl versions without `kubectl events`:

```bash
kubectl get events -n default \
  --field-selector involvedObject.kind=Node,involvedObject.name=<node-name> \
  --sort-by=.lastTimestamp
```

Kubernetes Events have cluster-configured retention and should not be used as the durable record of a run. Use the CR while it exists and metrics for longer-term aggregation.

## Standard-node condition

`NodeHealthy` publication defaults to off in the binary but is enabled by the supplied base Deployment with `--enable-node-condition`. Remove that flag in an overlay to disable publication.

Standard nodes receive:

```text
kubernetes.azure.com/NodeHealthy
```

Its status, reason, and message mirror the completed CR's aggregate Healthy condition.

AKS uses this condition internally. Other scheduling or remediation systems may also consume it.

GPU nodes never receive `NodeHealthy` or any other controller-managed Node condition. When condition publication is enabled, a completed GPU-node run removes an existing `NodeHealthy`, such as one left from before the node was identified as a GPU node. Use CR status, GPU-labeled metrics, and Warning Events for GPU-node results.

### Circuit breaker

One circuit breaker guards standard-node `NodeHealthy` publication. By default, three consecutive unhealthy standard-node results within 15 minutes open the breaker for ten minutes.

While open, standard-node condition updates are suppressed to reduce cascading scheduling or remediation effects from a systemic failure. A Healthy or Unknown standard-node result resets the consecutive-unhealthy count. GPU results do not participate because GPU nodes never receive `NodeHealthy`.

The circuit breaker does not suppress Warning Events.

### Condition cleanup

When `--enable-node-reboot-check` is enabled, the Node controller removes `NodeHealthy` from supported Linux nodes when:

- It has not been confirmed for more than 30 minutes, measured from `lastHeartbeatTime`, or `lastTransitionTime` if the heartbeat is unset.
- The node has `kubernetes.azure.com/accelerator=nvidia`. This controller's cached view omits allocatable GPU resources, so a GPU node identified only by `nvidia.com/gpu` is not eligible for immediate cleanup through this path.

Cleanup runs when the Node is reconciled. Writing the boot-ID annotation schedules one reconciliation after 30 minutes; subsequent cleanup depends on Node updates. Removal is not guaranteed at exactly 30 minutes. This cleanup does not require `--enable-node-condition` and never changes other Node conditions.

The CheckNodeHealth controller separately removes `NodeHealthy` when a GPU-node run completes and condition publication is enabled, using either the accelerator label or allocatable GPU resources for detection.

See the [Node controller specification](../../spec/components/node-controller.md) for the readiness and cleanup rules.

## `HealthSignal`

When `--enable-health-check-request` is enabled, the integration copies the completed `CheckNodeHealth.status.conditions` into the owned `HealthSignal.status.conditions`.

The individual `results` array remains on the `CheckNodeHealth`; inspect that resource for per-check details.

Runs controlled by a `health.aks.io/v1alpha1` `HealthSignal` execute only `PodStartup` and `PodNetwork`, even on GPU nodes with GPU checks enabled. They skip GPU startup waiting and use the standard image and two-minute pod timeout. GPU results are not added as missing, but metrics still classify the node as `node_kind="gpu"` and no `NodeHealthy` condition is published on that GPU node.

## Logs

Find an individual run by CR and node name in controller logs:

```bash
kubectl -n kube-system logs deployment/checknodehealth-controller
```

The completion log includes:

- CR name
- Node name
- Node kind
- Original node SKU
- Overall status and reason
- Duration in seconds, when available

Checker-pod logs are available only while the temporary pod still exists, so collect them promptly for an in-progress run.
