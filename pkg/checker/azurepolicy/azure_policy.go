// Package azurepolicy provides a checker for Azure Policy webhook validations.
package azurepolicy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
)

// WarningCapture provides access to captured warning headers
// This interface mainly exists so that it is possible to use a mock implementation in unit tests.
type WarningCapture interface {
	GetWarnings() []string
}

// warningCapturingHandler implements rest.WarningHandler to capture warnings
type warningCapturingHandler struct {
	mu       sync.Mutex
	warnings []string
}

// HandleWarningHeader captures warning headers
func (w *warningCapturingHandler) HandleWarningHeader(_ int, _ string, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.warnings = append(w.warnings, text)
}

// GetWarnings returns all captured warning headers
func (w *warningCapturingHandler) GetWarnings() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Return a copy of the slice to avoid race conditions
	warnings := make([]string, len(w.warnings))
	copy(warnings, w.warnings)
	return warnings
}

// ClientWithWarningCaptureFactory creates Kubernetes clients with warning capture capability
// This interface mainly exists so that it is possible to use a mock implementation in unit tests.
type ClientWithWarningCaptureFactory interface {
	CreateClientWithWarningCapture(restConfig *rest.Config) (kubernetes.Interface, WarningCapture, error)
}

// defaultClientFactory implements ClientWithWarningCaptureFactory
type defaultClientFactory struct{}

func (f *defaultClientFactory) CreateClientWithWarningCapture(restConfig *rest.Config) (kubernetes.Interface, WarningCapture, error) {
	warningHandler := &warningCapturingHandler{
		warnings: []string{},
	}

	config := rest.CopyConfig(restConfig)
	config.WarningHandler = warningHandler

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	return client, warningHandler, nil
}

// AzurePolicyChecker implements the Checker interface for Azure Policy checks.
type AzurePolicyChecker struct {
	name          string
	timeout       time.Duration
	restConfig    *rest.Config // used by the client factory to create a Kubernetes client with warning capture handler.
	clientFactory ClientWithWarningCaptureFactory
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
		name:          config.Name,
		timeout:       config.Timeout,
		restConfig:    restConfig,
		clientFactory: &defaultClientFactory{},
	}, nil
}

func (c *AzurePolicyChecker) Name() string {
	return c.name
}

func (c *AzurePolicyChecker) Type() config.CheckerType {
	return config.CheckTypeAzurePolicy
}

func (c *AzurePolicyChecker) Run(ctx context.Context) {
	result, err := c.check(ctx)
	checker.RecordResult(c, result, err)
}

// check executes the Azure Policy check by doing a dry run creation of a test pod that violates default AKS Deployment Safeguards policies.
// Currently, it is specifically trying to violate the "Ensure cluster containers have readiness or liveness probes configured" policy.
// If azure policy is running, we are expecting a response with warning headers or an error indicating the policy violations. The headers
// are mainly expected to be present when the policy enforcement is set to "Audit". The errors are mainly expected to be present when the
// policy enforcement is set to "Deny". That said, if a policy has recently had its enforcement mode changed, it is possible to receive
// both an error and warning headers in the response.
func (c *AzurePolicyChecker) check(ctx context.Context) (*checker.Result, error) {
	// Create client with warning capture
	client, warningCapture, err := c.clientFactory.CreateClientWithWarningCapture(c.restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Perform dry-run creation to trigger Azure Policy validation. We do not actually want to create the pod, just validate the policy.
	_, err = client.CoreV1().Pods("default").Create(timeoutCtx, c.createTestPod(), metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("dry run request to create pod timed out: %w", err)
		}
		if c.hasAzurePolicyViolation(err.Error()) {
			return checker.Healthy(), nil
		}
	}

	for _, warning := range warningCapture.GetWarnings() {
		if c.hasAzurePolicyViolation(warning) {
			return checker.Healthy(), nil
		}
	}
	return checker.Unhealthy(ErrCodeAzurePolicyEnforcementMissing, "no Azure Policy violations detected"), nil
}

// createTestPod creates a test pod with restricted labels to trigger Azure Policy warnings
func (c *AzurePolicyChecker) createTestPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-test-pod-%d", c.name, time.Now().Unix()),
			// The default configuration of azure-policy is not evaluated in the "kube-system" namespace. However, pod creation requests are
			// rejected by the API server before azure policy can be evaluated if attempting to perform an operation without the necessary
			// permission. There is a role to create pods in the "default" namespace which is why we are using it.
			Namespace: "default",
			Labels: map[string]string{
				"kubernetes.azure.com": "restricted", // Intentionally using a restricted label to trigger potential policy violations
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:  "synthetic",
					Image: "mcr.microsoft.com/azurelinux/base/nginx:1.25.4-4-azl3.0.20250702",
				},
			},
		},
	}
}

// hasAzurePolicyViolation checks if a string contains Azure Policy violation patterns
func (c *AzurePolicyChecker) hasAzurePolicyViolation(message string) bool {
	// Sample warning:
	// Warning: [azurepolicy-k8sazurev1restrictedlabels-4a872f727137b85dcf39] Label <{\"kubernetes.azure.com\"}> is reserved for AKS use only
	//
	// Sample error:
	// Error from server (Forbidden): admission webhook "validation.gatekeeper.sh" denied the request: [azurepolicy-k8sazurev1restrictedlabels-4a872f727137b85dcf39] Label <{\"kubernetes.azure.com\"}> is reserved for AKS use only
	azurePolicyString := "azurepolicy"
	azurePolicyMatchers := []string{
		"is reserved for AKS use only",
	}

	for _, matcher := range azurePolicyMatchers {
		if strings.Contains(message, azurePolicyString) && strings.Contains(message, matcher) {
			return true
		}
	}
	return false
}
