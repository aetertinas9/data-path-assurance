package graph

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// Clock is the only way anything here learns that time has passed. It is
// defined by the side that uses it, and all this side uses is a periodic tick,
// so that is all it asks for: a port with a method nobody calls would be a
// method every fake has to write.
type Clock interface {
	Ticker(d time.Duration) Ticker
}

// Ticker delivers the ticks a [Clock] was asked for until it is stopped.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// ReducerConfig is what a [Reducer] is built from.
//
// Capacity is the bounded queue's size and must be at least one. SnapshotEvery
// is how often a snapshot is published and must be positive. Clock is required.
//
// Emit and RequestResync are the two ways a reducer speaks outwards, and both
// are optional: a nil one means that signal is dropped, and nothing else about
// the reducer changes. Both are called on the reducer's own goroutine, so one
// that takes its time is one the queue fills up behind — which is the back
// pressure a bounded queue is for, not a fault.
type ReducerConfig struct {
	Capacity      int
	SnapshotEvery time.Duration
	Clock         Clock
	Emit          func([]Transition)
	RequestResync func(model.PartitionKey)
}

// Stats counts what a reducer has done, at the level the domain speaks in.
// Turning these into metrics is somebody else's job.
//
// Every field only ever grows. Enqueued and Evicted count what happened at the
// queue; the six outcome counters account for every event the reducer applied
// without error, one apiece; Invalid counts the events Apply refused, which a
// correctly wired reducer never sees because Offer refuses them first; and
// ResyncRequests counts the desync episodes a resync was asked for, at most one
// per episode.
type Stats struct {
	Enqueued            uint64
	Evicted             uint64
	Applied             uint64
	Resynced            uint64
	Stale               uint64
	Gaps                uint64
	AwaitingResync      uint64
	ObservationRejected uint64
	Invalid             uint64
	ResyncRequests      uint64
}

// Reducer is one partition's single writer. Events go in through
// [Reducer.Offer], are applied one at a time in the order they were enqueued by
// [Reducer.Run], and come out as published snapshots and emitted transitions.
//
// A Reducer must come from [NewReducer]. What the zero value does is not part
// of the contract.
//
// The concurrency it guarantees is one thing: [Reducer.Offer],
// [Reducer.Snapshot] and [Reducer.Stats] may be called from any goroutine while
// Run is running. Snapshot is lock-free, so a reader never waits on the writer
// and never sees half of an application.
type Reducer struct {
	partition model.PartitionKey
	cfg       ReducerConfig

	// published is the read path: one pointer, swapped whole, so a reader gets
	// some one consistent point of application and never a torn view of two.
	published atomic.Pointer[Snapshot]

	// started admits exactly one Run, ever.
	started atomic.Bool

	// wake tells a waiting Run that the queue has something in it. One buffered
	// slot is enough: a wakeup that finds the queue empty costs a loop, and a
	// wakeup can never be lost, because Offer signals after enqueuing and Run
	// only waits after finding nothing.
	wake chan struct{}

	// mu guards the queue and the counters, which Offer and Run both touch.
	// The ring under queue is allocated as it fills: capacity is the logical
	// bound, so a vast Capacity costs only what is actually queued.
	mu       sync.Mutex
	queue    []Event
	head     int
	length   int
	capacity int
	stopped  bool
	stats    Stats

	// The two fields below belong to the Run goroutine alone.
	state     State
	requested bool
}

// NewReducer returns the reducer for initial's partition and publishes
// initial's snapshot at once, so that [Reducer.Snapshot] answers before and
// whether or not [Reducer.Run] is ever called.
//
// initial must be a configured state and cfg must ask for a queue of at least
// one, a positive snapshot period and a clock; otherwise nil comes back beside
// an error wrapping [model.ErrInvalid].
func NewReducer(initial State, cfg ReducerConfig) (*Reducer, error) {
	switch {
	case !initial.configured():
		return nil, invalidf("NewReducer: the zero State is the state of no partition; obtain one from NewState")
	case cfg.Capacity < 1:
		return nil, invalidf("NewReducer: Capacity %d is less than one", cfg.Capacity)
	case cfg.SnapshotEvery <= 0:
		return nil, invalidf("NewReducer: SnapshotEvery %s is not positive", cfg.SnapshotEvery)
	case cfg.Clock == nil:
		return nil, invalidf("NewReducer: Clock is nil")
	}
	r := &Reducer{
		partition: initial.Partition(),
		cfg:       cfg,
		wake:      make(chan struct{}, 1),
		capacity:  cfg.Capacity,
		state:     initial,
	}
	r.published.Store(initial.Snapshot())
	return r, nil
}

