package main

import (
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	"github.com/Azure/cluster-health-monitor/pkg/checker/podstartup"
	"k8s.io/klog/v2"
)

func init() {
	klog.InitFlags(nil)
}

func main() {
	defer klog.Flush()

	registerCheckers()
	// TODO: Add cluster health monitor implementation
	fmt.Println("Hello world")
}

func registerCheckers() {
	dnscheck.Register()
	podstartup.Register()
}
