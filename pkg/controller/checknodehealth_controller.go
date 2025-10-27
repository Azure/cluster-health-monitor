package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/checker"
)

const (
	controllerName = "checknodehealth-controller"
	// maxRetries is the number of times a resource will be retried before it is dropped out of the queue
	maxRetries = 5
)

var checkNodeHealthGVR = schema.GroupVersionResource{
	Group:    chmv1alpha1.GroupName,
	Version:  chmv1alpha1.Version,
	Resource: "checknodehealths",
}

// CheckNodeHealthController watches CheckNodeHealth resources and executes health checks
type CheckNodeHealthController struct {
	kubeClient      kubernetes.Interface
	dynamicClient   dynamic.Interface
	informer        cache.SharedIndexInformer
	workqueue       workqueue.RateLimitingInterface
	checkerRegistry map[string]checker.Checker
}

// NewCheckNodeHealthController creates a new controller instance
func NewCheckNodeHealthController(
	kubeClient kubernetes.Interface,
	dynamicClient dynamic.Interface,
	checkerRegistry map[string]checker.Checker,
) *CheckNodeHealthController {
	// Create informer for CheckNodeHealth resources
	dynInformerFactory := dynamicinformer.NewDynamicSharedInformerFactory(dynamicClient, time.Minute*5)
	informer := dynInformerFactory.ForResource(checkNodeHealthGVR).Informer()

	controller := &CheckNodeHealthController{
		kubeClient:      kubeClient,
		dynamicClient:   dynamicClient,
		informer:        informer,
		workqueue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		checkerRegistry: checkerRegistry,
	}

	klog.Info("Setting up event handlers for CheckNodeHealth")
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueCheckNodeHealth,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueCheckNodeHealth(new)
		},
	})

	return controller
}

// Run starts the controller
func (c *CheckNodeHealthController) Run(ctx context.Context, workers int) error {
	defer c.workqueue.ShutDown()

	klog.Infof("Starting %s", controllerName)
	klog.Info("Waiting for informer caches to sync")

	go c.informer.Run(ctx.Done())

	if ok := cache.WaitForCacheSync(ctx.Done(), c.informer.HasSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	klog.Info("Starting workers")
	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

	klog.Info("Started workers")
	<-ctx.Done()
	klog.Info("Shutting down workers")

	return nil
}

func (c *CheckNodeHealthController) runWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *CheckNodeHealthController) processNextWorkItem(ctx context.Context) bool {
	obj, shutdown := c.workqueue.Get()
	if shutdown {
		return false
	}

	err := func(obj interface{}) error {
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		if key, ok = obj.(string); !ok {
			c.workqueue.Forget(obj)
			klog.Errorf("expected string in workqueue but got %#v", obj)
			return nil
		}

		if err := c.syncHandler(ctx, key); err != nil {
			if c.workqueue.NumRequeues(key) < maxRetries {
				c.workqueue.AddRateLimited(key)
				return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
			}
			c.workqueue.Forget(key)
			klog.Errorf("dropping CheckNodeHealth '%s' out of the queue after %d retries: %v", key, maxRetries, err)
			return nil
		}

		c.workqueue.Forget(obj)
		klog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		klog.Error(err)
		return true
	}

	return true
}

func (c *CheckNodeHealthController) syncHandler(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("invalid resource key: %s", key)
		return nil
	}

	obj, err := c.informer.GetIndexer().GetByKey(key)
	if err != nil {
		klog.Errorf("fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if obj == nil {
		klog.Infof("CheckNodeHealth %s has been deleted", key)
		return nil
	}

	// Convert unstructured to CheckNodeHealth
	unstructuredObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("object is not unstructured: %T", obj)
	}

	cnh := &chmv1alpha1.CheckNodeHealth{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredObj.Object, cnh); err != nil {
		return fmt.Errorf("failed to convert to CheckNodeHealth: %w", err)
	}

	// Check if already completed
	if isCompleted(cnh) {
		klog.V(4).Infof("CheckNodeHealth %s already completed, skipping", key)
		return nil
	}

	// Check if already in progress
	if isProgressing(cnh) {
		klog.V(4).Infof("CheckNodeHealth %s already in progress, skipping", key)
		return nil
	}

	// Execute health checks
	return c.executeHealthChecks(ctx, namespace, name, cnh)
}

