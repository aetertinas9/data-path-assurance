package liveingest_test

// Fakes for the liveingest ports (GLI-110, GLI-122). All of them are safe for
// concurrent use, and every blocking call honours ctx cancellation.

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

var (
	_ liveingest.Clock         = (*gliClock)(nil)
	_ liveingest.SessionStore  = (*gliSessions)(nil)
	_ liveingest.NodeDirectory = (*gliNodes)(nil)
	_ liveingest.LeaderGate    = (*gliLeader)(nil)
)

// --- clock ---------------------------------------------------------------

type gliClock struct {
	mu  sync.Mutex
	now time.Time
}

func gliNewClock(start time.Time) *gliClock { return &gliClock{now: start} }

func (c *gliClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock by d. A negative d moves it backwards (GLI-052 clock
// regression).
func (c *gliClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set puts the clock at an absolute instant (an addition for the wire tests).
func (c *gliClock) Set(tm time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = tm
}

// --- session store -------------------------------------------------------

type gliSessionCall struct {
	NodeUID       string
	After, Result int64
	Err           error
}

// gliSessions is an in-memory CAS following GLI-031: next = max(stored, after)+1,
// stored never decreases on failure, MaxInt64 overflows. Every call is recorded,
// including failed and cancelled ones.
type gliSessions struct {
	mu       sync.Mutex
	stored   map[string]int64
	calls    []gliSessionCall
	err      error
	delay    time.Duration
	inflight atomic.Int64
}

func gliNewSessions() *gliSessions { return &gliSessions{stored: map[string]int64{}} }

// SetErr makes every following call fail with err until SetErr(nil).
func (s *gliSessions) SetErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// SetDelay makes every following call block for d (or until ctx is done).
func (s *gliSessions) SetDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = d
}

// SetStored overwrites the persisted value for a node (NodePathState deletion,
// overflow scenarios).
func (s *gliSessions) SetStored(nodeUID string, v int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stored[nodeUID] = v
}

func (s *gliSessions) Stored(nodeUID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stored[nodeUID]
}

func (s *gliSessions) Calls() []gliSessionCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]gliSessionCall(nil), s.calls...)
}

// CallsFor returns the recorded calls of one node in call order.
func (s *gliSessions) CallsFor(nodeUID string) []gliSessionCall {
	var out []gliSessionCall
	for _, c := range s.Calls() {
		if c.NodeUID == nodeUID {
			out = append(out, c)
		}
	}
	return out
}

// InFlight is the number of AllocateSession calls currently executing.
func (s *gliSessions) InFlight() int { return int(s.inflight.Load()) }

func (s *gliSessions) record(c gliSessionCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
}

func (s *gliSessions) AllocateSession(ctx context.Context, node fleet.NodeRef, after int64) (int64, error) {
	s.inflight.Add(1)
	defer s.inflight.Add(-1)

	s.mu.Lock()
	delay, injected := s.delay, s.err
	s.mu.Unlock()

	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			s.record(gliSessionCall{NodeUID: node.UID, After: after, Err: ctx.Err()})
			return 0, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		s.record(gliSessionCall{NodeUID: node.UID, After: after, Err: err})
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	call := gliSessionCall{NodeUID: node.UID, After: after}
	if injected != nil {
		call.Err = injected
		s.calls = append(s.calls, call)
		return 0, injected
	}
	base := s.stored[node.UID]
	if after > base {
		base = after
	}
	if base == math.MaxInt64 {
		call.Err = liveingest.ErrSessionOverflow
		s.calls = append(s.calls, call)
		return 0, liveingest.ErrSessionOverflow
	}
	next := base + 1
	s.stored[node.UID] = next
	call.Result = next
	s.calls = append(s.calls, call)
	return next, nil
}

// --- node directory ------------------------------------------------------

type gliNodes struct {
	mu    sync.Mutex
	nodes map[string]string
	err   error
	delay time.Duration
	calls int
}

func gliNewNodes() *gliNodes { return &gliNodes{nodes: map[string]string{}} }

func (n *gliNodes) Put(name, uid string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nodes[name] = uid
}

func (n *gliNodes) Delete(name string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.nodes, name)
}

// SetErr makes every following GetNode fail with err until SetErr(nil).
func (n *gliNodes) SetErr(err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.err = err
}

// SetDelay makes every following GetNode block for d (or until ctx is done).
func (n *gliNodes) SetDelay(d time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.delay = d
}

// Calls is the number of GetNode calls so far.
func (n *gliNodes) Calls() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.calls
}

func (n *gliNodes) GetNode(ctx context.Context, name string) (liveingest.NodeInfo, error) {
	n.mu.Lock()
	n.calls++
	delay, injected := n.delay, n.err
	n.mu.Unlock()

	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return liveingest.NodeInfo{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return liveingest.NodeInfo{}, err
	}
	if injected != nil {
		return liveingest.NodeInfo{}, injected
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	uid, ok := n.nodes[name]
	if !ok {
		return liveingest.NodeInfo{}, liveingest.ErrNodeNotFound
	}
	return liveingest.NodeInfo{Name: name, UID: uid}, nil
}

// --- leader gate ---------------------------------------------------------

type gliLeader struct{ leading atomic.Bool }

func gliNewLeader(leading bool) *gliLeader {
	l := &gliLeader{}
	l.leading.Store(leading)
	return l
}

func (l *gliLeader) Set(leading bool) { l.leading.Store(leading) }

func (l *gliLeader) Leading() bool { return l.leading.Load() }
