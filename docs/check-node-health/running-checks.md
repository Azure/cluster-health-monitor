# Running Check Node Health

Each `CheckNodeHealth` custom resource requests one run against one Linux node.

## Manual request

List Linux nodes:

```bash
kubectl get nodes -l kubernetes.io/os=linux
```

Create a request after replacing `<node-name>`:

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

The resource is cluster-scoped; do not set a namespace.

Watch its printer columns:

```bash
kubectl get checknodehealth manual-node-check --watch
```

Wait for `status.finishedAt` to become non-empty:

```bash
kubectl wait \
  --for=jsonpath='{.status.finishedAt}' \
  checknodehealth/manual-node-check \
  --timeout=20m
```

Read the result:

```bash
kubectl get checknodehealth manual-node-check -o yaml
```

See [Consuming results](results.md) for JSONPath examples and status semantics.

## Resource lifecycle

- The controller adds a finalizer so it can clean up the checker pod.
- A temporary pod is created in `kube-system` and pinned to `spec.nodeRef.name`.
- The CR is terminal when `status.finishedAt` is present.
- The checker pod is removed after the terminal status is written.
- CRs are deleted on the first hourly resync after they reach six hours of age, normally six to seven hours after creation.

Delete a request manually when it is no longer needed:

```bash
kubectl delete checknodehealth manual-node-check
```

## Automatic triggers

The base Deployment enables automatic new-node and reboot checks; `HealthCheckRequest` integration remains opt-in. These triggers create ordinary `CheckNodeHealth` resources, with the GPU exemption below for `HealthSignal`-owned requests.

### New nodes and node reboots

The base Deployment enables this controller with:

```text
--enable-node-reboot-check
```

The Node controller stores the last observed boot ID in:

```text
checknodehealth.clusterhealthmonitor.azure.com/last-boot-id
```

Only nodes labeled `kubernetes.io/os=linux` are handled by this controller. Nodes without a boot ID are skipped until one is reported.

It creates a run when:

- A newly observed node is less than five minutes old and has no recorded boot ID.
- An existing node's boot ID changes.

It does not create a run until:

- The `kube-system/coredns` Deployment exists and has all desired replicas Ready. Zero desired replicas is an error, not readiness.
- A non-Karpenter node reports `Ready=True`.
- A Karpenter-managed node (identified by a non-empty `karpenter.sh/capacity-type` label) reports `karpenter.sh/initialized=true`.

The Karpenter path does not separately check `Ready`. The initialized label can survive a reboot, so a rebooted Karpenter node may receive a request before it becomes Ready again.

New nodes must satisfy these prerequisites before they are five minutes old, including time spent waiting for CoreDNS. Once that age is reached without a recorded boot ID, the controller initializes the annotation without creating a run. Older pre-existing nodes receive the same annotation-only initialization, avoiding a check on every node when the feature is first enabled.

For a detected reboot, the controller retries readiness every 30 seconds. It stops proactive retries after the node has been not Ready for more than ten minutes, leaving the old boot-ID annotation unchanged; a later Node update can still trigger the check. For Karpenter nodes without the initialized label, that timeout is measured from node creation. Waiting only for CoreDNS while the node is otherwise ready is not bounded by the ten-minute timeout.

Each generated request is named `boot-<hash>-<node-name>`, using the first eight hex characters of the boot ID's SHA-256 hash. The annotation is updated only after the request exists, and repeated reconciles reuse the same name rather than creating duplicate requests. These requests have no `HealthSignal` owner and remain eligible for GPU checks.

