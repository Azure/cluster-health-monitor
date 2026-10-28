# Component: Node Controller (NodeReboot)

## 1. What it is

This component is a Kubernetes controller that watches `Node` objects and automatically requests a health check when a node is new or has rebooted. It does this by creating a `CheckNodeHealth` (CNH) resource; the [CheckNodeHealth controller](checknodehealth-controller.md) runs the check.

It also cleans up the `kubernetes.azure.com/NodeHealthy` condition on Nodes when no check has confirmed it recently, or when it does not apply to the node (GPU nodes).

## 2. How to run it

The controller runs in the same binary and manager as the CheckNodeHealth controller (`/controller`) and shares its flags, leader election and metrics endpoint.

| Flag                        | Default | Purpose |
|-----------------------------|---------|---------|
| `-enable-node-reboot-check` | `false` | Enable this controller. When off, nothing in this spec happens. |

There are no environment variables specific to this controller.

## 3. Input

The controller watches `Node` objects. It is triggered when a Node is created or updated; deleted Nodes are ignored.

It also reads the `kube-system/coredns` Deployment to decide whether a check can run (§5.3).

## 4. Output

### 4.1 `CheckNodeHealth` resources

```yaml
apiVersion: clusterhealthmonitor.azure.com/v1alpha1
kind: CheckNodeHealth
metadata:
  name: boot-1a2b3c4d-aks-nodepool1-0
spec:
  nodeRef:
    name: aks-nodepool1-0
```

- **Name:** `boot-<hash>-<node name>` (§5.1).
- **No owner reference, no annotations.** These CNHs are ordinary (non-upgrade-request) checks and are eligible for GPU checks. Cleanup is done by the CheckNodeHealth controller (6–7h after creation); this controller never deletes CNHs.

### 4.2 Node conditions

The controller only removes `kubernetes.azure.com/NodeHealthy`; it never adds or changes it (§5.4).

## 5. Behavior

The controller handles only Linux nodes (by `kubernetes.io/os`; an empty OS counts as not Linux). Other nodes get no CNH, no annotation and no condition cleanup.

### 5.1 Detecting reboots

A node's `status.nodeInfo.bootID` changes every time the node boots. The controller remembers the last boot ID it handled in the Node annotation `checknodehealth.clusterhealthmonitor.azure.com/last-boot-id`, and treats a node as rebooted when the current boot ID differs from the annotation.

| Annotation | Result |
|------------|--------|
| Equal to current boot ID | Already handled. Nothing. |
| Differs from current boot ID | Rebooted. Create a CNH (§5.3), then update the annotation. |
| Missing | Not seen before. See §5.2. |

A node without a boot ID yet is skipped until it reports one.

**Each boot is checked at most once:**

- The annotation is updated only after the CNH exists. If the CNH cannot be created yet (§5.3), the annotation keeps the old value, so the reboot is still detected on the next reconcile.
- The CNH name is derived from the boot ID: `boot-<hash>-<node name>`, where `<hash>` is the first 8 hex characters of the SHA-256 of the boot ID. The name is cut to 253 characters if needed; the hash comes first, so it always survives. If a CNH with that name already exists, it counts as created, so repeated reconciles never produce duplicates.

### 5.2 Existing nodes when the feature is first enabled

When the feature is first enabled, every existing node already has a boot ID but no `last-boot-id` annotation, so the controller cannot tell whether it rebooted. It decides by node age (`metadata.creationTimestamp`):

| Node age | Meaning | Result |
|----------|---------|--------|
| ≥ **5m** | Existing node | Write the annotation with the current boot ID only. No CNH. |
| < 5m | New node | Create a CNH (§5.3), then write the annotation. |

**Why:** enabling the feature must not trigger a check on every node in the cluster. From then on, the annotation is the baseline, and the node gets a CNH only when it reboots (§5.1).

The same applies to any node whose annotation is lost, for example removed by hand.

**A new node must be ready within 5m of creation, or it is not checked.** The annotation is written only after the CNH is created, so a new node that is still waiting (§5.3) has no annotation. Once it is 5m old, it is treated as an existing node: the annotation is written and no CNH is created. For new nodes, the 5m age limit therefore applies before the 10m wait in §5.3. This includes waiting for CoreDNS.

### 5.3 Waiting for the node to be ready (10m timeout)

A node that has just booted, whether new or rebooted, is not ready for a check right away: the kubelet, networking and cluster add-ons need time to come up. A check run during this warm-up fails for reasons unrelated to the node's health and would mark a healthy node unhealthy. So the controller waits until the node is ready before it creates the CNH.

