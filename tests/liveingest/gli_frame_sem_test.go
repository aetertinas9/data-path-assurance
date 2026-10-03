package liveingest_test

// Frame processing semantics (GLI-040..045, GLI-124 (c)): ack classification
// against fleet.AdmitSnapshot, duplicate/conflict/gap/out-of-order handling,
// stage order F1..F7, atomicity, stream outcome after a rejection, and the
// acceptance scenarios L-DUP, L-GAP, L-CONFLICT. Oracle values come from direct
// fleet.AdmitSnapshot calls on the independent digest (GLI-041).

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/ingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

// gliFxAt is the observed_at of frame sequence seq in the 15 s agent cycle.
func gliFxAt(seq uint64) time.Time { return gliFxT0.Add(time.Duration(seq) * 15 * time.Second) }

// gliFxOrCtx is the live M context of the default node and profile.
func gliFxOrCtx() gliOrCtx {
	edge, bind := gliOrLiveStamps(gliFxProfile, ingest.DefaultTrustedSources())
	return gliOrCtx{Cluster: gliFxCluster, NodeName: gliFxNodeName, StampEdge: edge, StampBinding: bind}
}

// gliFxChanged is the BASE frame of seq with a different valid payload (GPU
// current width 8), so its digest differs from gliPayload's frame of seq.
func gliFxChanged(c *gliFxConn, seq uint64, at time.Time, complete bool) *ingestpb.SnapshotFrame {
	c.t.Helper()
	p := gliPayload(c.UID, c.Boot, c.Session, seq, at)
	gliFxFindObs(p, gliFxFnKey(gliFxGPUBDF), gliFxSigCurrent).Value = gliFxInt(8)
	return gliFrame(c.t, p, c.Session, seq, at, complete)
}

// gliFxUnknown is a well-formed unknown field (number 9999, varint 1).
func gliFxUnknown() []byte {
	b := protowire.AppendTag(nil, 9999, protowire.VarintType)
	return protowire.AppendVarint(b, 1)
}

// GLI-041, GLI-045: every accepted frame is acked with the stream session, the
// next expected sequence, and a fixed-vocabulary message; the ack follows the
// commit, so the NodeBundles call that follows the ack already reflects the
// frame (the cursor keeps Baseline, the envelope is the canonical one).
func TestGLI041_AckFieldsAcrossAcceptedSequence(t *testing.T) {
	env := gliFxStart(t)
	c := env.ConnectDefault()
	if c.Session <= 0 {
		t.Fatalf("GLI-031: ServerHello session %d must be positive", c.Session)
	}
	if c.Hello.GetCollectorProfileId() != gliFxProfile {
		t.Fatalf("GLI-030: ServerHello collector_profile_id %q, want the configured %q", c.Hello.GetCollectorProfileId(), gliFxProfile)
	}
	for seq := uint64(0); seq < 8; seq++ {
		f := c.Frame(seq, gliFxAt(seq), true)
		wantCode, wantNext := c.oracle.Admit(f)
		if wantCode != ingestpb.AckCode_ACCEPTED || wantNext != seq+1 {
			t.Fatalf("test bug: oracle classified sequence %d as %s next %d", seq, wantCode, wantNext)
		}
		a := c.Ack(f)
		clause := fmt.Sprintf("GLI-041 sequence %d", seq)
		gliFxWantAck(t, a, ingestpb.AckCode_ACCEPTED, c.Session, seq+1, clause)
		if a.GetMessage() == "" {
			t.Errorf("%s: ack message is empty, want a fixed vocabulary word", clause)
		}
		b := env.One(gliFxAt(seq).Add(time.Second), gliFxFresh)
		fr, err := gliOrApplyM(f, gliFxOrCtx())
		if err != nil {
			t.Fatalf("oracle M: %v", err)
		}
		if got, want := gliOrEnvString(b.Bundle.Snapshot), gliOrEnvString(fr.Env); got != want {
			t.Errorf("GLI-045/GLI-075: after the ack of sequence %d NodeBundles Snapshot\n got %s\nwant %s", seq, got, want)
		}
		if got, want := gliOrCursorString(b.Bundle.Admitted), gliOrCursorString(*c.oracle.prev); got != want {
			t.Errorf("GLI-075: Admitted cursor after sequence %d\n got %s\nwant %s", seq, got, want)
		}
		if !b.Bundle.Admitted.Baseline {
			t.Errorf("GLI-075: Admitted.Baseline is false after the baseline was accepted")
		}
	}
}

