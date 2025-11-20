package podnetwork

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	// DNS query settings
	dnsQueryDomain = "kubernetes.default.svc.cluster.local"
	dnsTimeout     = time.Second * 5

	// CoreDNS pod selection
	coreDNSNamespace = "kube-system"
	coreDNSSelector  = "k8s-app=kube-dns"
)

// PodNetworkChecker validates pod-to-pod network connectivity and cluster DNS functionality
type PodNetworkChecker struct {
	clientset    kubernetes.Interface
	checkerPodIP string
}

// NewPodNetworkChecker creates a new PodNetwork checker instance
func NewPodNetworkChecker(clientset kubernetes.Interface, checkerPodIP string) *PodNetworkChecker {
	return &PodNetworkChecker{
		clientset:    clientset,
		checkerPodIP: checkerPodIP,
	}
}

// Check performs the PodNetwork health check
func (p *PodNetworkChecker) Check(ctx context.Context, nodeName string) *checker.Result {
	klog.InfoS("Starting PodNetwork check", "checker", "PodNetwork", "node", nodeName)

	// Step 1: Get CoreDNS pods (excluding same node and subject node)
	coreDNSPods, err := p.getEligibleCoreDNSPods(ctx, nodeName)
	if err != nil {
		klog.ErrorS(err, "Failed to get CoreDNS pods", "checker", "PodNetwork", "node", nodeName)
		return checker.Unhealthy(ErrorCodeCoreDNSPodsRetrievalFailed, "Failed to connect to API server to get CoreDNS pods: "+err.Error())
	}

	if len(coreDNSPods) == 0 {
		klog.InfoS("No eligible CoreDNS pods found for testing", "checker", "PodNetwork", "node", nodeName)
		return checker.Skipped("No CoreDNS pods available for pod-to-pod network testing")
	}

	klog.InfoS("Found CoreDNS pods for testing", "checker", "PodNetwork", "node", nodeName, "count", len(coreDNSPods))

	// Step 2: Test DNS queries against individual CoreDNS pod IPs
	podToPodSuccess, _ := p.testCoreDNSPodConnectivity(ctx, coreDNSPods)

	// Step 3: Test DNS query using cluster DNS service IP
	clusterDNSSuccess, clusterDNSError := p.testClusterDNSConnectivity(ctx)

	// Step 4: Evaluate results according to the specified logic
	return p.evaluateResults(len(coreDNSPods), podToPodSuccess, clusterDNSSuccess, clusterDNSError)
}

// getEligibleCoreDNSPods returns CoreDNS pods that are not on the same node or subject node
func (p *PodNetworkChecker) getEligibleCoreDNSPods(ctx context.Context, nodeName string) ([]corev1.Pod, error) {
	// Get CoreDNS pods
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
		// Skip pods on the target node (where we're testing)
		if pod.Spec.NodeName == nodeName {
			klog.V(4).InfoS("Skipping CoreDNS pod on target node", "checker", "PodNetwork", "pod", pod.Name, "node", pod.Spec.NodeName)
			continue
		}

		// Skip pods that are not ready
		if pod.Status.Phase != corev1.PodRunning {
			klog.V(4).InfoS("Skipping non-running CoreDNS pod", "checker", "PodNetwork", "pod", pod.Name, "phase", pod.Status.Phase)
			continue
		}

		// Check if pod is ready
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}

		if !ready {
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

// testCoreDNSPodConnectivity tests DNS queries against individual CoreDNS pod IPs
func (p *PodNetworkChecker) testCoreDNSPodConnectivity(ctx context.Context, pods []corev1.Pod) (int, []error) {
	var successCount int
	var podErrors []error

	for _, pod := range pods {
		klog.V(2).InfoS("Testing DNS query to CoreDNS pod", "checker", "PodNetwork", "pod", pod.Name, "ip", pod.Status.PodIP)

		err := p.performDNSQuery(pod.Status.PodIP + ":53")
		if err != nil {
			klog.V(2).InfoS("DNS query to CoreDNS pod failed", "checker", "PodNetwork", "pod", pod.Name, "ip", pod.Status.PodIP, "error", err)
			podErrors = append(podErrors, fmt.Errorf("pod %s (%s): %w", pod.Name, pod.Status.PodIP, err))
		} else {
			klog.V(2).InfoS("DNS query to CoreDNS pod succeeded", "checker", "PodNetwork", "pod", pod.Name, "ip", pod.Status.PodIP)
			successCount++
		}
	}

	return successCount, podErrors
}

// testClusterDNSConnectivity tests DNS query using cluster DNS service IP
func (p *PodNetworkChecker) testClusterDNSConnectivity(ctx context.Context) (bool, error) {
	// Get kube-dns service to find cluster DNS IP
	service, err := p.clientset.CoreV1().Services(coreDNSNamespace).Get(ctx, "kube-dns", metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get kube-dns service: %w", err)
	}

	clusterDNSIP := service.Spec.ClusterIP
	klog.V(2).InfoS("Testing DNS query to cluster DNS service", "checker", "PodNetwork", "ip", clusterDNSIP)

	err = p.performDNSQuery(clusterDNSIP + ":53")
	if err != nil {
		klog.V(2).InfoS("DNS query to cluster DNS service failed", "checker", "PodNetwork", "ip", clusterDNSIP, "error", err)
		return false, err
	}

	klog.V(2).InfoS("DNS query to cluster DNS service succeeded", "checker", "PodNetwork", "ip", clusterDNSIP)
	return true, nil
}

// performDNSQuery performs a DNS query against the specified server
func (p *PodNetworkChecker) performDNSQuery(server string) error {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: dnsTimeout,
			}
			return d.DialContext(ctx, network, server)
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
	defer cancel()

	_, err := resolver.LookupIPAddr(ctx, dnsQueryDomain)
	return err
}

