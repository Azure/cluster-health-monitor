package podnetwork

import (
	"context"
	"fmt"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	// CoreDNS pod selection
	coreDNSNamespace     = "kube-system"
	coreDNSSelector      = "k8s-app=kube-dns"
	kubernetesSvcDNSName = "kubernetes.default.svc.cluster.local"
)

// PodNetworkChecker validates pod-to-pod network connectivity and cluster DNS functionality
type PodNetworkChecker struct {
	clientset kubernetes.Interface
	nodeName  string
	dnsPinger dnsPinger
}

// NewPodNetworkChecker creates a new PodNetwork checker instance
func NewPodNetworkChecker(clientset kubernetes.Interface, nodeName string) *PodNetworkChecker {
	return &PodNetworkChecker{
		clientset: clientset,
		nodeName:  nodeName,
		dnsPinger: newDNSPinger(),
	}
}

// Run performs the PodNetwork health check
func (p *PodNetworkChecker) Run(ctx context.Context) (*checker.Result, error) {
	klog.InfoS("Starting PodNetwork check", "checker", "PodNetwork", "node", p.nodeName)

	coreDNSPods, err := p.getEligibleCoreDNSPods(ctx)
	if err != nil {
		klog.ErrorS(err, "Failed to get CoreDNS pods", "checker", "PodNetwork", "node", p.nodeName)
		return nil, fmt.Errorf("failed to get CoreDNS pods: %w", err)
	}

	if len(coreDNSPods) == 0 {
		klog.InfoS("No eligible CoreDNS pods found for checking", "checker", "PodNetwork", "node", p.nodeName)
		return checker.Unknown("No CoreDNS pods available for pod-to-pod network checking"), nil
	}

	klog.InfoS("Found CoreDNS pods for checking", "checker", "PodNetwork", "node", p.nodeName, "count", len(coreDNSPods))

	// Get kube-dns service IP
	service, err := p.clientset.CoreV1().Services(coreDNSNamespace).Get(ctx, "kube-dns", metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "Failed to get kube-dns service", "checker", "PodNetwork", "node", p.nodeName)
		// Treat API failures as network connectivity issues
		message := fmt.Sprintf("Failed to get kube-dns service: %v", err)
		return checker.Unhealthy(ErrorCodeNetworkConnectivityFailed, message), nil
	}

	clusterDNSIP := service.Spec.ClusterIP
	klog.InfoS("Retrieved cluster DNS service IP", "checker", "PodNetwork", "ip", clusterDNSIP)

	podToPodSuccess := p.checkDNSPodConnectivity(ctx, coreDNSPods)
	clusterSvcError := p.checkDNSSvcConnectivity(ctx, clusterDNSIP)
	return p.evaluateResults(len(coreDNSPods), podToPodSuccess, clusterSvcError), nil
}

// getEligibleCoreDNSPods returns CoreDNS pods that are not on the same node or subject node
func (p *PodNetworkChecker) getEligibleCoreDNSPods(ctx context.Context) ([]corev1.Pod, error) {
	labelSelector, err := labels.Parse(coreDNSSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to parse label selector: %w", err)
	}

	pods, err := p.clientset.CoreV1().Pods(coreDNSNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list CoreDNS pods: %w", err)
	}

	var eligiblePods []corev1.Pod
	for _, pod := range pods.Items {
		// Skip pods on the target node (where we're checking)
		if pod.Spec.NodeName == p.nodeName {
			klog.V(4).InfoS("Skipping CoreDNS pod on target node", "checker", "PodNetwork", "pod", pod.Name, "node", pod.Spec.NodeName)
			continue
		}

		if pod.Status.Phase != corev1.PodRunning {
			klog.V(4).InfoS("Skipping non-running CoreDNS pod", "checker", "PodNetwork", "pod", pod.Name, "phase", pod.Status.Phase)
			continue
		}

		if !isPodReady(pod) {
			klog.V(4).InfoS("Skipping non-ready CoreDNS pod", "checker", "PodNetwork", "pod", pod.Name)
			continue
		}

		// Skip pods without IP
		if pod.Status.PodIP == "" {
			klog.V(4).InfoS("Skipping CoreDNS pod without IP", "checker", "PodNetwork", "pod", pod.Name)
			continue
		}

		eligiblePods = append(eligiblePods, pod)
	}

	return eligiblePods, nil
}

