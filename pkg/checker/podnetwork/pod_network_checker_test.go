package podnetwork

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Azure/cluster-health-monitor/pkg/checker"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestPodNetworkChecker(t *testing.T) {
	tests := []struct {
		name           string
		description    string
		nodeName       string
		pods           []corev1.Pod
		service        *corev1.Service
		setupReactors  func(*fake.Clientset)
		expectedStatus checker.Status
		expectError    bool
	}{
		{
			name:        "Run with no eligible pods",
			description: "should return Unknown when no eligible CoreDNS pods are found",
			nodeName:    "node1",
			pods: []corev1.Pod{
				createCoreDNSPod("coredns-1", "node1", "10.0.1.1", true), // Same node, should be filtered out
			},
			service:        createKubeDNSService("10.0.0.10"),
			setupReactors:  nil,
			expectedStatus: checker.StatusUnknown,
			expectError:    false,
		},
		{
			name:        "Run with API error",
			description: "should return error when API server returns error",
			nodeName:    "node1",
			pods:        []corev1.Pod{},
			service:     nil,
			setupReactors: func(clientset *fake.Clientset) {
				// Simulate API error
				clientset.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("API server error")
				})
			},
			expectedStatus: "",
			expectError:    true,
		},
		{
			name:        "Run with single pod",
			description: "should return Unknown when only one CoreDNS pod is available",
			nodeName:    "node1",
			pods: []corev1.Pod{
				createCoreDNSPod("coredns-1", "node2", "10.0.1.2", true),
			},
			service:        createKubeDNSService("10.0.0.10"),
			setupReactors:  nil,
			expectedStatus: checker.StatusUnknown,
			expectError:    false,
		},
		{
			name:        "Run with service error",
			description: "should return Unhealthy when kube-dns service lookup fails",
			nodeName:    "node1",
			pods: []corev1.Pod{
				createCoreDNSPod("coredns-1", "node2", "10.0.1.2", true),
				createCoreDNSPod("coredns-2", "node3", "10.0.1.3", true),
			},
			service: nil, // No service, will cause lookup to fail
			setupReactors: func(clientset *fake.Clientset) {
				// Simulate service error
				clientset.PrependReactor("get", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("service lookup error")
				})
			},
			expectedStatus: checker.StatusUnhealthy,
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []runtime.Object
			for _, pod := range tt.pods {
				objects = append(objects, &pod)
			}
			if tt.service != nil {
				objects = append(objects, tt.service)
			}

			clientset := fake.NewSimpleClientset(objects...)

			// Setup any additional reactors for this test case
			if tt.setupReactors != nil {
				tt.setupReactors(clientset)
			}

			podChecker := NewPodNetworkChecker(clientset, tt.nodeName)

			ctx := context.Background()
			result, err := podChecker.Run(ctx)

			// Check error expectation
			if tt.expectError {
				if err == nil {
					t.Errorf("%s: expected error, got none", tt.description)
				}
				if result != nil {
					t.Errorf("%s: expected nil result when error occurs, got %+v", tt.description, result)
				}

				// For API errors, check the error message
				if tt.name == "Run with API error" {
					expectedError := "failed to get CoreDNS pods"
					if !strings.Contains(err.Error(), expectedError) {
						t.Errorf("%s: expected error to contain %q, got %v", tt.description, expectedError, err)
					}
				}
			} else {
				if err != nil {
					t.Fatalf("%s: unexpected error: %v", tt.description, err)
				}

				if result.Status != tt.expectedStatus {
					t.Errorf("%s: expected status %s, got %s", tt.description, tt.expectedStatus, result.Status)
				}

				if result.Detail.Message == "" {
					t.Errorf("%s: result should have detail message", tt.description)
				}

				// For unhealthy results, check error code
				if tt.expectedStatus == checker.StatusUnhealthy && result.Detail.Code == "" {
					t.Errorf("%s: unhealthy result should have error code", tt.description)
				}
			}
		})
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