The node counts as ready when both hold:

1. **The node itself is ready:**
   - **Karpenter node** (`karpenter.sh/capacity-type` non-empty): `karpenter.sh/initialized=true`. The controller does not check `Ready` itself; Karpenter sets this label only after the node is `Ready=True`, its startup taints are removed and its resources are registered, so it is a stricter signal than `Ready` for a new node. The label stays after a reboot, so a rebooted Karpenter node already counts as ready even while it is still `Ready=False`, and its CNH can be created before the node is back up.
   - **Other nodes:** `Ready=True`.
2. **CoreDNS is ready:** the `kube-system/coredns` Deployment exists and `readyReplicas` equals desired replicas (desired defaults to 1). A Deployment with 0 desired replicas is an error and the reconcile is retried with backoff.

   **Why CoreDNS and not just the node:** the checker pod's `PodNetwork` check depends on cluster DNS. A node being `Ready` does not mean CoreDNS is. In a newly created cluster, for example, nodes can become `Ready` before CoreDNS is fully deployed. A check run then fails because DNS is not available yet, not because the node is unhealthy, so the result would be wrong.

Until then, the node is re-processed every **30s**. The controller waits at most **10m** for the node to become ready. For a new node, the wait is cut short at 5m after creation (§5.2).

After 10m the controller stops the 30s retries, but it does not give up on this boot: it creates no CNH and leaves the annotation unchanged. If a later Node update finds the node ready, the CNH is created then. The 10m matches Karpenter Node Auto Repair's unready timeout.

Note: while CoreDNS is not Ready but the node is `Ready=True`, retries continue every 30s without the 10m bound.

### 5.4 `NodeHealthy` creation and cleanup

**Creation:** this controller never creates or updates `kubernetes.azure.com/NodeHealthy`. The CheckNodeHealth controller writes it when a CNH finishes (see [checknodehealth-controller.md §4.2](checknodehealth-controller.md#42-node-conditions--enable-node-condition)).

**Cleanup:** this controller only removes it. Whenever a Linux node is processed, `NodeHealthy` is removed from the Node's status when:

- **The node is a GPU node.** GPU nodes never get `NodeHealthy` (see [checknodehealth-controller.md §4.2](checknodehealth-controller.md#42-node-conditions--enable-node-condition)); one left from before the node was a GPU node is removed right away. This controller identifies a GPU node only by the label `kubernetes.azure.com/accelerator=nvidia`; it does not see allocatable `nvidia.com/gpu`. A GPU node without the label keeps its condition until it goes stale.
- **It is stale:** no check has confirmed it for more than **30m**, measured from `lastHeartbeatTime` (or `lastTransitionTime` if the heartbeat is unset).

**Why:** the condition must reflect the node's real state, so once the node recovers it must flip back. Normally this happens on its own: the node is rebooted, this controller creates a new CNH (§5.1), and the CheckNodeHealth controller sets the condition back to `True`. But the check may not run, for example because the node did not become ready within the wait (§5.3) and was never updated afterwards, or the CNH was deleted before it finished. The CheckNodeHealth controller only writes the condition when a CNH finishes, so without a new check an unhealthy condition would stay on the node indefinitely. The cleanup is a safety net that removes the stale condition in these cases.

After writing the boot-ID annotation, the node is re-processed after **30m**. Beyond that, cleanup happens when the Node is next updated, so removal is not guaranteed at exactly 30m.

Other conditions on the Node are never touched.

## 6. E2E

Cases live in `test/e2e/nodereboot_test.go` (label `node-reboot`) and run on Kind with `-enable-node-reboot-check`.

| # | Case | Setup | Expected |
|---|------|-------|----------|
| 1 | Annotation initialized | All nodes Ready | Every node has `last-boot-id` equal to its `status.nodeInfo.bootID`. |
| 2 | No CNH for existing nodes | Nodes older than 5m; delete `boot-*` CNHs and annotations; restart the controller | Annotations are rewritten; no `boot-*` CNH is created. |
| 3 | Reboot triggers a check | Set a stale `last-boot-id` on a node; restart the controller | A `boot-*` CNH is created for the node; the annotation is updated to the current boot ID; the CNH finishes. |
| 4 | Windows node skipped | Fake Ready Windows node with a boot ID | No CNH is ever created for it. |
| 5 | No duplicates | Node with an up-to-date annotation | No additional `boot-*` CNH is created over time. |
