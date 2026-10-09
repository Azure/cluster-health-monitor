package e2e

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/controller/checknodehealth"
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner/checkers/gpu"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	gpuTestSKU               = "Standard_ND96isr_H100_v5"
	fakeDevicePluginImage    = "cluster-health-monitor-fake-device-plugin:test-latest"
	fakeDevicePluginName     = "chm-fake-nvidia-device-plugin"
	nvidiaGPUResource        = corev1.ResourceName("nvidia.com/gpu")
	gpuInstanceTypeLabel     = "node.kubernetes.io/instance-type"
	gpuAcceleratorLabel      = "kubernetes.azure.com/accelerator"
	gpuTaintKey              = "nvidia.com/gpu"
	gpuTaintValue            = "present"
	devicePluginSocketVolume = "device-plugin"
)

// This follows the node-resource simulation used by kind-gpu-sim at
// https://github.com/maryamtahhan/kind-gpu-sim/tree/fc70bf529ef73414ca5bb71408fce70e5062c30f.
// The simulator cannot execute CUDA kernels. A local GPU checker test image therefore supplies
// deterministic test doubles for nvidia-smi, mpirun/NCCL, and nvbandwidth while these tests
// exercise the real controller, nodechecker, parsers, Kubernetes clients, status aggregation, and
// cleanup. A minimal device plugin additionally exercises the fully managed resource-allocation
// path without requiring physical GPUs. Driver-only nodes (no device plugin) are not supported by
// the GPU checks yet and must report so.
var _ = Describe("GPU CheckNodeHealth flow on Kind", Serial, Ordered, func() {
	var (
		ctx                    context.Context
		k8sClient              client.Client
		originalControllerArgs []string
	)

	BeforeAll(func() {
		ctx = context.Background()
		Expect(chmv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
		restConfig, err := getKubeConfig()
		Expect(err).NotTo(HaveOccurred())
		k8sClient, err = client.New(restConfig, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())

		By("Enabling GPU checks only for this serial suite")
		deployment, err := clientset.AppsV1().Deployments(checkerNamespace).Get(
			ctx, "checknodehealth-controller", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		originalControllerArgs = slices.Clone(deployment.Spec.Template.Spec.Containers[0].Args)
		// The driver-only specs would otherwise sit out the full GPU wait before the controller
		// decides the node has no claimable GPUs.
		for _, arg := range []string{"-enable-gpu-checks", "-gpu-wait=10s"} {
			if !slices.Contains(deployment.Spec.Template.Spec.Containers[0].Args, arg) {
				deployment.Spec.Template.Spec.Containers[0].Args = append(
					deployment.Spec.Template.Spec.Containers[0].Args, arg)
			}
		}
		if !slices.Equal(deployment.Spec.Template.Spec.Containers[0].Args, originalControllerArgs) {
			deployment, err = clientset.AppsV1().Deployments(checkerNamespace).Update(
				ctx, deployment, metav1.UpdateOptions{})
			Expect(err).NotTo(HaveOccurred())
		}
		waitForControllerRollout(ctx, deployment.Generation)
	})

	AfterAll(func() {
		By("Restoring the controller arguments for the remaining E2E suites")
		deployment, err := clientset.AppsV1().Deployments(checkerNamespace).Get(
			ctx, "checknodehealth-controller", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		deployment.Spec.Template.Spec.Containers[0].Args = slices.Clone(originalControllerArgs)
		deployment, err = clientset.AppsV1().Deployments(checkerNamespace).Update(
			ctx, deployment, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())
		waitForControllerRollout(ctx, deployment.Generation)
	})

	It("reports the GPU checks as unsupported on a simulated driver-only NVIDIA node", func() {
		By("Selecting a node with both CoreDNS replicas on remote nodes")
		nodeName, err := nodeWithRemoteCoreDNS(ctx)
		Expect(err).NotTo(HaveOccurred())

		By("Simulating a driver-only NVIDIA H100 node")
		restoreNode, err := simulateNVIDIAGPUNode(ctx, nodeName, gpuTestSKU)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(restoreNode()).To(Succeed()) })

		node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(node.Status.Allocatable).NotTo(HaveKey(nvidiaGPUResource))

		runDriverOnlyGPUControllerFlow(ctx, k8sClient, nodeName)
	})

	It("runs healthy GPU checks on a simulated fully managed NVIDIA node", func() {
		By("Selecting a node with both CoreDNS replicas on remote nodes")
		nodeName, err := nodeWithRemoteCoreDNS(ctx)
		Expect(err).NotTo(HaveOccurred())

		simulateManagedNVIDIAGPUNode(ctx, nodeName)

		runHealthyGPUControllerFlow(ctx, k8sClient, nodeName)
	})

	It("reports base check and GPU conditions and never NodeHealthy on a GPU node", func() {
		By("Selecting a node with both CoreDNS replicas on remote nodes")
		nodeName, err := nodeWithRemoteCoreDNS(ctx)
		Expect(err).NotTo(HaveOccurred())

		simulateManagedNVIDIAGPUNode(ctx, nodeName)

		By("Running the GPU checks to completion")
		runCheckToCompletion(ctx, k8sClient, nodeName, "gpu-node-conditions")

		By("Verifying every base check and GPU condition reports healthy")
		for _, conditionType := range slices.Concat(baseCheckConditionTypes, gpuConditionTypes) {
			expectNodeConditionStatus(ctx, k8sClient, nodeName, conditionType, corev1.ConditionTrue)
		}

		By("Verifying NodeHealthy is never set on a GPU node")
		// The invariant the whole split exists for: remediation keyed on NodeHealthy must not act
		// on GPU nodes, even when every GPU check passed.
		Consistently(func() *corev1.NodeCondition {
			return getNodeCondition(ctx, k8sClient, nodeName, checknodehealth.NodeConditionNodeHealthy)
		}, "15s", "3s").Should(BeNil(), "NodeHealthy must never be set on a GPU node")
	})

	It("retires the GPU node conditions when a node stops being a GPU node", func() {
		By("Selecting a node with both CoreDNS replicas on remote nodes")
		nodeName, err := nodeWithRemoteCoreDNS(ctx)
		Expect(err).NotTo(HaveOccurred())

		By("Simulating a driver-only GPU node and running the checks so it carries GPU node conditions")
		// Driver-only nodes report every GPU condition as Unknown, which is all this needs: the
		// conditions exist and must be retired.
		restoreNode, err := simulateNVIDIAGPUNode(ctx, nodeName, gpuTestSKU)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(restoreNode()).To(Succeed()) })

		runCheckToCompletion(ctx, k8sClient, nodeName, "gpu-cond-before")
		Eventually(func() *corev1.NodeCondition {
			return getNodeCondition(ctx, k8sClient, nodeName, gpuConditionTypes[0])
		}, "60s", "2s").ShouldNot(BeNil(), "expected the GPU node conditions first")

		By("Restoring the node so it is no longer seen as a GPU node")
		Expect(restoreNode()).To(Succeed())

		By("Running another health check")
		runCheckToCompletion(ctx, k8sClient, nodeName, "gpu-cond-after")

		By("Verifying the node now reports the aggregate NodeHealthy condition")
		Eventually(func() *corev1.NodeCondition {
			return getNodeCondition(ctx, k8sClient, nodeName, checknodehealth.NodeConditionNodeHealthy)
		}, "60s", "2s").ShouldNot(BeNil(), "expected NodeHealthy once the node is not a GPU node")

		By("Verifying the GPU node conditions were retired rather than left behind")
		// A leftover condition would keep asserting a verdict nothing produces any more.
		for _, conditionType := range slices.Concat(baseCheckConditionTypes, gpuConditionTypes) {
			Eventually(func() *corev1.NodeCondition {
				return getNodeCondition(ctx, k8sClient, nodeName, conditionType)
			}, "60s", "2s").Should(BeNil(), "%s should have been removed", conditionType)
		}
	})
})

