package controller

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"

	"github.com/aetertinas9/data-path-assurance/internal/app"
)

// Clock supplies the controller's notion of now. A pass reads it exactly once.
type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// NewSystemClock returns the wall clock.
func NewSystemClock() Clock { return systemClock{} }

// LeaderElectionOptions configure the Lease lock.
type LeaderElectionOptions struct {
	Namespace     string        // required, DNS-1123 label (at most 63 characters)
	ID            string        // Lease name; "" means "path-controller"; DNS-1123 subdomain
	LeaseDuration time.Duration // 0 means 15s; at least 1s after defaulting
	RenewDeadline time.Duration // 0 means 10s
	RetryPeriod   time.Duration // 0 means 2s
}

// Options configure a Controller.
type Options struct {
	RESTConfig     *rest.Config     // required; copied, never modified
	Assessor       app.NodeAssessor // required
	Clock          Clock            // nil means NewSystemClock()
	ControllerID   string           // required, ^[A-Za-z0-9._-]{1,128}$
	ClusterID      string           // required, ^[A-Za-z0-9._-]{1,128}$
	ResyncInterval time.Duration    // 0 means DefaultResyncInterval
	LeaderElection LeaderElectionOptions
	Logger         *slog.Logger // nil means slog.Default()
}

const (
	// DefaultResyncInterval is the tick interval when Options.ResyncInterval is 0.
	DefaultResyncInterval = 30 * time.Second
	// MaxResyncInterval is the largest accepted Options.ResyncInterval.
	MaxResyncInterval = 30 * time.Second

	FieldManagerFleetStatus    = "dpa-fleet-status"
	FieldManagerDeviceStatus   = "dpa-device-status"
	FieldManagerNodePathSpec   = "dpa-nodepath-spec"
	FieldManagerNodePathStatus = "dpa-nodepath-status"
	FieldManagerNodeCondition  = "dpa-node-condition"
)

var (
	ErrInvalidOptions = errors.New("controller: invalid options")
	ErrLeadershipLost = errors.New("controller: leadership lost")
	ErrAlreadyRun     = errors.New("controller: already run")
)

const (
	defaultLeaderElectionID = "path-controller"
	defaultLeaseDuration    = 15 * time.Second
	defaultRenewDeadline    = 10 * time.Second
	defaultRetryPeriod      = 2 * time.Second

	defaultQPS     = 50
	defaultBurst   = 100
	defaultTimeout = 30 * time.Second

	// leaderElectionJitter is client-go's JitterFactor: RenewDeadline must exceed
	// RetryPeriod times this factor.
	leaderElectionJitter = 1.2
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// normalized is the validated form of Options that a Controller runs with.
type normalized struct {
	restConfig     *rest.Config
	assessor       app.NodeAssessor
	clock          Clock
	controllerID   string
	clusterID      string
	resyncInterval time.Duration
	leaderElection LeaderElectionOptions
	logger         *slog.Logger
}

func invalidOption(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidOptions, fmt.Sprintf(format, args...))
}

// normalize validates opts without any network access and returns the values
// the controller runs with (defaults applied, RESTConfig copied).
func normalize(opts Options) (normalized, error) {
	var n normalized
	if opts.RESTConfig == nil {
		return n, invalidOption("RESTConfig is nil")
	}
	if opts.Assessor == nil {
		return n, invalidOption("Assessor is nil")
	}
	if !identifierPattern.MatchString(opts.ClusterID) {
		return n, invalidOption("ClusterID is outside ^[A-Za-z0-9._-]{1,128}$")
	}
	if !identifierPattern.MatchString(opts.ControllerID) {
		return n, invalidOption("ControllerID is outside ^[A-Za-z0-9._-]{1,128}$")
	}
	if opts.ResyncInterval < 0 || opts.ResyncInterval > MaxResyncInterval {
		return n, invalidOption("ResyncInterval is outside 0..%s", MaxResyncInterval)
	}
	le := opts.LeaderElection
	if len(validation.IsDNS1123Label(le.Namespace)) > 0 {
		return n, invalidOption("LeaderElection.Namespace is not a DNS-1123 label")
	}
	if le.ID == "" {
		le.ID = defaultLeaderElectionID
	}
	if len(validation.IsDNS1123Subdomain(le.ID)) > 0 {
		return n, invalidOption("LeaderElection.ID is not a DNS-1123 subdomain")
	}
	if le.LeaseDuration < 0 || le.RenewDeadline < 0 || le.RetryPeriod < 0 {
		return n, invalidOption("LeaderElection durations must not be negative")
	}
	if le.LeaseDuration == 0 {
		le.LeaseDuration = defaultLeaseDuration
	}
	if le.RenewDeadline == 0 {
		le.RenewDeadline = defaultRenewDeadline
	}
	if le.RetryPeriod == 0 {
		le.RetryPeriod = defaultRetryPeriod
	}
	if le.LeaseDuration < time.Second {
		return n, invalidOption("LeaderElection.LeaseDuration is below 1s")
	}
	if le.LeaseDuration <= le.RenewDeadline {
		return n, invalidOption("LeaderElection.LeaseDuration must exceed RenewDeadline")
	}
	if le.RenewDeadline <= jitteredRetry(le.RetryPeriod) {
		return n, invalidOption("LeaderElection.RenewDeadline must exceed 1.2 x RetryPeriod")
	}

	n.restConfig = rest.CopyConfig(opts.RESTConfig)
	applyRESTDefaults(n.restConfig)
	n.assessor = opts.Assessor
	n.clock = opts.Clock
	if n.clock == nil {
		n.clock = NewSystemClock()
	}
	n.controllerID = opts.ControllerID
	n.clusterID = opts.ClusterID
	n.resyncInterval = opts.ResyncInterval
	if n.resyncInterval == 0 {
		n.resyncInterval = DefaultResyncInterval
	}
	n.leaderElection = le
	n.logger = opts.Logger
	if n.logger == nil {
		n.logger = slog.Default()
	}
	return n, nil
}

// applyRESTDefaults fills QPS, Burst and Timeout when they are zero; values the
// caller set are kept.
func applyRESTDefaults(cfg *rest.Config) {
	if cfg.QPS == 0 {
		cfg.QPS = defaultQPS
	}
	if cfg.Burst == 0 {
		cfg.Burst = defaultBurst
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
}

// jitteredRetry returns leaderElectionJitter x retry, saturated at the largest
// Duration. Converting an out-of-range float64 to an integer is
// implementation-dependent in Go (amd64 yields the minimum int64), which would
// let a huge RetryPeriod pass the relation check.
func jitteredRetry(retry time.Duration) time.Duration {
	f := leaderElectionJitter * float64(retry)
	if f >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return time.Duration(f)
}
