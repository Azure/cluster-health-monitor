package main

import (
	"context"
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	chmclient "sigs.k8s.io/controller-runtime/pkg/client"

	chmv1alpha1 "github.com/Azure/cluster-health-monitor/apis/chm/v1alpha1"
	"github.com/Azure/cluster-health-monitor/pkg/noderunner"
)

func init() {
	klog.InitFlags(nil)
}

func main() {
	var nodeName string
	var crName string

	flag.StringVar(&nodeName, "node-name", "", "Name of the node to check (required)")
	flag.StringVar(&crName, "cr-name", "", "Name of the CheckNodeHealth CR (required)")
	flag.Parse()
	defer klog.Flush()

	if nodeName == "" {
		klog.ErrorS(nil, "Missing required flag: --node-name")
		os.Exit(1)
	}

	if crName == "" {
		klog.ErrorS(nil, "Missing required flag: --cr-name")
		os.Exit(1)
	}

	klog.InfoS("Starting node-checker", "node", nodeName, "cr", crName)

	ctx := context.Background()

	// Create in-cluster Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		klog.ErrorS(err, "Failed to get in-cluster config")
		os.Exit(10) // Exit code 10 indicates API server connection failure
	}

	// Create Kubernetes clientset for checker
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.ErrorS(err, "Failed to create Kubernetes clientset")
		os.Exit(10)
	}

	// Create controller-runtime client for CR updates
	scheme := runtime.NewScheme()
	if err := chmv1alpha1.AddToScheme(scheme); err != nil {
		klog.ErrorS(err, "Failed to add CHM scheme")
		os.Exit(10)
	}

	chmClient, err := chmclient.New(config, chmclient.Options{Scheme: scheme})
	if err != nil {
		klog.ErrorS(err, "Failed to create controller-runtime client")
		os.Exit(10)
	}

	// Run all checkers
	if err := noderunner.Run(ctx, clientset, chmClient, nodeName, crName); err != nil {
		klog.ErrorS(err, "Failed to run node checkers")
		os.Exit(1)
	}

	klog.InfoS("Node-checker completed successfully", "node", nodeName, "cr", crName)
}
