package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/signal"
	"syscall"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/ingest"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/ingestadapter"
)

// Exit codes (GKA-161).
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// fail writes the one-line startup error of GKA-162.
func fail(w io.Writer, token, msg string) {
	_, _ = fmt.Fprintf(w, "path-controller: error: %s: %s\n", token, msg)
}

func logLevel(name string) slog.Level {
	switch name {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// run wires the flags, the REST config, the assessor and the controller and
// returns the process exit code. It writes nothing to stdout. Without
// --ingest-listen the assessor has no observation source and nothing listens
// (GLI-093); with it the order of GLI-091 applies: arguments, REST config,
// ingest adapter and server, controller, listener, then the three parts run
// together.
func run(args []string, stderr io.Writer) int {
	cfg, err := parseArgs(args)
	if errors.Is(err, errHelp) {
		writeUsage(stderr)
		return exitOK
	}
	var ue *usageError
	if errors.As(err, &ue) {
		fail(stderr, "usage", ue.msg)
		return exitUsage
	}

	restConfig, err := controller.LoadRESTConfig(cfg.kubeconfig)
	if err != nil {
		fail(stderr, "config", "cannot load kubeconfig")
		return exitFailure
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: logLevel(cfg.logLevel)}))

	assessor := app.NewLiveAssessor(app.NewNoObservationSource())
	var (
		adapter *ingestadapter.Adapter
		server  *ingest.Server
	)
	if cfg.ingestListen != "" {
		adapter, err = ingestadapter.New(ingestadapter.Options{
			RESTConfig:     restConfig,
			ControllerID:   cfg.controllerID,
			LeaseNamespace: cfg.namespace,
			LeaseName:      cfg.leaderID,
			StaleAfter:     cfg.renewDeadline,
		})
		if err != nil {
			fail(stderr, "config", "invalid ingest options")
			return exitFailure
		}
		server, err = ingest.NewServer(ingest.Config{
			ClusterID:          cfg.clusterID,
			ServiceDNS:         cfg.ingestServiceDNS,
			CollectorProfileID: cfg.ingestProfileID,
			TrustedSources:     ingest.DefaultTrustedSources(),
			TLS:                ingest.TLSFiles{CAFile: cfg.ingestCAFile, CertFile: cfg.ingestCertFile, KeyFile: cfg.ingestKeyFile},
			Nodes:              adapter.Nodes(),
			Sessions:           adapter.Sessions(),
			Leader:             adapter.Leader(),
			Logger:             logger,
		})
		if err != nil {
			fail(stderr, "config", "invalid ingest options")
			return exitFailure
		}
		assessor = app.NewLiveAssessor(server.BundleSource())
	}

	ctrl, err := controller.New(controller.Options{
		RESTConfig:     restConfig,
		Assessor:       assessor,
		ControllerID:   cfg.controllerID,
		ClusterID:      cfg.clusterID,
		ResyncInterval: cfg.resync,
		LeaderElection: controller.LeaderElectionOptions{
			Namespace:     cfg.namespace,
			ID:            cfg.leaderID,
			LeaseDuration: cfg.leaseDuration,
			RenewDeadline: cfg.renewDeadline,
			RetryPeriod:   cfg.retryPeriod,
		},
		Logger: logger,
	})
	if err != nil {
		fail(stderr, "config", "invalid controller options")
		return exitFailure
	}

	var lis net.Listener
	if server != nil {
		lis, err = net.Listen("tcp", cfg.ingestListen)
		if err != nil {
			fail(stderr, "config", "invalid ingest options")
			return exitFailure
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if server != nil {
		return runWithIngest(ctx, stderr, logger, ctrl, server, lis, adapter)
	}
	if err := ctrl.Run(ctx); err != nil {
		if errors.Is(err, controller.ErrLeadershipLost) {
			logger.Error("controller stopped", "reason", "leadership lost")
		} else {
			logger.Error("controller stopped", "reason", "run failed")
		}
		fail(stderr, "internal", "controller failed")
		return exitFailure
	}
	return exitOK
}

// finished is the end of one of the parts runWithIngest runs.
type finished struct {
	component string // fixed word for the log
	failure   string // the closing line of GKA-162: "controller failed" or "ingest failed"
	err       error
}

// runWithIngest runs the controller, the ingest server and the Lease poll of
// the ingest adapter together (GLI-091). A part that ends with an error, or
// that ends without an error although ctx was not cancelled (a silent stop),
// cancels the others and decides the exit: 1 with the closing line of the part
// that ended first. A cancelled ctx (SIGINT, SIGTERM) stops all three and
// exits 0 once they have stopped. The function only starts the parts and
// collects their results; the parts own their own shutdown time.
func runWithIngest(ctx context.Context, stderr io.Writer, logger *slog.Logger,
	ctrl *controller.Controller, server *ingest.Server, lis net.Listener, adapter *ingestadapter.Adapter) int {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	const parts = 3
	results := make(chan finished, parts)
	go func() {
		results <- finished{"controller", "controller failed", ctrl.Run(runCtx)}
	}()
	go func() {
		results <- finished{"ingest", "ingest failed", server.Serve(runCtx, lis)}
	}()
	go func() {
		results <- finished{"leader poll", "ingest failed", adapter.RunLeaderPoll(runCtx)}
	}()

	var first *finished
	for range parts {
		r := <-results
		if first != nil {
			continue
		}
		if r.err == nil && runCtx.Err() != nil {
			continue // stopped because ctx was cancelled
		}
		first = &r
		reason := "run failed"
		switch {
		case r.err == nil:
			reason = "stopped without cause"
		case errors.Is(r.err, controller.ErrLeadershipLost):
			reason = "leadership lost"
		}
		logger.Error("stopped", "component", r.component, "reason", reason)
		cancel()
	}
	if first != nil {
		fail(stderr, "internal", first.failure)
		return exitFailure
	}
	return exitOK
}
