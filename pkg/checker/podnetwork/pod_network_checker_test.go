package podnetwork

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestPodNetworkChecker_getEligibleCoreDNSPods(t *testing.T) {
	tests := []struct {
		name          string
		nodeName      string
		pods          []corev1.Pod
		expectedCount int
		description   string
	}{
		{
			name:     "filters out pods on same node",
			nodeName: "node1",
			pods: []corev1.Pod{
				createCoreDNSPod("coredns-1", "node1", "10.0.1.1", true),
				createCoreDNSPod("coredns-2", "node2", "10.0.1.2", true),
			},
			expectedCount: 1,
			description:   "should exclude pod on same node",
		},
		{
			name:     "filters out non-running pods",
			nodeName: "node1",
			pods: []corev1.Pod{
				createCoreDNSPodWithPhase("coredns-1", "node2", "10.0.1.2", corev1.PodPending, true),
				createCoreDNSPod("coredns-2", "node3", "10.0.1.3", true),
			},
			expectedCount: 1,
			description:   "should exclude non-running pods",
		},
		{
			name:     "filters out non-ready pods",
			nodeName: "node1",
			pods: []corev1.Pod{
				createCoreDNSPod("coredns-1", "node2", "10.0.1.2", false),
				createCoreDNSPod("coredns-2", "node3", "10.0.1.3", true),
			},
			expectedCount: 1,
			description:   "should exclude non-ready pods",
		},
		{
			name:     "filters out pods without IP",
			nodeName: "node1",
			pods: []corev1.Pod{
				createCoreDNSPod("coredns-1", "node2", "", true),
				createCoreDNSPod("coredns-2", "node3", "10.0.1.3", true),
			},
			expectedCount: 1,
			description:   "should exclude pods without IP",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []runtime.Object
			for _, pod := range tt.pods {
				objects = append(objects, &pod)
			}

			clientset := fake.NewSimpleClientset(objects...)
			podChecker := NewNodeNetworkChecker(clientset, "10.0.2.1")

			ctx := context.Background()

			pods, err := podChecker.getEligibleCoreDNSPods(ctx, tt.nodeName)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(pods) != tt.expectedCount {
				t.Errorf("%s: expected %d eligible pods, got %d", tt.description, tt.expectedCount, len(pods))
			}
		})
	}
}

func TestPodNetworkChecker_Check_NoEligiblePods(t *testing.T) {
	// Test case where no eligible CoreDNS pods are found
	pods := []corev1.Pod{
		createCoreDNSPod("coredns-1", "node1", "10.0.1.1", true), // Same node, should be filtered out
	}
	service := createKubeDNSService("10.0.0.10")

	var objects []runtime.Object
	for _, pod := range pods {
		objects = append(objects, &pod)
	}
	objects = append(objects, service)

	clientset := fake.NewSimpleClientset(objects...)
	podChecker := NewNodeNetworkChecker(clientset, "10.0.2.1")

	ctx := context.Background()
	result := podChecker.Check(ctx, "node1")

	if result.Status != checker.StatusSkipped {
		t.Errorf("expected status %s, got %s", checker.StatusSkipped, result.Status)
	}

	if result.Detail.Message == "" {
		t.Error("skipped result should have detail message")
	}
}

func TestPodNetworkChecker_Check_APIError(t *testing.T) {
	// Test case where API server returns error
	clientset := fake.NewSimpleClientset()

	// Simulate API error
	clientset.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("API server error")
	})

	podChecker := NewNodeNetworkChecker(clientset, "10.0.2.1")

	ctx := context.Background()
	result := podChecker.Check(ctx, "node1")

	if result.Status != checker.StatusUnhealthy {
		t.Errorf("expected status %s, got %s", checker.StatusUnhealthy, result.Status)
	}

	if result.Detail.Code != ErrorCodeCoreDNSPodsRetrievalFailed {
		t.Errorf("expected error code %s, got %s", ErrorCodeCoreDNSPodsRetrievalFailed, result.Detail.Code)
	}
}

func TestPodNetworkChecker_Check_SinglePod(t *testing.T) {
	// Test case where only one CoreDNS pod is available
	pods := []corev1.Pod{
		createCoreDNSPod("coredns-1", "node2", "10.0.1.2", true),
	}
	service := createKubeDNSService("10.0.0.10")

	var objects []runtime.Object
	for _, pod := range pods {
		objects = append(objects, &pod)
	}
	objects = append(objects, service)

	clientset := fake.NewSimpleClientset(objects...)
	podChecker := NewNodeNetworkChecker(clientset, "10.0.2.1")

	ctx := context.Background()
	result := podChecker.Check(ctx, "node1")

	if result.Status != checker.StatusSkipped {
		t.Errorf("expected status %s, got %s", checker.StatusSkipped, result.Status)
	}

	if result.Detail.Message == "" {
		t.Error("skipped result should have detail message")
	}
}

func TestPodNetworkChecker_Check_ServiceError(t *testing.T) {
	// Test case where kube-dns service lookup fails
	pods := []corev1.Pod{
		createCoreDNSPod("coredns-1", "node2", "10.0.1.2", true),
		createCoreDNSPod("coredns-2", "node3", "10.0.1.3", true),
	}

	var objects []runtime.Object
	for _, pod := range pods {
		objects = append(objects, &pod)
	}

	clientset := fake.NewSimpleClientset(objects...)

	// Simulate service error
	clientset.PrependReactor("get", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("service lookup error")
	})

	podChecker := NewNodeNetworkChecker(clientset, "10.0.2.1")

	ctx := context.Background()
	result := podChecker.Check(ctx, "node1")

	if result.Status != checker.StatusUnhealthy {
		t.Errorf("expected status %s, got %s", checker.StatusUnhealthy, result.Status)
	}

	// The result should contain some kind of network failure error
	if result.Detail.Message == "" {
		t.Error("unhealthy result should have detail message")
	}

	if result.Detail.Code == "" {
		t.Error("unhealthy result should have error code")
	}
}

// Helper functions to create test objects

func createCoreDNSPod(name, nodeName, podIP string, ready bool) corev1.Pod {
	return createCoreDNSPodWithPhase(name, nodeName, podIP, corev1.PodRunning, ready)
}

func createCoreDNSPodWithPhase(name, nodeName, podIP string, phase corev1.PodPhase, ready bool) corev1.Pod {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: coreDNSNamespace,
			Labels: map[string]string{
				"k8s-app": "kube-dns",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
		},
		Status: corev1.PodStatus{
			Phase: phase,
			PodIP: podIP,
		},
	}

	if ready {
		pod.Status.Conditions = []corev1.PodCondition{
			{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			},
		}
	} else {
		pod.Status.Conditions = []corev1.PodCondition{
			{
				Type:   corev1.PodReady,
				Status: corev1.ConditionFalse,
			},
		}
	}

	return pod
}

func createKubeDNSService(clusterIP string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kube-dns",
			Namespace: coreDNSNamespace,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: clusterIP,
		},
	}
}