// GLI-043, GLI-040 F5: the first frame of a session is accepted only as complete
// sequence 0. Partial sequence 0, a later first sequence and a wrap-around
// first sequence are WRONG_SESSION (S1 value) followed by the stream end
// FailedPrecondition; nothing is stored, and recovery needs a new hello.
func TestGLI043_FirstFrameMustBeCompleteSequenceZero(t *testing.T) {
	base := gliFxStart(t)
	cases := []struct {
		name     string
		seq      uint64
		complete bool
	}{
		{"partial sequence 0", 0, false},
		{"complete sequence 1", 1, true},
		{"partial sequence 1", 1, false},
		{"complete sequence 2", 2, true},
		{"complete sequence MaxUint64", math.MaxUint64, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			env.WantNoObservation("Awaiting before the first frame (GLI-075 (b))")
			f := c.Frame(tc.seq, gliFxT0, tc.complete)
			if got, _ := (&gliFxOracle{session: c.Session}).Admit(f); got != ingestpb.AckCode_WRONG_SESSION {
				t.Fatalf("test bug: S1 classifies this first frame as %s, want WRONG_SESSION", got)
			}
			c.Reject(f, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition, "GLI-043/GLI-044")
			env.WantNoObservation("after the rejected first frame")
			c2 := env.ConnectDefault()
			if c2.Session <= c.Session {
				t.Fatalf("GLI-031: the new hello got session %d, want > %d", c2.Session, c.Session)
			}
			c2.AcceptSeq(0, gliFxT0, true)
		})
	}
	t.Run("control: complete sequence 0 is the baseline", func(t *testing.T) {
		env := base.For(t)
		c := env.ConnectDefault()
		c.AcceptSeq(0, gliFxT0, true)
		b := env.One(gliFxT0.Add(time.Second), gliFxFresh)
		if b.Bundle.Snapshot.Sequence != 0 || !b.Bundle.Admitted.Baseline {
			t.Errorf("baseline view: sequence %d baseline %t", b.Bundle.Snapshot.Sequence, b.Bundle.Admitted.Baseline)
		}
	})
}

// GLI-043: after the baseline a PARTIAL frame with the right sequence and digest
// is ACCEPTED and becomes the latest envelope; a complete frame follows.
func TestGLI043_PartialAfterBaselineIsAccepted(t *testing.T) {
	env := gliFxStart(t)
	c := env.ConnectDefault()
	c.AcceptSeq(0, gliFxAt(0), true)
	c.AcceptSeq(1, gliFxAt(1), false)
	b := env.One(gliFxAt(1).Add(time.Second), gliFxFresh)
	if b.Bundle.Snapshot.Completeness != fleet.CompletenessPartial || b.Bundle.Snapshot.Sequence != 1 {
		t.Fatalf("GLI-043: latest envelope %s sequence %d, want Partial sequence 1", b.Bundle.Snapshot.Completeness, b.Bundle.Snapshot.Sequence)
	}
	if !b.Bundle.Admitted.Baseline || b.Bundle.Admitted.Completeness != fleet.CompletenessPartial {
		t.Errorf("GLI-043/GLI-075: Admitted = %s", gliOrCursorString(b.Bundle.Admitted))
	}
	c.AcceptSeq(2, gliFxAt(2), true)
	b = env.One(gliFxAt(2).Add(time.Second), gliFxFresh)
	if b.Bundle.Snapshot.Completeness != fleet.CompletenessComplete || b.Bundle.Snapshot.Sequence != 2 {
		t.Errorf("latest envelope %s sequence %d, want Complete sequence 2", b.Bundle.Snapshot.Completeness, b.Bundle.Snapshot.Sequence)
	}
}

// GLI-042, L-DUP: the same session, sequence and digest is DUPLICATE whatever
// its envelope observed_at or completeness (including a V2-violating time);
// cursor, envelope and bundles do not change and the stream continues.
func TestGLI042_DuplicateKeepsStateAndStream(t *testing.T) {
	env := gliFxStart(t)
	c := env.ConnectDefault()
	c.AcceptSeq(0, gliFxAt(0), true)
	f1 := c.AcceptSeq(1, gliFxAt(1), true)
	before := env.Sig()
	variants := []struct {
		name string
		mut  func(f *ingestpb.SnapshotFrame)
	}{
		{"identical frame", func(f *ingestpb.SnapshotFrame) {}},
		{"observed_at earlier than the accepted frame", func(f *ingestpb.SnapshotFrame) { f.ObservedAt = gliFxTS(gliFxAt(1).Add(-time.Hour)) }},
		{"observed_at equal to the previous accepted frame", func(f *ingestpb.SnapshotFrame) { f.ObservedAt = gliFxTS(gliFxAt(0)) }},
		{"observed_at one nanosecond earlier", func(f *ingestpb.SnapshotFrame) { f.ObservedAt = gliFxTS(gliFxAt(1).Add(-time.Nanosecond)) }},
		{"observed_at later", func(f *ingestpb.SnapshotFrame) { f.ObservedAt = gliFxTS(gliFxAt(1).Add(time.Hour)) }},
		{"completeness PARTIAL", func(f *ingestpb.SnapshotFrame) { f.Completeness = ingestpb.Completeness_PARTIAL }},
		{"earlier observed_at and PARTIAL", func(f *ingestpb.SnapshotFrame) {
			f.ObservedAt = gliFxTS(gliFxAt(0))
			f.Completeness = ingestpb.Completeness_PARTIAL
		}},
		{"observed_at outside the V2 margin", func(f *ingestpb.SnapshotFrame) {
			f.ObservedAt = gliFxTS(time.Date(1, 1, 1, 1, 0, 0, 0, time.UTC))
		}},
	}
	for _, v := range variants {
		d := gliFxCloneFrame(f1)
		v.mut(d)
		clause := "GLI-042 " + v.name
		if got := c.Classified(d, clause); got != ingestpb.AckCode_DUPLICATE {
			t.Fatalf("%s: oracle classified the resend as %s, want DUPLICATE", clause, got)
		}
		env.WantSigUnchanged(before, clause)
	}
	// The duplicates neither advanced the cursor nor ended the stream.
	c.AcceptSeq(2, gliFxAt(2), true)
}

