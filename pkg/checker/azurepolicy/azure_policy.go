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
	warnings := resp.Header.Values("Warning")
	w.warnings = append(w.warnings, warnings...)

	return resp, err
}

func Register() {
	checker.RegisterChecker(config.CheckTypeAzurePolicy, buildAzurePolicyChecker)
}

// buildAzurePolicyChecker creates a new AzurePolicyChecker instance.
func buildAzurePolicyChecker(config *config.CheckerConfig, kubeClient kubernetes.Interface) (checker.Checker, error) {
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

// Run executes the Azure Policy check by doing a dry run creation a test pod that violates default AKS Deployment Safeguards policies.
// If azure policy is running, we are expecting a warning in the response.
func (c AzurePolicyChecker) Run(ctx context.Context) (*types.Result, error) {
	// Create client with warning capture
	warningTransport := &warningCapturingTransport{
		base:     http.DefaultTransport,
		warnings: []string{},
	}

	client, err := c.createWarningCaptureClient(warningTransport)
	if err != nil {
		return types.Unhealthy(errCodeAzurePolicyUnexpected, fmt.Sprintf("failed to create client: %v", err)), nil
	}

	// Create context with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Perform dry-run creation to trigger Azure Policy validation
	testPod := c.createTestPod()
	_, err = client.CoreV1().Pods("default").Create(timeoutCtx, testPod, metav1.CreateOptions{
		DryRun: []string{metav1.DryRunAll}, // TODOcarlosalv unit test ensure dry runs only
	})

	// TODO carlosalv
	// a) test azure policy with enforce mode set to strict. Do we still see warning or just error?
	// b) if necessary, modify to be resilient across both cases
	// c) if there is an error but warning is present, do we want to return success (I think so) but like is this scenario possible?

	// TODO carlosalv
	// a) figure out if we want a timeout specific error code (or just generic fail)
	// b) maybe remove the timeout specific check/logic
	// Check for timeout specifically to provide better error categorization
	if err != nil && timeoutCtx.Err() == context.DeadlineExceeded {
		return types.Unhealthy(errCodeAzurePolicyDryRunTimeout, "dry-run creation timed out"), nil
	}

	if c.hasAzurePolicyWarnings(warningTransport.warnings) {
		return types.Healthy(), nil
	}
	return types.Unhealthy(errCodeAzurePolicyNoWarning, "no Azure Policy warnings detected"), nil
}

// createTestPod creates a test pod without probes to trigger Azure Policy warnings
func (c AzurePolicyChecker) createTestPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-test-pod-%d", c.name, time.Now().Unix()),
			Namespace: "default", // TODOcarlosalv unit test should verify default namespace
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					// TODO carlosalv Might as well use the hardcoded nginx pod from podStartup?
					Name:  "pause-pod",
					Image: "mcr.microsoft.com/oss/kubernetes/pause:3.6", // TODO carlosalv unit test should verify mcr source for image
					// Intentionally no liveness or readiness probes to trigger Azure Policy warnings // TODO carlosalv unit test for lack of probes
				},
			},
		},
	}
}

// createWarningCaptureClient creates a Kubernetes client with warning capture transport
func (c AzurePolicyChecker) createWarningCaptureClient(warningTransport *warningCapturingTransport) (kubernetes.Interface, error) {
	restConfig := rest.CopyConfig(c.restConfig)
	restConfig.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		warningTransport.base = rt
		return warningTransport
	}

	return kubernetes.NewForConfig(restConfig)
}

// hasAzurePolicyWarnings checks if any of the warnings are from Azure Policy
func (c AzurePolicyChecker) hasAzurePolicyWarnings(warnings []string) bool {
	// TODO carlosalv. Verify the warning message and ensure these are correct/comprehensive enough
	azurePolicyPatterns := []string{
		"azurepolicy-k8sazurev2containerenforceprob",
		"has no <livenessProbe>",
		"has no <readinessProbe>",
		"Required probes:",
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
