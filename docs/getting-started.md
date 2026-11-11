# Getting started

Choose and install the feature that answers your health question. Continuous checks and Check Node Health are independent; you do not need to enable both.

> [!IMPORTANT]
> All features are in preview and intended for AKS. Review the supplied images, permissions, configuration, and optional behavior before relying on them in your environment.

## Prerequisites

- An AKS cluster
- `kubectl` configured for the target cluster
- Permission to create the resources in the selected Kustomize base
- Linux nodes for Check Node Health

The examples assume this repository is the current directory.

## Path 1: continuous checks

Choose continuous checks to monitor cluster control plane components through DNS, synthetic pod startup, Kubernetes API server, and Metrics Server checks.

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

### Read metrics

The supplied manifests expose port 9800 on the container but do not create a Service or ServiceMonitor.

```bash
kubectl -n kube-system port-forward deployment/cluster-health-monitor 9800:9800
```

In another terminal:

```bash
curl http://127.0.0.1:9800/metrics
```

Each checker first runs after its configured interval. The default intervals range from 10 seconds to one minute, so allow at least one minute plus execution time for initial results. LocalDNS instances are not registered when the monitor pod's resolver configuration does not use LocalDNS.

Next:

- Review [what continuous checks run](continuous-checks/index.md).
- Customize [continuous-check configuration](continuous-checks/configuration.md).
- Learn how to [query continuous-check results](continuous-checks/results.md).

### Remove

```bash
kubectl delete -k manifests/base/cluster-health-monitor
```

## Path 2: Check Node Health

Choose Check Node Health when you need a health verdict for one specific node.

### Install

Install only the Check Node Health subsystem:

```bash
kubectl apply -k manifests/base/checknodehealth-controller
```

This creates:

- The `CheckNodeHealth` CRD
- The controller Deployment
- Controller and checker-pod service accounts
- Controller and checker-pod RBAC

The base installation supports manual requests, automatically checks eligible new and rebooted nodes, publishes `NodeHealthy` on standard nodes, and emits Warning Events for non-passing supported-node checks. Intrusive GPU checks are enabled with a separate GPU image, including for automatic boot checks. `HealthCheckRequest` reconciliation remains disabled.

Review [automatic checks and condition publication](check-node-health/running-checks.md) and [GPU checks](check-node-health/gpu-checks.md) before installing: GPU benchmarks claim all advertised GPUs, and external remediation may consume `NodeHealthy`. To disable a capability, remove its enable flag from the controller arguments in a Kustomize overlay. Removing `-enable-gpu-checks` limits runs to base checks; removing `-enable-node-reboot-check` disables automatic boot checks and Node-controller condition cleanup.

### Verify the controller

```bash
kubectl -n kube-system rollout status deployment/checknodehealth-controller
kubectl -n kube-system get pod -l app=checknodehealth-controller
kubectl get crd checknodehealths.clusterhealthmonitor.azure.com
```

### Request a node check

Select a Linux node:

```bash
kubectl get nodes -l kubernetes.io/os=linux
```

Replace `<node-name>` and create a request:

```bash
kubectl apply -f - <<EOF
apiVersion: clusterhealthmonitor.azure.com/v1alpha1
kind: CheckNodeHealth
metadata:
  name: manual-node-check
spec:
  nodeRef:
    name: <node-name>
EOF
```

Watch the run:

```bash
kubectl get checknodehealth manual-node-check --watch
```

Inspect the complete result:

```bash
kubectl get checknodehealth manual-node-check -o yaml
```

The `HEALTHY` printer column is `Unknown` while the check is running and becomes `True`, `False`, or remains `Unknown` when the run reaches a terminal result. `status.finishedAt` identifies completion.

Next:

- Review [what Check Node Health runs](check-node-health/index.md).
- Learn about [manual and automatic triggers](check-node-health/running-checks.md).
- Learn how to [consume CRs, metrics, Events, and the optional standard-node condition](check-node-health/results.md).
- Review [intrusive GPU checks](check-node-health/gpu-checks.md) before enabling them.

### Read controller metrics

The supplied manifests expose the controller-runtime metrics endpoint on port 8080 without creating a Service or ServiceMonitor:

```bash
kubectl -n kube-system port-forward deployment/checknodehealth-controller 8080:8080
```

In another terminal:

```bash
curl http://127.0.0.1:8080/metrics
```

### Remove

Delete any `CheckNodeHealth` resources before removing the controller so their finalizers and checker pods can be cleaned up:

```bash
kubectl delete checknodehealth --all
kubectl delete -k manifests/base/checknodehealth-controller
```

## Install the repository bundle

To install both features plus the optional external `HealthCheckRequest` and `HealthSignal` CRDs:

```bash
kubectl apply -k manifests/base
```

The bundle does not enable the corresponding `HealthCheckRequest` controller flag. See [automatic triggers](check-node-health/running-checks.md#automatic-triggers).

## Customize an installation

Create a Kustomize overlay rather than editing the base manifests directly. Use the subsystem base you need as the overlay resource, then patch images, configuration, controller arguments, and environment variables.

The repository's `manifests/overlays/test` directory is intended for tests and local development, not as a production configuration.