// GLI-041, L-CONFLICT: the same sequence with a different valid payload is
// CONFLICT, not INVALID; expected_next_sequence is the cursor + 1; the stream
// ends FailedPrecondition; the last accepted view stays; a new session with
// complete sequence 0 is required afterwards.
func TestGLI041_ConflictIsNotInvalid(t *testing.T) {
	base := gliFxStart(t)
	rows := []struct {
		name     string
		accepted []bool // completeness of the accepted frames, sequence 0..n-1
		seq      uint64
		at       time.Time
	}{
		{"different payload at the last accepted sequence", []bool{true, true}, 1, gliFxAt(1)},
		{"conflict at the baseline", []bool{true}, 0, gliFxAt(0)},
		{"conflict frame whose observed_at is not later (F5 before F6)", []bool{true, true}, 1, gliFxAt(0)},
		{"conflict at a partial last frame", []bool{true, false}, 1, gliFxAt(1)},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			for i, complete := range r.accepted {
				c.AcceptSeq(uint64(i), gliFxAt(uint64(i)), complete)
			}
			before := env.Sig()
			f := gliFxChanged(c, r.seq, r.at, true)
			if got := c.Classified(f, "GLI-041 conflict"); got != ingestpb.AckCode_CONFLICT {
				t.Fatalf("oracle classified the frame as %s, want CONFLICT (S1: same sequence, other digest)", got)
			}
			env.WantSigUnchanged(before, "GLI-041/GLI-045 after CONFLICT")
			env.WantNoObservationAfterHello(c, t)
		})
	}
	t.Run("same digest with another completeness is a duplicate, not a conflict", func(t *testing.T) {
		env := base.For(t)
		c := env.ConnectDefault()
		f := c.AcceptSeq(0, gliFxAt(0), true)
		d := gliFxCloneFrame(f)
		d.Completeness = ingestpb.Completeness_PARTIAL
		if got := c.Classified(d, "GLI-042"); got != ingestpb.AckCode_DUPLICATE {
			t.Fatalf("got %s, want DUPLICATE", got)
		}
	})
}

// WantNoObservationAfterHello reconnects the default node after a rejected
// stream: the new hello discards the old view (GLI-035), the next session is
// larger, and a new complete sequence 0 is the only way back (GLI-043).
func (e *gliFxEnv) WantNoObservationAfterHello(old *gliFxConn, t *testing.T) {
	t.Helper()
	c2 := e.ConnectDefault()
	if c2.Session <= old.Session {
		t.Fatalf("GLI-031: new session %d, want > %d", c2.Session, old.Session)
	}
	e.WantNoObservation("GLI-035 after the new hello")
	f := c2.AcceptSeq(0, gliFxAt(0), true)
	b := e.One(gliFxAt(0).Add(time.Second), gliFxFresh)
	if want := fmt.Sprintf("%d:0:", c2.Session); !strings.HasPrefix(b.Bundle.Snapshot.BundleRevision, want) || b.Bundle.Snapshot.Session != c2.Session {
		t.Errorf("GLI-035: baseline of the new session has revision %q session %d, want prefix %q", b.Bundle.Snapshot.BundleRevision, b.Bundle.Snapshot.Session, want)
	}
	if b.Bundle.Snapshot.BundleRevision != f.GetBundleRevision() {
		t.Errorf("GLI-061: revision %q, want the frame's canonical %q", b.Bundle.Snapshot.BundleRevision, f.GetBundleRevision())
	}
}