// baseCheckConditionTypes are the base check conditions a GPU node reports. Spelled out here rather
// than reused from the controller so the test fails if the published name changes.
var baseCheckConditionTypes = []corev1.NodeConditionType{
	"kubernetes.azure.com/PodStartupHealthy",
	"kubernetes.azure.com/PodNetworkHealthy",
}

// gpuConditionTypes are the GPU conditions, spelled out for the same reason.
var gpuConditionTypes = []corev1.NodeConditionType{
	"kubernetes.azure.com/GPUCountHealthy",
	"kubernetes.azure.com/GPUHostBandwidthHealthy",
	"kubernetes.azure.com/GPUPeerBandwidthHealthy",
	"kubernetes.azure.com/GPUAllReduceBandwidthHealthy",
	"kubernetes.azure.com/GPUCorrectnessHealthy",
}

// expectNodeConditionStatus waits for the condition to appear on the node and checks its status.
func expectNodeConditionStatus(ctx context.Context, k8sClient client.Client, nodeName string, conditionType corev1.NodeConditionType, want corev1.ConditionStatus) {
	Eventually(func() *corev1.NodeCondition {
		return getNodeCondition(ctx, k8sClient, nodeName, conditionType)
	}, "60s", "2s").ShouldNot(BeNil(), "expected %s on the node", conditionType)

	condition := getNodeCondition(ctx, k8sClient, nodeName, conditionType)
	Expect(condition.Status).To(Equal(want),
		"%s: reason=%s message=%s", conditionType, condition.Reason, condition.Message)
}

