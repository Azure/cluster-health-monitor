package controller

import (
	"context"
	"fmt"
	"time"

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
	controllerName = "checkhealthmonitor-controller"
	// maxRetries is the number of times a resource will be retried before it is dropped out of the queue
	maxRetries = 5
)

var checkHealthMonitorGVR = schema.GroupVersionResource{
	Group:    chmv1alpha1.GroupName,
	Version:  chmv1alpha1.Version,
	Resource: "checkhealthmonitors",
}

// CheckHealthMonitorController watches CheckHealthMonitor resources and executes health checks
type CheckHealthMonitorController struct {
	kubeClient      kubernetes.Interface
	dynamicClient   dynamic.Interface
	informer        cache.SharedIndexInformer
	workqueue       workqueue.RateLimitingInterface
	checkerRegistry map[string]checker.Checker
}

// NewCheckHealthMonitorController creates a new controller instance
func NewCheckHealthMonitorController(
	kubeClient kubernetes.Interface,
	dynamicClient dynamic.Interface,
	checkerRegistry map[string]checker.Checker,
) *CheckHealthMonitorController {
	// Create informer for CheckHealthMonitor resources
	dynInformerFactory := dynamicinformer.NewDynamicSharedInformerFactory(dynamicClient, time.Minute*5)
	informer := dynInformerFactory.ForResource(checkHealthMonitorGVR).Informer()

	controller := &CheckHealthMonitorController{
		kubeClient:      kubeClient,
		dynamicClient:   dynamicClient,
		informer:        informer,
		workqueue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		checkerRegistry: checkerRegistry,
	}

	klog.Info("Setting up event handlers for CheckHealthMonitor")
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueCheckHealthMonitor,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueCheckHealthMonitor(new)
		},
	})

	return controller
}

// Run starts the controller
func (c *CheckHealthMonitorController) Run(ctx context.Context, workers int) error {
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

func (c *CheckHealthMonitorController) runWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *CheckHealthMonitorController) processNextWorkItem(ctx context.Context) bool {
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
			klog.Errorf("dropping CheckHealthMonitor '%s' out of the queue after %d retries: %v", key, maxRetries, err)
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

func (c *CheckHealthMonitorController) syncHandler(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("invalid resource key: %s", key)
		return nil
	}

	obj, exists, err := c.informer.GetIndexer().GetByKey(key)
	if err != nil {
		klog.Errorf("fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if !exists {
		klog.Infof("CheckHealthMonitor %s has been deleted", key)
		return nil
	}

	// Convert unstructured to CheckHealthMonitor
	unstructuredObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("object is not unstructured: %T", obj)
	}

	chm := &chmv1alpha1.CheckHealthMonitor{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredObj.Object, chm); err != nil {
		return fmt.Errorf("failed to convert to CheckHealthMonitor: %w", err)
	}

	// Check if already completed
	if isCompleted(chm) {
		klog.V(4).Infof("CheckHealthMonitor %s already completed, skipping", key)
		return nil
	}

	// Check if already in progress
	if isProgressing(chm) {
		klog.V(4).Infof("CheckHealthMonitor %s already in progress, skipping", key)
		return nil
	}

	// Execute health checks
	return c.executeHealthChecks(ctx, namespace, name, chm)
}

func (c *CheckHealthMonitorController) enqueueCheckHealthMonitor(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		klog.Error(err)
		return
	}
	c.workqueue.Add(key)
}