// GLI-043, GLI-044, L-GAP: a gap and an older sequence are GAP / OUT_OF_ORDER
// (S1 classification), the stream ends FailedPrecondition, the last accepted
// view stays visible after the stream ended, and only a new session with
// complete sequence 0 revives the node (a non-baseline first frame of the new
// session is WRONG_SESSION).
func TestGLI043_GapAndOutOfOrderThenRecovery(t *testing.T) {
	base := gliFxStart(t)
	rows := []struct {
		name     string
		accepted int
		seq      uint64
		changed  bool
		want     ingestpb.AckCode
	}{
		{"gap of one", 1, 2, false, ingestpb.AckCode_GAP},
		{"gap after two frames", 2, 4, false, ingestpb.AckCode_GAP},
		{"large gap", 1, 1000000, false, ingestpb.AckCode_GAP},
		{"maximum sequence", 1, math.MaxUint64, false, ingestpb.AckCode_GAP},
		{"resend of an older sequence", 2, 0, false, ingestpb.AckCode_OUT_OF_ORDER},
		{"older sequence with another payload is not a conflict", 3, 1, true, ingestpb.AckCode_OUT_OF_ORDER},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			for i := 0; i < r.accepted; i++ {
				c.AcceptSeq(uint64(i), gliFxAt(uint64(i)), true)
			}
			before := env.Sig()
			var f *ingestpb.SnapshotFrame
			if r.changed {
				f = gliFxChanged(c, r.seq, gliFxAt(min(r.seq, 50)), true)
			} else {
				f = c.Frame(r.seq, gliFxAt(min(r.seq, 50)), true)
			}
			if got := c.Classified(f, "GLI-043/GLI-044"); got != r.want {
				t.Fatalf("S1 classification %s, want %s", got, r.want)
			}
			// L-GAP: the last accepted view survives the end of the stream.
			env.WantSigUnchanged(before, "GLI-045 view after the stream ended")
			c2 := env.ConnectDefault()
			env.WantNoObservation("GLI-035 after the new hello")
			// Within the new session only complete sequence 0 starts a view.
			f1 := c2.Frame(1, gliFxAt(1), true)
			c2.Reject(f1, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition, "GLI-043 non-baseline first frame")
			c3 := env.ConnectDefault()
			if c3.Session <= c2.Session || c2.Session <= c.Session {
				t.Fatalf("sessions %d, %d, %d are not strictly increasing (GLI-031)", c.Session, c2.Session, c3.Session)
			}
			c3.AcceptSeq(0, gliFxAt(0), true)
			// Frames of an earlier session are never accepted in a later one.
			stale := c.Frame(1, gliFxAt(1), true)
			c3.Reject(stale, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition, "GLI-027 earlier session")
		})
	}
}

// GLI-040 F2, GLI-044: envelope identity. node_uid differs from the
// certificate: ack INVALID and stream PermissionDenied; boot_id differs from
// the hello: INVALID and FailedPrecondition; session differs from the stream
// session (any value, also non-positive): WRONG_SESSION and FailedPrecondition.
// F2 precedes every later stage, which the broken-digest/unknown-field rows
// observe through the distinct stream codes. Each row runs before and after
// the baseline; nothing is stored.
func TestGLI040_F2EnvelopeIdentity(t *testing.T) {
	base := gliFxStart(t)
	other := "00000000-0000-4000-8000-000000000000"
	rows := []struct {
		name   string
		mut    func(f *ingestpb.SnapshotFrame, c *gliFxConn)
		ack    ingestpb.AckCode
		stream codes.Code
	}{
		{"node_uid of another node", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.NodeUid = gliFxOtherUID }, ingestpb.AckCode_INVALID, codes.PermissionDenied},
		{"node_uid empty", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.NodeUid = "" }, ingestpb.AckCode_INVALID, codes.PermissionDenied},
		{"node_uid upper-cased", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.NodeUid = strings.ToUpper(f.NodeUid) }, ingestpb.AckCode_INVALID, codes.PermissionDenied},
		{"boot_id of another boot", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.BootId = other }, ingestpb.AckCode_INVALID, codes.FailedPrecondition},
		{"boot_id empty", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.BootId = "" }, ingestpb.AckCode_INVALID, codes.FailedPrecondition},
		{"boot_id upper-cased", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.BootId = strings.ToUpper(f.BootId) }, ingestpb.AckCode_INVALID, codes.FailedPrecondition},
		{"session + 1", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.Session = c.Session + 1; gliReseal(c.t, f) }, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition},
		{"session - 1", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.Session = c.Session - 1; gliReseal(c.t, f) }, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition},
		{"session 0", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.Session = 0; gliReseal(c.t, f) }, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition},
		{"session -1", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.Session = -1; gliReseal(c.t, f) }, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition},
		{"session MaxInt64", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.Session = math.MaxInt64; gliReseal(c.t, f) }, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition},
		{"node mismatch beats a broken digest (F2 before F3/F4)", func(f *ingestpb.SnapshotFrame, c *gliFxConn) {
			f.NodeUid = gliFxOtherUID
			f.PayloadDigest = make([]byte, 32)
		}, ingestpb.AckCode_INVALID, codes.PermissionDenied},
		{"boot mismatch beats an invalid completeness (F2 before F3)", func(f *ingestpb.SnapshotFrame, c *gliFxConn) {
			f.BootId = other
			f.Completeness = ingestpb.Completeness_COMPLETENESS_UNSPECIFIED
		}, ingestpb.AckCode_INVALID, codes.FailedPrecondition},
		{"session mismatch beats a missing payload (F2 before F4)", func(f *ingestpb.SnapshotFrame, c *gliFxConn) {
			f.Session = c.Session + 1
			f.Payload = nil
			f.PayloadDigest = nil
		}, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition},
		{"session mismatch beats an envelope unknown field (F2 before F3)", func(f *ingestpb.SnapshotFrame, c *gliFxConn) {
			f.Session = c.Session + 1
			gliReseal(c.t, f)
			f.ProtoReflect().SetUnknown(gliFxUnknown())
		}, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition},
	}
	for _, afterBaseline := range []bool{false, true} {
		for _, r := range rows {
			name := r.name
			if afterBaseline {
				name += " after the baseline"
			}
			t.Run(name, func(t *testing.T) {
				env := base.For(t)
				c := env.ConnectDefault()
				seq := uint64(0)
				if afterBaseline {
					c.AcceptSeq(0, gliFxAt(0), true)
					seq = 1
				}
				before := env.Sig()
				f := c.Frame(seq, gliFxAt(seq), true)
				r.mut(f, c)
				c.Reject(f, r.ack, r.stream, "GLI-040 F2/GLI-044")
				env.WantSigUnchanged(before, "GLI-045")
			})
		}
	}
}