func (c *CheckNodeHealthController) enqueueCheckNodeHealth(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		klog.Error(err)
		return
	}
	c.workqueue.Add(key)
}

func (c *CheckNodeHealthController) executeHealthChecks(ctx context.Context, namespace, name string, cnh *chmv1alpha1.CheckNodeHealth) error {
	klog.Infof("Executing health checks for CheckNodeHealth %s/%s on node %s", namespace, name, cnh.Spec.NodeName)

	// Create a copy to modify
	cnhCopy := cnh.DeepCopy()

	// Set started timestamp and progressing condition
	now := metav1.Now()
	cnhCopy.Status.StartedAt = &now
	cnhCopy.Status.Conditions = setCondition(cnhCopy.Status.Conditions, chmv1alpha1.CheckNodeHealthCondition{
		Type:               chmv1alpha1.CheckNodeHealthConditionProgressing,
		Status:             corev1.ConditionTrue,
		Reason:             "ChecksStarted",
		Message:            "Health checks have started",
		LastTransitionTime: now,
	})

	// Update status to progressing
	if err := c.updateStatus(ctx, namespace, name, cnhCopy); err != nil {
		return fmt.Errorf("failed to update status to progressing: %w", err)
	}

	// Determine which checks to run
	checksToRun := c.getChecksToRun(cnhCopy.Spec.ChecksToRun)

	// Execute checks and collect results
	results := make([]chmv1alpha1.CheckResult, 0, len(checksToRun))
	allPassed := true
	var failedCount int

	for _, checkName := range checksToRun {
		chk, exists := c.checkerRegistry[checkName]
		if !exists {
			klog.Warningf("Checker %s not found in registry, skipping", checkName)
			results = append(results, chmv1alpha1.CheckResult{
				Check:   checkName,
				Status:  chmv1alpha1.CheckStatusSkipped,
				Message: "Checker not found in registry",
			})
			continue
		}

		// Execute the check with timeout
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result, err := chk.Run(checkCtx)
		cancel()

		completedAt := metav1.Now()
		checkResult := chmv1alpha1.CheckResult{
			Check:       checkName,
			CompletedAt: &completedAt,
		}

		if err != nil {
			klog.Errorf("Error running check %s: %v", checkName, err)
			checkResult.Status = chmv1alpha1.CheckStatusUnhealthy
			checkResult.Message = fmt.Sprintf("Check execution error: %v", err)
			allPassed = false
			failedCount++
		} else {
			checkResult.Status = convertStatus(result.Status)
			if result.Detail != nil {
				checkResult.Message = result.Detail.Message
				checkResult.ErrorCode = result.Detail.Code
			}
			if result.Status != types.StatusHealthy {
				allPassed = false
				failedCount++
			}
		}

		results = append(results, checkResult)
	}

	// Get latest version
	obj, err := c.dynamicClient.Resource(checkNodeHealthGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get latest CheckNodeHealth: %w", err)
	}

	cnhCopy = &chmv1alpha1.CheckNodeHealth{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, cnhCopy); err != nil {
		return fmt.Errorf("failed to convert to CheckNodeHealth: %w", err)
	}

	finishedAt := metav1.Now()
	cnhCopy.Status.FinishedAt = &finishedAt
	cnhCopy.Status.Results = results

	// Set final conditions
	if allPassed {
		cnhCopy.Status.Conditions = setCondition(cnhCopy.Status.Conditions, chmv1alpha1.CheckNodeHealthCondition{
			Type:               chmv1alpha1.CheckNodeHealthConditionCompleted,
			Status:             corev1.ConditionTrue,
			Reason:             "AllPassed",
			Message:            "All health checks passed",
			LastTransitionTime: finishedAt,
		})
		cnhCopy.Status.Conditions = setCondition(cnhCopy.Status.Conditions, chmv1alpha1.CheckNodeHealthCondition{
			Type:               chmv1alpha1.CheckNodeHealthConditionFailed,
			Status:             corev1.ConditionFalse,
			Reason:             "AllPassed",
			Message:            "All health checks passed",
			LastTransitionTime: finishedAt,
		})
	} else {
		cnhCopy.Status.Conditions = setCondition(cnhCopy.Status.Conditions, chmv1alpha1.CheckNodeHealthCondition{
			Type:               chmv1alpha1.CheckNodeHealthConditionCompleted,
			Status:             corev1.ConditionTrue,
			Reason:             "ChecksCompleted",
			Message:            fmt.Sprintf("%d of %d checks failed", failedCount, len(checksToRun)),
			LastTransitionTime: finishedAt,
		})
		cnhCopy.Status.Conditions = setCondition(cnhCopy.Status.Conditions, chmv1alpha1.CheckNodeHealthCondition{
			Type:               chmv1alpha1.CheckNodeHealthConditionFailed,
			Status:             corev1.ConditionTrue,
			Reason:             "ChecksFailed",
			Message:            fmt.Sprintf("%d of %d checks failed", failedCount, len(checksToRun)),
			LastTransitionTime: finishedAt,
		})
	}

	// Remove progressing condition
	cnhCopy.Status.Conditions = removeCondition(cnhCopy.Status.Conditions, chmv1alpha1.CheckNodeHealthConditionProgressing)

	if err := c.updateStatus(ctx, namespace, name, cnhCopy); err != nil {
		return fmt.Errorf("failed to update final status: %w", err)
	}

	klog.Infof("Completed health checks for CheckNodeHealth %s/%s: %d checks, %d failed", namespace, name, len(checksToRun), failedCount)
	return nil
}