// getNodeCondition returns the named condition from the node, or nil when it is absent.
func getNodeCondition(ctx context.Context, k8sClient client.Client, nodeName string, conditionType corev1.NodeConditionType) *corev1.NodeCondition {
	node := &corev1.Node{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return nil
	}
	for i, c := range node.Status.Conditions {
		if c.Type == conditionType {
			return &node.Status.Conditions[i]
		}
	}
	return nil
}

// runCheckToCompletion creates a CheckNodeHealth for the node and waits for the controller to
// finish it, so the node conditions it publishes have settled before they are asserted on.
func runCheckToCompletion(ctx context.Context, k8sClient client.Client, nodeName, prefix string) {
	cnhName := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	DeferCleanup(func() { _ = deleteCheckNodeHealthCR(ctx, k8sClient, cnhName) })

	Expect(createCheckNodeHealthCR(ctx, k8sClient, cnhName, nodeName)).To(Succeed())
	Eventually(func(g Gomega) {
		cnh, getErr := getCheckNodeHealthCR(ctx, k8sClient, cnhName)
		g.Expect(getErr).NotTo(HaveOccurred())
		g.Expect(cnh.Status.FinishedAt).NotTo(BeNil())
	}, "120s", "2s").Should(Succeed(), "CheckNodeHealth %s did not complete", cnhName)
}

