# Continuous checks

Continuous checks monitor cluster control plane components on recurring schedules and expose results as cumulative Prometheus counters.

- [Installation](#installation)
- [Configuration and scheduling](#configuration-and-scheduling)
- [Checkers](#checkers)
- [Metrics and results](#metrics-and-results)

> [!IMPORTANT]
> Configuration, metrics, labels, and error codes are in preview and may change between versions.

## Installation

See [Getting started](../getting-started.md#install-only-continuous-checks) for installation, verification, and removal commands.

## Configuration and scheduling

The Deployment reads the `cluster-health-monitor-config` ConfigMap at startup. Use the [base ConfigMap](../../manifests/base/cluster-health-monitor/configmap.yaml) for defaults and a configuration example. Its `checkers` list must contain at least one instance:

| Field | Description |
|---|---|
| `name` | Unique instance name used in logs and the `checker_name` metric label. |
| `type` | Checker implementation. Documented types are `DNS`, `PodStartup`, `APIServer`, and `MetricsServer`. |
| `interval` | Time between runs, such as `10s` or `1m`. Must be greater than zero. |
| `timeout` | Maximum duration of one run. Must be greater than zero. |

Instances run independently. Each first runs after its configured interval; runs of the same instance do not overlap.

After changing the ConfigMap, restart the Deployment:

```bash
kubectl -n kube-system rollout restart deployment/cluster-health-monitor
kubectl -n kube-system rollout status deployment/cluster-health-monitor
```

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
| `CoreDNSPerPod` | Query endpoints directly and emit a separate result for each CoreDNS pod. Discovery failures or missing Pod references can leave results absent. |
| `LocalDNS` | Query through `169.254.10.11`. Runs only when the monitor pod's resolver configuration uses that address. |

A missing sample is not a Healthy verdict.

### Pod startup

`PodStartup` creates a synthetic pod, measures startup time excluding image pulls, tests TCP connectivity, and cleans up test resources.

It requires `podStartupConfig`:

| Field | Description |
|---|---|
| `syntheticPodNamespace` | Required namespace for synthetic pods and optional PVCs. |
| `syntheticPodLabelKey` | Required label key identifying synthetic resources. |
| `syntheticPodStartupTimeout` | Required startup-duration threshold, excluding image pulls. Must be greater than zero. |
| `maxSyntheticPods` | Required limit on existing test pods; reaching it prevents another from being created. Must be greater than zero. |
| `tcpTimeout` | Required timeout per TCP attempt. Must be greater than zero. |
| `tcpMaxRetries` | Number of retries after the initial TCP attempt. Must be zero or greater. |
| `tcpRetryInterval` | Must be zero with no retries, or greater than zero when retries are enabled. |
| `enableNodeProvisioningTest` | Optional Karpenter NodePool test that schedules the pod onto a newly provisioned node. |
| `enabledCSIs` | Optional list of CSI-backed volumes to attach. |

Creation failures, startup duration at or above the threshold, exhausted TCP attempts, or missing StorageClasses produce Unhealthy. Inconclusive client or infrastructure errors produce Unknown.

Optional node-provisioning tests require the Karpenter NodePool CRD; otherwise the result is Unknown with `KarpenterNodePoolCRDNotFound`. CSI tests require a non-empty `enabledCSIs` list whose entries specify `type` (`azureFile`, `azureDisk`, or `azureBlob`) and a unique, existing `storageClass`. Both features are disabled in the base configuration.

Before enabling CSI tests, grant the monitor's service account (`kube-system/cluster-health-monitor` in the base installation) these additional permissions, which the base RBAC does not include:

| API group / resource | Verbs | Scope |
|---|---|---|
| `storage.k8s.io` / `storageclasses` | `get` | Cluster-scoped, through a ClusterRole and ClusterRoleBinding. |
| Core (`""`) / `persistentvolumeclaims` | `list`, `create`, `delete` | The configured `syntheticPodNamespace`, through a Role and RoleBinding. |

Without these permissions, CSI checks report Unknown on forbidden API requests even when the StorageClass exists.

### API server

`APIServer` creates, reads, and deletes a test ConfigMap to validate API reachability and backing storage. These are real operations, not dry runs.

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

It uses only the common configuration fields. The cluster must expose the metrics API and the service account must be able to list Node metrics.

## Metrics and results

Metrics are available on port 9800 at `/metrics`:

```bash
kubectl -n kube-system port-forward deployment/cluster-health-monitor 9800:9800
```

In another terminal:

```bash
curl http://127.0.0.1:9800/metrics
```

| Metric | Recorded outcomes |
|---|---|
| `cluster_health_monitor_checker_result_total` | Aggregate checker outcomes. |
| `cluster_health_monitor_pod_health_result_total` | Per-pod DNS outcomes from `CoreDNSPerPod`, instead of the aggregate counter. |

| Label | Meaning |
|---|---|
| `checker_type` | Checker implementation type. |
| `checker_name` | Configured instance name. |
| `status` | `Healthy` when criteria pass, `Unhealthy` for a detected failure, or `Unknown` when a reliable verdict cannot be produced. |
| `error_code` | `Healthy` for healthy results; the checker-specific code for Unhealthy or explicitly coded Unknown results; otherwise `Unknown`. |

The per-pod counter adds `pod_namespace` and `pod_name` from the endpoint's Pod reference.

### Troubleshooting

Inspect logs for checker failures or configuration validation errors:

```bash
kubectl -n kube-system logs deployment/cluster-health-monitor
```

See the [checker source](../../pkg/checker) for error codes and the [base RBAC](../../manifests/base/cluster-health-monitor/rbac.yaml) for permissions.
