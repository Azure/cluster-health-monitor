# Cluster Health Monitor

A Kubernetes cluster health monitoring tool that checks various aspects of cluster health, including DNS resolution and pod startup time.

## Overview

Cluster Health Monitor runs as a Kubernetes deployment and exposes metrics about the health of your cluster through a Prometheus endpoint.

## Release new version to MCC

### 1. Create a new tag
```bash
tag=v0.0.x
git tag ${tag}
git push origin ${tag}
```

### 2. Build and push image
![1](https://github.com/user-attachments/assets/180087f1-eed4-44b7-8293-96dcf372f0f7)
![2](https://github.com/user-attachments/assets/cf58301a-0f56-4364-9d0a-2a8ece93a963)
![3](https://github.com/user-attachments/assets/53f25313-bc56-4c35-972d-4ac2e1fd4da7)


## Deployment

### Deploying Base Manifests

To deploy the Cluster Health Monitor using the base manifests:

```bash
kubectl apply -k manifests/base/
```

### Removing Deployment

To remove the base deployment:

```bash
kubectl delete -k manifests/base/
```

### Customizing Deployment

For custom deployments, create your own overlay in `manifests/overlays/` and change the directory to the directory containing `kustomization.yaml`, e.g., `manifests/overlays/test`.

## Testing

### Running Unit Tests

To run unit tests:

```bash
make test-unit
```

### Running End-to-End (E2E) Tests

To run E2E tests, ensure [Ginkgo](https://onsi.github.io/ginkgo/#getting-started) is installed and run:

```bash
# Full E2E test: set up cluster, run tests, clean up cluster.
make test-e2e
```

This command supports the following environment variables:

- `E2E_SKIP_CLUSTER_SETUP=true` - Skip cluster setup and use existing Kind cluster.
- `E2E_SKIP_CLUSTER_CLEANUP=true` - Skip cluster cleanup after tests.
- `KIND_CLUSTER_NAME=<name>` - Customize the name of the Kind cluster to use or create.

#### Setting Up a Kind Cluster for E2E Testing

To set up a Kind cluster for E2E tests:

```bash
make kind-setup-e2e
```

This creates a Kind cluster and loads the necessary images without deploying the application. You can also set `KIND_CLUSTER_NAME` to use a custom cluster name.

See [useful commands](#useful-commands) for other commands that can be used once the test environment is set up.

### Local Testing with Kind

Kind (Kubernetes IN Docker) is used to create a local Kubernetes cluster for testing and development.

#### Prerequisites

- [Docker](https://www.docker.com/)
- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)

#### Setting Up a Local Environment

To set up a local test environment with Kind:

```bash
make kind-test-local
```

This command:

1. Creates a Kind cluster.
1. Builds a Docker image for the cluster health monitor.
1. Loads the image into the Kind cluster.
1. Applies the Kubernetes manifests using the test overlay.

### Useful Commands

Once your test environment is set up, you can use the following commands:

- **Set kubectl context**:

  ```bash
  make kind-export-kubeconfig
  ```

- **Redeploy after changes**:

  ```bash
  make kind-redeploy
  ```

  This rebuilds the image, loads it into the Kind cluster, and redeploys the application.

- **Clean up**:

  ```bash
  make kind-delete-cluster
  ```

  This deletes the Kind cluster when you're done testing.
