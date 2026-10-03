// Command path-controller is the Deployment that publishes GPU fleet status to
// Kubernetes: it elects a leader, watches GPUFleet, GPUDevice, NodePathState
// and Node objects and writes their status.
//
// By default this build has no live evidence source: every assessable node is
// reported with "no observation" and nothing listens. With --ingest-listen the
// process also serves live snapshot ingest over mutual TLS and the assessor
// reads the observations the connected agents sent.
package main

import (
	"io"
	"os"

	"google.golang.org/grpc/grpclog"
)

func main() {
	// The only global setting of this process besides the controller's klog and
	// controller-runtime loggers: gRPC's default logger writes to stderr in a
	// format that is not slog and would break the stderr contract. It runs
	// before anything makes a gRPC call.
	grpclog.SetLoggerV2(grpclog.NewLoggerV2(io.Discard, io.Discard, io.Discard))
	os.Exit(run(os.Args[1:], os.Stderr))
}
