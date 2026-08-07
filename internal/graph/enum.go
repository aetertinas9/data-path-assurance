package graph

import "strconv"

// The enumerations here follow the model's convention: a signed integer type
// whose constants start at one, so that the zero value names nothing and a
// value that was never set cannot pass for a value that was.

// enumInRange reports whether n names one of count enumerated constants.
func enumInRange(n, count int) bool {
	i := n - 1
	return i >= 0 && i < count
}

// enumName renders n as names[n-1] when n is enumerated, and as the diagnostic
// form "TypeName(n)" otherwise.
//
// Only the enumerated renderings are part of the contract. The diagnostic form
// exists so that String never panics on a value that escaped validation, and so
// that such a value can never be mistaken for an enumerated one.
func enumName(typeName string, n int, names []string) string {
	if i := n - 1; i >= 0 && i < len(names) {
		return names[i]
	}
	return typeName + "(" + strconv.Itoa(n) + ")"
}

// ApplyOutcome says what [State.Apply] made of an event. The zero value is not
// an outcome.
//
// None of them is an error. A stale event, a gap and an observation the window
// refuses are all ordinary things that happen to a stream, and a caller that
// wants to count them reads them here.
type ApplyOutcome int

// The outcomes of applying an event.
const (
	// OutcomeApplied reports that the event changed the state, or would have
	// had it said anything new.
	OutcomeApplied ApplyOutcome = iota + 1
	// OutcomeResynced reports that a snapshot replaced the topology and
	// re-established the baseline.
	OutcomeResynced
	// OutcomeStale reports that a delta repeated or went backwards and was
	// discarded.
	OutcomeStale
	// OutcomeGap reports that a delta skipped ahead: it was not applied, and
	// the state is no longer synced.
	OutcomeGap
	// OutcomeAwaitingResync reports that a delta arrived while the state had no
	// baseline to apply it to.
	OutcomeAwaitingResync
	// OutcomeObservationRejected reports that the window would not retain the
	// observation.
	OutcomeObservationRejected
)

var applyOutcomeNames = []string{
	"Applied",
	"Resynced",
	"Stale",
	"Gap",
	"AwaitingResync",
	"ObservationRejected",
}

// String returns the outcome's name, e.g. "AwaitingResync".
func (o ApplyOutcome) String() string {
	return enumName("ApplyOutcome", int(o), applyOutcomeNames)
}

// IsValid reports whether o is one of the enumerated outcomes. The zero value
// is not.
func (o ApplyOutcome) IsValid() bool {
	return enumInRange(int(o), len(applyOutcomeNames))
}

// TransitionKind says what kind of change a [Transition] records. The zero
// value is not a kind.
//
// There are four, and they are all about existence: a thing came into the graph
// or left it. Replacing what is stored for a thing that stays is not a
// transition, and neither is an observation — a consumer driven by transitions
// would then be driven by the observation rate, which is the churn this
// division exists to avoid.
type TransitionKind int

// The kinds of transition.
const (
	TransitionAssetAdded TransitionKind = iota + 1
	TransitionAssetRemoved
	TransitionEdgeAdded
	TransitionEdgeRemoved
)

var transitionKindNames = []string{
	"AssetAdded",
	"AssetRemoved",
	"EdgeAdded",
	"EdgeRemoved",
}

// String returns the kind's name, e.g. "EdgeRemoved".
func (k TransitionKind) String() string {
	return enumName("TransitionKind", int(k), transitionKindNames)
}

// IsValid reports whether k is one of the enumerated kinds. The zero value is
// not.
func (k TransitionKind) IsValid() bool {
	return enumInRange(int(k), len(transitionKindNames))
}
