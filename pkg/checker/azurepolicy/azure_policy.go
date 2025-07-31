// Package azurepolicy provides a checker for Azure Policy webhook validations.
package azurepolicy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

// AzurePolicyChecker implements the Checker interface for Azure Policy checks.
type AzurePolicyChecker struct {
	name       string
	timeout    time.Duration
	kubeClient kubernetes.Interface
	restConfig *rest.Config
}

// warningCapturingTransport wraps an HTTP transport to capture warning headers
type warningCapturingTransport struct {
	base     http.RoundTripper
	warnings []string
}

func (w *warningCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := w.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	// Capture warning headers from the response
	if warnings := resp.Header.Values("Warning"); len(warnings) > 0 {
		w.warnings = append(w.warnings, warnings...)
	}

	return resp, err
}

func Register() {
	checker.RegisterChecker(config.CheckTypeAzurePolicy, buildAzurePolicyChecker)
}

// buildAzurePolicyChecker creates a new AzurePolicyChecker instance.
func buildAzurePolicyChecker(config *config.CheckerConfig, kubeClient kubernetes.Interface) (checker.Checker, error) {
	// Get the rest config - we need this to create a custom client with warning capture
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	return &AzurePolicyChecker{
		name:       config.Name,
		timeout:    config.Timeout,
		kubeClient: kubeClient,
		restConfig: restConfig,
	}, nil
}

func (c AzurePolicyChecker) Name() string {
	return c.name
}

func (c AzurePolicyChecker) Type() config.CheckerType {
	return config.CheckTypeAzurePolicy
}

// Run executes the Azure Policy check.
func (c AzurePolicyChecker) Run(ctx context.Context) (*types.Result, error) {
	// Create a test pod similar to: kubectl run pause-pod --image=gcr.io/google_containers/pause:3.2 --restart=Never
	testPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-test-pod-%d", c.name, time.Now().Unix()),
			Namespace: "default", // Using default namespace for simplicity
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:  "pause-pod",
					Image: "gcr.io/google_containers/pause:3.2",
					// Intentionally no liveness or readiness probes to trigger Azure Policy warnings
				},
			},
		},
	}

	// Create a custom client with warning capture transport
	warningTransport := &warningCapturingTransport{
		base:     http.DefaultTransport,
		warnings: []string{},
	}

	// Clone the rest config and use our custom transport
	restConfig := rest.CopyConfig(c.restConfig)
	restConfig.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		warningTransport.base = rt
		return warningTransport
	}

	// Create a new client with the warning-capturing transport
	customClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return types.Unhealthy(errCodeAzurePolicyUnexpected, fmt.Sprintf("failed to create custom client: %v", err)), nil
	}

	// Perform dry-run creation with timeout
	dryRunCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	_, err = customClient.CoreV1().Pods("default").Create(dryRunCtx, testPod, metav1.CreateOptions{
		DryRun: []string{metav1.DryRunAll},
	})

	// Check for timeout
	if err != nil && dryRunCtx.Err() == context.DeadlineExceeded {
		return types.Unhealthy(errCodeAzurePolicyDryRunTimeout, "dry-run pod creation timed out"), nil
	}

	// Even if there's an error, we still want to check for warnings
	// The pod creation might fail for other reasons, but Azure Policy warnings should still be captured

	// Check if we captured any Azure Policy warnings
	hasAzurePolicyWarnings := c.hasAzurePolicyWarnings(warningTransport.warnings)

	if hasAzurePolicyWarnings {
		// Azure Policy is working - it detected the missing probes and issued warnings
		return types.Healthy(), nil
	} else {
		// No Azure Policy warnings detected - this means Azure Policy might not be working
		return types.Unhealthy(errCodeAzurePolicyNoWarning, "no Azure Policy warnings detected for pod without probes"), nil
	}
}

// hasAzurePolicyWarnings checks if any of the warnings are from Azure Policy
func (c AzurePolicyChecker) hasAzurePolicyWarnings(warnings []string) bool {
	azurePolicyPatterns := []string{
		"azurepolicy-k8sazurev2containerenforceprob",
		"has no <livenessProbe>",
		"has no <readinessProbe>",
		"Required probes:",
		"azurepolicy-k8sazurev3containerlimits",
		"has no resource limits",
	}

	for _, warning := range warnings {
		for _, pattern := range azurePolicyPatterns {
			if strings.Contains(warning, pattern) {
				return true
			}
		}
	}

	return false
}

// containsAzurePolicyWarning checks if the error contains Azure Policy warnings.
func (c AzurePolicyChecker) containsAzurePolicyWarning(_ error) bool {
	// TODO
	return false
}
