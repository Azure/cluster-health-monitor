// Package utils contains small helpers for inspecting Kubernetes Node objects
// that are shared across controllers.
package utils

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// nodeOSLabel is the well-known label carrying the node operating system.
	nodeOSLabel = "kubernetes.io/os"

	// osLinux is the value of nodeOSLabel / NodeInfo.OperatingSystem for Linux nodes.
	// The cluster health monitor only supports Linux nodes.
	osLinux = "linux"
)

// IsSupported reports whether the cluster health monitor supports running checks
// against the node. Only Linux nodes are supported; a node is considered Linux
// when either the well-known kubernetes.io/os label or NodeInfo.OperatingSystem
// reports "linux". The check is positive (allowlist) so nodes with an unknown or
// unset OS are treated as unsupported rather than silently running Linux checks
// against them.
func IsSupported(node *corev1.Node) bool {
	if strings.EqualFold(node.Labels[nodeOSLabel], osLinux) {
		return true
	}
	return strings.EqualFold(node.Status.NodeInfo.OperatingSystem, osLinux)
}
