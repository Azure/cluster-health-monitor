package e2e

import (
	"context"
	"fmt"
	"time"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	gpuTestSKU            = "Standard_ND96isr_H100_v5"
	gpuTestImage          = "cluster-health-monitor:test-latest"
	gpuTestServiceAccount = "checknodehealth-checker"
	nvidiaGPUResource     = corev1.ResourceName("nvidia.com/gpu")
	gpuInstanceTypeLabel  = "node.kubernetes.io/instance-type"
	gpuAcceleratorLabel   = "kubernetes.azure.com/accelerator"
	gpuPresenceLabel      = "nvidia.com/gpu.present"
	gpuHardwareTypeLabel  = "hardware-type"
	gpuWorkerRoleLabel    = "node-role.kubernetes.io/worker"
	gpuTaintKey           = "gpu"
	fakeGPUToolsVolume    = "fake-gpu-tools"
	fakeGPUToolsMount     = "/fake-gpu-tools"
	fakeGPUToolsInitImage = "registry.k8s.io/e2e-test-images/busybox:1.29-4@sha256:2e0f836850e09b8b7cc937681d6194537a09fbd5f6b9e08f4d646a85128e8937"
)

// This follows the node-resource simulation used by
// https://github.com/maryamtahhan/kind-gpu-sim. The simulator cannot execute CUDA kernels, so the
// pod replaces the three external GPU tools with deterministic fakes while exercising the real
// nodechecker, result parsing, Kubernetes client, and CheckNodeHealth status update path.
var _ = Describe("GPU node checker on Kind", Serial, func() {
	It("reports healthy GPU results on a simulated NVIDIA node", func() {
		ctx := context.Background()

		By("Registering the CheckNodeHealth API")
		Expect(chmv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
		restConfig, err := getKubeConfig()
		Expect(err).NotTo(HaveOccurred())
		k8sClient, err := client.New(restConfig, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())

		By("Selecting a Kind worker")
		nodeName, err := kindWorkerNode(ctx)
		Expect(err).NotTo(HaveOccurred())

		By("Simulating an eight-GPU NVIDIA node")
		restoreNode, err := simulateNVIDIAGPUNode(ctx, nodeName, 8, gpuTestSKU)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(restoreNode()).To(Succeed())
		})

		now := time.Now().UnixNano()
		cnhName := fmt.Sprintf("gpu-sim-%d", now)
		podName := fmt.Sprintf("gpu-sim-runner-%d", now)
		DeferCleanup(func() {
			_ = clientset.CoreV1().Pods(checkerNamespace).Delete(ctx, podName, metav1.DeleteOptions{})
			_ = deleteCheckNodeHealthCR(ctx, k8sClient, cnhName)
		})

		By("Creating a CheckNodeHealth resource for the simulated node")
		Expect(createCheckNodeHealthCR(ctx, k8sClient, cnhName, nodeName)).To(Succeed())

		By("Running the real nodechecker with deterministic GPU tool output")
		pod := gpuRunnerPod(podName, cnhName, nodeName)
		_, err = clientset.CoreV1().Pods(checkerNamespace).Create(ctx, pod, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() corev1.PodPhase {
			p, getErr := clientset.CoreV1().Pods(checkerNamespace).Get(ctx, podName, metav1.GetOptions{})
			if getErr != nil {
				return corev1.PodUnknown
			}
			return p.Status.Phase
		}, "120s", "2s").Should(Equal(corev1.PodSucceeded))

		completedPod, err := clientset.CoreV1().Pods(checkerNamespace).Get(ctx, podName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(completedPod.Spec.NodeName).To(Equal(nodeName), "runner was not scheduled on the simulated GPU node")

		logs, err := clientset.CoreV1().Pods(checkerNamespace).GetLogs(podName, &corev1.PodLogOptions{
			Container: "nodechecker",
		}).DoRaw(ctx)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("GPU nodechecker logs:\n%s\n", logs)

		By("Verifying all GPU checks reported healthy measurements")
		Eventually(func(g Gomega) {
			cnh, getErr := getCheckNodeHealthCR(ctx, k8sClient, cnhName)
			g.Expect(getErr).NotTo(HaveOccurred())

			results := make(map[string]chmv1alpha1.CheckResult, len(cnh.Status.Results))
			for _, result := range cnh.Status.Results {
				results[result.Name] = result
			}
			for _, name := range []string{"NcclAllReduce", "GpuBandwidth"} {
				result, found := results[name]
				g.Expect(found).To(BeTrue(), "%s result was not reported", name)
				g.Expect(result.Status).To(Equal(chmv1alpha1.CheckStatusHealthy),
					"%s result: code=%s message=%s", name, result.ErrorCode, result.Message)
			}
		}, "30s", "1s").Should(Succeed())
	})
})

func kindWorkerNode(ctx context.Context) (string, error) {
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("list nodes: %w", err)
	}
	for _, node := range nodes.Items {
		if _, controlPlane := node.Labels["node-role.kubernetes.io/control-plane"]; !controlPlane {
			return node.Name, nil
		}
	}
	return "", fmt.Errorf("no Kind worker node found")
}

