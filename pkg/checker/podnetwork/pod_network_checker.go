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

	// DNS query timeout
	dnsTimeout = time.Second * 5
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

func (p *PodNetworkChecker) Name() string {
	return "PodNetwork"
}

// Run performs the PodNetwork health check
func (p *PodNetworkChecker) Run(ctx context.Context) (*checker.Result, error) {
	klog.InfoS("Starting PodNetwork check", "checker", "PodNetwork", "node", p.nodeName)

	coreDNSPods, err := p.getEligibleCoreDNSPods(ctx)
	if err != nil {
		klog.ErrorS(err, "Failed to get CoreDNS pods", "checker", "PodNetwork", "node", p.nodeName)
		return nil, fmt.Errorf("failed to get CoreDNS pods: %w", err)
	}

	if len(coreDNSPods) <= 1 {
		klog.InfoS("No eligible CoreDNS pods found for checking", "checker", "PodNetwork", "node", p.nodeName, "count", len(coreDNSPods))
		return checker.Unknown("No CoreDNS pods available for pod-to-pod network checking"), nil
	}

	klog.InfoS("Found CoreDNS pods for checking", "checker", "PodNetwork", "node", p.nodeName, "count", len(coreDNSPods))

	// Get kube-dns service IP
	service, err := p.clientset.CoreV1().Services(coreDNSNamespace).Get(ctx, "kube-dns", metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "Failed to get kube-dns service", "checker", "PodNetwork", "node", p.nodeName)
		return nil, fmt.Errorf("Failed to get kube-dns service: %v", err)
	}

	clusterDNSIP := service.Spec.ClusterIP
	klog.InfoS("Retrieved cluster DNS service IP", "checker", "PodNetwork", "ip", clusterDNSIP)

	podToPodSuccess := p.checkDNSPodConnectivity(ctx, coreDNSPods)
	clusterSvcError := p.checkDNSSvcConnectivity(ctx, clusterDNSIP)
	clusterSvcSuccess := clusterSvcError == nil
	return p.evaluateResults(len(coreDNSPods), podToPodSuccess, clusterSvcSuccess), nil
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
		ip := pod.Status.PodIP
		klog.V(2).InfoS("Checking DNS connectivity to CoreDNS pod", "checker", "PodNetwork", "pod", pod.Name, "ip", ip)

		err := p.dnsPinger.ping(ctx, pod.Status.PodIP, kubernetesSvcDNSName, dnsTimeout)
		if err != nil {
			klog.V(2).ErrorS(err, "DNS connectivity to CoreDNS pod failed", "checker", "PodNetwork", "pod", pod.Name, "ip", ip)
		} else {
			klog.V(2).InfoS("DNS connectivity to CoreDNS pod succeeded", "checker", "PodNetwork", "pod", pod.Name, "ip", ip)
			succCount++
		}
	}

	return succCount
}

// checkDNSSvcConnectivity tests network connectivity to cluster DNS service IP
// by sending DNS queries and verifying that any response packet is received
func (p *PodNetworkChecker) checkDNSSvcConnectivity(ctx context.Context, clusterDNSIP string) error {
	klog.V(2).InfoS("checking DNS connectivity to cluster DNS service", "checker", "PodNetwork", "ip", clusterDNSIP)

	err := p.dnsPinger.ping(ctx, clusterDNSIP, kubernetesSvcDNSName, dnsTimeout)
	if err != nil {
		klog.V(2).InfoS("DNS connectivity to cluster DNS service failed", "checker", "PodNetwork", "ip", clusterDNSIP, "error", err)
		return err
	}

	klog.V(2).InfoS("DNS connectivity to cluster DNS service succeeded", "checker", "PodNetwork", "ip", clusterDNSIP)
	return nil
}

// evaluateResults evaluates the test results according to the specified logic
func (p *PodNetworkChecker) evaluateResults(totalPods, podToPodSuccess int, clusterSvcSuccess bool) *checker.Result {
	klog.InfoS("Evaluating PodNetwork test results", "checker", "PodNetwork",
		"totalCoreDNSPods", totalPods,
		"podToPodSuccess", podToPodSuccess,
		"clusterDNSSuccess", clusterSvcSuccess)

	// Logic matrix implementation:
	// 1. If cluster DNS service works AND at least one pod-to-pod test succeeds → Healthy
	// 2. If cluster DNS service works BUT all pod-to-pod tests fail → Unhealthy (pod connectivity issues)
	// 3. If cluster DNS service fails BUT at least one pod-to-pod test succeeds → Unhealthy (service issues)
	// 4. If cluster DNS service fails AND all pod-to-pod tests fail → Unhealthy (complete network failure)

	if clusterSvcSuccess && podToPodSuccess > 0 {
		// Case 1: Both cluster DNS and pod-to-pod connectivity work
		klog.InfoS("PodNetwork check result: Healthy - both cluster DNS and pod-to-pod connectivity working", "checker", "PodNetwork")
		return checker.Healthy()
	}

	var message string
	if clusterSvcSuccess && podToPodSuccess == 0 {
		// Case 2: Pod connectivity issues but service works
		klog.InfoS("PodNetwork check result: Unhealthy - pod connectivity failure", "checker", "PodNetwork")
		message = "Pod-to-pod network connectivity failure detected; cluster DNS service is reachable"
	}
	if !clusterSvcSuccess && podToPodSuccess > 0 {
		// Case 3: Service issues but pod connectivity works
		klog.InfoS("PodNetwork check result: Unhealthy - cluster DNS service failure", "checker", "PodNetwork")
		message = "Cluster DNS service connectivity failure detected; pod-to-pod network connectivity is functioning"
	}

	if !clusterSvcSuccess && podToPodSuccess == 0 {
		// Case 4: Complete network failure
		klog.InfoS("PodNetwork check result: Unhealthy - complete network failure", "checker", "PodNetwork")
		message = "A complete pod network failure has been detected. Pod-to-pod connectivity and the cluster DNS service are both failing"
	}

	return checker.Unhealthy(ErrorCodeNetworkConnectivityFailed, message)
}