func (c *CheckNodeHealthController) updateStatus(ctx context.Context, namespace, name string, cnh *chmv1alpha1.CheckNodeHealth) error {
	// Convert to unstructured
	unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cnh)
	if err != nil {
		return fmt.Errorf("failed to convert to unstructured: %w", err)
	}

	u := &unstructured.Unstructured{Object: unstructuredObj}

	_, err = c.dynamicClient.Resource(checkNodeHealthGVR).Namespace(namespace).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	return err
}

func (c *CheckNodeHealthController) getChecksToRun(requestedChecks []string) []string {
	if len(requestedChecks) == 0 {
		// Return all registered checkers
		checks := make([]string, 0, len(c.checkerRegistry))
		for name := range c.checkerRegistry {
			checks = append(checks, name)
		}
		return checks
	}
	return requestedChecks
}

func convertStatus(status types.Status) chmv1alpha1.CheckStatus {
	switch status {
	case types.StatusHealthy:
		return chmv1alpha1.CheckStatusHealthy
	case types.StatusUnhealthy:
		return chmv1alpha1.CheckStatusUnhealthy
	case types.StatusSkipped:
		return chmv1alpha1.CheckStatusSkipped
	default:
		return chmv1alpha1.CheckStatusPending
	}
}

func isCompleted(cnh *chmv1alpha1.CheckNodeHealth) bool {
	for _, cond := range cnh.Status.Conditions {
		if cond.Type == chmv1alpha1.CheckNodeHealthConditionCompleted && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func isProgressing(cnh *chmv1alpha1.CheckNodeHealth) bool {
	for _, cond := range cnh.Status.Conditions {
		if cond.Type == chmv1alpha1.CheckNodeHealthConditionProgressing && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func setCondition(conditions []chmv1alpha1.CheckNodeHealthCondition, newCondition chmv1alpha1.CheckNodeHealthCondition) []chmv1alpha1.CheckNodeHealthCondition {
	for i, cond := range conditions {
		if cond.Type == newCondition.Type {
			conditions[i] = newCondition
			return conditions
		}
	}
	return append(conditions, newCondition)
}

func removeCondition(conditions []chmv1alpha1.CheckNodeHealthCondition, condType chmv1alpha1.CheckNodeHealthConditionType) []chmv1alpha1.CheckNodeHealthCondition {
	result := make([]chmv1alpha1.CheckNodeHealthCondition, 0, len(conditions))
	for _, cond := range conditions {
		if cond.Type != condType {
			result = append(result, cond)
		}
	}
	return result
}