func runHealthyGPUControllerFlow(ctx context.Context, k8sClient client.Client, nodeName string) {
	By("Verifying the simulated NVIDIA node metadata")
	simulatedNode, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	Expect(simulatedNode.Labels).To(HaveKeyWithValue(gpuAcceleratorLabel, "nvidia"))
	Expect(simulatedNode.Labels).To(HaveKeyWithValue(gpuInstanceTypeLabel, gpuTestSKU))
	Expect(simulatedNode.Spec.Taints).To(ContainElement(corev1.Taint{
		Key: gpuTaintKey, Value: gpuTaintValue, Effect: corev1.TaintEffectNoSchedule,
	}))

	cnhName := fmt.Sprintf("gpu-sim-%d", time.Now().UnixNano())
	DeferCleanup(func() { _ = deleteCheckNodeHealthCR(ctx, k8sClient, cnhName) })

	By("Creating a CheckNodeHealth resource and letting the controller create the GPU checker pod")
	Expect(createCheckNodeHealthCR(ctx, k8sClient, cnhName, nodeName)).To(Succeed())

	By("Waiting for the controller to complete the CheckNodeHealth resource")
	var completed *chmv1alpha1.CheckNodeHealth
	Eventually(func(g Gomega) {
		completed, err = getCheckNodeHealthCR(ctx, k8sClient, cnhName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(completed.Status.FinishedAt).NotTo(BeNil())
	}, "90s", "1s").Should(Succeed())

	By("Verifying all core and GPU checks reported healthy results")
	Expect(completed.Status.Results).To(HaveLen(5))
	results := make(map[string]chmv1alpha1.CheckResult, len(completed.Status.Results))
	for _, result := range completed.Status.Results {
		_, duplicate := results[result.Name]
		Expect(duplicate).To(BeFalse(), "duplicate result %s", result.Name)
		results[result.Name] = result
	}
	for _, name := range []string{"PodStartup", "PodNetwork", "NcclAllReduce", "GpuHostBandwidth", "GpuPeerBandwidth"} {
		result, found := results[name]
		Expect(found).To(BeTrue(), "%s result was not reported", name)
		Expect(result.Status).To(Equal(chmv1alpha1.CheckStatusHealthy),
			"%s result: code=%s message=%s", name, result.ErrorCode, result.Message)
		Expect(result.ErrorCode).To(BeEmpty(), "%s unexpectedly reported an error code", name)
	}

	nccl := results["NcclAllReduce"]
	Expect(nccl.Message).To(ContainSubstring("480.000 GB/s"))
	Expect(nccl.Message).To(ContainSubstring("460.000 GB/s threshold"))

	hostBandwidth := results["GpuHostBandwidth"]
	Expect(hostBandwidth.Message).To(ContainSubstring("host_to_device_memcpy_ce"))
	Expect(hostBandwidth.Message).To(ContainSubstring("device_to_host_memcpy_ce"))
	Expect(hostBandwidth.Message).To(ContainSubstring("48.000 GB/s threshold"))
	Expect(hostBandwidth.Message).NotTo(ContainSubstring("device_to_device_memcpy_read_ce"))

	peerBandwidth := results["GpuPeerBandwidth"]
	Expect(peerBandwidth.Message).To(ContainSubstring("device_to_device_memcpy_read_ce"))
	Expect(peerBandwidth.Message).To(ContainSubstring("335.000 GB/s threshold"))
	Expect(peerBandwidth.Message).NotTo(ContainSubstring("host_to_device_memcpy_ce"))

	By("Verifying the aggregate CheckNodeHealth condition is healthy")
	Expect(completed.Status.Conditions).To(HaveLen(1))
	condition := completed.Status.Conditions[0]
	Expect(condition.Type).To(Equal(checknodehealth.ConditionTypeHealthy))
	Expect(condition.Status).To(Equal(metav1.ConditionTrue))
	Expect(condition.Reason).To(Equal(checknodehealth.ReasonCheckPassed))
	for _, name := range []string{"PodStartup", "PodNetwork", "NcclAllReduce", "GpuHostBandwidth", "GpuPeerBandwidth"} {
		Expect(condition.Message).To(ContainSubstring(name + ": Healthy"))
	}

	By("Verifying the controller cleaned up the checker pod")
	Eventually(func(g Gomega) {
		pods, listErr := clientset.CoreV1().Pods(checkerNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s", checknodehealth.CheckNodeHealthLabel, cnhName),
		})
		g.Expect(listErr).NotTo(HaveOccurred())
		g.Expect(pods.Items).To(BeEmpty())
	}, "30s", "1s").Should(Succeed())
}

