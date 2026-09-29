// Command path-controller is the Deployment that publishes GPU fleet status to
// Kubernetes: it elects a leader, watches GPUFleet, GPUDevice, NodePathState
// and Node objects and writes their status. This build has no live evidence
// source; every assessable node is reported with "no observation".
package main

import "os"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}