This controller also removes stale `NodeHealthy` conditions and removes them from nodes labeled `kubernetes.azure.com/accelerator=nvidia`. See [condition cleanup](results.md#condition-cleanup) for timing and detection limits, and the [Node controller specification](../../spec/components/node-controller.md) for the full contract.

### `HealthCheckRequest` integration

Enable with:

```text
--enable-health-check-request
```

Install the external request and signal CRDs if they are not already present:

```bash
kubectl apply -k manifests/base/healthcheckrequest-controller
```

The integration watches a cluster-scoped `HealthCheckRequest`, creates a `HealthSignal`, creates a `CheckNodeHealth` owned by that signal, and copies the completed `CheckNodeHealth` conditions into `HealthSignal.status.conditions`.

These requests run only `PodStartup` and `PodNetwork`, even when GPU checks are enabled. A `CheckNodeHealth` whose controller owner reference is a `health.aks.io/v1alpha1` `HealthSignal` uses the standard image and two-minute pod timeout, skips GPU startup waiting, and requests no GPUs. No GPU results are expected or added as missing.

This exemption keeps experimental GPU benchmarks out of upgrade health checks; it does not guarantee completion within an external upgrade deadline. GPU-node classification is preserved, so these runs still cannot publish `NodeHealthy` on GPU nodes. Manual and reboot-triggered requests without this controller owner remain eligible for GPU checks.

Example Node request:

```yaml
apiVersion: health.aks.io/v1alpha1
kind: HealthCheckRequest
metadata:
  name: node-health-request
spec:
  scope: Node
  targetRef:
    name: <node-name>
```

The integration currently expects a Node target even though the external API also defines broader scope values.

Inspect the resulting objects:

```bash
kubectl get healthcheckrequest,healthsignal,checknodehealth
```

Deleting the `HealthCheckRequest` allows Kubernetes garbage collection to remove the owned signal and node-check resource.

### Periodic node checks

Periodic automatic creation of `CheckNodeHealth` resources is planned but is not implemented in the current controller.

## Node Events

No feature flag is required for Node Events. When a run completes with `Healthy=False` or `Healthy=Unknown`, the controller emits one Warning Event on the target Node containing all individual check results. A run that completes with `Healthy=True` emits no Event.

This applies to standard and GPU nodes and is not suppressed by the `NodeHealthy` circuit breaker.

See [Node Events](results.md#node-events) for the Event fields, message format, and commands.

## Optional standard-node condition

The base Deployment enables standard-node `NodeHealthy` publication with:

```text
--enable-node-condition
```

Standard nodes receive the aggregate `kubernetes.azure.com/NodeHealthy` condition.

GPU nodes never receive `NodeHealthy` or any other controller-managed Node condition. If a GPU node already carries `NodeHealthy`, the controller removes it when a check completes and condition publication is enabled. Non-passing GPU checks are reported through CR status, metrics, and Warning Events.

`NodeHealthy` can be consumed by scheduling or remediation systems. AKS uses it internally. Review [Standard-node condition](results.md#standard-node-condition) before deploying. Remove `-enable-node-condition` in an overlay to disable publication.

## Optional GPU execution

GPU checks require:

```text
--enable-gpu-checks
GPU_CHECKER_POD_IMAGE=<gpu-image>
```

The standard `CHECKER_POD_IMAGE` remains required. You can also set:

```text
--gpu-wait=<duration>
```

The default GPU wait is two minutes from request creation, shared between waiting for the node's SKU label and claimable GPUs. A zero value disables waiting; missing prerequisites are reported as Unknown. This wait does not apply to `HealthSignal`-owned upgrade requests.

The supplied base Deployment already enables GPU checks and sets the GPU image. To disable them, remove `-enable-gpu-checks` from its arguments in your overlay.

See [GPU checks](gpu-checks.md) for supported SKUs, resource claims, and result semantics.

## Controller capability table

| Setting | Binary default / base manifest | Effect |
|---|---|---|
| `CHECKER_POD_IMAGE` | Set by the base manifest | Image used for standard checker pods. Required. |
| `GPU_CHECKER_POD_IMAGE` | Unset / set by the base manifest | GPU image. Required when `--enable-gpu-checks` is enabled. |
| `--enable-node-reboot-check` | `false` / enabled | Create checks for new and rebooted nodes, remove stale `NodeHealthy`, and remove `NodeHealthy` from GPU nodes. |
| `--enable-health-check-request` | `false` | Bridge `HealthCheckRequest` through `HealthSignal` to `CheckNodeHealth`. |
| `--enable-node-condition` | `false` / enabled | Publish terminal outcomes as `NodeHealthy` on standard Nodes. GPU Nodes never receive a managed condition. |
| `--enable-gpu-checks` | `false` / enabled | Run intrusive GPU checks on detected NVIDIA GPU nodes, except `HealthSignal`-owned upgrade requests. |
| `--gpu-wait` | `2m` | Shared startup budget for GPU SKU metadata and claimable GPUs before reporting missing prerequisites as Unknown. |
| `--metrics-bind-address` | `:8080` | Controller-runtime metrics address. |
| `--health-probe-bind-address` | `:8081` | Health and readiness probe address. |
| `--leader-elect` | `false` | Enable leader election for multiple controller replicas. |

## Configuring capabilities with Kustomize

Create an overlay that uses `manifests/base/checknodehealth-controller` and patches the controller arguments and environment variables. Boot checks, condition publication, and GPU checks are already enabled. To make runs manual-only and disable Node-condition publication and GPU benchmarks, replace the argument list:

```yaml
spec:
  template:
    spec:
      containers:
        - name: controller
          args:
            - -v=5
            - --metrics-bind-address=:8080
```

Use Kustomize patches rather than editing the base Deployment. Verify the rendered manifest before applying it:

```bash
kubectl kustomize <overlay-directory>
```

The repository's test overlay customizes images and capabilities for local tests and should not be applied as a production configuration.