// GLI-040 F1, GLI-011 (f): an empty oneof and a second hello end the stream
// with InvalidArgument and no ack; the committed view stays. A snapshot frame
// that is all zero is not accepted.
func TestGLI040_F1WrapperErrors(t *testing.T) {
	base := gliFxStart(t)
	rows := []struct {
		name string
		cf   func(c *gliFxConn) *ingestpb.ClientFrame
	}{
		{"empty ClientFrame", func(c *gliFxConn) *ingestpb.ClientFrame { return &ingestpb.ClientFrame{} }},
		{"second hello", func(c *gliFxConn) *ingestpb.ClientFrame {
			return &ingestpb.ClientFrame{Frame: &ingestpb.ClientFrame_Hello{Hello: &ingestpb.ClientHello{
				Version: "v1alpha1", ClusterId: gliFxCluster, NodeName: gliFxNodeName, NodeUid: gliFxNodeUID, BootId: gliFxBootID,
			}}}
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			c.AcceptSeq(0, gliFxAt(0), true)
			before := env.Sig()
			c.SendClient(r.cf(c))
			c.WantEnd(codes.InvalidArgument, "GLI-040 F1: no ack, stream InvalidArgument")
			env.WantSigUnchanged(before, "GLI-045")
		})
	}
	t.Run("all-zero SnapshotFrame is rejected", func(t *testing.T) {
		env := base.For(t)
		c := env.ConnectDefault()
		before := env.Sig()
		a := c.Ack(&ingestpb.SnapshotFrame{})
		if a.GetCode() != ingestpb.AckCode_INVALID && a.GetCode() != ingestpb.AckCode_WRONG_SESSION {
			t.Errorf("all-zero frame: ack %s, want INVALID or WRONG_SESSION (F2)", a.GetCode())
		}
		if got, _ := c.End(); got == codes.OK {
			t.Errorf("all-zero frame: the stream ended OK, want a non-OK status")
		}
		env.WantSigUnchanged(before, "GLI-045")
	})
}