func runDriverOnlyGPUControllerFlow(ctx context.Context, k8sClient client.Client, nodeName string) {
	cnhName := fmt.Sprintf("gpu-driver-only-%d", time.Now().UnixNano())
	DeferCleanup(func() { _ = deleteCheckNodeHealthCR(ctx, k8sClient, cnhName) })

	By("Creating a CheckNodeHealth resource for the driver-only node")
	Expect(createCheckNodeHealthCR(ctx, k8sClient, cnhName, nodeName)).To(Succeed())

	By("Waiting for the controller to complete the CheckNodeHealth resource")
	var completed *chmv1alpha1.CheckNodeHealth
	Eventually(func(g Gomega) {
		var err error
		completed, err = getCheckNodeHealthCR(ctx, k8sClient, cnhName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(completed.Status.FinishedAt).NotTo(BeNil())
	}, "90s", "1s").Should(Succeed())

	results := make(map[string]chmv1alpha1.CheckResult, len(completed.Status.Results))
	for _, result := range completed.Status.Results {
		results[result.Name] = result
	}
	Expect(results).To(HaveLen(5))

	By("Verifying the base checks still ran")
	for _, name := range []string{"PodStartup", "PodNetwork"} {
		Expect(results[name].Status).To(Equal(chmv1alpha1.CheckStatusHealthy),
			"%s result: code=%s message=%s", name, results[name].ErrorCode, results[name].Message)
	}

	By("Verifying every GPU check reports that no GPUs could be claimed")
	// Had the GPU checker run, its test doubles would have reported these as Healthy.
	for _, name := range []string{"NcclAllReduce", "GpuHostBandwidth", "GpuPeerBandwidth"} {
		result, found := results[name]
		Expect(found).To(BeTrue(), "%s result was not reported", name)
		Expect(result.Status).To(Equal(chmv1alpha1.CheckStatusUnknown))
		Expect(result.ErrorCode).To(Equal(gpu.ErrorCodeGPUsNotClaimable))
		Expect(result.Message).To(ContainSubstring("advertised as healthy by the NVIDIA device plugin"))
	}

	By("Verifying the aggregate CheckNodeHealth condition is unknown")
	Expect(completed.Status.Conditions).To(HaveLen(1))
	condition := completed.Status.Conditions[0]
	Expect(condition.Type).To(Equal(checknodehealth.ConditionTypeHealthy))
	Expect(condition.Status).To(Equal(metav1.ConditionUnknown))
	Expect(condition.Reason).To(Equal(checknodehealth.ReasonCheckUnknown))

	By("Verifying the GPU node conditions are unknown and say why")
	for _, conditionType := range gpuConditionTypes {
		expectNodeConditionStatus(ctx, k8sClient, nodeName, conditionType, corev1.ConditionUnknown)
		Expect(getNodeCondition(ctx, k8sClient, nodeName, conditionType).Message).
			To(ContainSubstring(gpu.ErrorCodeGPUsNotClaimable))
	}
	Expect(getNodeCondition(ctx, k8sClient, nodeName, checknodehealth.NodeConditionNodeHealthy)).To(BeNil(),
		"NodeHealthy must never be set on a GPU node")
}

// simulateManagedNVIDIAGPUNode makes the node look like a fully managed NVIDIA H100 node: GPU labels
// plus a fake device plugin advertising eight GPUs. Both are undone when the spec ends.
func simulateManagedNVIDIAGPUNode(ctx context.Context, nodeName string) {
	By("Simulating an NVIDIA H100 node with driver labels")
	restoreNode, err := simulateNVIDIAGPUNode(ctx, nodeName, gpuTestSKU)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(restoreNode()).To(Succeed()) })

	By("Starting a fake NVIDIA device plugin that advertises eight healthy GPUs")
	removePlugin, err := deployFakeNVIDIADevicePlugin(ctx, nodeName)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(removePlugin()).To(Succeed()) })

	Eventually(func(g Gomega) {
		node, getErr := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		g.Expect(getErr).NotTo(HaveOccurred())
		quantity, found := node.Status.Allocatable[nvidiaGPUResource]
		g.Expect(found).To(BeTrue())
		g.Expect(quantity.Value()).To(Equal(int64(8)))
	}, "90s", "1s").Should(Succeed())
}

