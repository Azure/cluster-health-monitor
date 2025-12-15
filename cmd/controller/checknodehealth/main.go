package main

import (
	"flag"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlmetricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/controller/checknodehealth"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	klog.InitFlags(nil)
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(chmv1alpha1.AddToScheme(scheme))
}

const (
	checkerPodNamespace = "kube-system"
)

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	flag.Parse()
	defer klog.Flush()

	// Set up controller-runtime logger
	ctrl.SetLogger(klog.NewKlogr())

	klog.InfoS("Starting CheckNodeHealth Controller")

	// Get checker pod image from environment variable
	checkerPodImage := os.Getenv("CHECKER_POD_IMAGE")
	if checkerPodImage == "" {
		klog.ErrorS(nil, "CHECKER_POD_IMAGE environment variable is not set")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
	klog.InfoS("Using checker pod image from CHECKER_POD_IMAGE", "image", checkerPodImage)

	// Get Kubernetes config
	cfg, err := ctrl.GetConfig()
	if err != nil {
		klog.ErrorS(err, "Unable to get kubeconfig")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	// Create manager
	syncPeriod := checknodehealth.SyncPeriod
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Metrics: ctrlmetricsserver.Options{
			BindAddress: metricsAddr,
		},
		Cache: cache.Options{
			SyncPeriod: &syncPeriod,
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Pod{}: {
					Namespaces: map[string]cache.Config{
						checkerPodNamespace: {},
					},
				},
			},
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "checknodehealth.clusterhealthmonitor.azure.com",
	})
	if err != nil {
		klog.ErrorS(err, "Unable to create manager")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	// Setup controller
	if err = (&checknodehealth.CheckNodeHealthReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		CheckerPodLabel:     "checknodehealth", // Label to identify health check pods
		CheckerPodImage:     checkerPodImage,
		CheckerPodNamespace: checkerPodNamespace,
	}).SetupWithManager(mgr); err != nil {
		klog.ErrorS(err, "Unable to create controller", "controller", "CheckNodeHealth")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	// Add health and readiness checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		klog.ErrorS(err, "Unable to set up health check")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		klog.ErrorS(err, "Unable to set up ready check")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	klog.InfoS("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		klog.ErrorS(err, "Problem running manager")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
}
