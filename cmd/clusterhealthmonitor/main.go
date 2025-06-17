package main

import (
	"fmt"

	"github.com/Azure/cluster-health-monitor/pkg/checker/dnscheck"
	"github.com/Azure/cluster-health-monitor/pkg/checker/podstartup"
)

func main() {
	registerCheckers()
	// TODO: Add cluster health monitor implementation
	fmt.Println("Hello world")
}

func registerCheckers() {
	dnscheck.Register()
	podstartup.Register()
}