// Offer puts ev at the tail of the queue. It never blocks, and any number of
// goroutines may call it at once.
//
// An event that is nil, foreign, unconstructed or for another partition is
// refused with an error wrapping [model.ErrInvalid], and nothing about the
// reducer changes — the same checks Apply makes, made here so that a wiring
// fault cannot eat the queue. After Run has returned, every offer is refused
// with [ErrStopped].
//
// A full queue does not refuse the new event; it makes room by dropping the
// oldest one it holds. What arrives is what is worth keeping, and the drop is
// not hidden: Evicted counts it, and evidence lost that way shows up as
// freshness falling off.
func (r *Reducer) Offer(ev Event) error {
	if err := validateEvent(ev); err != nil {
		return invalidf("Offer: %s", err)
	}
	if ev.Partition() != r.partition {
		return invalidf("Offer: event is for partition %q, not %q", ev.Partition(), r.partition)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return fmt.Errorf("%w: Offer: the reducer for %s has stopped", ErrStopped, r.partition)
	}
	if r.length == r.capacity {
		r.queue[r.head] = nil
		r.head = (r.head + 1) % len(r.queue)
		r.length--
		r.stats.Evicted++
	} else if r.length == len(r.queue) {
		grown := r.length * 2
		if grown < 4 {
			grown = 4
		}
		if grown > r.capacity || grown < 0 {
			grown = r.capacity
		}
		next := make([]Event, grown)
		for i := range r.length {
			next[i] = r.queue[(r.head+i)%len(r.queue)]
		}
		r.queue = next
		r.head = 0
	}
	r.queue[(r.head+r.length)%len(r.queue)] = ev
	r.length++
	r.stats.Enqueued++
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// dequeue takes the oldest queued event, if there is one.
func (r *Reducer) dequeue() (Event, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.length == 0 {
		return nil, false
	}
	ev := r.queue[r.head]
	r.queue[r.head] = nil
	r.head = (r.head + 1) % len(r.queue)
	r.length--
	return ev, true
}

// Run is the writer loop: it applies queued events one at a time in the order
// they were enqueued, emits the transitions each application brought about,
// asks for a resync when the stream needs one, and publishes a snapshot on
// every tick of the injected clock.
//
// It runs at most once per reducer. A nil context, a second call while the
// first is running, and a call after the first returned are all argument
// violations.
//
// An event Apply refuses is counted and stepped over — a reducer does not stop
// over an event. Cancelling ctx does stop it: it publishes one last snapshot,
// in which everything applied so far is visible, and returns ctx.Err(). Events
// still queued at that point are dropped unapplied, and every later offer is
// refused with [ErrStopped].
func (r *Reducer) Run(ctx context.Context) error {
	if ctx == nil {
		return invalidf("Run: ctx is nil")
	}
	if !r.started.CompareAndSwap(false, true) {
		return invalidf("Run: the reducer for %s has already been run", r.partition)
	}
	defer r.stop()

	// The ticker is asked for once and stopped once. A Clock that hands back no
	// ticker leaves ticks nil, and a receive on a nil channel simply never
	// fires, which is one fewer way for a fake to bring the loop down.
	var ticks <-chan time.Time
	if ticker := r.cfg.Clock.Ticker(r.cfg.SnapshotEvery); ticker != nil {
		defer ticker.Stop()
		ticks = ticker.C()
	}

	for {
		if err := ctx.Err(); err != nil {
			r.publish()
			return err
		}
		if ev, ok := r.dequeue(); ok {
			r.apply(ev)
			continue
		}
		select {
		case <-ctx.Done():
			r.publish()
			return ctx.Err()
		case <-r.wake:
		case <-ticks:
			r.publish()
		}
	}
}

// apply advances the state by one event and reports what came of it.
func (r *Reducer) apply(ev Event) {
	next, res, err := r.state.Apply(ev)
	if err != nil {
		r.countInvalid()
		return
	}
	r.state = next
	r.countOutcome(res.Outcome)

	if len(res.Transitions) > 0 && r.cfg.Emit != nil {
		r.cfg.Emit(res.Transitions)
	}

	// A resync is asked for once per desync episode. Every delta arriving
	// behind the first one would ask for the same thing, and the request that
	// goes missing is healed by the producer's own periodic snapshot anyway.
	// An episode is closed by the snapshot that ends it.
	switch res.Outcome {
	case OutcomeResynced:
		r.requested = false
	case OutcomeGap, OutcomeAwaitingResync:
		if !r.requested {
			r.requested = true
			if r.cfg.RequestResync != nil {
				r.cfg.RequestResync(r.partition)
			}
			r.countResyncRequest()
		}
	}
}

// publish makes the current state the one readers see.
func (r *Reducer) publish() {
	r.published.Store(r.state.Snapshot())
}

// stop closes the reducer to further events.
func (r *Reducer) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
}

// Snapshot returns the snapshot published last. It is never nil, it takes no
// lock, and it may be called while Run is running.
func (r *Reducer) Snapshot() *Snapshot {
	return r.published.Load()
}

// Stats returns a copy of the counters as they stand. It may be called while
// Run is running.
func (r *Reducer) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

// countOutcome records one applied event under the counter its outcome names.
func (r *Reducer) countOutcome(o ApplyOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch o {
	case OutcomeApplied:
		r.stats.Applied++
	case OutcomeResynced:
		r.stats.Resynced++
	case OutcomeStale:
		r.stats.Stale++
	case OutcomeGap:
		r.stats.Gaps++
	case OutcomeAwaitingResync:
		r.stats.AwaitingResync++
	case OutcomeObservationRejected:
		r.stats.ObservationRejected++
	}
}

// countInvalid records one event Apply refused.
func (r *Reducer) countInvalid() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats.Invalid++
}

// countResyncRequest records one desync episode a resync was asked for. It
// counts whether or not there was a RequestResync to call, so that a reducer
// wired without the port still reports how often one was needed.
func (r *Reducer) countResyncRequest() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats.ResyncRequests++
}