func simulateNVIDIAGPUNode(ctx context.Context, nodeName string, gpuCount int64, sku string) (func() error, error) {
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
	node.Labels[gpuHardwareTypeLabel] = "gpu"
	node.Labels[gpuWorkerRoleLabel] = ""
	node.Labels[gpuPresenceLabel] = "true"
	node.Labels[gpuAcceleratorLabel] = "nvidia"
	node.Labels[gpuInstanceTypeLabel] = sku
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key: gpuTaintKey, Value: "true", Effect: corev1.TaintEffectNoSchedule,
	})
	node, err = nodes.Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("label and taint node %s: %w", nodeName, err)
	}

	if node.Status.Capacity == nil {
		node.Status.Capacity = corev1.ResourceList{}
	}
	if node.Status.Allocatable == nil {
		node.Status.Allocatable = corev1.ResourceList{}
	}
	quantity := *resource.NewQuantity(gpuCount, resource.DecimalSI)
	node.Status.Capacity[nvidiaGPUResource] = quantity
	node.Status.Allocatable[nvidiaGPUResource] = quantity
	if _, err := nodes.UpdateStatus(ctx, node, metav1.UpdateOptions{}); err != nil {
		return nil, fmt.Errorf("advertise simulated GPUs on node %s: %w", nodeName, err)
	}

	return func() error {
		current, getErr := nodes.Get(ctx, nodeName, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		for _, key := range []string{gpuHardwareTypeLabel, gpuWorkerRoleLabel, gpuPresenceLabel, gpuAcceleratorLabel, gpuInstanceTypeLabel} {
			if value, existed := original.Labels[key]; existed {
				current.Labels[key] = value
			} else {
				delete(current.Labels, key)
			}
		}
		current.Spec.Taints = original.Spec.Taints
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

func gpuRunnerPod(name, cnhName, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: checkerNamespace},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				gpuHardwareTypeLabel:     "gpu",
				"kubernetes.io/hostname": nodeName,
			},
			RestartPolicy:      corev1.RestartPolicyNever,
			ServiceAccountName: gpuTestServiceAccount,
			Tolerations: []corev1.Toleration{{
				Key: gpuTaintKey, Operator: corev1.TolerationOpEqual,
				Value: "true", Effect: corev1.TaintEffectNoSchedule,
			}},
			InitContainers: []corev1.Container{{
				Name:            "fake-gpu-tools",
				Image:           fakeGPUToolsInitImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"/bin/sh", "-c"},
				Args:            []string{fakeGPUToolsSetupScript},
				VolumeMounts: []corev1.VolumeMount{{
					Name: fakeGPUToolsVolume, MountPath: fakeGPUToolsMount,
				}},
			}},
			Containers: []corev1.Container{{
				Name:            "nodechecker",
				Image:           gpuTestImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"/nodechecker"},
				Args: []string{
					"--name=" + cnhName,
					"--enable-gpu-checks",
					"--sku=" + gpuTestSKU,
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: fakeGPUToolsVolume, MountPath: fakeGPUToolsMount},
					{Name: fakeGPUToolsVolume, MountPath: "/usr/bin"},
					{Name: fakeGPUToolsVolume, MountPath: "/usr/local/bin"},
					{Name: fakeGPUToolsVolume, MountPath: "/opt/openmpi/bin"},
				},
			}},
			Volumes: []corev1.Volume{{
				Name: fakeGPUToolsVolume,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			}},
		},
	}
}

const fakeGPUToolsSetupScript = `set -eu
cp /bin/busybox /fake-gpu-tools/busybox
cat >/fake-gpu-tools/nvidia-smi <<'EOF'
#!/fake-gpu-tools/busybox sh
for i in 0 1 2 3 4 5 6 7; do
  echo "GPU $i: NVIDIA H100 80GB HBM3 (UUID: GPU-$i)"
done
EOF
cat >/fake-gpu-tools/nvbandwidth <<'EOF'
#!/fake-gpu-tools/busybox sh
/fake-gpu-tools/busybox cat <<'JSON'
{"nvbandwidth":{"testcases":[
{"name":"host_to_device_memcpy_ce","status":"Passed","bandwidth_matrix":[["55","55","55","55","55","55","55","55"]]},
{"name":"device_to_host_memcpy_ce","status":"Passed","bandwidth_matrix":[["55","55","55","55","55","55","55","55"]]},
{"name":"device_to_device_memcpy_read_ce","status":"Passed","bandwidth_matrix":[
["N/A","395","395","395","395","395","395","395"],
["395","N/A","395","395","395","395","395","395"],
["395","395","N/A","395","395","395","395","395"],
["395","395","395","N/A","395","395","395","395"],
["395","395","395","395","N/A","395","395","395"],
["395","395","395","395","395","N/A","395","395"],
["395","395","395","395","395","395","N/A","395"],
["395","395","395","395","395","395","395","N/A"]]}
]}}
JSON
EOF
cat >/fake-gpu-tools/mpirun <<'EOF'
#!/fake-gpu-tools/busybox sh
report=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-J" ]; then
    shift
    report="$1"
  fi
  shift
done
if [ -z "$report" ]; then
  echo "missing -J report path" >&2
  exit 1
fi
/fake-gpu-tools/busybox cat >"$report" <<'JSON'
{"results":[{"size":17179869184}],"out_of_bounds":{"count":0},"average_bus_bandwidth":{"bandwidth":480.0}}
JSON
EOF
chmod 0755 /fake-gpu-tools/busybox /fake-gpu-tools/nvidia-smi /fake-gpu-tools/nvbandwidth /fake-gpu-tools/mpirun
`
