package e2e

import (
	"context"
	"fmt"
	"slices"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/controller/checknodehealth"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
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
// path without requiring physical GPUs.
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
		if !slices.Contains(deployment.Spec.Template.Spec.Containers[0].Args, "-enable-gpu-checks") {
			deployment.Spec.Template.Spec.Containers[0].Args = append(
				deployment.Spec.Template.Spec.Containers[0].Args, "-enable-gpu-checks")
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

	It("runs healthy GPU checks on a simulated driver-only NVIDIA node", func() {
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

		runHealthyGPUControllerFlow(ctx, k8sClient, nodeName)
	})

	It("runs healthy GPU checks on a simulated fully managed NVIDIA node", func() {
		By("Selecting a node with both CoreDNS replicas on remote nodes")
		nodeName, err := nodeWithRemoteCoreDNS(ctx)
		Expect(err).NotTo(HaveOccurred())

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

		runHealthyGPUControllerFlow(ctx, k8sClient, nodeName)
	})
})

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
	Expect(completed.Status.Results).To(HaveLen(4))
	results := make(map[string]chmv1alpha1.CheckResult, len(completed.Status.Results))
	for _, result := range completed.Status.Results {
		_, duplicate := results[result.Name]
		Expect(duplicate).To(BeFalse(), "duplicate result %s", result.Name)
		results[result.Name] = result
	}
	for _, name := range []string{"PodStartup", "PodNetwork", "NcclAllReduce", "GpuBandwidth"} {
		result, found := results[name]
		Expect(found).To(BeTrue(), "%s result was not reported", name)
		Expect(result.Status).To(Equal(chmv1alpha1.CheckStatusHealthy),
			"%s result: code=%s message=%s", name, result.ErrorCode, result.Message)
		Expect(result.ErrorCode).To(BeEmpty(), "%s unexpectedly reported an error code", name)
	}

	nccl := results["NcclAllReduce"]
	Expect(nccl.Message).To(ContainSubstring("480.000 GB/s"))
	Expect(nccl.Message).To(ContainSubstring("460.000 GB/s threshold"))

	bandwidth := results["GpuBandwidth"]
	for _, testcase := range []string{
		"host_to_device_memcpy_ce",
		"device_to_host_memcpy_ce",
		"device_to_device_memcpy_read_ce",
	} {
		Expect(bandwidth.Message).To(ContainSubstring(testcase))
	}
	Expect(bandwidth.Message).To(ContainSubstring("48.000 GB/s threshold"))
	Expect(bandwidth.Message).To(ContainSubstring("335.000 GB/s threshold"))

	By("Verifying the aggregate CheckNodeHealth condition is healthy")
	Expect(completed.Status.Conditions).To(HaveLen(1))
	condition := completed.Status.Conditions[0]
	Expect(condition.Type).To(Equal(checknodehealth.ConditionTypeHealthy))
	Expect(condition.Status).To(Equal(metav1.ConditionTrue))
	Expect(condition.Reason).To(Equal(checknodehealth.ReasonCheckPassed))
	for _, name := range []string{"PodStartup", "PodNetwork", "NcclAllReduce", "GpuBandwidth"} {
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

	node := original.DeepCopy()
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	// Mirror the controller-relevant metadata observed on an actual managed AKS GPU node.
	node.Labels[gpuAcceleratorLabel] = "nvidia"
	node.Labels[gpuInstanceTypeLabel] = sku
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key: gpuTaintKey, Value: gpuTaintValue, Effect: corev1.TaintEffectNoSchedule,
	})
	node, err = nodes.Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("label and taint node %s: %w", nodeName, err)
	}

	// Start every scenario without a stale device-plugin resource. The fully managed test lets its
	// fake plugin advertise the resource through kubelet, rather than patching node status directly.
	if node.Status.Capacity != nil {
		delete(node.Status.Capacity, nvidiaGPUResource)
	}
	if node.Status.Allocatable != nil {
		delete(node.Status.Allocatable, nvidiaGPUResource)
	}
	if _, err := nodes.UpdateStatus(ctx, node, metav1.UpdateOptions{}); err != nil {
		return nil, fmt.Errorf("clear GPU status on node %s: %w", nodeName, err)
	}

	return func() error {
		current, getErr := nodes.Get(ctx, nodeName, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		for _, key := range []string{gpuAcceleratorLabel, gpuInstanceTypeLabel} {
			if value, existed := original.Labels[key]; existed {
				current.Labels[key] = value
			} else {
				delete(current.Labels, key)
			}
		}
		current.Spec.Taints = slices.Clone(original.Spec.Taints)
		current, getErr = nodes.Update(ctx, current, metav1.UpdateOptions{})
		if getErr != nil {
			return getErr
		}

		if value, existed := original.Status.Capacity[nvidiaGPUResource]; existed {
			current.Status.Capacity[nvidiaGPUResource] = value
		} else {
			delete(current.Status.Capacity, nvidiaGPUResource)
		}
		if value, existed := original.Status.Allocatable[nvidiaGPUResource]; existed {
			current.Status.Allocatable[nvidiaGPUResource] = value
		} else {
			delete(current.Status.Allocatable, nvidiaGPUResource)
		}
		_, getErr = nodes.UpdateStatus(ctx, current, metav1.UpdateOptions{})
		return getErr
	}, nil
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
		err := clientset.AppsV1().DaemonSets(checkerNamespace).Delete(ctx, fakeDevicePluginName, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}, nil
}
