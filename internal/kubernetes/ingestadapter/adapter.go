package ingestadapter

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	coordinationv1client "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// ErrInvalidOptions is matched by the error New returns for unusable Options.
var ErrInvalidOptions = errors.New("ingestadapter: invalid options")

// Options configure an Adapter.
type Options struct {
	// RESTConfig is required. It is copied and never modified; a zero QPS,
	// Burst or Timeout of the copy becomes 50, 100 and 30 seconds. Every API
	// request goes through the transport settings of the copy
	// (WrapTransport included).
	RESTConfig *rest.Config
	// ControllerID is the Lease holder identity of this process (required).
	ControllerID string
	// LeaseNamespace and LeaseName name the leader election Lease (required).
	LeaseNamespace, LeaseName string
	// Clock times the Lease expiry and staleness decisions; nil means the
	// system clock.
	Clock liveingest.Clock
	// PollInterval is the real-time period of the Lease polls; 0 means 2s.
	PollInterval time.Duration
	// StaleAfter is how old the last successful Lease lookup may be before
	// the process stops counting as the leader; 0 means 3 x PollInterval.
	StaleAfter time.Duration
}

// Defaults of the REST config (the values of the status controller) and of the
// Options.
const (
	defaultQPS          = 50
	defaultBurst        = 100
	defaultTimeout      = 30 * time.Second
	defaultPollInterval = 2 * time.Second
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Adapter implements the Kubernetes side of live snapshot ingest. Its methods
// may be called from several goroutines.
type Adapter struct {
	clock        liveingest.Clock
	controllerID string
	leaseNS      string
	leaseName    string
	pollInterval time.Duration
	staleAfter   time.Duration

	// hello is the client of the hello path (Node and NodePathState); lease is
	// the separate client of the Lease polls. Both are immutable after New.
	hello client.Client
	lease coordinationv1client.LeasesGetter

	// Allocations: locks serialize one node, sem bounds all of them together.
	locks keyedLocks
	sem   chan struct{}

	// leader is the immutable snapshot of the last successful Lease lookup,
	// replaced as a whole by the poller (the only writer). nil until the first
	// success.
	leader  atomic.Pointer[leaderState]
	polling atomic.Bool
}

// New validates opts without any network I/O and returns an Adapter. An
// unusable opts yields a nil Adapter and an error that matches
// ErrInvalidOptions.
func New(opts Options) (*Adapter, error) {
	switch {
	case opts.RESTConfig == nil:
		return nil, fmt.Errorf("%w: RESTConfig is nil", ErrInvalidOptions)
	case opts.ControllerID == "":
		return nil, fmt.Errorf("%w: ControllerID is empty", ErrInvalidOptions)
	case opts.LeaseNamespace == "":
		return nil, fmt.Errorf("%w: LeaseNamespace is empty", ErrInvalidOptions)
	case opts.LeaseName == "":
		return nil, fmt.Errorf("%w: LeaseName is empty", ErrInvalidOptions)
	case opts.PollInterval < 0:
		return nil, fmt.Errorf("%w: PollInterval is negative", ErrInvalidOptions)
	case opts.StaleAfter < 0:
		return nil, fmt.Errorf("%w: StaleAfter is negative", ErrInvalidOptions)
	}

	base := rest.CopyConfig(opts.RESTConfig)
	if base.QPS == 0 {
		base.QPS = defaultQPS
	}
	if base.Burst == 0 {
		base.Burst = defaultBurst
	}
	if base.Timeout == 0 {
		base.Timeout = defaultTimeout
	}
	if base.WarningHandler == nil {
		base.WarningHandler = rest.NoWarnings{}
	}

	// One rate limiter per client. A limiter the caller put into the config is
	// the one of the hello path; the Lease polls always get their own, so a
	// burst of hellos cannot starve the leadership gate. QPS below zero means
	// "no limit" in client-go and leaves the limiter nil.
	helloCfg := rest.CopyConfig(base)
	leaseCfg := rest.CopyConfig(base)
	if helloCfg.RateLimiter == nil && helloCfg.QPS > 0 {
		helloCfg.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(helloCfg.QPS, helloCfg.Burst)
	}
	leaseCfg.RateLimiter = nil
	if leaseCfg.QPS > 0 {
		leaseCfg.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(leaseCfg.QPS, leaseCfg.Burst)
	}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("ingestadapter: build scheme: %w", err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("ingestadapter: build scheme: %w", err)
	}
	// A static mapper keeps New and the client free of discovery requests:
	// both kinds are cluster scoped.
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion, v1alpha1.GroupVersion})
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Node"), meta.RESTScopeRoot)
	mapper.Add(v1alpha1.GroupVersion.WithKind("NodePathState"), meta.RESTScopeRoot)

	// A direct (uncached) client: reads always go to the API server.
	hello, err := client.New(helloCfg, client.Options{Scheme: scheme, Mapper: mapper})
	if err != nil {
		return nil, fmt.Errorf("ingestadapter: build client: %w", err)
	}
	lease, err := coordinationv1client.NewForConfig(leaseCfg)
	if err != nil {
		return nil, fmt.Errorf("ingestadapter: build lease client: %w", err)
	}

	a := &Adapter{
		clock:        opts.Clock,
		controllerID: opts.ControllerID,
		leaseNS:      opts.LeaseNamespace,
		leaseName:    opts.LeaseName,
		pollInterval: opts.PollInterval,
		staleAfter:   opts.StaleAfter,
		hello:        hello,
		lease:        lease,
		sem:          make(chan struct{}, maxConcurrentAllocations),
	}
	if a.clock == nil {
		a.clock = systemClock{}
	}
	if a.pollInterval == 0 {
		a.pollInterval = defaultPollInterval
	}
	if a.staleAfter == 0 {
		a.staleAfter = 3 * a.pollInterval
	}
	return a, nil
}

// Nodes returns the NodeDirectory backed by the API server.
func (a *Adapter) Nodes() liveingest.NodeDirectory { return nodeDirectory{a: a} }

// Sessions returns the SessionStore backed by the NodePathState status.
func (a *Adapter) Sessions() liveingest.SessionStore { return sessionStore{a: a} }

// Leader returns the LeaderGate that follows the Lease. It is false until
// RunLeaderPoll has completed a successful lookup.
func (a *Adapter) Leader() liveingest.LeaderGate { return leaderGate{a: a} }
