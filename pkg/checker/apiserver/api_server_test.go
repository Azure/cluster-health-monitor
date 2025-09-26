package apiserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Azure/cluster-health-monitor/pkg/config"
	"github.com/Azure/cluster-health-monitor/pkg/types"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestAPIServerChecker_check(t *testing.T) {
	checkerName := "test-api-server-checker"
	configMapNamespace := "test-namespace"
	configMapLabelKey := "cluster-health-monitor/checker-name"
	maxConfigMaps := 3

	tests := []struct {
		name           string
		client         *k8sfake.Clientset
		validateResult func(g *WithT, result *types.Result, err error)
	}{
		{
			name: "healthy result - all operations succeed",
			client: func() *k8sfake.Clientset {
				return k8sfake.NewSimpleClientset()
			}(),
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusHealthy))
			},
		},
		{
			name: "unhealthy result - configmap create fails",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewSimpleClientset()
				client.PrependReactor("create", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("create error")
				})
				return client
			}(),
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodeAPIServerCreateError))
			},
		},
		{
			name: "unhealthy result - configmap create times out",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewSimpleClientset()
				client.PrependReactor("create", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, context.DeadlineExceeded
				})
				return client
			}(),
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodeAPIServerCreateTimeout))
			},
		},
		{
			name: "unhealthy result - configmap get fails",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewSimpleClientset()
				client.PrependReactor("get", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("get error")
				})
				return client
			}(),
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodeAPIServerGetError))
			},
		},
		{
			name: "unhealthy result - configmap get times out",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewSimpleClientset()
				client.PrependReactor("get", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, context.DeadlineExceeded
				})
				return client
			}(),
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodeAPIServerGetTimeout))
			},
		},
		{
			name: "unhealthy result - configmap delete fails",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewSimpleClientset()
				client.PrependReactor("delete", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("delete error")
				})
				return client
			}(),
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodeAPIServerDeleteError))
			},
		},
		{
			name: "unhealthy result - configmap delete times out",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewSimpleClientset()
				client.PrependReactor("delete", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, context.DeadlineExceeded
				})
				return client
			}(),
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).ToNot(BeNil())
				g.Expect(result.Status).To(Equal(types.StatusUnhealthy))
				g.Expect(result.Detail.Code).To(Equal(ErrCodeAPIServerDeleteTimeout))
			},
		},
		{
			name: "error - max configmaps reached",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewClientset()
				// Preload client with the maximum number of ConfigMaps.
				for i := range maxConfigMaps {
					configMapName := fmt.Sprintf("configmap%d", i)
					client.CoreV1().ConfigMaps(configMapNamespace).Create(context.Background(), //nolint:errcheck // ignore error for test setup
						configMapWithLabels(configMapName, configMapNamespace, map[string]string{configMapLabelKey: checkerName}, time.Now()), metav1.CreateOptions{})
				}
				// Prevent ConfigMaps deletion from succeeding.
				client.PrependReactor("delete", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("error occurred")
				})
				return client
			}(),
			validateResult: func(g *WithT, result *types.Result, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("maximum number of ConfigMaps reached"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			apiServerChecker := &APIServerChecker{
				name: checkerName,
				config: &config.APIServerConfig{
					Namespace:     configMapNamespace,
					LabelKey:      configMapLabelKey,
					MutateTimeout: 1 * time.Second,
					ReadTimeout:   1 * time.Second,
					MaxObjects:    maxConfigMaps,
				},
				timeout:    5 * time.Second,
				kubeClient: tt.client,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			result, err := apiServerChecker.check(ctx)
			tt.validateResult(g, result, err)
		})
	}
}

