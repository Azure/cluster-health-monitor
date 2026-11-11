# Cluster Health Monitor

Cluster Health Monitor provides preview health-checking features for Azure Kubernetes Service (AKS). It can monitor the control plane continuously and run targeted health checks against individual nodes.

> [!IMPORTANT]
> All features in this repository are in preview. Configuration, metrics, result fields, error codes, and supported checks may change between versions. All interfaces are subject to change.

## Two independent features

Continuous checks and Check Node Health are separate features. You can install, configure, run, and consume either feature without enabling the other.

| | Continuous checks | Check Node Health |
|---|---|---|
| Scope | Cluster control plane components | One Kubernetes node |
| Execution | Long-running scheduled checkers | One checker pod per request |
| Trigger | Per-checker interval | `CheckNodeHealth` creation or an automatic trigger |
| Configuration | ConfigMap checker list | Controller flags and custom resource (CR) requests |
| Results | Prometheus metrics | CR status, metrics, Warning Events for non-passing supported-node checks, and an optional standard-node condition |

### Continuous checks

Continuous checks monitor cluster control plane components on configured intervals and validate:

- CoreDNS and LocalDNS resolution
- Synthetic pod startup and pod connectivity
- API server create, read, and delete operations
- Metrics Server availability

Start with [Continuous checks](docs/continuous-checks/index.md).

### Check Node Health

Check Node Health runs a set of checks against a specific Linux node. Each `CheckNodeHealth` resource represents one run. Runs can be requested manually or created by optional automatic triggers.

Base node checks validate:

- Whether the node can start the checker pod
- Pod-to-pod and Service networking through CoreDNS

Optional intrusive GPU checks validate GPU count, host and peer bandwidth, and NCCL all-reduce correctness and bandwidth on explicitly supported NVIDIA GPU SKUs.

Start with [Check Node Health](docs/check-node-health/index.md).

## Choose a feature

Use **continuous checks** when you want continous monitoring of the cluster control plane.

Use **Check Node Health** when you want a health verdict for a particular node.

See [Getting started](docs/getting-started.md) for separate installation and verification paths.

## Install the bundled preview

The repository-wide base installs both the continous monitoring and node health check features, as well as the optional `HealthCheckRequest` and `HealthSignal` CRDs:

```bash
kubectl apply -k manifests/base/
```

The base manifests enable automatic health checks for new and rebooted nodes, including intrusive GPU checks on GPU nodes.

For standard (non-GPU) nodes, the controller publishes each run's aggregate result as the `kubernetes.azure.com/NodeHealthy` Node condition. AKS can use this condition as a signal for node remediation. GPU nodes do not receive this condition.

Review [controller settings](docs/check-node-health/index.md#controller-settings) and [GPU checks](docs/check-node-health/index.md#gpu-checks) before deploying.

To remove the bundled installation:

```bash
kubectl delete -k manifests/base/
```

For independent installation commands, see [Getting started](docs/getting-started.md).

## Documentation

- [Getting started](docs/getting-started.md)
- [Continuous checks](docs/continuous-checks/index.md)
- [Check Node Health](docs/check-node-health/index.md)
- [Development and testing](docs/development.md)
- [Component specifications](spec/README.md) (behavioral source of truth)
- [Security](SECURITY.md)

## Usage notes

- The project is intended for AKS.
- Node checks run on Linux nodes only.
- Check the configuration before deploying and adjust it if needed.

## Contributing

See [Development and testing](docs/development.md) for unit tests, end-to-end tests, and local Kind workflows.
