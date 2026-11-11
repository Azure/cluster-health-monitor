# Consuming continuous-check results

Continuous checks expose cumulative Prometheus counters on port 9800 at `/metrics`.

```bash
kubectl -n kube-system port-forward deployment/cluster-health-monitor 9800:9800
```

Then inspect the endpoint:

```bash
curl http://127.0.0.1:9800/metrics
```

> [!IMPORTANT]
> Metrics and label contracts are in preview and may change between versions.

## Checker result counter

```text
cluster_health_monitor_checker_result_total
```

This counter records aggregate checker outcomes.

| Label | Meaning |
|---|---|
| `checker_type` | Implementation type: `DNS`, `PodStartup`, `APIServer`, `MetricsServer`, or `AzurePolicy`. |
| `checker_name` | User-configured checker instance name. |
| `status` | `Healthy`, `Unhealthy`, or `Unknown`. |
| `error_code` | `Healthy` for healthy results; the checker-specific code for Unhealthy or explicitly coded Unknown results; otherwise `Unknown` for inconclusive results and execution errors. |

Examples:

```text
cluster_health_monitor_checker_result_total{
  checker_type="DNS",
  checker_name="InternalCoreDNS",
  status="Healthy",
  error_code="Healthy"
} 42
```

```text
cluster_health_monitor_checker_result_total{
  checker_type="APIServer",
  checker_name="APIServer",
  status="Unhealthy",
  error_code="APIServerGetTimeout"
} 1
```

## Per-pod DNS result counter

```text
cluster_health_monitor_pod_health_result_total
```

`CoreDNSPerPod` instances emit this counter instead of the aggregate checker counter.

| Label | Meaning |
|---|---|
| `checker_type` | `DNS`. |
| `checker_name` | User-configured checker instance name. |
| `pod_namespace` | Namespace from the endpoint's Pod reference. |
| `pod_name` | CoreDNS Pod name from the endpoint reference. |
| `status` | `Healthy`, `Unhealthy`, or `Unknown`. |
| `error_code` | `Healthy`, a DNS failure code, or `Unknown`. |

## Counters are not current-state gauges

A raw counter shows how many times a labeled result has occurred since the process started. It does not say which status was most recent.

Prefer `increase` or `rate` over a window that is longer than the checker interval:

```promql
sum by (checker_name, status, error_code) (
  increase(cluster_health_monitor_checker_result_total[15m])
)
```

Per-CoreDNS-pod outcomes:

```promql
sum by (pod_namespace, pod_name, status, error_code) (
  increase(cluster_health_monitor_pod_health_result_total[15m])
)
```

Checkers with an unhealthy result in the last ten minutes:

```promql
sum by (checker_name) (
  increase(cluster_health_monitor_checker_result_total{status="Unhealthy"}[10m])
) > 0
```

Execution failures that could not produce a verdict:

```promql
sum by (checker_name) (
  increase(cluster_health_monitor_checker_result_total{status="Unknown"}[10m])
) > 0
```

Choose windows based on the configured checker interval. A window shorter than an interval can legitimately contain no sample.

## Alerting guidance

Separate Unhealthy from Unknown:

- **Unhealthy** means the checker completed enough work to identify a failed criterion.
- **Unknown** means it could not produce a reliable verdict, commonly because of a client, permission, timeout, or environmental error.

For low-noise alerts:

1. Use `increase`, not the raw counter.
2. Select a window containing multiple expected runs.
3. Alert on repeated failures or a failure ratio, not a single historical sample.
4. Group by `checker_name` and `error_code`.
5. Treat process restarts and missing scrapes separately from checker health.

Example repeated-failure expression for a checker that runs every 30 seconds:

```promql
sum by (checker_name, error_code) (
  increase(cluster_health_monitor_checker_result_total{status="Unhealthy"}[5m])
) >= 3
```

## Error-code overview

| Checker | Error codes |
|---|---|
| DNS | `ServiceNotReady`, `PodsNotReady`, `ServiceTimeout`, `PodTimeout`, `ServiceError`, `PodError`, `LocalDNSTimeout`, `LocalDNSError` |
| Pod startup | `PodCreationError`, `PodCreationTimeout`, `PodStartupDurationExceeded`, `RequestFailed`, `RequestTimeout`, `StorageClassNotFound`, `KarpenterNodePoolCRDNotFound` |
| API server | `APIServerCreateError`, `APIServerCreateTimeout`, `APIServerGetError`, `APIServerGetTimeout`, `APIServerDeleteError`, `APIServerDeleteTimeout` |
| Metrics Server | `MetricsServerUnavailable`, `MetricsServerTimeout` |
| Azure Policy | `AzurePolicyEnforcementMissing` |

An `Unknown` result can carry a checker-specific error code. When Pod startup has node provisioning enabled but the Karpenter NodePool CRD is missing, it reports `status="Unknown"` with `error_code="KarpenterNodePoolCRDNotFound"` without creating a synthetic pod. Uncoded Unknown results and execution errors use `error_code="Unknown"`.

For example:

```text
cluster_health_monitor_checker_result_total{checker_type="PodStartup",checker_name="PodStartup",status="Unknown",error_code="KarpenterNodePoolCRDNotFound"} 1
```

Inspect logs for the underlying reason:

```bash
kubectl -n kube-system logs deployment/cluster-health-monitor
```

## Operational interpretation

- A healthy CoreDNS aggregate result means both the Service path and every discovered ready endpoint succeeded.
- A healthy per-pod DNS result applies only to that endpoint and configured domain.
- A healthy Pod startup result includes startup-duration and TCP-connectivity success.
- A healthy API server result includes persistent create, get, and delete operations.
- A healthy Metrics Server result means the Node metrics list API responded.
- A healthy Azure Policy result means the checker observed expected policy enforcement.

For checker mechanics and side effects, see [Continuous checks](index.md).
