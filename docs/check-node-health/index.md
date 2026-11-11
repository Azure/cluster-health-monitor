# Check Node Health

Check Node Health is a preview feature that evaluates one AKS node per `CheckNodeHealth` custom resource.

Each custom resource represents one run. Runs can be created manually or by automatic triggers.

## When to use Check Node Health

Use this feature when you need to:

- Request a health verdict for a particular node.
- Check new or rebooted nodes automatically.
- Integrate with an external `HealthCheckRequest`.
- Expose node-health outcomes as Prometheus metrics.
- Report non-passing supported-node runs as Warning Events on their target Nodes.
- Optionally publish `NodeHealthy` on standard nodes for scheduling or remediation.
- Run intrusive GPU validation on supported NVIDIA GPU nodes.

Only Linux nodes are supported.

## Install

```bash
kubectl apply -k manifests/base/checknodehealth-controller
kubectl -n kube-system rollout status deployment/checknodehealth-controller
```

The base installation enables manual requests, automatic checks for eligible new and rebooted nodes, standard-node `NodeHealthy` publication, Warning Events for non-passing supported-node checks, and intrusive GPU checks. The supplied Deployment explicitly enables boot checks, condition publication, and GPU checks, while the binary defaults these capabilities to off. `HealthCheckRequest` reconciliation remains disabled.

Automatic GPU-node checks can claim all advertised GPUs, and standard-node conditions can trigger external remediation. See [Running checks](running-checks.md) for requests and controller configuration.

## Execution lifecycle

For each `CheckNodeHealth` resource, the controller:

1. Reads the target Node.
2. Completes unsupported non-Linux targets as Unknown without running a checker pod.
3. Determines whether the target is a standard or NVIDIA GPU node.
4. Chooses the standard or GPU checker image and expected checks.
5. Creates a temporary checker pod in `kube-system`, pinned to the target node.
6. Records the pod's ability to start as `PodStartup`.
7. Receives checker results through `CheckNodeHealth.status.results`.
8. Adds Unknown results for expected checks that did not report.
9. Computes the overall `Healthy` condition.
10. Emits aggregate metrics.
11. Emits a Warning Event on the target Node if the overall result is Unhealthy or Unknown.
12. Optionally publishes `NodeHealthy` if the target is a standard node.
13. Removes any stale `NodeHealthy` from a GPU node.
14. Deletes the checker pod.

The controller deletes resources on the first hourly resync after they reach six hours of age, normally six to seven hours after creation.

## Base checks

### `PodStartup`

The controller owns this result. It tests whether the target node can start all containers in the checker pod.

A container passes startup if it is running or if it terminated after it started. A later application or checker failure does not turn `PodStartup` into Unhealthy because the container runtime successfully started it.

If the pod remains Pending until its run timeout and no checker-reported result proves that the container ran, `PodStartup` is Unhealthy.

### `PodNetwork`

The checker pod:

1. Lists ready, running CoreDNS pods that are not on the target node.
2. Reads the `kube-dns` Service IP.
3. Sends DNS queries directly to the eligible CoreDNS Pod IPs.
4. Sends a DNS query through the Service IP.

The result is:

| Service path | At least one direct Pod path | Result |
|---|---|---|
| Succeeds | Succeeds | Healthy |
| Succeeds | All fail | Unhealthy |
| Fails | At least one succeeds | Unhealthy |
| Fails | All fail | Unhealthy |

If there are not enough eligible CoreDNS pods to perform the off-node test, the result is Unknown.

## Optional GPU checks

When GPU checks are enabled, supported NVIDIA GPU nodes use a separate image and can run:

- GPU count validation
- Host-to-device and device-to-host bandwidth
- GPU-to-GPU peer bandwidth
- NCCL all-reduce correctness and bus bandwidth

The exact set depends on the node's SKU profile. These checks claim all advertised GPUs and are intrusive.

`HealthSignal`-owned upgrade requests run only `PodStartup` and `PodNetwork`, use the standard image and two-minute pod timeout, and skip GPU startup waiting. They do not acquire GPU resources or add missing GPU results. Manual and reboot-triggered requests without that controller owner remain eligible for GPU checks.

Read [GPU checks](gpu-checks.md) before enabling them.

## Time budgets and retries

| Behavior | Value |
|---|---:|
| Checker-pod timeout without GPU checks (including upgrade requests) | 2 minutes |
| GPU checker-pod timeout | 15 minutes |
| Default shared wait for GPU SKU metadata and claimable GPUs | 2 minutes from request creation |
| GPU polling interval during that wait | 30 seconds |
| Checker attempts | 3 |
| Delay between failed attempts | 3 seconds |
| CR cleanup age | 6–7 hours from creation |

Node checker implementations run sequentially inside the checker pod and write each result as it completes. If the pod is interrupted, already-written results remain and missing expected results become Unknown.

## Overall result

The CR's `Healthy` condition is calculated from all expected individual results:

1. Any Unhealthy result produces `Healthy=False` with reason `CheckFailed`.
2. Otherwise, any Unknown result produces `Healthy=Unknown` with reason `CheckUnknown`.
3. If every result is Healthy, the condition is `Healthy=True` with reason `CheckPassed`.

An unsupported node completes with `Healthy=Unknown` and reason `CheckUnsupported`.

These reason strings describe current behavior, not a stable API contract. Automations should use condition status and individual result status/error codes rather than parsing reasons or messages.

See [Consuming Check Node Health results](results.md) for complete examples.

## Next steps

- [Create requests and enable automatic triggers](running-checks.md)
- [Consume CR status, metrics, Events, and the optional standard-node condition](results.md)
- [Review intrusive GPU checks](gpu-checks.md)
- [Return to the project README](../../README.md)