func waitForControllerRollout(ctx context.Context, generation int64) {
	Eventually(func(g Gomega) {
		deployment, err := clientset.AppsV1().Deployments(checkerNamespace).Get(
			ctx, "checknodehealth-controller", metav1.GetOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(deployment.Status.ObservedGeneration).To(BeNumerically(">=", generation))
		// Requiring total replicas to equal the desired count matters during a rolling update: an
		// old controller with the previous feature gates can remain Available while the new replica
		// is merely counted as Updated, and can otherwise reconcile the test CR first.
		g.Expect(deployment.Status.Replicas).To(Equal(int32(1)))
		g.Expect(deployment.Status.UpdatedReplicas).To(Equal(int32(1)))
		g.Expect(deployment.Status.ReadyReplicas).To(Equal(int32(1)))
		g.Expect(deployment.Status.AvailableReplicas).To(Equal(int32(1)))
		g.Expect(deployment.Status.UnavailableReplicas).To(Equal(int32(0)))
	}, "90s", "1s").Should(Succeed())
}

// nodeWithRemoteCoreDNS picks the third Kind node because PodNetwork intentionally excludes
// CoreDNS pods on the target node and requires at least two eligible remote replicas to pass.
func nodeWithRemoteCoreDNS(ctx context.Context) (string, error) {
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("list nodes: %w", err)
	}
	pods, err := getCoreDNSPodList(clientset)
	if err != nil {
		return "", fmt.Errorf("list CoreDNS pods: %w", err)
	}

	occupied := make(map[string]struct{}, len(pods.Items))
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil && pod.Spec.NodeName != "" {
			occupied[pod.Spec.NodeName] = struct{}{}
		}
	}
	for _, node := range nodes.Items {
		if _, found := occupied[node.Name]; !found {
			return node.Name, nil
		}
	}
	return "", fmt.Errorf("every node runs a CoreDNS pod")
}

