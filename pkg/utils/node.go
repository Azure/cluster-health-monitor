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

	// osWindows is the value of nodeOSLabel / NodeInfo.OperatingSystem for Windows nodes.
	osWindows = "windows"
)

// IsWindows reports whether the node runs Windows, checking both the well-known
// kubernetes.io/os label and NodeInfo.OperatingSystem.
func IsWindows(node *corev1.Node) bool {
	if strings.EqualFold(node.Labels[nodeOSLabel], osWindows) {
		return true
	}
	return strings.EqualFold(node.Status.NodeInfo.OperatingSystem, osWindows)
}
