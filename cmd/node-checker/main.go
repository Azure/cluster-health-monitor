package main

import (
	"context"
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/noderunner"
)

func init() {
	klog.InitFlags(nil)
}

func main() {
	var name string

	flag.StringVar(&name, "name", "", "Name of the CheckNodeHealth resource (required)")
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

	nodeName := readNodeName(ctx, crClient, name)

	klog.InfoS("Retrieved node name from CR", "node", nodeName, "name", name)

	// Run all checkers
	if err := noderunner.Run(ctx, clientset, crClient, nodeName, name); err != nil {
		klog.ErrorS(err, "Failed to run node checkers")
		os.Exit(1)
	}

	klog.InfoS("Node-checker completed successfully", "node", nodeName, "name", name)
}

func readNodeName(ctx context.Context, crClient runtimeclient.Client, name string) string {
	cnh := &chmv1alpha1.CheckNodeHealth{}
	if err := crClient.Get(ctx, runtimeclient.ObjectKey{Name: name}, cnh); err != nil {
		klog.ErrorS(err, "Failed to get CheckNodeHealth CR")
		os.Exit(1)
	}

	nodeName := cnh.Spec.NodeRef.Name
	if nodeName == "" {
		klog.ErrorS(nil, "NodeRef.Name is empty in CheckNodeHealth CR", "name", name)
		os.Exit(1)
	}
	return nodeName
}

// createClients creates and returns both Kubernetes clientset and controller-runtime client
func createClients() (kubernetes.Interface, runtimeclient.Client, error) {
	// Create in-cluster Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, nil, err
	}

	// Create Kubernetes clientset for checker
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	// Create controller-runtime client for CR updates
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