// evaluateResults evaluates the test results according to the specified logic
func (p *PodNetworkChecker) evaluateResults(totalPods, podToPodSuccess int, clusterDNSSuccess bool, clusterDNSError error) *checker.Result {
	klog.InfoS("Evaluating PodNetwork test results", "checker", "PodNetwork",
		"totalCoreDNSPods", totalPods,
		"podToPodSuccess", podToPodSuccess,
		"clusterDNSSuccess", clusterDNSSuccess)

	// Logic matrix implementation:
	// 1. If only one or less one available pod, return Unknown
	// 2. If cluster DNS service works AND at least one pod-to-pod test succeeds → Healthy
	// 3. If cluster DNS service works BUT all pod-to-pod tests fail → Unhealthy (pod connectivity issues)
	// 4. If cluster DNS service fails BUT at least one pod-to-pod test succeeds → Unhealthy (service issues)
	// 5. If cluster DNS service fails AND all pod-to-pod tests fail → Unhealthy (complete network failure)

	// Case 1: If only one available pod, return Unknown (insufficient data for conclusive test)
	if totalPods <= 1 {
		klog.InfoS("PodNetwork check result: Unknown - only one CoreDNS pod available, insufficient for conclusive network test", "checker", "PodNetwork")
		return checker.Unknown("Only less one CoreDNS pod available, insufficient for conclusive pod-to-pod network testing")
	}

	if clusterDNSSuccess && podToPodSuccess > 0 {
		// Case 2: Both cluster DNS and pod-to-pod connectivity work
		klog.InfoS("PodNetwork check result: Healthy - both cluster DNS and pod-to-pod connectivity working", "checker", "PodNetwork")
		return checker.Healthy()
	}

	// Generate detailed error message
	var errorDetails []string

	if !clusterDNSSuccess {
		errorDetails = append(errorDetails, fmt.Sprintf("Cluster DNS service failed: %v", clusterDNSError))
	}

	if podToPodSuccess == 0 {
		errorDetails = append(errorDetails, fmt.Sprintf("All %d pod-to-pod tests failed", totalPods))
	} else if podToPodSuccess < totalPods {
		failedCount := totalPods - podToPodSuccess
		errorDetails = append(errorDetails, fmt.Sprintf("%d of %d pod-to-pod tests failed", failedCount, totalPods))
	}

	// Determine the primary error code and message
	var errorCode string
	if !clusterDNSSuccess && podToPodSuccess == 0 {
		// Case 5: Complete network failure
		errorCode = ErrorCodeCompleteNetworkFailure
		klog.InfoS("PodNetwork check result: Unhealthy - complete network failure", "checker", "PodNetwork")
	} else if !clusterDNSSuccess {
		// Case 4: Service issues but pod connectivity works
		errorCode = ErrorCodeClusterDNSServiceFailure
		klog.InfoS("PodNetwork check result: Unhealthy - cluster DNS service failure", "checker", "PodNetwork")
	} else {
		// Case 3: Pod connectivity issues but service works
		errorCode = ErrorCodePodConnectivityFailure
		klog.InfoS("PodNetwork check result: Unhealthy - pod connectivity failure", "checker", "PodNetwork")
	}

	message := strings.Join(errorDetails, "; ")
	return checker.Unhealthy(errorCode, message)
}
