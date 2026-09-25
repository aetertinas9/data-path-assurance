package app

import (
	"context"
	"errors"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// TargetKind selects what an explanation is about.
type TargetKind int

// The explanation targets.
const (
	TargetGPU TargetKind = iota + 1
	TargetNode
)

// Request names the explanation target: a GPU device by its fleet device name,
// or the replayed node by its node name.
type Request struct {
	Kind TargetKind
	Name string
}

// NodeIdentity is the name and UID of the node a replay was collected on.
type NodeIdentity struct {
	Name, UID string
}

// Diagnostic is one collector diagnostic carried beside a snapshot frame. It is
// not part of the snapshot digest.
type Diagnostic struct {
	Code, Subject string
}

// Frame is one snapshot frame of a replay, already converted to ratified
// values. Assets, edges, provenance, observations and bindings are in the
// order the frame listed them.
type Frame struct {
	Envelope             fleet.SnapshotEnvelope
	Assets               []model.AssetRef
	Edges                []graph.Edge
	Provenance           []fleet.EdgeProvenance
	Observations         []model.Observation
	Bindings             []fleet.ObservedBinding
	Diagnostics          []Diagnostic
	DiagnosticsTotal     uint64
	DiagnosticsTruncated bool
}

// Replay is everything one offline explanation is computed from: the frames of
// one collector session on one node, the collector trust profile, and the
// fleet policy with the device intents of that node.
type Replay struct {
	ClusterID      string
	Node           NodeIdentity
	CollectorTrust fleet.CollectorTrustProfile
	Frames         []Frame
	FleetUID       string
	Policy         fleet.Policy
	// Devices holds one intent per device, in the order the fleet input
	// listed them.
	Devices []fleet.Intent
}

// ReplaySource supplies a validated replay. Errors it returns are passed
// through ExplainFleet unchanged (wrapped), so the caller can classify them.
type ReplaySource interface {
	LoadReplay(ctx context.Context) (Replay, error)
}

// ErrTargetNotFound reports that the requested device or node is not part of
// the replay.
var ErrTargetNotFound = errors.New("explain target not found")

// ErrEvaluation reports an unexpected failure while evaluating or assembling
// an explanation from a replay that was accepted as valid.
var ErrEvaluation = errors.New("offline evaluation failed")

// PublicError is an error whose message is safe to show to a user: it names
// only fixed phrases and never echoes input values or paths.
type PublicError struct {
	class  error
	detail string
}

// Error renders the class and the detail.
func (e *PublicError) Error() string { return e.class.Error() + ": " + e.detail }

// Unwrap reports the error class.
func (e *PublicError) Unwrap() error { return e.class }

// PublicMessage returns the detail, which is safe to display.
func (e *PublicError) PublicMessage() string { return e.detail }

func evaluationError(detail string) error {
	return &PublicError{class: ErrEvaluation, detail: detail}
}

func notFound(detail string) error {
	return &PublicError{class: ErrTargetNotFound, detail: detail}
}
