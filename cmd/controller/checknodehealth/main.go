package main

import (
	"flag"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
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
	defaultConfigPath = "/etc/config.yaml"
	// TODO: make configurable
	podImage     = "ubuntu:latest"
	podNamespace = "kube-system"
)

func main() {
	var configPath string
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string

	flag.StringVar(&configPath, "config", defaultConfigPath, "Path to the configuration file")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	flag.Parse()
	defer klog.Flush()

	klog.InfoS("Starting CheckNodeHealth Controller")

	// Create manager
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: ctrlmetricsserver.Options{
			BindAddress: metricsAddr,
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
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		PodLabel:     "checknodehealth", // Label to identify health check pods
		PodImage:     podImage,
		PodNamespace: podNamespace,
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
