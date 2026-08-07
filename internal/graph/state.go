package graph

import (
	"slices"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// State is one partition's graph: the assets and edges stored for it, the
// evidence window of what was observed there, and the sequence bookkeeping that
// says how much of the producer's stream it has seen.
//
// A State is an immutable value. [State.Apply] returns a new state and leaves
// the receiver exactly as it was, so a state can be copied, passed and held
// onto freely, and a snapshot taken of one keeps answering as it did however
// far the stream has moved on since.
//
// The zero State is not a state of anything: it has no partition, no window to
// assemble into, and applying an event to it is refused. A state to apply to
// comes from [NewState].
//
// A State is not safe for concurrent use. Serializing access is the caller's
// business — in this system, the [Reducer]'s.
type State struct {
	partition model.PartitionKey
	// assets holds the stored assets by Key, and edges the stored edges by
	// identity. Neither map is mutated once a state holds it; an application
	// builds new ones, which is what lets a state and the snapshots taken of it
	// share what they have in common.
	assets map[string]model.AssetRef
	edges  map[edgeKey]Edge
	window evidence.Window
	// last is the number of the last delta taken, meaningful only once
	// baseline is set. synced says the state has a baseline and has seen no gap
	// since; only a synced state takes topology deltas.
	last     uint64
	baseline bool
	synced   bool
}

// NewState returns the empty state of partition, with an evidence window
// bounded by wcfg: no topology, no baseline, and nothing observed yet.
//
// The partition key must say something and wcfg must be bounds a window can be
// built from; otherwise the zero State comes back beside an error wrapping
// [model.ErrInvalid].
func NewState(partition model.PartitionKey, wcfg evidence.Config) (State, error) {
	if !partition.IsValid() {
		return State{}, invalidf("NewState: partition key is empty")
	}
	window, err := evidence.NewWindow(wcfg)
	if err != nil {
		return State{}, invalidf("NewState: %s", err)
	}
	return State{
		partition: partition,
		assets:    make(map[string]model.AssetRef),
		edges:     make(map[edgeKey]Edge),
		window:    window,
	}, nil
}

// Partition returns the partition the state is of. The zero State returns the
// zero key.
func (s State) Partition() model.PartitionKey {
	return s.partition
}

// configured reports whether s came from NewState. Only such a state has the
// maps and the window an application needs, and the fields are unexported, so
// there is no other way to come by one.
func (s State) configured() bool {
	return s.assets != nil
}

// ApplyResult is what one application of an event amounted to: the outcome, and
// the transitions the application brought about. A result with no transitions
// carries a collection of length zero.
type ApplyResult struct {
	Outcome     ApplyOutcome
	Transitions []Transition
}

// Transition is one change in what the graph holds: an asset or an edge came
// into it, or left it. The Kind says which of the two fields is filled in; the
// other is the zero value.
type Transition struct {
	Kind  TransitionKind
	Asset model.AssetRef
	Edge  Edge
}

// Apply returns the state that applying ev leads to, beside what the
// application amounted to. It is a pure function: the receiver is untouched,
// nothing outside the arguments is read, and the same state and the same event
// always give the same answer.
//
// Three things are errors, and all three are wiring faults rather than
// conditions of the data: applying to the zero State, an event that is nil,
// foreign or never went through a constructor, and an event for another
// partition. They are reported in that order, and each of them leaves the state
// as it was.
//
// Everything else is an outcome, not an error. What the sequence discipline
// makes of an event — that it is next in line, that it repeats, that it skips
// ahead, that there is no baseline to apply it to — and what the window makes
// of an observation are ordinary things that happen to a stream, and the caller
// reads them from [ApplyResult.Outcome].
func (s State) Apply(ev Event) (State, ApplyResult, error) {
	if !s.configured() {
		return s, ApplyResult{}, invalidf(
			"Apply: the zero State is the state of no partition; obtain one from NewState")
	}
	if err := validateEvent(ev); err != nil {
		return s, ApplyResult{}, invalidf("Apply: %s", err)
	}
	if ev.Partition() != s.partition {
		return s, ApplyResult{}, invalidf("Apply: event is for partition %q, not %q",
			ev.Partition(), s.partition)
	}

	// A snapshot is the whole truth about the partition, so it outranks every
	// sequence number: a producer that restarted and renumbered its stream must
	// be able to be heard again.
	if e, ok := ev.(Resync); ok {
		next, res := s.applyResync(e)
		return next, res, nil
	}

	// Without a baseline, or with a gap since the last one, a topology delta is
	// refused: applying it would be exactly the partial recovery this system
	// does not attempt. An observation is not refused — evidence going stale is
	// visible where a missing edge is not, so the window keeps taking readings
	// while the topology waits, and takes them without sequence bookkeeping.
	if !s.synced {
		if e, ok := ev.(ObservationAppend); ok {
			next, res := s.applyObservation(e)
			return next, res, nil
		}
		return s, ApplyResult{Outcome: OutcomeAwaitingResync}, nil
	}

	// The wraparound case is why the next-in-line test comes first: at last ==
	// 2^64-1 the number 0 is both last+1 and not greater than last, and it is
	// the next one.
	seq := ev.Sequence()
	switch {
	case seq == s.last+1:
		next, res := s.applyDelta(ev)
		// Delivery and acceptance are independent: the stream has been
		// followed this far whatever the payload amounted to, so the number
		// advances for a removal that removed nothing and for an observation
		// the window refused alike.
		next.last = seq
		return next, res, nil
	case seq <= s.last:
		return s, ApplyResult{Outcome: OutcomeStale}, nil
	default:
		desynced := s
		desynced.synced = false
		return desynced, ApplyResult{Outcome: OutcomeGap}, nil
	}
}

// applyDelta applies the one delta the sequence discipline has admitted.
func (s State) applyDelta(ev Event) (State, ApplyResult) {
	switch e := ev.(type) {
	case AssetUpsert:
		return s.applyAssetUpsert(e)
	case AssetRemove:
		return s.applyAssetRemove(e)
	case EdgeUpsert:
		return s.applyEdgeUpsert(e)
	case EdgeRemove:
		return s.applyEdgeRemove(e)
	case ObservationAppend:
		return s.applyObservation(e)
	default:
		// Unreachable: the event set is closed, validateEvent enumerated it,
		// and Resync was taken before this point.
		return s, ApplyResult{Outcome: OutcomeApplied}
	}
}

// applyResync replaces the topology with the snapshot's and re-establishes the
// baseline. The window is left as it was: what a partition holds and what was
// observed there are two different claims, and replacing one does not withdraw
// the other.
func (s State) applyResync(e Resync) (State, ApplyResult) {
	assets := make(map[string]model.AssetRef, len(e.assets))
	for _, a := range e.assets {
		assets[a.Key()] = a
	}
	edges := make(map[edgeKey]Edge, len(e.edges))
	for _, edge := range e.edges {
		edges[keyOf(edge)] = edge
	}

	// The transitions are the difference between the two topologies, reported
	// as a removal pass before an addition pass so that a consumer replaying
	// them never holds an edge whose endpoint has gone. What stayed, under the
	// same key or the same identity, is replaced without a transition:
	// transitions are about existence.
	var transitions []Transition
	for _, edge := range sortedEdges(s.edges) {
		if _, kept := edges[keyOf(edge)]; !kept {
			transitions = append(transitions, Transition{
				Kind: TransitionEdgeRemoved,
				Edge: edge.clone(),
			})
		}
	}
	for _, a := range sortedAssets(s.assets) {
		if _, kept := assets[a.Key()]; !kept {
			transitions = append(transitions, Transition{
				Kind:  TransitionAssetRemoved,
				Asset: cloneAssetRef(a),
			})
		}
	}
	for _, a := range e.assets {
		if _, had := s.assets[a.Key()]; !had {
			transitions = append(transitions, Transition{
				Kind:  TransitionAssetAdded,
				Asset: cloneAssetRef(a),
			})
		}
	}
	for _, edge := range e.edges {
		if _, had := s.edges[keyOf(edge)]; !had {
			transitions = append(transitions, Transition{
				Kind: TransitionEdgeAdded,
				Edge: edge.clone(),
			})
		}
	}

	next := s
	next.assets = assets
	next.edges = edges
	next.last = e.sequence
	next.baseline = true
	next.synced = true
	return next, ApplyResult{Outcome: OutcomeResynced, Transitions: transitions}
}

// applyAssetUpsert stores the asset. A key not stored before is an addition; a
// key already stored has its value replaced, which is not a transition.
func (s State) applyAssetUpsert(e AssetUpsert) (State, ApplyResult) {
	key := e.asset.Key()
	_, existed := s.assets[key]

	assets := copyAssets(s.assets)
	assets[key] = e.asset
	next := s
	next.assets = assets

	res := ApplyResult{Outcome: OutcomeApplied}
	if !existed {
		res.Transitions = []Transition{{
			Kind:  TransitionAssetAdded,
			Asset: cloneAssetRef(e.asset),
		}}
	}
	return next, res
}

// applyAssetRemove removes the asset and cascades to every edge naming it at
// either end.
//
// The cascade goes by key and does not ask whether the asset itself is stored:
// an edge may name an asset this partition never listed — a remote endpoint —
// and removing that asset is the only way such an edge is ever cleaned up.
func (s State) applyAssetRemove(e AssetRemove) (State, ApplyResult) {
	key := e.asset.Key()
	var removed []Edge
	for k, edge := range s.edges {
		if k.from == key || k.to == key {
			removed = append(removed, edge)
		}
	}
	stored, existed := s.assets[key]
	if len(removed) == 0 && !existed {
		return s, ApplyResult{Outcome: OutcomeApplied}
	}

	next := s
	if len(removed) > 0 {
		edges := copyEdges(s.edges)
		for _, edge := range removed {
			delete(edges, keyOf(edge))
		}
		next.edges = edges
	}
	if existed {
		assets := copyAssets(s.assets)
		delete(assets, key)
		next.assets = assets
	}

	slices.SortFunc(removed, compareEdges)
	transitions := make([]Transition, 0, len(removed)+1)
	for _, edge := range removed {
		transitions = append(transitions, Transition{
			Kind: TransitionEdgeRemoved,
			Edge: edge.clone(),
		})
	}
	if existed {
		transitions = append(transitions, Transition{
			Kind:  TransitionAssetRemoved,
			Asset: cloneAssetRef(stored),
		})
	}
	return next, ApplyResult{Outcome: OutcomeApplied, Transitions: transitions}
}

// applyEdgeUpsert stores the edge. An identity not stored before is an
// addition; one already stored has its value — endpoints and all — replaced,
// which is not a transition.
func (s State) applyEdgeUpsert(e EdgeUpsert) (State, ApplyResult) {
	key := keyOf(e.edge)
	_, existed := s.edges[key]

	edges := copyEdges(s.edges)
	edges[key] = e.edge
	next := s
	next.edges = edges

	res := ApplyResult{Outcome: OutcomeApplied}
	if !existed {
		res.Transitions = []Transition{{
			Kind: TransitionEdgeAdded,
			Edge: e.edge.clone(),
		}}
	}
	return next, res
}

// applyEdgeRemove removes the edge of the event's identity, if one is stored.
// Removing what was never there changes nothing and is not a failure.
func (s State) applyEdgeRemove(e EdgeRemove) (State, ApplyResult) {
	key := keyOf(e.edge)
	stored, existed := s.edges[key]
	if !existed {
		return s, ApplyResult{Outcome: OutcomeApplied}
	}

	edges := copyEdges(s.edges)
	delete(edges, key)
	next := s
	next.edges = edges

	return next, ApplyResult{
		Outcome: OutcomeApplied,
		Transitions: []Transition{{
			Kind: TransitionEdgeRemoved,
			Edge: stored.clone(),
		}},
	}
}

// applyObservation offers the observation to the window and reports what the
// window made of it. A refusal — a conflicting reading at an instant already
// spoken for, one falling outside the bounds, one that is not a finite number —
// leaves both window and topology as they were and is an outcome, not an error:
// a reducer must not stop over a reading.
//
// An observation never produces a transition, whether it was retained or not.
func (s State) applyObservation(e ObservationAppend) (State, ApplyResult) {
	window, err := s.window.Add(e.observation)
	if err != nil {
		return s, ApplyResult{Outcome: OutcomeObservationRejected}
	}
	next := s
	next.window = window
	return next, ApplyResult{Outcome: OutcomeApplied}
}

// Snapshot returns the immutable view of the state as it is now. It is never
// nil: the zero State returns an empty snapshot.
func (s State) Snapshot() *Snapshot {
	return &Snapshot{
		partition: s.partition,
		assets:    s.assets,
		edges:     s.edges,
		window:    s.window,
		last:      s.last,
		baseline:  s.baseline,
		synced:    s.synced,
	}
}

// copyAssets returns a map like m that a state may store. Every application
// that changes the stored assets builds one, so the map a published state or
// snapshot holds is never written to.
func copyAssets(m map[string]model.AssetRef) map[string]model.AssetRef {
	c := make(map[string]model.AssetRef, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// copyEdges returns a map like m that a state may store, for the same reason
// copyAssets does.
func copyEdges(m map[edgeKey]Edge) map[edgeKey]Edge {
	c := make(map[edgeKey]Edge, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