func simulateNVIDIAGPUNode(ctx context.Context, nodeName, sku string) (func() error, error) {
	nodes := clientset.CoreV1().Nodes()
	original, err := nodes.Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get node %s: %w", nodeName, err)
	}
	original = original.DeepCopy()

	// The controller and kubelet write the node concurrently. For example, relabeling it as a GPU node
	// makes the controller remove NodeHealthy straight away. So every write starts from the latest node
	// and retries on conflict.
	updateNode := func(mutate func(*corev1.Node)) error {
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			node, getErr := nodes.Get(ctx, nodeName, metav1.GetOptions{})
			if getErr != nil {
				return getErr
			}
			mutate(node)
			_, updateErr := nodes.Update(ctx, node, metav1.UpdateOptions{})
			return updateErr
		})
	}
	updateNodeStatus := func(mutate func(*corev1.Node)) error {
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			node, getErr := nodes.Get(ctx, nodeName, metav1.GetOptions{})
			if getErr != nil {
				return getErr
			}
			mutate(node)
			_, updateErr := nodes.UpdateStatus(ctx, node, metav1.UpdateOptions{})
			return updateErr
		})
	}

	restore := func() error {
		if err := updateNode(func(current *corev1.Node) {
			for _, key := range []string{gpuAcceleratorLabel, gpuInstanceTypeLabel} {
				if value, existed := original.Labels[key]; existed {
					current.Labels[key] = value
				} else {
					delete(current.Labels, key)
				}
			}
			current.Spec.Taints = slices.Clone(original.Spec.Taints)
		}); err != nil {
			return fmt.Errorf("restore labels and taints on node %s: %w", nodeName, err)
		}
		if err := updateNodeStatus(func(current *corev1.Node) {
			for _, resources := range []struct{ current, original corev1.ResourceList }{
				{current.Status.Capacity, original.Status.Capacity},
				{current.Status.Allocatable, original.Status.Allocatable},
			} {
				if resources.current == nil {
					continue
				}
				if value, existed := resources.original[nvidiaGPUResource]; existed {
					resources.current[nvidiaGPUResource] = value
				} else {
					delete(resources.current, nvidiaGPUResource)
				}
			}
		}); err != nil {
			return fmt.Errorf("restore GPU status on node %s: %w", nodeName, err)
		}
		return nil
	}

	gpuTaint := corev1.Taint{Key: gpuTaintKey, Value: gpuTaintValue, Effect: corev1.TaintEffectNoSchedule}
	if err := updateNode(func(node *corev1.Node) {
		if node.Labels == nil {
			node.Labels = make(map[string]string)
		}
		// Mirror the controller-relevant metadata observed on an actual managed AKS GPU node.
		node.Labels[gpuAcceleratorLabel] = "nvidia"
		node.Labels[gpuInstanceTypeLabel] = sku
		if !slices.ContainsFunc(node.Spec.Taints, func(t corev1.Taint) bool { return t.MatchTaint(&gpuTaint) }) {
			node.Spec.Taints = append(node.Spec.Taints, gpuTaint)
		}
	}); err != nil {
		return nil, errors.Join(fmt.Errorf("label and taint node %s: %w", nodeName, err), restore())
	}

	// Start every scenario without a stale device-plugin resource. The fully managed test lets its
	// fake plugin advertise the resource through kubelet, rather than patching node status directly.
	if err := updateNodeStatus(func(node *corev1.Node) {
		delete(node.Status.Capacity, nvidiaGPUResource)
		delete(node.Status.Allocatable, nvidiaGPUResource)
	}); err != nil {
		// Undo the labels so a failure here does not leave the node looking like a GPU node to later specs.
		return nil, errors.Join(fmt.Errorf("clear GPU status on node %s: %w", nodeName, err), restore())
	}

	return restore, nil
}
func deployFakeNVIDIADevicePlugin(ctx context.Context, nodeName string) (func() error, error) {
	labels := map[string]string{"app": fakeDevicePluginName}
	daemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: fakeDevicePluginName, Namespace: checkerNamespace},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: ptr.To(intstr.FromInt32(1)),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{"kubernetes.io/hostname": nodeName},
					// The node without CoreDNS can be the Kind control plane. Tolerate both its
					// control-plane taint and the simulated GPU taint.
					Tolerations: []corev1.Toleration{{
						Operator: corev1.TolerationOpExists,
						Effect:   corev1.TaintEffectNoSchedule,
					}},
					Containers: []corev1.Container{{
						Name:            "device-plugin",
						Image:           fakeDevicePluginImage,
						ImagePullPolicy: corev1.PullIfNotPresent,
						SecurityContext: &corev1.SecurityContext{
							Privileged: ptr.To(true),
							RunAsUser:  ptr.To(int64(0)),
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name: devicePluginSocketVolume, MountPath: "/var/lib/kubelet/device-plugins",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: devicePluginSocketVolume,
						VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
							Path: "/var/lib/kubelet/device-plugins",
							Type: ptr.To(corev1.HostPathDirectory),
						}},
					}},
				},
			},
		},
	}

	_, err := clientset.AppsV1().DaemonSets(checkerNamespace).Create(ctx, daemonSet, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create fake device plugin: %w", err)
	}

	Eventually(func(g Gomega) {
		current, getErr := clientset.AppsV1().DaemonSets(checkerNamespace).Get(ctx, fakeDevicePluginName, metav1.GetOptions{})
		g.Expect(getErr).NotTo(HaveOccurred())
		g.Expect(current.Status.NumberReady).To(Equal(int32(1)))
	}, "60s", "1s").Should(Succeed())

	return func() error {
		// Several specs deploy the plugin in turn, so wait until it and its pod are fully gone
		// before the next one recreates it under the same name.
		err := clientset.AppsV1().DaemonSets(checkerNamespace).Delete(ctx, fakeDevicePluginName, metav1.DeleteOptions{
			PropagationPolicy: ptr.To(metav1.DeletePropagationForeground),
		})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		Eventually(func() bool {
			_, getErr := clientset.AppsV1().DaemonSets(checkerNamespace).Get(ctx, fakeDevicePluginName, metav1.GetOptions{})
			return apierrors.IsNotFound(getErr)
		}, "90s", "1s").Should(BeTrue(), "fake device plugin %s was not deleted", fakeDevicePluginName)
		return nil
	}, nil
}
