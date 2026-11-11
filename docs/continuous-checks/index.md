# Continuous checks

Continuous checks are a standalone preview feature that monitors cluster control plane components on recurring schedules. It is independent of Check Node Health: it does not create `CheckNodeHealth` resources, target individual nodes, or use the Check Node Health controller.

## When to use continuous checks

Use this feature when you want recurring health signals for cluster control plane components through:

- Cluster DNS resolution paths
- Synthetic pod startup and connectivity
- Kubernetes API server read/write behavior
- Metrics Server availability

Each checker emits cumulative Prometheus counters. See [Consuming continuous-check results](results.md).

## Install

```bash
kubectl apply -k manifests/base/cluster-health-monitor
kubectl -n kube-system rollout status deployment/cluster-health-monitor
```

The Deployment reads its checker list from the `cluster-health-monitor-config` ConfigMap at `/etc/cluster-health-monitor/config.yaml`.

See [Configuration](configuration.md) before changing the defaults.

## Scheduling behavior

Every configured checker has:

- A unique `name`
- A checker `type`
- An `interval` controlling how often it runs
- A `timeout` bounding each run

Checker instances are scheduled independently and can run concurrently with other checker instances. A checker does not run immediately at process start: its first run begins after its first configured interval.

A checker runs synchronously within its own schedule. It does not intentionally overlap another run of the same configured checker. If a run lasts longer than its interval, the next pending ticker event is handled after the current run returns.

## Default checker instances

The supplied ConfigMap defines:

| Name | Type | Interval | Purpose |
|---|---|---:|---|
| `InternalCoreDNS` | `DNS` | 10s | Resolve the Kubernetes Service name through the CoreDNS Service and CoreDNS pods. |
| `ExternalCoreDNS` | `DNS` | 10s | Resolve `mcr.microsoft.com` through the CoreDNS Service and CoreDNS pods. |
| `InternalCoreDNSPerPod` | `DNS` | 10s | Record a separate internal-domain result for each CoreDNS pod. |
| `ExternalCoreDNSPerPod` | `DNS` | 10s | Record a separate external-domain result for each CoreDNS pod. |
| `InternalLocalDNS` | `DNS` | 10s | Resolve the Kubernetes Service name through LocalDNS. |
| `ExternalLocalDNS` | `DNS` | 10s | Resolve `mcr.microsoft.com` through LocalDNS. |
| `PodStartup` | `PodStartup` | 1m | Create a synthetic pod, measure startup, and verify TCP connectivity. |
| `APIServer` | `APIServer` | 30s | Create, get, and delete a ConfigMap through the Kubernetes API. |
| `MetricsServer` | `MetricsServer` | 30s | List Node metrics through the resource metrics API. |

These are configured instances, not a fixed required set. You can add, remove, or rename instances within the validation limits.

## Checker behavior

### DNS

The DNS checker supports three targets.

#### `CoreDNS`

This target:

1. Reads the `kube-dns` Service IP.
2. Resolves the configured domain through the Service IP.
3. Reads ready CoreDNS endpoints.
4. Resolves the domain directly through every endpoint address.

The aggregate result is Healthy only when the Service query and every endpoint query succeed.

#### `CoreDNSPerPod`

This target reads CoreDNS endpoints and queries each endpoint directly. It emits a separate `cluster_health_monitor_pod_health_result_total` series for every CoreDNS pod instead of the aggregate checker metric.

Endpoint-discovery failures and endpoints without a Pod target are logged without recording a checker result. A missing sample is not a Healthy verdict.

#### `LocalDNS`

This target resolves the configured domain through the AKS LocalDNS address `169.254.10.11`.

LocalDNS instances are registered only when the monitor pod's `/etc/resolv.conf` contains that address. Otherwise they are skipped at startup and emit no results.

The configured `domain` determines whether an instance checks an internal or external name. “Internal” and “external” are naming conventions in the default ConfigMap, not separate checker implementations.

### Pod startup

The recurring Pod startup checker:

1. Garbage-collects old synthetic resources from earlier runs.
2. Optionally validates configured CSI StorageClasses.
3. Optionally creates PVC-backed test resources.
4. Optionally creates a Karpenter NodePool to test node provisioning.
5. Creates a synthetic pod.
6. Waits for its container to run.
7. Calculates startup duration as creation-to-running time minus image-pull duration.
8. Opens a TCP connection to the pod.
9. Removes the synthetic resources.

The checker reports Unhealthy when:

- A required StorageClass does not exist.
- Synthetic pod creation fails or times out.
- The adjusted startup duration meets or exceeds the configured healthy threshold.
- TCP connectivity exhausts its configured attempts.

Infrastructure or Kubernetes client errors that do not represent a measured health failure are recorded as Unknown.

If node provisioning is enabled but the Karpenter NodePool CRD is not installed, the checker returns Unknown without creating a synthetic pod. The result counter records `status="Unknown"` and `error_code="KarpenterNodePoolCRDNotFound"`.

Do not confuse this recurring synthetic checker with the controller-owned `PodStartup` result in [Check Node Health](../check-node-health/index.md).

### API server

The API server checker performs real create, get, and delete operations on an empty ConfigMap.

The create request intentionally is not a dry run. This validates:

- API server reachability
- Mutating requests
- Read requests
- The API server's backing storage path
- Delete requests

The checker attempts to garbage-collect old ConfigMaps before each run and enforces a maximum number of retained checker objects.

### Metrics Server

The Metrics Server checker lists Node metrics through the `metrics.k8s.io/v1beta1` API.

A successful API response is Healthy. A timeout or unavailable resource metrics API is Unhealthy.

## Permissions and side effects

The supplied RBAC permits the continuous-check service account to:

- Read the `kube-dns` Service and EndpointSlices.
- Create, list, read, and delete synthetic pods and read their events in `kube-system`.
- Create, list, read, and delete test ConfigMaps in `kube-system`.
- Make dry-run Pod create requests in `default`.
- Read the resource metrics API.
- Manage Karpenter NodePool resources used by the optional node-provisioning test.
- Manage PVCs used by optional CSI tests.

Review these permissions and disable checker instances that are not appropriate for your environment.

## Next steps

- [Configure checker instances](configuration.md)
- [Consume Prometheus results](results.md)
- [Return to the project overview](../overview.md)
