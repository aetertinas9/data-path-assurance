package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
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
// returns the process exit code. It writes nothing to stdout.
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
	ctrl, err := controller.New(controller.Options{
		RESTConfig:     restConfig,
		Assessor:       app.NewLiveAssessor(app.NewNoObservationSource()),
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
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
