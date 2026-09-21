package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/nodecheckerrunner"
)

func init() {
	klog.InitFlags(nil)
}

func main() {
	var (
		name            string
		enableGPUChecks bool
		sku             string
		runTimeout      time.Duration
	)

	flag.StringVar(&name, "name", "", "Name of the CheckNodeHealth resource (required)")
	flag.BoolVar(&enableGPUChecks, "enable-gpu-checks", false, "Run the GPU checks. Requires the GPU image.")
	flag.StringVar(&sku, "sku", "", "VM size of the node, used to select the expected GPU count and thresholds")
	flag.DurationVar(&runTimeout, "run-timeout", 0,
		"Budget for all checks together, after which the remaining ones are reported as Unknown. "+
			"Zero means no limit.")
	flag.Parse()
	defer klog.Flush()

	if name == "" {
		klog.ErrorS(nil, "Missing required flag: --name")
		os.Exit(1)
	}

	klog.InfoS("Starting node-checker", "name", name)

	// We don't need a timeout context here because the controller will handle timeouts
	ctx := context.Background()

	// Create Kubernetes clients
	clientset, crClient, err := createClients()
	if err != nil {
		klog.ErrorS(err, "Failed to create Kubernetes clients")
		os.Exit(1)
	}

	nodeName, err := readNodeName(ctx, crClient, name)
	if err != nil {
		klog.ErrorS(err, "Failed to read node name")
		os.Exit(1)
	}

	klog.InfoS("Retrieved node name from CR", "node", nodeName, "name", name)

	opts := nodecheckerrunner.Options{NodeName: nodeName, CRName: name, RunTimeout: runTimeout}
	if enableGPUChecks {
		opts.GPU = &nodecheckerrunner.GPUOptions{SKU: sku}
	}

	// Create runner and execute all checkers
	runner := nodecheckerrunner.NewRunner(clientset, crClient, opts)
	if err := runner.Run(ctx); err != nil {
		klog.ErrorS(err, "Failed to run node checkers")
		os.Exit(1)
	}

	klog.InfoS("Node-checker completed successfully", "node", nodeName, "name", name)
}

func readNodeName(ctx context.Context, crClient runtimeclient.Client, name string) (string, error) {
	cnh := &chmv1alpha1.CheckNodeHealth{}
	if err := crClient.Get(ctx, runtimeclient.ObjectKey{Name: name}, cnh); err != nil {
		return "", err
	}

	nodeName := cnh.Spec.NodeRef.Name
	if nodeName == "" {
		return "", fmt.Errorf("NodeRef.Name is empty in CheckNodeHealth CR")
	}
	return nodeName, nil
}

// createClients creates and returns both Kubernetes clientset and controller-runtime client
func createClients() (kubernetes.Interface, runtimeclient.Client, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, nil, err
	}

	// Create Kubernetes clientset for native Kubernetes resources
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	// Create controller-runtime client for CheckNodeHealth CRD
	scheme := runtime.NewScheme()
	if err := chmv1alpha1.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}
	crClient, err := runtimeclient.New(config, runtimeclient.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, err
	}

	return clientset, crClient, nil
}
