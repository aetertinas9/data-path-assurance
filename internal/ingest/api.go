package ingest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/transport/mtls"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// TLSFiles names the PEM files of the server TLS setup: the CA certificates
// that sign the client certificates, and the server certificate chain and key.
type TLSFiles struct{ CAFile, CertFile, KeyFile string }

// Limits are the resource limits of the server. A zero field selects its
// default, and no field may be wider than the default of the wire contract
// (FrameBurst and HelloBurst above 3, FrameInterval and HelloInterval below 10
// seconds, MaxStreams below 1 are rejected).
type Limits struct {
	// FrameBurst, HelloBurst and MaxStreams default to 3, 3 and 4096.
	FrameBurst, HelloBurst, MaxStreams int
	// FrameInterval and HelloInterval default to 10s.
	FrameInterval, HelloInterval time.Duration
	// HelloTimeout, IdleTimeout and NodeRetention default to 10s, 120s and 24h.
	HelloTimeout, IdleTimeout, NodeRetention time.Duration
}

// Config configures a Server.
type Config struct {
	ClusterID, ServiceDNS, CollectorProfileID string
	TrustedSources                            []fleet.TrustedSource
	TLS                                       TLSFiles
	Nodes                                     liveingest.NodeDirectory
	Sessions                                  liveingest.SessionStore
	Leader                                    liveingest.LeaderGate // nil means always leading
	Clock                                     liveingest.Clock      // nil means the system clock
	Limits                                    Limits
	Logger                                    *slog.Logger // nil means slog.Default()
}

// ErrInvalidConfig is matched by the error NewServer returns for an unusable
// Config.
var ErrInvalidConfig = errors.New("ingest: invalid config")

// ErrAlreadyServed is returned by a second call of Serve.
var ErrAlreadyServed = errors.New("ingest: already served")

// errNilListener is the listener error of a Serve call that was given none.
var errNilListener = errors.New("no listener")

// The defaults of Limits (GFL-080) and the bounds that no setting may widen.
const (
	defaultBurst        = 3
	defaultInterval     = 10 * time.Second
	defaultMaxStreams   = 4096
	defaultHelloTimeout = 10 * time.Second
	defaultIdleTimeout  = 120 * time.Second
	defaultRetention    = 24 * time.Hour
)

// resolve applies the defaults to the zero fields of l and reports the
// settings that are negative or wider than the wire contract allows.
func (l Limits) resolve() (Limits, error) {
	if l.FrameBurst < 0 || l.HelloBurst < 0 || l.MaxStreams < 0 ||
		l.FrameInterval < 0 || l.HelloInterval < 0 ||
		l.HelloTimeout < 0 || l.IdleTimeout < 0 || l.NodeRetention < 0 {
		return Limits{}, fmt.Errorf("%w: a limit is negative", ErrInvalidConfig)
	}
	set := func(v *int, def int) {
		if *v == 0 {
			*v = def
		}
	}
	setD := func(v *time.Duration, def time.Duration) {
		if *v == 0 {
			*v = def
		}
	}
	set(&l.FrameBurst, defaultBurst)
	set(&l.HelloBurst, defaultBurst)
	set(&l.MaxStreams, defaultMaxStreams)
	setD(&l.FrameInterval, defaultInterval)
	setD(&l.HelloInterval, defaultInterval)
	setD(&l.HelloTimeout, defaultHelloTimeout)
	setD(&l.IdleTimeout, defaultIdleTimeout)
	setD(&l.NodeRetention, defaultRetention)
	if l.FrameBurst > defaultBurst || l.HelloBurst > defaultBurst ||
		l.FrameInterval < defaultInterval || l.HelloInterval < defaultInterval {
		return Limits{}, fmt.Errorf("%w: a limit is wider than the wire contract", ErrInvalidConfig)
	}
	return l, nil
}

// DefaultTrustedSources returns the sources the live collector trust profile
// trusts by default: the sysfs parent source for the physical parent
// capability, the sysfs width source for the PCIe width capability and the
// nvidia-smi source for the NVIDIA UUID binding capability. The operator
// baseline capability is not included. Every call returns a new slice.
func DefaultTrustedSources() []fleet.TrustedSource {
	agent := func(name string) model.SourceRef {
		return model.SourceRef{Type: model.SourceTypeAgent, Name: name}
	}
	return []fleet.TrustedSource{
		{Capability: fleet.TrustSysfsPhysicalParent, Source: agent("path-agent/sysfs-parent")},
		{Capability: fleet.TrustSysfsPCIeWidth, Source: agent("path-agent/sysfs-width")},
		{Capability: fleet.TrustNVIDIAUUIDBinding, Source: agent("path-agent/nvidia-smi")},
	}
}

