# Continuous checks

Continuous checks monitor cluster control plane components on recurring schedules and expose results as cumulative Prometheus counters.

- [Installation](#installation)
- [Configuration and scheduling](#configuration-and-scheduling)
- [Checkers](#checkers)
- [Apply configuration changes](#apply-configuration-changes)
- [Metrics and results](#metrics-and-results)
- [Permissions and side effects](#permissions-and-side-effects)

> [!IMPORTANT]
> Configuration, metrics, labels, and error codes are in preview and may change between versions.

## Installation

See [Getting started](../getting-started.md#install-only-continuous-checks) for installation, verification, and removal commands.

The Deployment reads `/etc/cluster-health-monitor/config.yaml` from the `cluster-health-monitor-config` ConfigMap at startup. The [base ConfigMap](../../manifests/base/cluster-health-monitor/configmap.yaml) contains the full shipped configuration.

## Configuration and scheduling

The YAML configuration requires a non-empty `checkers` list. Each instance has these fields:

| Field | Description |
|---|---|
| `name` | Non-empty, unique, case-sensitive identifier used in logs and the `checker_name` metric label. RFC 1123 syntax is not enforced. |
| `type` | Checker implementation. Documented types are `DNS`, `PodStartup`, `APIServer`, and `MetricsServer`. |
| `interval` | How often the checker runs, using Go duration syntax such as `10s`, `1m`, or `1m30s`. Must be greater than zero. |
| `timeout` | Maximum duration of one run, using Go duration syntax. Must be greater than zero. |

Instances run independently and can run concurrently with other instances. The first run begins after one `interval`, not immediately at startup. Runs of the same instance do not intentionally overlap: if one lasts longer than its interval, the next pending ticker event is handled after it returns.

Use the [base ConfigMap](../../manifests/base/cluster-health-monitor/configmap.yaml) as a configuration example and reference for defaults. Instances can be added, removed, or renamed.

## Checkers

### DNS

A `DNS` instance requires `dnsConfig`:

| Field | Description |
|---|---|
| `domain` | Required DNS name to resolve. |
| `target` | Required: `CoreDNS`, `CoreDNSPerPod`, or `LocalDNS`. |
| `queryTimeout` | Required timeout per lookup. Must be greater than zero and less than the checker `timeout`. |

| Target | Behavior |
|---|---|
| `CoreDNS` | Query the configured domain through the `kube-dns` Service IP and directly through every ready CoreDNS endpoint. Healthy requires all queries to succeed. |
| `CoreDNSPerPod` | Query endpoints directly and emit a separate result for each CoreDNS pod instead of an aggregate result. Endpoint-discovery failures and endpoints without a Pod target are logged without recording a result. |
| `LocalDNS` | Query through the AKS LocalDNS address `169.254.10.11`. Registered only when the monitor pod's `/etc/resolv.conf` contains that address; otherwise skipped at startup without results. |

A missing sample is not a Healthy verdict. A per-pod result applies only to that endpoint and configured domain.

### Pod startup

The recurring `PodStartup` checker creates a synthetic pod, waits for its container to run, measures startup duration minus image-pull duration, and tests TCP connectivity. It garbage-collects old synthetic resources before each run and removes test resources afterwards.

It requires `podStartupConfig`:

| Field | Description |
|---|---|
| `syntheticPodNamespace` | Required valid namespace for synthetic pods and optional PVCs. |
| `syntheticPodLabelKey` | Required qualified label key identifying synthetic resources. |
| `syntheticPodStartupTimeout` | Required healthy startup-duration threshold after subtracting image-pull duration. Must be greater than zero. |
| `maxSyntheticPods` | Required maximum matching pods that may already exist. Must be greater than zero; reaching the limit prevents another pod from being created. |
| `tcpTimeout` | Required timeout per TCP attempt. Must be greater than zero. |
| `tcpMaxRetries` | Number of retries after the initial TCP attempt. Must be zero or greater. |
| `tcpRetryInterval` | Must be zero with no retries, or greater than zero when retries are enabled. |
| `enableNodeProvisioningTest` | Optional Karpenter NodePool test that schedules the pod onto a newly provisioned node. Disabled in the base configuration. |
| `enabledCSIs` | Optional list of CSI-backed volumes to attach. Disabled in the base configuration. |

The result is Unhealthy if pod creation fails or times out, adjusted startup duration meets or exceeds the threshold, TCP attempts are exhausted, or a required StorageClass is missing. Infrastructure or client errors that do not establish a health failure produce Unknown.

If node provisioning is enabled but the Karpenter NodePool CRD is missing, the checker creates no synthetic pod and reports Unknown with `KarpenterNodePoolCRDNotFound`.

#### Optional CSI checks

Each `enabledCSIs` entry requires:

| Field | Description |
|---|---|
| `type` | `azureFile`, `azureDisk`, or `azureBlob`. |
| `storageClass` | Existing StorageClass name. Must be a valid DNS subdomain and unique in the list. |

An explicitly present list must not be empty. For example, add this to `podStartupConfig`:

```yaml
enabledCSIs:
  - type: azureDisk
    storageClass: managed-csi
  - type: azureFile
    storageClass: azurefile-csi
```

The checker validates the StorageClasses and creates PVC-backed test resources. Ensure the service account has the required permissions and the StorageClasses exist.

### API server

`APIServer` performs real create, get, and delete operations on an empty ConfigMap, validating API reachability and the backing storage path. Creation is not a dry run. The checker garbage-collects old test ConfigMaps and enforces a retained-object limit.

It requires `apiServerConfig`:

| Field | Description |
|---|---|
| `namespace` | Required namespace for test ConfigMaps. |
| `labelKey` | Required qualified label key identifying test ConfigMaps. |
| `mutateTimeout` | Required timeout per create/delete operation. Must be less than the checker `timeout`. |
| `readTimeout` | Required timeout for get operations. Must be less than the checker `timeout`. |
| `maxObjects` | Required maximum matching test ConfigMaps that may already exist. Must be greater than zero. |

The supplied RBAC grants these operations in `kube-system`; update RBAC if you change the namespace.

### Metrics Server

`MetricsServer` lists Node metrics through `metrics.k8s.io/v1beta1`. A successful response is Healthy; a timeout or unavailable metrics API is Unhealthy.

It uses only the common configuration fields, with no checker-specific object. The cluster must expose the metrics API and the service account must be able to list Node metrics.

## Apply configuration changes

Update the ConfigMap and restart the Deployment after applying changes because configuration is read only at startup:

```bash
kubectl -n kube-system rollout restart deployment/cluster-health-monitor
kubectl -n kube-system rollout status deployment/cluster-health-monitor
```

## Metrics and results

Metrics are available on port 9800 at `/metrics`:

```bash
kubectl -n kube-system port-forward deployment/cluster-health-monitor 9800:9800
```

In another terminal:

```bash
curl http://127.0.0.1:9800/metrics
```

### Counters and labels

| Metric | Recorded outcomes |
|---|---|
| `cluster_health_monitor_checker_result_total` | Aggregate checker outcomes. |
| `cluster_health_monitor_pod_health_result_total` | Per-pod DNS outcomes from `CoreDNSPerPod`, instead of the aggregate counter. |

Both counters use:

| Label | Meaning |
|---|---|
| `checker_type` | Implementation type; default instances use `DNS`, `PodStartup`, `APIServer`, and `MetricsServer`. |
| `checker_name` | Configured instance name. |
| `status` | `Healthy` when criteria pass, `Unhealthy` for a detected failure, or `Unknown` when a reliable verdict cannot be produced. |
| `error_code` | `Healthy` for healthy results; the checker-specific code for Unhealthy or explicitly coded Unknown results; otherwise `Unknown`. |

The per-pod counter adds `pod_namespace` and `pod_name` from the endpoint's Pod reference.

For example:

```text
cluster_health_monitor_checker_result_total{checker_type="APIServer",checker_name="APIServer",status="Unhealthy",error_code="APIServerGetTimeout"} 1
```

### Querying and alerting

Counters accumulate outcomes since process startup; they are not current-state gauges. Use `increase` or `rate` over a window containing multiple expected runs, and distinguish detected failures from inconclusive results. Handle process restarts and missing scrapes separately.

### Error codes and troubleshooting

| Checker | Error codes |
|---|---|
| DNS | `ServiceNotReady`, `PodsNotReady`, `ServiceTimeout`, `PodTimeout`, `ServiceError`, `PodError`, `LocalDNSTimeout`, `LocalDNSError` |
| Pod startup | `PodCreationError`, `PodCreationTimeout`, `PodStartupDurationExceeded`, `RequestFailed`, `RequestTimeout`, `StorageClassNotFound`, `KarpenterNodePoolCRDNotFound` |
| API server | `APIServerCreateError`, `APIServerCreateTimeout`, `APIServerGetError`, `APIServerGetTimeout`, `APIServerDeleteError`, `APIServerDeleteTimeout` |
| Metrics Server | `MetricsServerUnavailable`, `MetricsServerTimeout` |

Inspect logs for checker failures or configuration validation errors:

```bash
kubectl -n kube-system logs deployment/cluster-health-monitor
```

## Permissions and side effects

The supplied RBAC permits the service account to:

- Read the `kube-dns` Service and EndpointSlices.
- Create, list, read, and delete synthetic pods and read their events in `kube-system`.
- Create, list, read, and delete test ConfigMaps in `kube-system`.
- Make dry-run Pod create requests in `default`.
- Read the resource metrics API.
- Manage Karpenter NodePools and PVCs for optional node-provisioning and CSI tests.