// isPodReady checks if a pod is in ready state
func isPodReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// checkDNSPodConnectivity tests network connectivity to individual CoreDNS pod IPs
// by sending DNS queries and verifying that any response packet is received
func (p *PodNetworkChecker) checkDNSPodConnectivity(ctx context.Context, pods []corev1.Pod) int {
	var succCount int

	for _, pod := range pods {
		klog.V(2).InfoS("Checking DNS connectivity to CoreDNS pod", "checker", "PodNetwork", "pod", pod.Name, "ip", pod.Status.PodIP)

		err := p.dnsPinger.ping(ctx, pod.Status.PodIP, kubernetesSvcDNSName, time.Second*5)
		if err != nil {
			klog.V(2).ErrorS(err, "DNS connectivity to CoreDNS pod failed", "checker", "PodNetwork", "pod", pod.Name, "ip", pod.Status.PodIP)
		} else {
			klog.V(2).InfoS("DNS connectivity to CoreDNS pod succeeded", "checker", "PodNetwork", "pod", pod.Name, "ip", pod.Status.PodIP)
			succCount++
		}
	}

	return succCount
}

// checkDNSSvcConnectivity tests network connectivity to cluster DNS service IP
// by sending DNS queries and verifying that any response packet is received
func (p *PodNetworkChecker) checkDNSSvcConnectivity(ctx context.Context, clusterDNSIP string) error {
	klog.V(2).InfoS("checking DNS connectivity to cluster DNS service", "checker", "PodNetwork", "ip", clusterDNSIP)

	err := p.dnsPinger.ping(ctx, clusterDNSIP, kubernetesSvcDNSName, time.Second*5)
	if err != nil {
		klog.V(2).InfoS("DNS connectivity to cluster DNS service failed", "checker", "PodNetwork", "ip", clusterDNSIP, "error", err)
		return err
	}

	klog.V(2).InfoS("DNS connectivity to cluster DNS service succeeded", "checker", "PodNetwork", "ip", clusterDNSIP)
	return nil
}

// evaluateResults evaluates the test results according to the specified logic
func (p *PodNetworkChecker) evaluateResults(totalPods, podToPodSuccess int, clusterSvcError error) *checker.Result {
	clusterSvcSuccess := clusterSvcError == nil
	klog.InfoS("Evaluating PodNetwork test results", "checker", "PodNetwork",
		"totalCoreDNSPods", totalPods,
		"podToPodSuccess", podToPodSuccess,
		"clusterDNSSuccess", clusterSvcSuccess)

	// Logic matrix implementation:
	// 1. If only one or less one available pod, return Unknown
	// 2. If cluster DNS service works AND at least one pod-to-pod test succeeds → Healthy
	// 3. If cluster DNS service works BUT all pod-to-pod tests fail → Unhealthy (pod connectivity issues)
	// 4. If cluster DNS service fails BUT at least one pod-to-pod test succeeds → Unhealthy (service issues)
	// 5. If cluster DNS service fails AND all pod-to-pod tests fail → Unhealthy (complete network failure)

	// Case 1: If only one available pod, return Unknown (insufficient data for conclusive test)
	if totalPods <= 1 {
		klog.InfoS("PodNetwork check result: Unknown - only one CoreDNS pod available, insufficient for conclusive network test", "checker", "PodNetwork")
		return checker.Unknown("Only less one CoreDNS pod available, insufficient for conclusive pod-to-pod network checking")
	}

	if clusterSvcSuccess && podToPodSuccess > 0 {
		// Case 2: Both cluster DNS and pod-to-pod connectivity work
		klog.InfoS("PodNetwork check result: Healthy - both cluster DNS and pod-to-pod connectivity working", "checker", "PodNetwork")
		return checker.Healthy()
	}

	var message string
	if clusterSvcSuccess && podToPodSuccess == 0 {
		// Case 3: Pod connectivity issues but service works
		klog.InfoS("PodNetwork check result: Unhealthy - pod connectivity failure", "checker", "PodNetwork")
		message = "Pod-to-pod network connectivity failure detected; cluster DNS service is reachable"
	}
	if !clusterSvcSuccess && podToPodSuccess > 0 {
		// Case 4: Service issues but pod connectivity works
		klog.InfoS("PodNetwork check result: Unhealthy - cluster DNS service failure", "checker", "PodNetwork")
		message = "Cluster DNS service connectivity failure detected; pod-to-pod network connectivity is functioning"
	}

	if !clusterSvcSuccess && podToPodSuccess == 0 {
		// Case 5: Complete network failure
		klog.InfoS("PodNetwork check result: Unhealthy - complete network failure", "checker", "PodNetwork")
		message = "Complete pod network failure detected; both pod-to-pod connectivity and cluster DNS service are unreachable"
	}

	return checker.Unhealthy(ErrorCodeNetworkConnectivityFailed, message)
}