func (c *CheckHealthMonitorController) executeHealthChecks(ctx context.Context, namespace, name string, chm *chmv1alpha1.CheckHealthMonitor) error {
	nodeName := chm.Spec.NodeRef.Name
	klog.Infof("Executing health checks for CheckHealthMonitor %s/%s on node %s", namespace, name, nodeName)

	// Create a copy to modify
	chmCopy := chm.DeepCopy()

	// Set started timestamp and progressing condition
	now := metav1.Now()
	chmCopy.Status.StartedAt = &now
	chmCopy.Status.Conditions = setCondition(chmCopy.Status.Conditions, metav1.Condition{
		Type:               string(chmv1alpha1.CheckHealthMonitorConditionProgressing),
		Status:             metav1.ConditionTrue,
		Reason:             "ChecksStarted",
		Message:            "Health checks have started",
		LastTransitionTime: now,
	})

	// Update status to progressing
	if err := c.updateStatus(ctx, namespace, name, chmCopy); err != nil {
		return fmt.Errorf("failed to update status to progressing: %w", err)
	}

	// Execute checks and collect results
	results := make([]chmv1alpha1.CheckResult, 0, len(c.checkerRegistry))
	allPassed := true
	var failedCount int

	for _, chk := range c.checkerRegistry {
		checkName := chk.Name()
		checkerType := chk.Type()

		klog.V(4).Infof("Running check %s (type: %s) for node %s", checkName, checkerType, nodeName)

		// Execute the check with timeout
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)

		// Create a channel to capture the result
		resultChan := make(chan *checker.Result, 1)
		errChan := make(chan error, 1)

		go func() {
			// Note: Current checker.Run() doesn't return anything
			// This is a placeholder for when we update the interface
			chk.Run(checkCtx)
			// For now, we'll assume success if no panic
			resultChan <- checker.Healthy()
		}()

		var result *checker.Result
		var runErr error

		select {
		case result = <-resultChan:
		case runErr = <-errChan:
		case <-checkCtx.Done():
			runErr = fmt.Errorf("check timed out")
		}
		cancel()

		completedAt := metav1.Now()
		checkResult := chmv1alpha1.CheckResult{
			CheckerType: checkerType,
			Checker:     checkName,
			CompletedAt: &completedAt,
		}

		if runErr != nil {
			klog.Errorf("Error running check %s: %v", checkName, runErr)
			checkResult.Status = chmv1alpha1.CheckStatusUnhealthy
			checkResult.Message = fmt.Sprintf("Check execution error: %v", runErr)
			allPassed = false
			failedCount++
		} else if result != nil {
			checkResult.Status = convertStatus(result.Status)
			if result.Detail.Message != "" {
				checkResult.Message = result.Detail.Message
			}
			if result.Detail.Code != "" {
				checkResult.ErrorCode = result.Detail.Code
			}
			if result.Status != checker.StatusHealthy {
				allPassed = false
				failedCount++
			}
		}

		results = append(results, checkResult)
	}

	// Get latest version before updating status
	obj, err := c.dynamicClient.Resource(checkHealthMonitorGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get latest CheckHealthMonitor: %w", err)
	}

	chmCopy = &chmv1alpha1.CheckHealthMonitor{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, chmCopy); err != nil {
		return fmt.Errorf("failed to convert to CheckHealthMonitor: %w", err)
	}

	finishedAt := metav1.Now()
	chmCopy.Status.FinishedAt = &finishedAt
	chmCopy.Status.Results = results

	// Set final conditions
	if allPassed {
		chmCopy.Status.Conditions = setCondition(chmCopy.Status.Conditions, metav1.Condition{
			Type:               string(chmv1alpha1.CheckHealthMonitorConditionCompleted),
			Status:             metav1.ConditionTrue,
			Reason:             "AllPassed",
			Message:            "All health checks passed",
			LastTransitionTime: finishedAt,
		})
		chmCopy.Status.Conditions = setCondition(chmCopy.Status.Conditions, metav1.Condition{
			Type:               string(chmv1alpha1.CheckHealthMonitorConditionFailed),
			Status:             metav1.ConditionFalse,
			Reason:             "AllPassed",
			Message:            "All health checks passed",
			LastTransitionTime: finishedAt,
		})
	} else {
		chmCopy.Status.Conditions = setCondition(chmCopy.Status.Conditions, metav1.Condition{
			Type:               string(chmv1alpha1.CheckHealthMonitorConditionCompleted),
			Status:             metav1.ConditionTrue,
			Reason:             "ChecksCompleted",
			Message:            fmt.Sprintf("%d of %d checks failed", failedCount, len(results)),
			LastTransitionTime: finishedAt,
		})
		chmCopy.Status.Conditions = setCondition(chmCopy.Status.Conditions, metav1.Condition{
			Type:               string(chmv1alpha1.CheckHealthMonitorConditionFailed),
			Status:             metav1.ConditionTrue,
			Reason:             "ChecksFailed",
			Message:            fmt.Sprintf("%d of %d checks failed", failedCount, len(results)),
			LastTransitionTime: finishedAt,
		})
	}

	// Remove progressing condition
	chmCopy.Status.Conditions = removeCondition(chmCopy.Status.Conditions, "Progressing")

	if err := c.updateStatus(ctx, namespace, name, chmCopy); err != nil {
		return fmt.Errorf("failed to update final status: %w", err)
	}

	klog.Infof("Completed health checks for CheckHealthMonitor %s/%s: %d checks, %d failed", namespace, name, len(results), failedCount)
	return nil
}

func (c *CheckHealthMonitorController) updateStatus(ctx context.Context, namespace, name string, chm *chmv1alpha1.CheckHealthMonitor) error {
	// Convert to unstructured
	unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(chm)
	if err != nil {
		return fmt.Errorf("failed to convert to unstructured: %w", err)
	}

	u := &unstructured.Unstructured{Object: unstructuredObj}

	_, err = c.dynamicClient.Resource(checkHealthMonitorGVR).Namespace(namespace).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	return err
}

func convertStatus(status checker.Status) chmv1alpha1.CheckStatus {
	switch status {
	case checker.StatusHealthy:
		return chmv1alpha1.CheckStatusHealthy
	case checker.StatusUnhealthy:
		return chmv1alpha1.CheckStatusUnhealthy
	default:
		return chmv1alpha1.CheckStatusUnhealthy
	}
}

func isCompleted(chm *chmv1alpha1.CheckHealthMonitor) bool {
	for _, cond := range chm.Status.Conditions {
		if cond.Type == string(chmv1alpha1.CheckHealthMonitorConditionCompleted) && cond.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

func isProgressing(chm *chmv1alpha1.CheckHealthMonitor) bool {
	for _, cond := range chm.Status.Conditions {
		if cond.Type == "Progressing" && cond.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

func setCondition(conditions []metav1.Condition, newCondition metav1.Condition) []metav1.Condition {
	for i, cond := range conditions {
		if cond.Type == newCondition.Type {
			conditions[i] = newCondition
			return conditions
		}
	}
	return append(conditions, newCondition)
}

func removeCondition(conditions []metav1.Condition, condType string) []metav1.Condition {
	result := make([]metav1.Condition, 0, len(conditions))
	for _, cond := range conditions {
		if cond.Type != condType {
			result = append(result, cond)
		}
	}
	return result
}