func TestAPIServerChecker_garbageCollect(t *testing.T) {
	checkerName := "test-api-server-checker"
	configMapNamespace := "test-namespace"
	configMapLabelKey := "cluster-health-monitor/checker-name"
	checkerTimeout := 5 * time.Second

	tests := []struct {
		name        string
		client      *k8sfake.Clientset
		validateRes func(g *WithT, configMaps *corev1.ConfigMapList, err error)
	}{
		{
			name: "only removes configmaps older than timeout",
			client: k8sfake.NewSimpleClientset(
				configMapWithLabels(checkerName+"-empty-configmap-old", configMapNamespace, map[string]string{configMapLabelKey: checkerName}, time.Now().Add(-2*checkerTimeout)),
				configMapWithLabels(checkerName+"-empty-configmap-new", configMapNamespace, map[string]string{configMapLabelKey: checkerName}, time.Now()),
			),
			validateRes: func(g *WithT, configMaps *corev1.ConfigMapList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(configMaps.Items).To(HaveLen(1))
				g.Expect(configMaps.Items[0].Name).To(Equal(checkerName + "-empty-configmap-new"))
			},
		},
		{
			name: "no configmaps to delete",
			client: k8sfake.NewSimpleClientset(
				configMapWithLabels(checkerName+"-empty-configmap-new", configMapNamespace, map[string]string{configMapLabelKey: checkerName}, time.Now()), // configmap too new
				configMapWithLabels(checkerName+"-empty-configmap-no-labels", configMapNamespace, map[string]string{}, time.Now().Add(-2*checkerTimeout)),  // old configmap wrong labels
				configMapWithLabels("no-prefix-no-label", configMapNamespace, map[string]string{}, time.Now().Add(-2*checkerTimeout)),                      // configmap missing name prefix
			),
			validateRes: func(g *WithT, configMaps *corev1.ConfigMapList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(configMaps.Items).To(HaveLen(3))
				actualNames := make([]string, len(configMaps.Items))
				for i, cm := range configMaps.Items {
					actualNames[i] = cm.Name
				}
				g.Expect(actualNames).To(ConsistOf([]string{
					checkerName + "-empty-configmap-new",
					checkerName + "-empty-configmap-no-labels",
					"no-prefix-no-label",
				}))
			},
		},
		{
			name: "only removes configmap with checker labels",
			client: k8sfake.NewSimpleClientset(
				configMapWithLabels(checkerName+"-empty-configmap-old", configMapNamespace, map[string]string{configMapLabelKey: checkerName}, time.Now().Add(-2*checkerTimeout)),
				configMapWithLabels(checkerName+"-empty-configmap-no-label", configMapNamespace, map[string]string{}, time.Now().Add(-2*checkerTimeout)),
			),
			validateRes: func(g *WithT, configMaps *corev1.ConfigMapList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(configMaps.Items).To(HaveLen(1))
				g.Expect(configMaps.Items[0].Name).To(Equal(checkerName + "-empty-configmap-no-label"))
			},
		},
		{
			name: "error listing configmaps",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewSimpleClientset()
				client.PrependReactor("list", "configmaps", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					// fail the List call in garbageCollect because it uses a label selector.
					// This prevents breaking the test which also lists but does not use a selector.
					listAction, ok := action.(k8stesting.ListAction)
					if ok && listAction.GetListRestrictions().Labels.String() != "" {
						return true, nil, errors.New("error listing configmaps")
					}
					return false, nil, nil
				})
				return client
			}(),
			validateRes: func(g *WithT, configMaps *corev1.ConfigMapList, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to list ConfigMaps for garbage collection"))
			},
		},
		{
			name: "error deleting configmap",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewSimpleClientset(
					configMapWithLabels(checkerName+"-empty-configmap-1", configMapNamespace, map[string]string{configMapLabelKey: checkerName}, time.Now().Add(-2*checkerTimeout)),
					configMapWithLabels(checkerName+"-empty-configmap-2", configMapNamespace, map[string]string{configMapLabelKey: checkerName}, time.Now().Add(-2*checkerTimeout)),
				)
				// only fail the Delete call for configmap-1
				client.PrependReactor("delete", "configmaps", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					deleteAction, ok := action.(k8stesting.DeleteAction)
					if ok && deleteAction.GetName() == checkerName+"-empty-configmap-1" {
						return true, nil, errors.New("error deleting configmap")
					}
					return false, nil, nil
				})
				return client
			}(),
			validateRes: func(g *WithT, configMaps *corev1.ConfigMapList, err error) {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring("failed to delete old empty ConfigMap"))
				g.Expect(configMaps.Items).To(HaveLen(1)) // one configmap should be deleted
			},
		},
		{
			name: "not found error during delete is ignored",
			client: func() *k8sfake.Clientset {
				client := k8sfake.NewSimpleClientset(
					configMapWithLabels(checkerName+"-empty-configmap-old", configMapNamespace, map[string]string{configMapLabelKey: checkerName}, time.Now().Add(-2*checkerTimeout)),
				)
				client.PrependReactor("delete", "configmaps", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, apierrors.NewNotFound(
						schema.GroupResource{
							Group:    "",
							Resource: "configmaps",
						},
						checkerName+"-empty-configmap-old",
					)
				})
				return client
			}(),
			validateRes: func(g *WithT, configMaps *corev1.ConfigMapList, err error) {
				g.Expect(err).NotTo(HaveOccurred())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			checker := &APIServerChecker{
				name: checkerName,
				config: &config.APIServerConfig{
					Namespace:     configMapNamespace,
					LabelKey:      configMapLabelKey,
					MutateTimeout: 1 * time.Second,
					ReadTimeout:   1 * time.Second,
					MaxObjects:    5,
				},
				timeout:    checkerTimeout,
				kubeClient: tt.client,
			}

			err := checker.garbageCollect(context.Background())

			configMaps, listErr := tt.client.CoreV1().ConfigMaps(configMapNamespace).List(context.Background(), metav1.ListOptions{})
			g.Expect(listErr).NotTo(HaveOccurred())

			tt.validateRes(g, configMaps, err)
		})
	}
}

func TestAPIServerChecker_generateConfigMap(t *testing.T) {
	tests := []struct {
		name        string
		checkerName string
	}{
		{
			name:        "generates valid configmap",
			checkerName: "test-checker",
		},
		{
			name:        "successfully handles uppercase checker name",
			checkerName: "UPPERCASE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			checker := &APIServerChecker{
				name: tt.checkerName,
				config: &config.APIServerConfig{
					LabelKey: "cluster-health-monitor/checker-name",
				},
			}

			configMap := checker.generateConfigMap()
			g.Expect(configMap).ToNot(BeNil())

			// Verify configmap name is k8s compliant (DNS subdomain format).
			g.Expect(validation.NameIsDNSSubdomain(configMap.Name, false)).To(BeEmpty())
			// Verify configmap name has expected prefix.
			g.Expect(configMap.Name).To(HavePrefix(strings.ToLower(checker.Name()) + "-empty-configmap-"))
			// Verify checker labels are applied.
			g.Expect(configMap.Labels).To(Equal(checker.configMapLabels()))
		})
	}
}

// --- helpers ---
func configMapWithLabels(name, namespace string, labels map[string]string, creationTime time.Time) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			Labels:            labels,
			CreationTimestamp: metav1.NewTime(creationTime),
		},
	}
}
