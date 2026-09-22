// Package utils contains small helpers for inspecting Kubernetes Node objects
// that are shared across controllers.
package utils

import (
	"fmt"
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
// against the node. Only Linux nodes are supported;
func IsSupported(node *corev1.Node) (bool, string) {
	os := node.Status.NodeInfo.OperatingSystem
	if v := node.Labels[nodeOSLabel]; v != "" {
		os = v
	}
	if !strings.EqualFold(os, osLinux) {
		if os == "" {
			return false, "node OS is unknown; only Linux is supported"
		}
		return false, fmt.Sprintf("node OS %q is not supported; only Linux is supported", os)
	}
	return true, ""
}
