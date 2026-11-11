# Cluster Health Monitor

Cluster Health Monitor provides preview health-checking features for Azure Kubernetes Service (AKS). It can monitor the control plane continuously and run targeted health checks against individual nodes.

> [!IMPORTANT]
> All features in this repository are in preview. Configuration, metrics, result fields, error codes, and supported checks may change between versions.

## Two independent features

Continuous checks and Check Node Health are separate features. You can install, configure, run, and consume either feature without enabling the other.

| | Continuous checks | Check Node Health |
|---|---|---|
| Scope | Cluster services and synthetic workloads | One Kubernetes node |
| Execution | Long-running scheduled checkers | One checker pod per request |
| Trigger | Per-checker interval | `CheckNodeHealth` creation or an automatic trigger |
| Configuration | ConfigMap checker list | Controller flags, images, and request resources |
| Results | Prometheus metrics | CR status, metrics, Warning Events for non-passing supported-node checks, and an optional standard-node condition |
| Required together? | No | No |

### Continuous checks

Continuous checks run independently on configured intervals and validate:

- CoreDNS and LocalDNS resolution
- Synthetic pod startup and pod connectivity
- API server create, read, and delete operations
- Metrics Server availability
- Azure Policy enforcement

Start with [Continuous checks](docs/continuous-checks/index.md).

### Check Node Health

Check Node Health runs a set of checks against a specific Linux node. Each `CheckNodeHealth` resource represents one run. Runs can be requested manually or created by optional automatic triggers.

Base node checks validate:

- Whether the node can start the checker pod
- Pod-to-pod and Service networking through CoreDNS

Optional intrusive GPU checks validate GPU count, host and peer bandwidth, and NCCL all-reduce correctness and bandwidth on explicitly supported NVIDIA GPU SKUs.

Start with [Check Node Health](docs/check-node-health/index.md).

## Choose a feature

Use **continuous checks** when you want recurring signals about cluster-level dependencies or synthetic workload behavior.

Use **Check Node Health** when you want a verdict for a particular node, need to react to node lifecycle events, or want non-passing results reported on the target Node.

See [Getting started](docs/getting-started.md) for separate installation and verification paths.

## Install the bundled preview

The repository-wide base installs both features and the optional `HealthCheckRequest` and `HealthSignal` CRDs:

```bash
kubectl apply -k manifests/base/
```

The base manifests enable intrusive GPU checks and configure a separate GPU checker image. The binary defaults GPU checks to off, but the supplied Deployment explicitly enables them. Review [GPU checks](docs/check-node-health/gpu-checks.md) before requesting a run on a GPU node.

Automatic node-reboot checks, `HealthCheckRequest` reconciliation, and standard-node `NodeHealthy` publication remain disabled in the base manifests. Warning Events for non-passing supported-node checks do not require an additional feature flag.

To remove the bundled installation:

```bash
kubectl delete -k manifests/base/
```

For independent installation commands, see [Getting started](docs/getting-started.md).

## Documentation

- [Overview and architecture](docs/overview.md)
- [Getting started](docs/getting-started.md)
- [Continuous checks](docs/continuous-checks/index.md)
  - [Configuration](docs/continuous-checks/configuration.md)
  - [Consuming results](docs/continuous-checks/results.md)
- [Check Node Health](docs/check-node-health/index.md)
  - [Running checks](docs/check-node-health/running-checks.md)
  - [Consuming results](docs/check-node-health/results.md)
  - [GPU checks](docs/check-node-health/gpu-checks.md)
- [Development and testing](docs/development.md)
- [Component specifications](spec/README.md) (behavioral source of truth)
- [Security](SECURITY.md)

## Support boundaries

- The project is intended for AKS.
- Node checks support Linux nodes only.
- The supplied manifests use preview images and defaults. Review and customize them before relying on them in your environment.
- The manifests expose metrics on container ports but do not create a Service, ServiceMonitor, or PodMonitor. The documented access method is `kubectl port-forward`.

## Contributing

See [Development and testing](docs/development.md) for unit tests, end-to-end tests, and local Kind workflows.
