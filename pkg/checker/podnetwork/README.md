# PodNetwork Checker

The PodNetwork Checker validates pod-to-pod network connectivity (CNI networking) and service network functionality by communicating with CoreDNS pods.

## Overview

This checker ensures that:
1. Pod-to-pod networking is functioning correctly across nodes
2. Cluster DNS service is accessible and working
3. CoreDNS pods are reachable from other nodes

## Key Features

- **dnsPolicy: Default**: Uses default DNS policy to ensure API server connectivity is independent of CoreDNS
- **Cross-node testing**: Only tests CoreDNS pods running on different nodes
- **Multiple validation points**: Tests both individual CoreDNS pod IPs and cluster DNS service IP
- **Intelligent failure detection**: Distinguishes between conclusive and inconclusive test results

## Check Logic

### CoreDNS Pod Discovery
1. Lists all CoreDNS pods using label selector `k8s-app=kube-dns` in `kube-system` namespace
2. Filters out pods that are:
   - Not in Running state
   - Running on the same node as the checker
   - Running on the subject node being tested
   - Missing IP addresses

### DNS Query Testing
1. **Pod-to-Pod Test**: Performs DNS query for `kubernetes.default.svc` directly against each eligible CoreDNS pod IP
2. **Cluster DNS Test**: Performs the same DNS query using the cluster DNS service IP from kube-dns service

### Result Evaluation

| Scenario | Pod-to-Pod | Cluster DNS | CoreDNS Pods | Result | Reason |
|----------|------------|-------------|--------------|---------|---------|
| Success | ✅ | ✅ | ≥1 | **Healthy** | Both connectivity types work |
| Pod failure (multiple) | ❌ | Any | >1 | **Unhealthy** | Pod-to-pod connectivity broken |
| Pod failure (single/zero) | ❌ | Any | ≤1 | **Unknown** | Insufficient pods for conclusive test |
| Service failure | ✅ | ❌ | ≥1 | **Unhealthy** | Cluster DNS service broken |
| API failure | N/A | N/A | N/A | **Unknown** | Cannot reach API server |

## Usage

```go
import (
    "github.com/Azure/cluster-health-monitor/pkg/checker/podnetwork"
    "k8s.io/client-go/kubernetes"
)

// Create checker
clientset := kubernetes.NewForConfig(config)
checker := podnetwork.NewPodNetworkChecker(
    clientset,
    "worker-node-1",    // Node being tested
    "10.244.1.100",     // Checker pod IP
)

// Run check
result := checker.Check(context.Background())

switch result.Status {
case checker.StatusHealthy:
    // Pod-to-pod and cluster DNS working
case checker.StatusUnhealthy:
    // Network connectivity issues detected
case checker.StatusUnknown:
    // Inconclusive test or API server issues
}
```

## Configuration

### Environment Variables
- **DNS_TIMEOUT**: DNS query timeout (default: 10s)
- **COREDNS_NAMESPACE**: CoreDNS namespace (default: kube-system)
- **COREDNS_SELECTOR**: CoreDNS label selector (default: k8s-app=kube-dns)

### DNS Policy
The checker pod **must** use `dnsPolicy: Default` to ensure it can connect to the API server even if CoreDNS is failing.

```yaml
apiVersion: v1
kind: Pod
spec:
  dnsPolicy: Default  # Critical for API server independence
  containers:
  - name: pod-network-checker
    # ...
```

## Troubleshooting

### Common Issues

1. **No eligible CoreDNS pods**
   - Check if CoreDNS pods are running: `kubectl get pods -n kube-system -l k8s-app=kube-dns`
   - Ensure CoreDNS pods are distributed across multiple nodes

2. **API server connection failures**
   - Verify checker pod can reach API server
   - Check if `dnsPolicy: Default` is set
   - Verify RBAC permissions for listing pods and services

3. **All CoreDNS pods failing**
   - Check CoreDNS pod logs: `kubectl logs -n kube-system -l k8s-app=kube-dns`
   - Verify CNI networking is functioning
   - Check firewall rules between nodes

4. **Cluster DNS service failures**
   - Check kube-dns service: `kubectl get svc -n kube-system kube-dns`
   - Verify service endpoints: `kubectl get endpoints -n kube-system kube-dns`
   - Test service connectivity manually

### Debugging Commands

```bash
# Check CoreDNS pods
kubectl get pods -n kube-system -l k8s-app=kube-dns -o wide

# Check kube-dns service
kubectl get svc -n kube-system kube-dns

# Test DNS manually from a pod
kubectl run test-pod --rm -it --image=busybox -- nslookup kubernetes.default.svc

# Check node networking
kubectl get nodes -o wide
```

## Error Codes

- `ErrNoCoreDNSPods`: No CoreDNS pods available for testing
- `ErrAPIServerConnection`: Failed to connect to Kubernetes API server
- `ErrAllCoreDNSPodsFailed`: All CoreDNS pod connections failed
- `ErrClusterDNSServiceFailed`: Cluster DNS service query failed
- `ErrInsufficientCoreDNSPods`: Insufficient CoreDNS pods for conclusive testing
- `ErrDNSQueryTimeout`: DNS query timed out