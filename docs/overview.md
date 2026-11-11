# Overview

Cluster Health Monitor contains two independent preview features for Azure Kubernetes Service (AKS):

1. **Continuous checks** monitor shared cluster services and synthetic workload behavior on configurable schedules.
2. **Check Node Health** performs a requested set of checks against one specific node.

They share a repository and status vocabulary, but they have separate deployments, configuration models, execution paths, and primary outputs. Neither feature requires the other.

> [!IMPORTANT]
> All features are in preview. Configuration, metrics, result fields, error codes, and supported checks may change between versions.

## Feature comparison

| | Continuous checks | Check Node Health |
|---|---|---|
| Primary question | Are important cluster services and synthetic workload paths working? | Is this particular node healthy? |
| Scope | Cluster-wide services and synthetic resources | One Linux node |
| Process | `cluster-health-monitor` Deployment | `checknodehealth-controller` plus temporary checker pods |
| Trigger | A configured interval for each checker | A `CheckNodeHealth` custom resource |
| Automatic triggers | Not applicable; scheduling is intrinsic to the feature | Optional new-node, reboot, or `HealthCheckRequest` integration; periodic triggers may be added later |
| Configuration | Checker list in a ConfigMap | Controller flags, environment variables, and request resources |
| Primary result | Prometheus counters | `CheckNodeHealth` status |
| Additional results | Logs | Prometheus metrics, Warning Events for non-passing supported-node checks, optional standard-node `NodeHealthy`, and optional `HealthSignal` status |

## Continuous-check architecture

```text
ConfigMap
   |
   v
cluster-health-monitor Deployment
   |
   +-- checker scheduler
          |
          +-- DNS checks
          +-- Pod startup checks
          +-- API server checks
          +-- Metrics Server checks
          +-- Azure Policy checks
   |
   v
Prometheus metrics on :9800/metrics
```

Each configured checker has its own interval and timeout. Checker instances are scheduled independently and emit cumulative Prometheus counters for their outcomes.

Continuous checks do not create `CheckNodeHealth` resources and do not schedule node-health runs.

See [Continuous checks](continuous-checks/index.md).

## Check Node Health architecture

```text
Manual request or optional automatic trigger
   |
   v
CheckNodeHealth custom resource
   |
   v
checknodehealth-controller
   |
   +-- creates a temporary pod pinned to the target node
   |      |
   |      +-- PodNetwork
   |      +-- optional GPU checkers
   |
   +-- evaluates PodStartup
   +-- writes the overall CR condition
   +-- emits node-check metrics
   +-- emits a Warning Event for a non-passing supported-node check
   +-- optionally writes NodeHealthy on a standard node
```

Each `CheckNodeHealth` custom resource represents one run. The controller chooses the appropriate checker pod, waits for results, records missing results as Unknown, computes the overall condition, and removes the checker pod.

The controller can optionally create runs in response to new nodes, node reboots, or external `HealthCheckRequest` resources. Future periodic node checks belong to this trigger layer; they are not part of the continuous-check scheduler.

See [Check Node Health](check-node-health/index.md).

## Shared status vocabulary

Both features use the following health concepts:

| Status | Meaning |
|---|---|
| `Healthy` | The checker ran and its success criteria were met. |
| `Unhealthy` | The checker ran and detected a failed health criterion. |
| `Unknown` | A reliable health verdict could not be produced. This can indicate an execution, environment, support, or reporting problem rather than an unhealthy target. |

Continuous-check results are exposed as metric label values. Check Node Health uses `Healthy`, `Unhealthy`, and `Unknown` for individual results, then maps the overall condition to Kubernetes condition values `True`, `False`, and `Unknown`.

## Installation boundaries

The repository provides a separate Kustomize base for each feature:

- `manifests/base/cluster-health-monitor` installs continuous checks.
- `manifests/base/checknodehealth-controller` installs Check Node Health.
- `manifests/base/healthcheckrequest-controller` installs the external `HealthCheckRequest` and `HealthSignal` CRDs used by an optional Check Node Health integration.

`manifests/base` bundles all three for convenience. Installing the bundle does not enable every optional Check Node Health capability.

## Defaults and opt-in capabilities

| Capability | Base state |
|---|---|
| Continuous scheduled checks | Enabled in the continuous-check Deployment |
| Manual `CheckNodeHealth` requests | Enabled |
| New-node and reboot-triggered node checks | Enabled by the supplied Deployment; disabled by default in the binary |
| `HealthCheckRequest` integration | Disabled |
| Warning Events for non-passing supported-node checks | Enabled |
| Publishing `NodeHealthy` on standard nodes | Enabled by the supplied Deployment; disabled by default in the binary |
| Intrusive GPU checks | Enabled by the supplied Deployment; disabled by default in the binary |

Requests whose `CheckNodeHealth` controller owner is a `health.aks.io/v1alpha1` `HealthSignal` run only base checks, even on GPU nodes. This keeps experimental GPU benchmarks and their startup wait out of upgrade health checks. Manual and reboot-triggered requests without that owner remain eligible for GPU checks.

See [Getting started](getting-started.md) for the installation and verification path for each feature.

## Result consumers

### Continuous checks

Use Prometheus-compatible tooling to scrape or query counters from port 9800. In the supplied manifests, use port-forwarding because no Service or monitoring custom resource is installed.

See [Continuous-check results](continuous-checks/results.md).

### Check Node Health

Use the completed `CheckNodeHealth` status as the authoritative result for an individual run. Metrics provide aggregate fleet-level views. A supported-node run that executes checks and finishes non-passing emits a Warning Event on its target Node. Standard nodes can also publish the optional `NodeHealthy` condition for scheduling or remediation; AKS consumes this condition internally. GPU nodes never receive controller-managed Node conditions.

See [Check Node Health results](check-node-health/results.md).