// Server is the live ingest gRPC server.
//
// Ownership. The engine owns every node's observation state and its own locks
// (see liveingest.Engine). The server owns the gRPC server of one Serve call,
// the count of authenticated streams (streams, atomic) and the receive
// goroutines of the streams (wg): every handler starts at most one, and Serve
// returns only after all of them ended. runCtx is written by Serve before the
// gRPC server starts and only read afterwards by the handlers it starts.
type Server struct {
	cfg    Config // limits resolved, sources copied
	log    *slog.Logger
	engine *liveingest.Engine
	tlsCfg *tls.Config

	served   atomic.Bool
	stopping chan struct{} // closed when the context of Serve is cancelled
	runCtx   context.Context
	streams  atomic.Int64
	wg       sync.WaitGroup
}

// NewServer validates cfg without any network I/O and returns the server.
// An unusable cfg yields a nil Server and an error that matches
// ErrInvalidConfig.
func NewServer(cfg Config) (*Server, error) {
	if !liveingest.ValidClusterID(cfg.ClusterID) {
		return nil, fmt.Errorf("%w: cluster ID", ErrInvalidConfig)
	}
	if liveingest.ValidateProfileID(cfg.CollectorProfileID) != nil {
		return nil, fmt.Errorf("%w: collector profile ID", ErrInvalidConfig)
	}
	if liveingest.ValidateTrustedSources(cfg.TrustedSources) != nil {
		return nil, fmt.Errorf("%w: trusted sources", ErrInvalidConfig)
	}
	if cfg.Nodes == nil || cfg.Sessions == nil {
		return nil, fmt.Errorf("%w: node directory and session store are required", ErrInvalidConfig)
	}
	limits, err := cfg.Limits.resolve()
	if err != nil {
		return nil, err
	}
	cfg.Limits = limits
	cfg.TrustedSources = append([]fleet.TrustedSource(nil), cfg.TrustedSources...)
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{cfg: cfg, log: logger.With(slog.String("component", "ingest")), stopping: make(chan struct{})}

	// The TLS material is read now (a failure is a config error) and again for
	// every new connection; a failed reload fails that handshake only.
	tlsCfg, err := mtls.ServerConfig(mtls.Files{CAFile: cfg.TLS.CAFile, CertFile: cfg.TLS.CertFile, KeyFile: cfg.TLS.KeyFile},
		cfg.ServiceDNS, func(err error) {
			s.log.Warn("tls reload failed", slog.String("class", classOfMTLS(err)))
		})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidConfig, classOfMTLS(err))
	}
	s.tlsCfg = tlsCfg

	engine, err := liveingest.NewEngine(liveingest.EngineConfig{
		ClusterID:          cfg.ClusterID,
		CollectorProfileID: cfg.CollectorProfileID,
		TrustedSources:     cfg.TrustedSources,
		Nodes:              boundedNodes{inner: cfg.Nodes},
		Sessions:           boundedSessions{inner: cfg.Sessions},
		Leader:             cfg.Leader,
		Clock:              cfg.Clock,
		FrameBurst:         limits.FrameBurst,
		HelloBurst:         limits.HelloBurst,
		FrameInterval:      limits.FrameInterval,
		HelloInterval:      limits.HelloInterval,
		NodeRetention:      limits.NodeRetention,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: engine", ErrInvalidConfig)
	}
	s.engine = engine
	return s, nil
}

// BundleSource returns the source of the assessment bundles of the nodes
// served. It is valid before Serve.
func (s *Server) BundleSource() app.LiveBundleSource {
	if s == nil || s.engine == nil {
		return app.NewNoObservationSource()
	}
	return s.engine.Source()
}

// Serve takes the ownership of lis, serves the Ingest service on it over TLS
// and returns when ctx is cancelled and the server has stopped. A second call
// returns ErrAlreadyServed.
func (s *Server) Serve(ctx context.Context, lis net.Listener) error {
	if s == nil || s.engine == nil {
		return fmt.Errorf("ingest: serve: %w", ErrInvalidConfig)
	}
	// A second call is refused before anything else is looked at; a nil
	// listener is refused without using up the one Serve call.
	if s.served.Load() {
		return ErrAlreadyServed
	}
	if lis == nil {
		return fmt.Errorf("ingest: serve: %w", errNilListener)
	}
	return s.serve(ctx, lis)
}
