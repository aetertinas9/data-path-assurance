package liveingest

import (
	"errors"
	"math"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/app/framecore"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// The class words of a FrameResult that CheckFrame, CheckRelative and the
// engine itself name (the invalid-frame classes come from Class).
const (
	classAccepted     = "accepted"
	classDuplicate    = "duplicate"
	classOutOfOrder   = "out_of_order"
	classGap          = "gap"
	classWrongSession = "wrong_session"
	classConflict     = "conflict"
	classIdentityNode = "identity_node"
	classIdentityBoot = "identity_boot"
	classAdmission    = "admission"
	classTopology     = "topology"
	classSequenceEnd  = "sequence_exhausted"
)

// commitFreshness is the window freshness of the topology snapshot the commit
// builds. The topology of a frame does not depend on it: the snapshot holds
// the frame's assets and edges and an empty window that no rule reads.
const commitFreshness = time.Minute

// FrameResult is the outcome of one snapshot frame that was processed up to
// an acknowledgement. Ack says which acknowledgement to send (AckNone: none),
// ExpectedNext is its expected_next_sequence, End is how the stream ends after
// the acknowledgement (EndNone: it stays open) and Class names the outcome in
// the fixed vocabulary for logs. Size is the length of the canonical encoding
// of an accepted frame.
type FrameResult struct {
	Ack          AckCode
	ExpectedNext uint64
	End          End
	Class        string
	Size         int
}

// Begin is step F1 of GLI-040 for a snapshot frame that was just received: a
// superseded or leaderless stream, leadership (a false answer drops the
// observation state), the certificate lifetime against the injected Clock and
// one token of the node's frame bucket. It runs before anything of the frame
// is looked at, and the token is not given back. The error is a *Reject; no
// acknowledgement goes with it.
func (h *Handle) Begin() error {
	select {
	case <-h.done:
		return h.cause
	default:
	}
	e := h.e
	if !e.Leading() {
		return reject(EndUnavailable, ClassNotLeader)
	}
	now := e.cfg.Clock.Now()
	if !h.notAfter.IsZero() && !now.Before(h.notAfter) {
		return reject(EndUnauthenticated, ClassCertExpired)
	}
	if !h.n.takeFrame(now, e.cfg.FrameBurst, e.cfg.FrameInterval) {
		return reject(EndResourceExhausted, ClassFrameRate)
	}
	return nil
}

// nextAfter is the sequence that follows seq; the sequence space is not
// circular, so the one after math.MaxUint64 is reported as 0.
func nextAfter(seq uint64) uint64 {
	if seq == math.MaxUint64 {
		return 0
	}
	return seq + 1
}

// expectedNext is the expected_next_sequence of a rejection: the sequence
// after the last accepted one, or 0 without one.
func expectedNext(v *view) uint64 {
	if v == nil {
		return 0
	}
	return nextAfter(v.cursor.Sequence)
}

// Submit processes a frame that passed Begin: steps F2 to F7 of GLI-040. A
// frame that is rejected leaves the node's state as it was. The error is a
// *Reject for the cases that end the stream without an acknowledgement (the
// stream was superseded or lost its leadership, or an internal failure).
func (h *Handle) Submit(f *Frame) (FrameResult, error) {
	e := h.e
	rejectFrame := func(ack AckCode, end End, class string) (FrameResult, error) {
		return FrameResult{Ack: ack, ExpectedNext: expectedNext(h.n.view.Load()), End: end, Class: class}, nil
	}

	// Retention is judged first, where the node is touched (GLI-053), so that
	// a node whose view expired has no cursor for the acknowledgement of a
	// frame that is rejected before admission (GLI-041: no cursor, 0).
	h.n.expire(e.cfg.Clock.Now(), e.cfg.NodeRetention)

	if f == nil {
		return rejectFrame(AckInvalid, EndInvalidArgument, "envelope")
	}

	// F2 envelope identity.
	switch {
	case f.NodeUID != h.nodeUID:
		return rejectFrame(AckInvalid, EndPermissionDenied, classIdentityNode)
	case f.BootID != h.bootID:
		return rejectFrame(AckInvalid, EndFailedPrecondition, classIdentityBoot)
	case f.Session != h.session:
		return rejectFrame(AckWrongSession, EndFailedPrecondition, classWrongSession)
	}

	// F3 envelope structure and F4 payload, digest and revision.
	c, err := CheckFrame(FrameContext{
		ClusterID: e.cfg.ClusterID,
		NodeName:  h.nodeName,
		NodeUID:   h.nodeUID,
		BootID:    h.bootID,
		Session:   h.session,
		ProfileID: e.cfg.CollectorProfileID,
		Sources:   e.cfg.TrustedSources,
	}, f)
	switch {
	case errors.Is(err, ErrInvalidFrame):
		return rejectFrame(AckInvalid, EndInvalidArgument, Class(err))
	case err != nil:
		return FrameResult{}, reject(EndInternal, ClassInternal)
	}

	// F5 to F7 are the node's single writer.
	n := h.n
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.active != h {
		return FrameResult{}, h.Cause()
	}
	cur := n.view.Load()
	now := e.cfg.Clock.Now()
	if cur != nil && cur.expired(now, e.cfg.NodeRetention) {
		n.view.Store(nil) // retention (GLI-053), judged where the node is written
		cur = nil
	}
	var previous *fleet.SnapshotCursor
	if cur != nil {
		p := cur.cursor
		previous = &p
	}

	// F5 admission.
	cursor, order, err := fleet.AdmitSnapshot(h.session, previous, c.Envelope)
	next := expectedNext(cur)
	switch {
	case errors.Is(err, fleet.ErrInvalidInput):
		return FrameResult{Ack: AckInvalid, ExpectedNext: next, End: EndInvalidArgument, Class: classAdmission}, nil
	case order == fleet.SnapshotConflict && errors.Is(err, fleet.ErrConflict):
		return FrameResult{Ack: AckConflict, ExpectedNext: next, End: EndFailedPrecondition, Class: classConflict}, nil
	case err != nil:
		return FrameResult{}, reject(EndInternal, ClassInternal)
	}
	switch order {
	case fleet.SnapshotAccepted:
	case fleet.SnapshotDuplicate:
		return FrameResult{Ack: AckDuplicate, ExpectedNext: next, Class: classDuplicate}, nil
	case fleet.SnapshotOutOfOrder:
		return FrameResult{Ack: AckOutOfOrder, ExpectedNext: next, End: EndFailedPrecondition, Class: classOutOfOrder}, nil
	case fleet.SnapshotGap:
		return FrameResult{Ack: AckGap, ExpectedNext: next, End: EndFailedPrecondition, Class: classGap}, nil
	case fleet.SnapshotWrongSession:
		return FrameResult{Ack: AckWrongSession, ExpectedNext: next, End: EndFailedPrecondition, Class: classWrongSession}, nil
	case fleet.SnapshotConflict:
		return FrameResult{Ack: AckConflict, ExpectedNext: next, End: EndFailedPrecondition, Class: classConflict}, nil
	default:
		return FrameResult{}, reject(EndInternal, ClassInternal)
	}

	// F6 frame-relative rules, for an accepted frame only.
	var previousAt time.Time
	if previous != nil {
		previousAt = previous.ObservedAt
	}
	if err := CheckRelative(c, previousAt, previous != nil); err != nil {
		if !errors.Is(err, ErrInvalidFrame) {
			return FrameResult{}, reject(EndInternal, ClassInternal)
		}
		return FrameResult{Ack: AckInvalid, ExpectedNext: next, End: EndInvalidArgument, Class: Class(err)}, nil
	}

	// F7 commit: one atomic publication of the new view. The topology of the
	// frame is built here, where a failure still rejects the frame as a whole.
	topology, err := framecore.Topology(c.Partition, framecore.WindowConfig(commitFreshness), c.Envelope.Sequence, c.Assets, c.Edges)
	if err != nil {
		return FrameResult{Ack: AckInvalid, ExpectedNext: next, End: EndInvalidArgument, Class: classTopology}, nil
	}
	rec := &frameRec{
		c:              c,
		topology:       topology,
		topologyDigest: framecore.TopologyDigest(c.Partition, c.Assets, c.Edges, c.Provenance),
	}
	n.view.Store(newView(cur, rec, cursor, now))

	res := FrameResult{Ack: AckAccepted, ExpectedNext: nextAfter(cursor.Sequence), Class: classAccepted, Size: c.CanonicalLen}
	if cursor.Sequence == math.MaxUint64 {
		// The sequence space of the session is used up: the agent needs a new
		// session (wire-unreachable [defensive]).
		res.ExpectedNext = 0
		res.End = EndFailedPrecondition
		res.Class = classSequenceEnd
	}
	return res, nil
}