// GLI-040 F3, GLI-061: envelope structure and the canonical digest/revision
// binding. Every row ends INVALID with InvalidArgument and stores nothing; the
// unmodified frame is the accepted control.
func TestGLI040_F3EnvelopeStructureAndDigestBinding(t *testing.T) {
	base := gliFxStart(t)
	rows := []struct {
		name string
		mut  func(f *ingestpb.SnapshotFrame)
	}{
		{"completeness UNSPECIFIED", func(f *ingestpb.SnapshotFrame) { f.Completeness = ingestpb.Completeness_COMPLETENESS_UNSPECIFIED }},
		{"completeness 3", func(f *ingestpb.SnapshotFrame) { f.Completeness = ingestpb.Completeness(3) }},
		{"completeness -1", func(f *ingestpb.SnapshotFrame) { f.Completeness = ingestpb.Completeness(-1) }},
		{"payload_digest absent", func(f *ingestpb.SnapshotFrame) { f.PayloadDigest = nil }},
		{"payload_digest 31 bytes", func(f *ingestpb.SnapshotFrame) { f.PayloadDigest = f.PayloadDigest[:31] }},
		{"payload_digest 33 bytes", func(f *ingestpb.SnapshotFrame) { f.PayloadDigest = append(append([]byte(nil), f.PayloadDigest...), 0) }},
		{"payload_digest as 64 hex characters", func(f *ingestpb.SnapshotFrame) { f.PayloadDigest = []byte(gliFxHex(f.PayloadDigest)) }},
		{"payload_digest with one flipped bit", func(f *ingestpb.SnapshotFrame) {
			d := append([]byte(nil), f.PayloadDigest...)
			d[0] ^= 0x01
			f.PayloadDigest = d
		}},
		{"payload changed without resealing", func(f *ingestpb.SnapshotFrame) { f.Payload.Observations[1].Unit = "lanez" }},
		{"payload absent", func(f *ingestpb.SnapshotFrame) { f.Payload = nil }},
		{"bundle_revision empty", func(f *ingestpb.SnapshotFrame) { f.BundleRevision = "" }},
		{"bundle_revision 129 bytes", func(f *ingestpb.SnapshotFrame) { f.BundleRevision = strings.Repeat("a", 129) }},
		{"bundle_revision with upper-case hex", func(f *ingestpb.SnapshotFrame) { f.BundleRevision = strings.ToUpper(f.BundleRevision) }},
		{"bundle_revision with another session", func(f *ingestpb.SnapshotFrame) {
			f.BundleRevision = fmt.Sprintf("%d:%d:%s", f.Session+1, f.Sequence, gliFxHex(f.PayloadDigest))
		}},
		{"bundle_revision with another sequence", func(f *ingestpb.SnapshotFrame) {
			f.BundleRevision = fmt.Sprintf("%d:%d:%s", f.Session, f.Sequence+1, gliFxHex(f.PayloadDigest))
		}},
		{"bundle_revision with a leading zero", func(f *ingestpb.SnapshotFrame) { f.BundleRevision = "0" + f.BundleRevision }},
		{"bundle_revision with a plus sign", func(f *ingestpb.SnapshotFrame) { f.BundleRevision = "+" + f.BundleRevision }},
		{"bundle_revision with a trailing space", func(f *ingestpb.SnapshotFrame) { f.BundleRevision += " " }},
		{"bundle_revision truncated by one", func(f *ingestpb.SnapshotFrame) { f.BundleRevision = f.BundleRevision[:len(f.BundleRevision)-1] }},
		{"bundle_revision of another digest", func(f *ingestpb.SnapshotFrame) {
			f.BundleRevision = fmt.Sprintf("%d:%d:%s", f.Session, f.Sequence, strings.Repeat("0", 64))
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			before := env.Sig()
			f := c.Frame(0, gliFxAt(0), true)
			r.mut(f)
			c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, "GLI-040 F3/F4, GLI-061")
			env.WantSigUnchanged(before, "GLI-045")
		})
	}
	t.Run("control: the untouched frame is accepted", func(t *testing.T) {
		env := base.For(t)
		c := env.ConnectDefault()
		c.AcceptSeq(0, gliFxAt(0), true)
	})
}

