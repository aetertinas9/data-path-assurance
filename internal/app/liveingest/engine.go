package liveingest

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// The retention bounds of one node's observation ring (GLI-072): at most this
// many accepted frames and, together, at most this many observations. The last
// accepted frame is always kept.
const (
	ringFrames       = 16
	ringObservations = 8192
)

// sweepEvery is how far apart (on the injected Clock) the opportunistic
// release of expired observation state runs. Expiry is judged lazily wherever
// a node is touched; the sweep only frees nodes nobody touches again.
const sweepEvery = time.Minute

// EngineConfig configures an Engine. The engine reads time only through Clock
// (rate, retention and the certificate lifetime of a frame); every real-time
// timer — the hello, idle and certificate timers, the call limits of the
// ports — belongs to the adapter that owns the transport.
type EngineConfig struct {
	// ClusterID is the cluster the controller serves.
	ClusterID string
	// CollectorProfileID and TrustedSources make the collector trust profile of
	// every bundle (GLI-073); both must have passed ValidateProfileID and
	// ValidateTrustedSources.
	CollectorProfileID string
	TrustedSources     []fleet.TrustedSource

	Nodes    NodeDirectory
	Sessions SessionStore
	// Leader is nil when this process always leads.
	Leader LeaderGate
	Clock  Clock

	// FrameBurst and FrameInterval, HelloBurst and HelloInterval are the token
	// buckets of GLI-052; NodeRetention is GLI-053.
	FrameBurst, HelloBurst       int
	FrameInterval, HelloInterval time.Duration
	NodeRetention                time.Duration
}

// Engine is the per-node reducer of live snapshot ingest: it decides hellos,
// owns the session bookkeeping, the fence of superseded streams, the rate
// buckets, the admission of frames and the immutable per-node view that
// NodeBundles turns into assessment bundles. It implements
// app.LiveBundleSource.
//
// Ownership and lock order. Every node has one writer lock (node.mu) that
// serializes the changes of its observation state: the commit of a frame, the
// hello commit, a discard. The published view is an atomic pointer that
// readers (NodeBundles) load without a lock. node.mu may be taken without any
// other lock held and is never held while a port is called. Engine.mu guards
// only the node table; it is a leaf (nothing else is locked or called while
// holding it).
//
// Three things live at different lifetimes and are never mixed: the highest
// issued session (node.after) lives as long as the process; the rate buckets
// outlive streams and sessions; the observation state (view) is dropped by a
// new hello, by retention and by a loss of leadership.
type Engine struct {
	cfg EngineConfig

	// epoch counts the observed losses of leadership. A hello that started
	// before a loss cannot commit after it.
	epoch atomic.Uint64

	mu        sync.Mutex
	nodes     map[string]*node // guarded by mu; entries are never removed (a few words each)
	lastSweep time.Time        // guarded by mu
}

// node is everything the engine keeps for one node UID.
type node struct {
	// helloSem serializes the hello steps that look the Node up, allocate the
	// session and commit (GLI-034). It is a channel so that waiting honours a
	// context. after is touched only while holding it.
	helloSem chan struct{}
	after    int64

	// bmu guards the two buckets.
	bmu            sync.Mutex
	frames, hellos bucket

	// mu is the single writer lock of the observation state: active and every
	// store into view happen under it.
	mu     sync.Mutex
	active *Handle
	view   atomic.Pointer[view]
}

// NewEngine validates cfg and returns the engine. An unusable cfg yields an
// error that wraps fleet.ErrInvalidInput; the text names the setting, never
// its value.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	switch {
	case !ValidClusterID(cfg.ClusterID):
		return nil, fmt.Errorf("%w: engine cluster ID", fleet.ErrInvalidInput)
	case cfg.Nodes == nil || cfg.Sessions == nil || cfg.Clock == nil:
		return nil, fmt.Errorf("%w: engine ports", fleet.ErrInvalidInput)
	case cfg.FrameBurst < 1 || cfg.HelloBurst < 1:
		return nil, fmt.Errorf("%w: engine burst", fleet.ErrInvalidInput)
	case cfg.FrameInterval <= 0 || cfg.HelloInterval <= 0 || cfg.NodeRetention <= 0:
		return nil, fmt.Errorf("%w: engine interval", fleet.ErrInvalidInput)
	}
	if err := ValidateProfileID(cfg.CollectorProfileID); err != nil {
		return nil, err
	}
	if err := ValidateTrustedSources(cfg.TrustedSources); err != nil {
		return nil, err
	}
	cfg.TrustedSources = append([]fleet.TrustedSource(nil), cfg.TrustedSources...)
	return &Engine{cfg: cfg, nodes: map[string]*node{}}, nil
}

// Leading reports whether this process currently leads. WHEN the gate says it
// does not, the engine drops every node's observation state and ends every
// active stream with Unavailable before it answers (GLI-037); a later true
// answer does not bring the state back.
func (e *Engine) Leading() bool {
	if e.cfg.Leader == nil || e.cfg.Leader.Leading() {
		return true
	}
	e.leaderLost()
	return false
}

// leaderLost drops all observation state and ends all active streams.
func (e *Engine) leaderLost() {
	e.epoch.Add(1)
	for _, n := range e.allNodes() {
		n.mu.Lock()
		n.view.Store(nil)
		if h := n.active; h != nil {
			n.active = nil
			h.finish(reject(EndUnavailable, ClassNotLeader))
		}
		n.mu.Unlock()
	}
}

// allNodes returns a copy of the node table.
func (e *Engine) allNodes() []*node {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*node, 0, len(e.nodes))
	for _, n := range e.nodes {
		out = append(out, n)
	}
	return out
}

// node returns the entry of uid, creating it when there is none.
func (e *Engine) node(uid string) *node {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := e.nodes[uid]
	if n == nil {
		n = &node{helloSem: make(chan struct{}, 1)}
		e.nodes[uid] = n
	}
	return n
}

// find returns the entry of uid, or nil when the node was never seen.
func (e *Engine) find(uid string) *node {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nodes[uid]
}

// sweepIfDue releases the expired observation state of all nodes at most once
// per sweepEvery of Clock time. It changes no observable result: expiry is
// also judged wherever a node is read or written.
func (e *Engine) sweepIfDue(now time.Time) {
	e.mu.Lock()
	if d := now.Sub(e.lastSweep); !e.lastSweep.IsZero() && d >= 0 && d < sweepEvery {
		e.mu.Unlock()
		return
	}
	e.lastSweep = now
	e.mu.Unlock()
	for _, n := range e.allNodes() {
		n.expire(now, e.cfg.NodeRetention)
	}
}

// expire drops the node's view when it is older than retention (GLI-053). It
// is a no-op for a view published meanwhile.
func (n *node) expire(now time.Time, retention time.Duration) {
	v := n.view.Load()
	if v == nil || !v.expired(now, retention) {
		return
	}
	n.mu.Lock()
	if n.view.Load() == v {
		n.view.Store(nil)
	}
	n.mu.Unlock()
}

// takeFrame and takeHello consume one token of the node's bucket.
func (n *node) takeFrame(now time.Time, burst int, interval time.Duration) bool {
	n.bmu.Lock()
	defer n.bmu.Unlock()
	return n.frames.take(now, burst, interval)
}

func (n *node) takeHello(now time.Time, burst int, interval time.Duration) bool {
	n.bmu.Lock()
	defer n.bmu.Unlock()
	return n.hellos.take(now, burst, interval)
}

// Source returns the engine as the source of assessment bundles.
func (e *Engine) Source() app.LiveBundleSource { return e }
