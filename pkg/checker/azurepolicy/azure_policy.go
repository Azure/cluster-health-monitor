// Package azurepolicy provides a checker for Azure Policy webhook validations.
package azurepolicy

import (
	"context"
	"errors"
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

const syntheticPodImage = "mcr.microsoft.com/azurelinux/base/nginx:1.25.4-4-azl3.0.20250702"

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
// If azure policy is running, we are expecting a warning in the response. This warning is expected to be present regardless of if the
// policy is in "Audit" or "Deny" mode and regardless of whether the request returns an error.
func (c AzurePolicyChecker) Run(ctx context.Context) (*types.Result, error) {
	// Create client with warning capture
	warningTransport := &warningCapturingTransport{
		base:     http.DefaultTransport,
		warnings: []string{},
	}

	client, err := c.createWarningCaptureClient(warningTransport)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Perform dry-run creation to trigger Azure Policy validation
	testPod := c.createTestPod()
	_, err = client.CoreV1().Pods("default").Create(timeoutCtx, testPod, metav1.CreateOptions{
		DryRun: []string{metav1.DryRunAll}, // TODOcarlosalv unit test ensure dry runs only
	})

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.Unhealthy(errCodeAzurePolicyTimeout, "timed out pod creation request"), nil
		}
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
			Name: fmt.Sprintf("%s-test-pod-%d", c.name, time.Now().Unix()),
			// The default configuration of azure-policy is not evaluated in the "kube-system" namespace. However, pod creation requests are
			// rejected by the API server before azure policy can be evaluated if attempting to perform an operation without the necessary
			// permission. There is a role to create pods in the "default" namespace which is why we are using it.
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:  "synthetic",
					Image: syntheticPodImage,
					// Intentionally no liveness or readiness probes to trigger Azure Policy warnings
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
// TODO carlosalv: Do we want to change matchers or use regex?
func (c AzurePolicyChecker) hasAzurePolicyWarnings(warnings []string) bool {
	// Sample warning:
	// Warning: [azurepolicy-k8sazurev2containerenforceprob-74321cbd58a88a12c510] Container <pause> in your Pod <pause> has no <livenessProbe>. Required probes: ["readinessProbe", "livenessProbe"]
	azurePolicyMatchers := []string{
		"azurepolicy-k8sazurev2containerenforceprob",
		"has no <livenessProbe>",
		"has no <readinessProbe>",
	}

	for _, warning := range warnings {
		for _, pattern := range azurePolicyMatchers {
			if strings.Contains(warning, pattern) {
				return true
			}
		}
	}

	return false
}
