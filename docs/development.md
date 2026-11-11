# Development and testing

This page covers contributor workflows. For user installation, see [Getting started](getting-started.md).

## Prerequisites

- Go, using the version declared in `go.mod`
- Docker
- `kubectl`
- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/)
- [Ginkgo](https://onsi.github.io/ginkgo/#getting-started) for end-to-end tests

## Unit tests

Run all non-E2E tests with the race detector:

```bash
make test-unit
```

## End-to-end tests

Run the full E2E workflow, including cluster setup and cleanup:

```bash
make test-e2e
```

The E2E workflow supports:

- `E2E_SKIP_CLUSTER_SETUP=true` to use an existing Kind cluster.
- `E2E_SKIP_CLUSTER_CLEANUP=true` to preserve the cluster after tests.
- `KIND_CLUSTER_NAME=<name>` to select the Kind cluster name.

Set up an E2E Kind cluster and load the required images without deploying the application:

```bash
make kind-setup-e2e
```

## Local Kind environment

Create a three-node Kind cluster, build and load the images, deploy Metrics Server, and apply the test manifests:

```bash
make kind-test-local
```

Use a custom cluster name if needed:

```bash
KIND_CLUSTER_NAME=my-chm-cluster make kind-test-local
```

### Useful commands

Export the Kind kubeconfig:

```bash
make kind-export-kubeconfig
```

Redeploy after code changes:

```bash
make kind-redeploy
```

Port-forward continuous-check metrics:

```bash
kubectl -n kube-system port-forward deployment/cluster-health-monitor 9800:9800
```

Port-forward Check Node Health controller metrics:

```bash
kubectl -n kube-system port-forward deployment/checknodehealth-controller 8080:8080
```

Delete the local cluster:

```bash
make kind-delete-cluster
```

## Build binaries

Build the continuous-check binary:

```bash
make build
```

The output is written under `bin/`.

Run the continuous-check entry point from the host:

```bash
make run-clusterhealthmonitor
```

The process still expects in-cluster Kubernetes configuration, so the Kind deployment workflow is generally more useful for end-to-end local validation.

## Build images

Build and push the standard multi-architecture image:

```bash
REGISTRY=<registry> CLUSTER_HEALTH_MONITOR_IMAGE_VERSION=<tag> make push
```

Build and push the amd64 GPU image:

```bash
REGISTRY=<registry> CLUSTER_HEALTH_MONITOR_IMAGE_VERSION=<tag> make push-gpu
```

## Generated APIs and manifests

See:

- [API generation instructions](../apis/README.md)
- [Controller RBAC generation instructions](../pkg/controller/README.md)

Regenerate derived files when changing API types or kubebuilder RBAC annotations.