// GLI-040 stage order F4 -> F5 -> F6: a payload that fails V1 is INVALID even
// where the sequence is a gap or too small; frame-relative rules (V2) are only
// checked for frames S1 classifies as Accepted, so a gap, an old sequence, a
// conflict or a wrong first frame with V2-violating times keeps its S1 code.
func TestGLI040_StageOrderPayloadBeforeAdmissionBeforeFrameRules(t *testing.T) {
	base := gliFxStart(t)
	// v2bad breaks the V2 (j) time equality of the first edgeEvidence (and
	// reseals); V1 does not look at it.
	v2bad := func(f *ingestpb.SnapshotFrame, c *gliFxConn) {
		f.Payload.EdgeEvidence[0].ObservedAt = gliFxShift(f.Payload.EdgeEvidence[0].ObservedAt, time.Nanosecond)
		gliReseal(c.t, f)
	}
	invalidRows := []struct {
		name     string
		accepted int
		seq      uint64
		mut      func(f *ingestpb.SnapshotFrame, c *gliFxConn)
	}{
		{"invalid payload at a gap position (F4 before F5)", 1, 5, func(f *ingestpb.SnapshotFrame, c *gliFxConn) {
			f.Payload.Observations[1].Unit = "é"
			gliReseal(c.t, f)
		}},
		{"broken digest at an older sequence (F4 before F5)", 2, 0, func(f *ingestpb.SnapshotFrame, c *gliFxConn) {
			f.Payload.Observations[1].Unit = "lanez"
		}},
		{"unsorted payload at the next sequence (F4 before F5)", 1, 1, func(f *ingestpb.SnapshotFrame, c *gliFxConn) {
			a := f.Payload.Assets
			a[1], a[2] = a[2], a[1]
			gliReseal(c.t, f)
		}},
	}
	for _, r := range invalidRows {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			for i := 0; i < r.accepted; i++ {
				c.AcceptSeq(uint64(i), gliFxAt(uint64(i)), true)
			}
			before := env.Sig()
			f := c.Frame(r.seq, gliFxAt(r.seq), true)
			r.mut(f, c)
			c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, "GLI-040 F4 before F5")
			env.WantSigUnchanged(before, "GLI-045")
		})
	}
	classified := []struct {
		name     string
		accepted []bool
		seq      uint64
		changed  bool
		want     ingestpb.AckCode
	}{
		{"gap frame with V2-violating times (F5 before F6)", []bool{true}, 3, false, ingestpb.AckCode_GAP},
		{"older sequence with V2-violating times (F5 before F6)", []bool{true, true}, 0, true, ingestpb.AckCode_OUT_OF_ORDER},
		{"conflict with V2-violating times (F5 before F6)", []bool{true, true}, 1, true, ingestpb.AckCode_CONFLICT},
		{"partial sequence 0 with V2-violating times (F5 before F6)", nil, 0, false, ingestpb.AckCode_WRONG_SESSION},
	}
	for _, r := range classified {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			for i, complete := range r.accepted {
				c.AcceptSeq(uint64(i), gliFxAt(uint64(i)), complete)
			}
			before := env.Sig()
			var f *ingestpb.SnapshotFrame
			switch {
			case r.changed:
				f = gliFxChanged(c, r.seq, gliFxAt(r.seq), true)
			case r.want == ingestpb.AckCode_WRONG_SESSION:
				f = c.Frame(r.seq, gliFxAt(r.seq), false)
			default:
				f = c.Frame(r.seq, gliFxAt(r.seq), true)
			}
			v2bad(f, c)
			if got := c.Classified(f, "GLI-040 F5 before F6"); got != r.want {
				t.Fatalf("S1 classification %s, want %s", got, r.want)
			}
			env.WantSigUnchanged(before, "GLI-045")
		})
	}
}

// GLI-011 (e), GLI-040 F3/F4: unknown fields are rejected by position. In the
// envelope (ClientFrame, SnapshotFrame, observed_at) the ack is INVALID from F3;
// in the payload (every nested message, Timestamps, allocation_batch) from F4.
// The digest is built from decoded values, so the digest stays valid; the same
// frame without the unknown field is accepted.
func TestGLI011_UnknownFieldsRejectedByPosition(t *testing.T) {
	base := gliFxStart(t)
	withAlloc := func(c *gliFxConn, f *ingestpb.SnapshotFrame) {
		at := gliFxAt(0)
		f.Payload.AllocationBatch = &ingestpb.AllocationBatch{
			NodeUid: c.UID, BootId: c.Boot, Session: c.Session, Sequence: 0,
			ObservedAt: gliFxTS(at), ExpiresAt: gliFxTS(at.Add(gliFxTTL)), Complete: true,
			Profile: ingestpb.AllocationProfile_NVIDIA_PODRESOURCES_UUID,
			Entries: []*ingestpb.AllocationEntry{{ResourceName: "nvidia.com/gpu", DeviceId: gliFxUUID, PodNamespace: "ml", PodName: "p", ContainerName: "main"}},
		}
		gliReseal(c.t, f)
	}
	type unk func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame)
	rows := []struct {
		name  string
		alloc bool
		mut   unk
	}{
		{"ClientFrame wrapper", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			cf.ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"SnapshotFrame envelope", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) { f.ProtoReflect().SetUnknown(gliFxUnknown()) }},
		{"envelope observed_at Timestamp", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.ObservedAt.ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload asset", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.Assets[0].ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload edge", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.Edges[0].ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload edgeEvidence", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.EdgeEvidence[0].ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload edgeEvidence Timestamp", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.EdgeEvidence[0].ObservedAt.ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload observation", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.Observations[0].ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload observation source", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.Observations[0].Source.ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload observation subject", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.Observations[0].Subject.ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload observation value", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.Observations[0].Value.ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload observation dimension", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.Observations[1].Dimensions[0].ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload observation Timestamp", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.Observations[0].ReceivedAt.ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload gpuBinding", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.GpuBindings[0].ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"payload gpuBinding Timestamp", false, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.GpuBindings[0].ExpiresAt.ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"allocation_batch", true, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.AllocationBatch.ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"allocation_batch Timestamp", true, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.AllocationBatch.ExpiresAt.ProtoReflect().SetUnknown(gliFxUnknown())
		}},
		{"allocation_batch entry", true, func(cf *ingestpb.ClientFrame, f *ingestpb.SnapshotFrame) {
			f.Payload.AllocationBatch.Entries[0].ProtoReflect().SetUnknown(gliFxUnknown())
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			before := env.Sig()
			f := c.Frame(0, gliFxAt(0), true)
			if r.alloc {
				withAlloc(c, f)
			}
			cf := gliFxSnapshotFrame(f)
			r.mut(cf, f)
			c.SendClient(cf)
			a := c.WantAck()
			gliFxWantAck(t, a, ingestpb.AckCode_INVALID, c.Session, 0, "GLI-011 (e) unknown field")
			c.WantEnd(codes.InvalidArgument, "GLI-011 (e)/GLI-044")
			env.WantSigUnchanged(before, "GLI-045")
		})
	}
	t.Run("control: the same frame with an allocation_batch and without unknown fields is accepted", func(t *testing.T) {
		env := base.For(t)
		c := env.ConnectDefault()
		f := c.Frame(0, gliFxAt(0), true)
		withAlloc(c, f)
		a := c.Ack(f)
		gliFxWantAck(t, a, ingestpb.AckCode_ACCEPTED, c.Session, 1, "control")
	})
}

