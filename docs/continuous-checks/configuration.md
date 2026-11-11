# Continuous-check configuration

The continuous-check Deployment reads YAML from:

```text
/etc/cluster-health-monitor/config.yaml
```

The supplied Deployment mounts this file from the `cluster-health-monitor-config` ConfigMap.

> [!IMPORTANT]
> The configuration schema is in preview and may change between versions.

## Example

```yaml
checkers:
  - name: InternalCoreDNS
    type: DNS
    interval: 10s
    timeout: 5s
    dnsConfig:
      domain: kubernetes.default.svc.cluster.local
      target: CoreDNS
      queryTimeout: 2s

  - name: PodStartup
    type: PodStartup
    interval: 1m
    timeout: 30s
    podStartupConfig:
      syntheticPodNamespace: kube-system
      syntheticPodLabelKey: cluster-health-monitor.azure.com/checker-synthetic
      syntheticPodStartupTimeout: 5s
      maxSyntheticPods: 10
      tcpTimeout: 2s
      tcpMaxRetries: 0
      tcpRetryInterval: 0s

  - name: APIServer
    type: APIServer
    interval: 30s
    timeout: 5s
    apiServerConfig:
      namespace: kube-system
      labelKey: cluster-health-monitor.azure.com/checker-apiserver
      mutateTimeout: 1s
      readTimeout: 1s
      maxObjects: 10

  - name: MetricsServer
    type: MetricsServer
    interval: 30s
    timeout: 10s

```

For the complete default configuration, see `manifests/base/cluster-health-monitor/configmap.yaml`.

## Top-level fields

| Field | Required | Description |
|---|---|---|
| `checkers` | Yes | List of checker instances. At least one checker is required. |

Current validation requires a non-empty list and rejects duplicate names. It does not enforce an upper bound on the number of entries or RFC 1123 syntax for checker names.

## Common checker fields

| Field | Required | Description |
|---|---|---|
| `name` | Yes | Non-empty, unique, case-sensitive identifier used in logs and the `checker_name` metric label. The shipped names use mixed case; RFC 1123 syntax is not enforced. |
| `type` | Yes | Checker implementation. Documented types are `DNS`, `PodStartup`, `APIServer`, and `MetricsServer`. |
| `interval` | Yes | How often the checker runs, using Go duration syntax such as `10s`, `1m`, or `1m30s`. Must be greater than zero. |
| `timeout` | Yes | Maximum duration of one run. Uses Go duration syntax and must be greater than zero. |

The first run begins after one `interval`; checkers do not run immediately when the process starts.

## DNS configuration

A `DNS` checker requires `dnsConfig`.

| Field | Required | Description |
|---|---|---|
| `domain` | Yes | DNS name to resolve. |
| `target` | Yes | One of `CoreDNS`, `CoreDNSPerPod`, or `LocalDNS`. |
| `queryTimeout` | Yes | Timeout for an individual DNS lookup. Must be greater than zero and less than the checker `timeout`. |

Target behavior:

| Target | Behavior | Metric |
|---|---|---|
| `CoreDNS` | Query through the CoreDNS Service and every ready CoreDNS endpoint. | `cluster_health_monitor_checker_result_total` |
| `CoreDNSPerPod` | Query each CoreDNS endpoint and retain the Pod identity. | `cluster_health_monitor_pod_health_result_total` |
| `LocalDNS` | Query through `169.254.10.11`. | `cluster_health_monitor_checker_result_total` |

## Pod startup configuration

A `PodStartup` checker requires `podStartupConfig`.

| Field | Required | Description |
|---|---|---|
| `syntheticPodNamespace` | Yes | Namespace in which synthetic pods and optional PVCs are created. Must be a valid namespace name. |
| `syntheticPodLabelKey` | Yes | Qualified label key used to identify synthetic resources. |
| `syntheticPodStartupTimeout` | Yes | Healthy startup-duration threshold after subtracting image-pull duration. Must be greater than zero. |
| `maxSyntheticPods` | Yes | Maximum matching synthetic pods that may already exist. Must be greater than zero. Reaching the limit prevents the run from creating another pod. |
| `tcpTimeout` | Yes | Timeout for each TCP connection attempt. Must be greater than zero. |
| `tcpMaxRetries` | Yes | Number of retry attempts after the initial TCP attempt. Must be zero or greater. |
| `tcpRetryInterval` | Conditional | Delay between retries. Must be zero when `tcpMaxRetries` is zero and greater than zero when retries are enabled. |
| `enableNodeProvisioningTest` | No | When true, create a Karpenter NodePool and schedule the synthetic pod onto a newly provisioned node. Intended for AKS Automatic clusters. |
| `enabledCSIs` | No | List of CSI-backed volumes to attach to the synthetic pod. Presence enables the listed tests. |

The checker timeout must be greater than:

```text
syntheticPodStartupTimeout
+ (tcpMaxRetries + 1) * tcpTimeout
+ tcpMaxRetries * tcpRetryInterval
```

### CSI configuration

Each `enabledCSIs` entry has:

| Field | Required | Description |
|---|---|---|
| `type` | Yes | One of `azureFile`, `azureDisk`, or `azureBlob`. |
| `storageClass` | Yes | Existing StorageClass used by this test. Must be a valid DNS subdomain and unique within the list. |

An explicitly present `enabledCSIs` list must not be empty.

Example:

```yaml
podStartupConfig:
  syntheticPodNamespace: kube-system
  syntheticPodLabelKey: cluster-health-monitor.azure.com/checker-synthetic
  syntheticPodStartupTimeout: 20s
  maxSyntheticPods: 10
  tcpTimeout: 2s
  tcpMaxRetries: 2
  tcpRetryInterval: 1s
  enabledCSIs:
    - type: azureDisk
      storageClass: managed-csi
    - type: azureFile
      storageClass: azurefile-csi
```

Ensure the service account has the required permissions and the selected StorageClasses exist.

## API server configuration

An `APIServer` checker requires `apiServerConfig`.

| Field | Required | Description |
|---|---|---|
| `namespace` | Yes | Namespace in which test ConfigMaps are created. |
| `labelKey` | Yes | Qualified label key used to identify test ConfigMaps. |
| `mutateTimeout` | Yes | Per-operation timeout for create and delete calls. Must be less than the checker `timeout`. |
| `readTimeout` | Yes | Timeout for the get call. Must be less than the checker `timeout`. |
| `maxObjects` | Yes | Maximum matching test ConfigMaps that may already exist. Must be greater than zero. |

The supplied RBAC grants these operations in `kube-system`. If you change `namespace`, update RBAC accordingly.

## Metrics Server configuration

`MetricsServer` has no checker-specific object. Configure only the common fields:

```yaml
- name: MetricsServer
  type: MetricsServer
  interval: 30s
  timeout: 10s
```

The cluster must expose the `metrics.k8s.io/v1beta1` API and the service account must be able to list Node metrics.

## Apply configuration changes

Prefer a Kustomize overlay that patches the ConfigMap. After applying a changed ConfigMap, restart the Deployment because the process reads configuration only at startup:

```bash
kubectl -n kube-system rollout restart deployment/cluster-health-monitor
kubectl -n kube-system rollout status deployment/cluster-health-monitor
```

Check logs if validation prevents startup:

```bash
kubectl -n kube-system logs deployment/cluster-health-monitor
```

## Related documentation

- [Continuous-check behavior](index.md)
- [Consuming results](results.md)
- [Getting started](../getting-started.md)
