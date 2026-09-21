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
// against the node. Only Linux nodes are supported; a node is considered Linux
// when either the well-known kubernetes.io/os label or NodeInfo.OperatingSystem
// reports "linux". The check is positive (allowlist) so nodes with an unknown or
// unset OS are treated as unsupported rather than silently running Linux checks
// against them.
//
// When the node is unsupported the returned reason explains why, suitable for
// surfacing in logs and on the CheckNodeHealth condition. The reason is empty
// when the node is supported. OS is currently the only criterion, but returning
// a reason lets future criteria (e.g. architecture, required labels) report the
// specific cause without every caller hard-coding a message.
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
