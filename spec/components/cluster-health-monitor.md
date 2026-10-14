# Component: Cluster Health Monitor

## 1. What it is

This component runs in the overlay and periodically runs checkers to monitor the control-plane components, exposing the results as Prometheus metrics.

## 2. How to run it

### Command line

The binary takes two flags:

| Flag       | Default                                      | Purpose                                   |
|------------|----------------------------------------------|-------------------------------------------|
| `-config`  | `/etc/cluster-health-monitor/config.yaml`    | Path to the YAML config file.             |
| `-v`       | `0`                                          | klog verbosity (the deployment uses `-v=3`). |

### Configuration

The config file is a YAML document with a single top-level `checkers` list. Each entry is one checker, explained inline below:

```yaml
checkers:
    # Every checker has these four fields:
    #   name     - unique instance name; becomes a metric label
    #   type     - which checker: DNS | PodStartup | APIServer | MetricsServer | AzurePolicy
    #   interval - how often it runs (Go duration)
    #   timeout  - deadline for a single run: the scheduler runs the checker
    #              with a context that is cancelled after this duration. If the
    #              run has not finished by then, its context is cancelled and
    #              the run is considered failed. It does not stop the next
    #              scheduled run, which still fires on the next `interval`.
    # Some types take an extra <type>Config block.

  - name: "InternalCoreDNS"
    type: "DNS"
    interval: "10s"
    timeout: "5s"
    dnsConfig:
      domain: "kubernetes.default.svc.cluster.local"
      target: "CoreDNS"          # CoreDNS | CoreDNSPerPod | LocalDNS
      queryTimeout: "2s"

  - name: "PodStartup"
    type: "PodStartup"
    interval: "1m"
    timeout: "30s"
    podStartupConfig:
      syntheticPodNamespace: "kube-system"
      syntheticPodLabelKey: "cluster-health-monitor.azure.com/checker-synthetic"
      syntheticPodStartupTimeout: "5s"
      maxSyntheticPods: 10
      tcpTimeout: "2s"

  - name: "APIServer"
    type: "APIServer"
    interval: "30s"
    timeout: "5s"
    apiServerConfig:
      namespace: "kube-system"
      labelKey: "cluster-health-monitor.azure.com/checker-apiserver"
      mutateTimeout: "1s"
      readTimeout: "1s"
      maxObjects: 10

  - name: "MetricsServer"      # no extra config block
    type: "MetricsServer"
    interval: "30s"
    timeout: "10s"

  - name: "AzurePolicy"        # no extra config block
    type: "AzurePolicy"
    interval: "1m"
    timeout: "10s"
```

Each checker runs independently on its own `interval`, each run bounded by its `timeout`. See `manifests/base/cluster-health-monitor/configmap.yaml` for the full shipped config.

## 3. Output: metrics

The **only output is Prometheus metrics**, served over HTTP:

```
GET http://<pod>:9800/metrics
```

Each checker run increments a counter. Two counters are exposed:

### `cluster_health_monitor_checker_result_total`
Incremented once per checker run.

| Label          | Value                                                        |
|----------------|-------------------------------------------------------------|
| `checker_type` | The `type` from config (`DNS`, `PodStartup`, …).            |
| `checker_name` | The `name` from config (the instance).                      |
| `status`       | `Healthy`, `Unhealthy`, or `Unknown`.                       |
| `error_code`   | `Result.Detail.Code` when unhealthy; `Healthy`/`Unknown` sentinel otherwise. |

### `cluster_health_monitor_pod_health_result_total`
Per-pod variant, used by the per-pod CoreDNS checks. Same labels as above, plus:

| Label           | Value                               |
|-----------------|-------------------------------------|
| `pod_namespace` | Namespace of the checked pod.       |
| `pod_name`      | Name of the checked pod.            |

### How to read the output

- A run that **passes** → `status="Healthy"`.
- A run that **fails a check** → `status="Unhealthy"`, with `error_code` naming the failure.
- A run that **errors out** (e.g. timeout, unexpected error) → `status="Unknown"`.


Example scrape output:

```
cluster_health_monitor_checker_result_total{checker_type="DNS",checker_name="InternalCoreDNS",status="Healthy",error_code="Healthy"} 42
cluster_health_monitor_checker_result_total{checker_type="DNS",checker_name="ExternalCoreDNS",status="Unhealthy",error_code="LookupTimeout"} 3
cluster_health_monitor_checker_result_total{checker_type="PodStartup",checker_name="PodStartup",status="Healthy",error_code="Healthy"} 10
cluster_health_monitor_pod_health_result_total{checker_type="DNS",checker_name="InternalCoreDNSPerPod",pod_namespace="kube-system",pod_name="coredns-abc",status="Healthy",error_code="Healthy"} 42
```
