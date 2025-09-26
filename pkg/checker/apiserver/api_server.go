// Package apiserver provides a checker for the Kubernetes API server.
package apiserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
)

// APIServerChecker implements the Checker interface for API server checks.
type APIServerChecker struct {
	name       string
	config     *config.APIServerConfig
	timeout    time.Duration
	kubeClient kubernetes.Interface
}

func Register() {
	checker.RegisterChecker(config.CheckTypeAPIServer, buildAPIServerChecker)
}

// buildAPIServerChecker creates a new APIServerChecker instance.
func buildAPIServerChecker(config *config.CheckerConfig, kubeClient kubernetes.Interface) (checker.Checker, error) {
	chk := &APIServerChecker{
		name:       config.Name,
		config:     config.APIServerConfig,
		timeout:    config.Timeout,
		kubeClient: kubeClient,
	}
	klog.InfoS("Built APIServerChecker",
		"name", chk.name,
		"config", chk.config,
		"timeout", chk.timeout.String(),
	)
	return chk, nil
}

func (c APIServerChecker) Name() string {
	return c.name
}

func (c APIServerChecker) Type() config.CheckerType {
	return config.CheckTypeAPIServer
}

func (c APIServerChecker) Run(ctx context.Context) {
	result, err := c.check(ctx)
	checker.DefaultRecordResult(c, result, err)
}

// check executes the API server check.
// It creates an empty ConfigMap, gets it, and then deletes it.
// If all operations succeed, the check is considered healthy.
func (c APIServerChecker) check(ctx context.Context) (*types.Result, error) {
	// Garbage collect any leftover ConfigMaps previously created by this checker.
	if err := c.garbageCollect(ctx); err != nil {
		// Logging instead of returning an error to avoid failing the checker run.
		klog.ErrorS(err, "Failed to garbage collect old ConfigMaps")
	}

	// Check if the ConfigMap limit has been reached.
	// Do not run the checker if the maximum number been reached.
	configMapList, err := c.kubeClient.CoreV1().ConfigMaps(c.config.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set(c.configMapLabels())).String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list ConfigMaps: %w", err)
	}
	if len(configMapList.Items) >= c.config.MaxObjects {
		return nil, fmt.Errorf("maximum number of ConfigMaps reached, current: %d, max allowed: %d, delete some ConfigMaps before running the checker again",
			len(configMapList.Items), c.config.MaxObjects)
	}

	// Create ConfigMap.
	createCtx, createCancel := context.WithTimeout(ctx, c.config.MutateTimeout)
	defer createCancel()
	createdConfigMap, err := c.kubeClient.CoreV1().ConfigMaps(c.config.Namespace).Create(createCtx, c.generateConfigMap(), metav1.CreateOptions{})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.Unhealthy(ErrCodeAPIServerCreateTimeout, "timed out while creating ConfigMap"), nil
		}
		return types.Unhealthy(ErrCodeAPIServerCreateError, fmt.Sprintf("failed to create ConfigMap: %v", err)), nil
	}
	// Defer deletion of the created ConfigMap in case of failure later in the function.
	defer func() {
		err := c.kubeClient.CoreV1().ConfigMaps(c.config.Namespace).Delete(ctx, createdConfigMap.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			// Logging instead of returning an error here to avoid failing the checker run.
			klog.ErrorS(err, "Failed to delete ConfigMap", "name", createdConfigMap.Name)
		}
	}()

	// Get ConfigMap.
	getCtx, getCancel := context.WithTimeout(ctx, c.config.ReadTimeout)
	defer getCancel()
	_, err = c.kubeClient.CoreV1().ConfigMaps(c.config.Namespace).Get(getCtx, createdConfigMap.Name, metav1.GetOptions{})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.Unhealthy(ErrCodeAPIServerGetTimeout, "timed out while getting ConfigMap"), nil
		}
		return types.Unhealthy(ErrCodeAPIServerGetError, fmt.Sprintf("failed to get ConfigMap: %v", err)), nil
	}

	// Delete ConfigMap.
	deleteCtx, deleteCancel := context.WithTimeout(ctx, c.config.MutateTimeout)
	defer deleteCancel()
	err = c.kubeClient.CoreV1().ConfigMaps(c.config.Namespace).Delete(deleteCtx, createdConfigMap.Name, metav1.DeleteOptions{})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return types.Unhealthy(ErrCodeAPIServerDeleteTimeout, "timed out while deleting ConfigMap"), nil
		}
		return types.Unhealthy(ErrCodeAPIServerDeleteError, fmt.Sprintf("failed to delete ConfigMap: %v", err)), nil
	}

	return types.Healthy(), nil
}

// configMapLabels returns the labels to be applied to ConfigMaps created specifically by this checker.
// The checker's name is a unique identifier for each checker.
func (c APIServerChecker) configMapLabels() map[string]string {
	return map[string]string{
		c.config.LabelKey: c.name,
	}
}

// generateConfigMap creates a ConfigMap object for this checker.
func (c APIServerChecker) generateConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-empty-configmap-%d", strings.ToLower(c.name), time.Now().UnixNano()),
			Namespace: c.config.Namespace,
			Labels:    c.configMapLabels(),
		},
	}
}

// garbageCollect attempts to delete any leftover ConfigMaps created by this checker
// in previous runs that may not have been properly deleted.
func (c APIServerChecker) garbageCollect(ctx context.Context) error {
	configMapList, err := c.kubeClient.CoreV1().ConfigMaps(c.config.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set(c.configMapLabels())).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to list ConfigMaps for garbage collection: %w", err)
	}

	var errs []error
	for _, cm := range configMapList.Items {
		if time.Since(cm.CreationTimestamp.Time) > c.timeout {
			err := c.kubeClient.CoreV1().ConfigMaps(cm.Namespace).Delete(ctx, cm.Name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("failed to delete old empty ConfigMap %s: %w", cm.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}
