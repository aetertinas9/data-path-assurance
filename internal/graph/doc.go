// Package graph holds one partition's physical graph, applies the events that
// change it, and publishes the snapshots everything downstream reads.
//
// Four things live here. A [State] is one partition's topology — its assets and
// the edges between them — together with the evidence window of what has been
// observed there. [State.Apply] is the one way a state changes: it takes one of
// the six events this package defines, judges it by the sequence discipline,
// and returns a new state beside the list of [Transition]s that became true.
// A [Snapshot] is the immutable view readers get, in which topology and window
// are always the same point of application. A [Reducer] is the single writer
// that serializes all of it for one partition.
//
// The sequence discipline is the heart of it. A partition's producer numbers
// the events it sends, and a state that has seen a [Resync] and no gap since is
// synced. Only a synced state takes deltas, and only the next number in line:
// an event that repeats or goes backwards is discarded as stale, and one that
// skips ahead is a gap, which desyncs the state and is not itself applied.
// There is no partial recovery — the next Resync is what heals it, and the
// reducer asks for one, once per episode. Observations are exempt from the
// wait: they keep reaching the window while the topology is desynced, because
// an observation that never arrives shows up as evidence going stale, which is
// something a rule can see, whereas an edge that never arrives is invisible.
//
// Nothing here infers. An edge from A to B does not create the edge from B to
// A, an edge does not create its endpoints, and a claim about intent is never
// reconciled against a claim about what was observed — the two coexist as two
// edges, and reading a disagreement out of them is a rule's business.
//
// Purity: nothing outside [Reducer] touches the clock, and a reducer reaches it
// only through the injected [Clock]. Nothing here touches I/O, randomness or
// the environment. Every operation returning a collection states its order, so
// map iteration order never reaches a caller and the same events always give
// the same states, snapshots and transitions. An operation that fails returns
// the zero value beside the error, except [State.Apply], which returns the
// state it was given, so that s, res, err = s.Apply(ev) never costs a caller
// the state it had. No exported function or method here panics, whatever it is
// given, including the zero value of every type and a nil *Snapshot; the one
// exception is a Reducer that did not come from [NewReducer], which is not a
// reducer.
//
// A [State] is not safe for concurrent use. The concurrency this package does
// guarantee is one thing: [Reducer.Offer], [Reducer.Snapshot] and
// [Reducer.Stats] may be called from any goroutine while [Reducer.Run] is
// running. A published *Snapshot is immutable, so any number of goroutines may
// share one.
package graph
