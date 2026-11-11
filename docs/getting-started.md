# Getting started

> [!IMPORTANT]
> All features are in preview and intended for AKS. Review the supplied images, permissions, configuration, and optional behavior before relying on them in your environment.

## Prerequisites

- An AKS cluster
- `kubectl` configured for the target cluster
- Permission to create the resources in the selected manifests
- Linux nodes for Check Node Health

The examples assume this repository is the current directory.

## Choose an installation

Install everything to monitor cluster control plane components and check individual nodes. If you need only one of these capabilities, install that feature on its own:

1. [Install everything](#install-everything)
2. [Install only continuous checks](#install-only-continuous-checks)
3. [Install only Check Node Health](#install-only-check-node-health)

## Install everything

This installs both features plus the optional external `HealthCheckRequest` and `HealthSignal` CRDs. Review the [node-check defaults and precautions](#install-only-check-node-health) before installing.

```bash
kubectl apply -k manifests/base
```

The bundle does not enable `HealthCheckRequest` reconciliation. See the [integration guide](check-node-health/index.md#healthcheckrequest-integration).

### Verify the installation

```bash
kubectl -n kube-system rollout status deployment/cluster-health-monitor
kubectl -n kube-system rollout status deployment/checknodehealth-controller
```

Continue with [continuous-check metrics](continuous-checks/index.md#metrics-and-results) or [request a node check](check-node-health/index.md#manual-request).

### Remove everything

Delete node-check requests before removing the controller so their finalizers and checker pods can be cleaned up:

```bash
kubectl delete checknodehealth --all
kubectl delete -k manifests/base
```

## Install only continuous checks

Choose continuous checks to monitor cluster control plane components.

### Install

Install only the continuous-check subsystem:

```bash
kubectl apply -k manifests/base/cluster-health-monitor
```

This creates the `cluster-health-monitor` Deployment, its service account and RBAC, its ConfigMap, and the Azure Blob StorageClass used by optional CSI checks.

### Verify the Deployment

```bash
kubectl -n kube-system rollout status deployment/cluster-health-monitor
kubectl -n kube-system get pod -l app=cluster-health-monitor
```

See the [continuous-check guide](continuous-checks/index.md) for checker behavior, configuration, and metrics.

### Remove

```bash
kubectl delete -k manifests/base/cluster-health-monitor
```

## Install only Check Node Health

Choose Check Node Health when you need a health verdict for one specific node.

### Install

Install only the Check Node Health subsystem:

```bash
kubectl apply -k manifests/base/checknodehealth-controller
```

This creates the `CheckNodeHealth` CRD, controller Deployment, service accounts, and RBAC.

The base enables automatic checks for new and rebooted nodes, `NodeHealthy` publication on non-GPU nodes, and intrusive GPU checks. `HealthCheckRequest` reconciliation remains disabled.

GPU checks claim all advertised GPUs while running, and external remediation may consume `NodeHealthy`. Review [controller settings](check-node-health/index.md#controller-settings) and [GPU checks](check-node-health/index.md#gpu-checks) before installing and adjust enabled capabilities as needed.

### Verify the controller

```bash
kubectl -n kube-system rollout status deployment/checknodehealth-controller
kubectl -n kube-system get pod -l app=checknodehealth-controller
kubectl get crd checknodehealths.clusterhealthmonitor.azure.com
```

Next:

- [Request a node check](check-node-health/index.md#manual-request).
- [Read results](check-node-health/index.md#checknodehealth-status) and [controller metrics](check-node-health/index.md#prometheus-metrics).

### Remove

Delete any `CheckNodeHealth` resources before removing the controller so their finalizers and checker pods can be cleaned up:

```bash
kubectl delete checknodehealth --all
kubectl delete -k manifests/base/checknodehealth-controller
```