// GLI-053, GLI-041: a node whose state was discarded by NodeRetention (lazy
// evaluation at frame processing, fake Clock) has no cursor, so a frame of the
// same stream that is rejected at F2..F4 gets expected_next_sequence 0; a frame
// that would continue the sequence is WRONG_SESSION with 0. One minute inside the
// retention the cursor still exists and the same rejections say 1. NodeBundles
// is ErrNoObservation after the discard.
func TestGLI053_RejectedFrameAfterRetentionExpiryAcksNoCursor(t *testing.T) {
	const retention = time.Hour
	base := gliFxStart(t, func(c *ingest.Config) { c.Limits.NodeRetention = retention })
	other := "00000000-0000-4000-8000-000000000000"
	rows := []struct {
		name   string
		mut    func(f *ingestpb.SnapshotFrame, c *gliFxConn)
		ack    ingestpb.AckCode
		stream codes.Code
		always bool // the row also runs inside the retention (it is a rejection there too)
	}{
		{"F2 boot_id of another boot", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.BootId = other }, ingestpb.AckCode_INVALID, codes.FailedPrecondition, true},
		{"F2 node_uid of another node", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.NodeUid = gliFxOtherUID }, ingestpb.AckCode_INVALID, codes.PermissionDenied, true},
		{"F2 session + 1", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.Session = c.Session + 1; gliReseal(c.t, f) }, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition, true},
		{"F3 completeness UNSPECIFIED", func(f *ingestpb.SnapshotFrame, c *gliFxConn) {
			f.Completeness = ingestpb.Completeness_COMPLETENESS_UNSPECIFIED
		}, ingestpb.AckCode_INVALID, codes.InvalidArgument, true},
		{"F3 payload_digest absent", func(f *ingestpb.SnapshotFrame, c *gliFxConn) { f.PayloadDigest = nil }, ingestpb.AckCode_INVALID, codes.InvalidArgument, true},
		{"F4 payload with a non-ASCII character", func(f *ingestpb.SnapshotFrame, c *gliFxConn) {
			f.Payload.Observations[1].Unit = "lan\u00e9s"
			gliReseal(c.t, f)
		}, ingestpb.AckCode_INVALID, codes.InvalidArgument, true},
		{"valid next sequence after the discard", func(f *ingestpb.SnapshotFrame, c *gliFxConn) {}, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition, false},
	}
	for _, expired := range []bool{true, false} {
		for _, r := range rows {
			if !expired && !r.always {
				continue
			}
			name := r.name + " after the retention"
			if !expired {
				name = r.name + " inside the retention"
			}
			t.Run(name, func(t *testing.T) {
				env := base.For(t)
				c := env.ConnectDefault()
				c.AcceptSeq(0, gliFxAt(0), true)
				wantNext := uint64(1)
				if expired {
					env.S.Clock.Advance(retention + time.Minute)
					wantNext = 0
				} else {
					env.S.Clock.Advance(retention - time.Minute)
				}
				f := c.Frame(1, gliFxAt(1), true)
				r.mut(f, c)
				a := c.Ack(f)
				gliFxWantAck(t, a, r.ack, c.Session, wantNext, "GLI-053/GLI-041 "+name)
				c.WantEnd(r.stream, "GLI-053/GLI-044 "+name)
				if expired {
					env.WantNoObservation("GLI-053 after the lazy discard")
				}
			})
		}
	}
}
