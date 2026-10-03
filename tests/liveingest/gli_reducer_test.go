package liveingest_test

// Per-node reducer and LiveBundleSource (GLI-070..077, GLI-124 (e)): the bundle
// NodeBundles returns is compared field by field with an independent
// expectation (GLI-072 ring, GLI-073 stamps and trust, GLI-074 records), the
// S1 decision of the live bundle is compared with the one of the oracle bundle,
// and the acceptance scenarios L-STALE, L-RESTART, L-FIXTURE-NOT-LIVE,
// L-SPOOF-SOURCE and the GLI-077 identity axis are observed.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/ingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

const gliFxFleetUID = "5d7c1b7e-2f0a-4c11-9a51-0b6e3c2d4f10"

// gliFxExtraIntent is a second device of the node with its own claim UUID.
func gliFxExtraIntent(t *testing.T, name, uid string, n int) fleet.Intent {
	t.Helper()
	in := gliFxIntent(t, name, uid)
	in.Claim.UUID = fmt.Sprintf("GPU-00000000-0000-4000-8000-%012x", n)
	in.Claim.EvidenceID = fmt.Sprintf("asset-db:rack7-u12-gpu%d", n)
	if err := in.Validate(); err != nil {
		t.Fatalf("Intent.Validate: %v", err)
	}
	return in
}

func gliFxTrust(session int64, srcs []fleet.TrustedSource) fleet.CollectorTrustProfile {
	return fleet.CollectorTrustProfile{
		ID: gliFxProfile, Mode: fleet.TrustModeLive, ClusterID: gliFxCluster, NodeUID: gliFxNodeUID, Session: session, Sources: srcs,
	}
}

// gliFxOFrames maps wire frames with the live stamps of GLI-073.
func gliFxOFrames(t *testing.T, frames []*ingestpb.SnapshotFrame, srcs []fleet.TrustedSource) []gliOrFrame {
	t.Helper()
	edge, bind := gliOrLiveStamps(gliFxProfile, srcs)
	ctx := gliOrCtx{Cluster: gliFxCluster, NodeName: gliFxNodeName, StampEdge: edge, StampBinding: bind}
	var out []gliOrFrame
	for k, f := range frames {
		fr, err := gliOrApplyM(f, ctx)
		if err != nil {
			t.Fatalf("oracle M(frame %d): %v", k, err)
		}
		out = append(out, fr)
	}
	return out
}

// gliFxAdmitAll replays the S1 admission chain; every frame must be accepted.
func gliFxAdmitAll(t *testing.T, session int64, frames []*ingestpb.SnapshotFrame) fleet.SnapshotCursor {
	t.Helper()
	o := gliFxOracle{session: session}
	for k, f := range frames {
		if code, _ := o.Admit(f); code != ingestpb.AckCode_ACCEPTED {
			t.Fatalf("test bug: S1 does not accept frame %d (%s)", k, code)
		}
	}
	return *o.prev
}

// gliFxCheckBundle compares a NodeBundles result with the independent
// expectation for the accepted frames, field by field (GLI-072, 073, 074, 075).
func gliFxCheckBundle(t *testing.T, set app.LiveBundleSet, frames []*ingestpb.SnapshotFrame, session int64, pol fleet.Policy, intents []fleet.Intent, now time.Time, srcs []fleet.TrustedSource, clause string) {
	t.Helper()
	x, err := gliOrExpectFor(gliFxOFrames(t, frames, srcs), pol.Freshness, now)
	if err != nil {
		t.Fatalf("%s: oracle: %v", clause, err)
	}
	cursor := gliFxAdmitAll(t, session, frames)
	if len(set.Devices) != len(intents) {
		t.Fatalf("%s: NodeBundles returned %d device bundles for %d intents (GLI-075 (c))", clause, len(set.Devices), len(intents))
	}
	eq := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s: %s\n got %s\nwant %s", clause, what, got, want)
		}
	}
	wantTrust := gliOrTrustString(gliFxTrust(session, srcs))
	var wantRecords []string
	for _, f := range x.Findings {
		wantRecords = append(wantRecords, strings.Join([]string{f.ID, f.Type.String(), f.Severity.String(), f.State.String()}, "|"))
	}
	sort.Strings(wantRecords)
	wantWindow := gliOrWindowSig(x.Window)
	for i, d := range set.Devices {
		bd := d.Bundle
		eq("DeviceUID", d.DeviceUID, intents[i].Device.UID)
		eq("Bundle.Intent", gliOrIntentString(bd.Intent), gliOrIntentString(intents[i]))
		eq("Bundle.Policy", gliOrPolicyString(bd.Policy), gliOrPolicyString(pol))
		eq("Snapshot (canonical digest and revision, GLI-061)", gliOrEnvString(bd.Snapshot), gliOrEnvString(x.Last.Env))
		eq("Admitted (the last accepted cursor)", gliOrCursorString(bd.Admitted), gliOrCursorString(cursor))
		eq("GraphRevision", bd.GraphRevision, x.Last.Env.BundleRevision)
		eq("WindowRevision", bd.WindowRevision, x.Last.Env.BundleRevision)
		eq("FindingsGraphRevision", bd.FindingsGraphRevision, x.Last.Env.BundleRevision)
		eq("TopologyDigest (last frame only)", bd.TopologyDigest, x.Topology)
		eq("BaselineDigest (ring window)", bd.BaselineDigest, x.Baseline)
		eq("Provenance (stamps of GLI-073)", fmt.Sprint(gliOrProvStrings(bd.Provenance)), fmt.Sprint(gliOrProvStrings(x.Last.Prov)))
		eq("Bindings (stamps of GLI-073)", fmt.Sprint(gliOrBindStrings(bd.Bindings)), fmt.Sprint(gliOrBindStrings(x.Last.Binds)))
		eq("CollectorTrust", gliOrTrustString(bd.CollectorTrust), wantTrust)
		eq("Findings", fmt.Sprint(gliOrFindingSigs(bd.Findings)), fmt.Sprint(gliOrFindingSigs(x.Findings)))
		eq("FindingsEvaluatedAt (= q.Now)", gliOrT(bd.FindingsEvaluatedAt), gliOrT(now))
		eq("Window (ring of accepted frames)", gliOrWindowSig(bd.Window), wantWindow)
		var gotRecords []string
		for _, r := range d.Findings {
			gotRecords = append(gotRecords, strings.Join([]string{r.ID, r.Type, r.Severity, r.State}, "|"))
		}
		sort.Strings(gotRecords)
		eq("finding records (GLI-074)", fmt.Sprint(gotRecords), fmt.Sprint(wantRecords))
		if bd.Topology == nil {
			t.Errorf("%s: Bundle.Topology is nil", clause)
		} else {
			if seq, ok := bd.Topology.Sequence(); !ok || seq != x.Last.Env.Sequence {
				t.Errorf("%s: Topology.Sequence() = %v/%v, want %d/true (GFX-043)", clause, seq, ok, x.Last.Env.Sequence)
			}
			if !bd.Topology.Synced() {
				t.Errorf("%s: Topology is not synced", clause)
			}
		}
		if bd.Allocation != nil || bd.Fence != nil || bd.FenceTrust != nil || d.Allocation != nil {
			t.Errorf("%s: allocation, fence and fence trust are S3b-2 and must stay nil (GLI-002)", clause)
		}
	}
	for i := 1; i < len(set.Devices); i++ {
		a, b := set.Devices[0].Bundle, set.Devices[i].Bundle
		if gliOrEnvString(a.Snapshot) != gliOrEnvString(b.Snapshot) || a.GraphRevision != b.GraphRevision ||
			a.Snapshot.Completeness != b.Snapshot.Completeness || a.Snapshot.Session != b.Snapshot.Session {
			t.Errorf("%s: device bundles 0 and %d disagree on Snapshot/GraphRevision/Completeness/Session (GLI-075 (c), GKA-032 (b))", clause, i)
		}
	}
}

// gliFxWantSameDecision requires that EvaluateDevice gives the live bundle and
// the oracle bundle the same decision (S1 sees the same input, GLI-078).
func gliFxWantSameDecision(t *testing.T, live fleet.AssessmentBundle, frames []*ingestpb.SnapshotFrame, session int64, srcs []fleet.TrustedSource, now time.Time, clause string) fleet.DeviceDecision {
	t.Helper()
	cursor := gliFxAdmitAll(t, session, frames)
	want, err := gliOrBundle(live.Policy, live.Intent, gliFxOFrames(t, frames, srcs), cursor, gliFxTrust(session, srcs), now)
	if err != nil {
		t.Fatalf("%s: oracle bundle: %v", clause, err)
	}
	got, err1 := fleet.EvaluateDevice(live, nil, now)
	exp, err2 := fleet.EvaluateDevice(want, nil, now)
	if err1 != nil || err2 != nil {
		t.Fatalf("%s: EvaluateDevice errors: live=%v oracle=%v (an evaluation error would be fleet.ErrInvalidInput)", clause, err1, err2)
	}
	if g, w := gliOrDecisionString(got), gliOrDecisionString(exp); g != w {
		t.Errorf("%s: decision of the live bundle differs from the decision of the oracle bundle\n got %s\nwant %s", clause, g, w)
	}
	return got
}

// gliFxWidthPayload is the BASE payload with the GPU current width cur.
func gliFxWidthPayload(c *gliFxConn, seq uint64, at time.Time, cur int64) *ingestpb.HostSnapshotV1 {
	p := gliPayload(c.UID, c.Boot, c.Session, seq, at)
	gliFxFindObs(p, gliFxFnKey(gliFxGPUBDF), gliFxSigCurrent).Value = gliFxInt(cur)
	return p
}

// gliFxNodeOnlyPayload is a payload with the KubernetesNode asset only.
func gliFxNodeOnlyPayload(c *gliFxConn, seq uint64, at time.Time) *ingestpb.HostSnapshotV1 {
	p := gliPayload(c.UID, c.Boot, c.Session, seq, at)
	p.Assets = p.Assets[:1]
	p.Edges, p.EdgeEvidence, p.Observations, p.GpuBindings = nil, nil, nil, nil
	return p
}

// gliFxAcceptPayload sends the frame of p as the next frame and requires ACCEPTED.
func gliFxAcceptPayload(t *testing.T, c *gliFxConn, p *ingestpb.HostSnapshotV1, seq uint64, at time.Time, complete bool) *ingestpb.SnapshotFrame {
	t.Helper()
	f := gliFrame(t, p, c.Session, seq, at, complete)
	if got := c.Classified(f, fmt.Sprintf("sequence %d", seq)); got != ingestpb.AckCode_ACCEPTED {
		t.Fatalf("sequence %d: %s, want ACCEPTED", seq, got)
	}
	return f
}

// GLI-075 (a)/(b): invalid queries are fleet.ErrInvalidInput; no state, a state
// that has not reached the baseline (Awaiting), a state discarded by a leader
// loss and a state replaced by a new hello are app.ErrNoObservation with the
// empty set (GKA-032 (a)).
func TestGLI075_QueryValidationAndNoObservation(t *testing.T) {
	env := gliFxStart(t)
	env.WantNoObservation("no node state")
	c := env.ConnectDefault()
	env.WantNoObservation("Awaiting: hello without a baseline")
	c.AcceptSeq(0, gliFxAt(0), true)
	now := gliFxAt(0).Add(time.Second)
	pol := gliFxPolicy(t, gliFxFresh)
	in := gliFxDefaultIntent(t)
	if _, err := env.Query(now, pol, in); err != nil {
		t.Fatalf("control: the valid query failed: %v", err)
	}
	node := fleet.NodeRef{ClusterID: gliFxCluster, Name: gliFxNodeName, UID: gliFxNodeUID}
	badIntent := in
	badIntent.RequestID = ""
	otherDevice := gliFxExtraIntent(t, "gpu-node-1-gpu1", "1c6a3e5b-7f2d-4b9c-8a1e-5d3f7b9c2e4a", 1)
	bad := []struct {
		name string
		q    app.LiveBundleQuery
	}{
		{"cluster of another controller", app.LiveBundleQuery{Node: fleet.NodeRef{ClusterID: "other-cluster", Name: node.Name, UID: node.UID}, Policy: pol, Intents: []fleet.Intent{in}, Now: now}},
		{"node UID empty", app.LiveBundleQuery{Node: fleet.NodeRef{ClusterID: node.ClusterID, Name: node.Name}, Policy: pol, Intents: []fleet.Intent{in}, Now: now}},
		{"zero policy", app.LiveBundleQuery{Node: node, Intents: []fleet.Intent{in}, Now: now}},
		{"policy freshness zero", app.LiveBundleQuery{Node: node, Policy: fleet.Policy{Revision: "r", ReadyFor: time.Second}, Intents: []fleet.Intent{in}, Now: now}},
		{"duplicate intents", app.LiveBundleQuery{Node: node, Policy: pol, Intents: []fleet.Intent{in, otherDevice, in}, Now: now}},
		{"intent that fails Validate", app.LiveBundleQuery{Node: node, Policy: pol, Intents: []fleet.Intent{badIntent}, Now: now}},
	}
	for _, b := range bad {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		set, err := env.S.Server.BundleSource().NodeBundles(ctx, b.q)
		cancel()
		if !errors.Is(err, fleet.ErrInvalidInput) {
			t.Errorf("GLI-075 (a) %s: error = %v, want fleet.ErrInvalidInput", b.name, err)
		}
		if len(set.Devices) != 0 {
			t.Errorf("GLI-075 (a) %s: %d device bundles with an error, want none", b.name, len(set.Devices))
		}
	}
	// A new hello discards the old view (GLI-035).
	c2 := env.ConnectDefault()
	env.WantNoObservation("GLI-035 after a second hello")
	c2.AcceptSeq(0, gliFxAt(0), true)
	if _, err := env.Query(now, pol, in); err != nil {
		t.Fatalf("after the new baseline: %v", err)
	}
	// Leader loss: observed by the call, state discarded, still empty after the
	// leadership returns until a new hello and baseline (GLI-037).
	env.S.Leader.Set(false)
	env.WantNoObservation("GLI-037 Leading() is false")
	env.S.Leader.Set(true)
	env.WantNoObservation("GLI-037 leadership back but no new hello/baseline")
	c3 := env.ConnectDefault()
	env.WantNoObservation("GLI-037 new hello, no baseline yet")
	c3.AcceptSeq(0, gliFxAt(0), true)
	if _, err := env.Query(now, pol, in); err != nil {
		t.Fatalf("after the leader's new baseline: %v", err)
	}
}

// GLI-075 (c)/(d), GLI-071..073: after every accepted frame (complete, partial,
// complete) the bundle for one device and for three devices in request order
// equals the independent expectation field by field.
func TestGLI075_ActiveBundleMatchesIndependentExpectation(t *testing.T) {
	env := gliFxStart(t)
	c := env.ConnectDefault()
	srcs := ingest.DefaultTrustedSources()
	pol := gliFxPolicy(t, gliFxFresh)
	in := gliFxDefaultIntent(t)
	z := gliFxExtraIntent(t, "gpu-node-1-gpuz", "ffffffff-6e2d-4f8b-a1c7-3d9e5f2a7b41", 26)
	a := gliFxExtraIntent(t, "gpu-node-1-gpua", "00000000-6e2d-4f8b-a1c7-3d9e5f2a7b41", 10)
	var frames []*ingestpb.SnapshotFrame
	for seq, complete := range []bool{true, false, true} {
		frames = append(frames, c.AcceptSeq(uint64(seq), gliFxAt(uint64(seq)), complete))
		now := gliFxAt(uint64(seq)).Add(5 * time.Second)
		clause := fmt.Sprintf("after sequence %d", seq)
		set, err := env.Query(now, pol, in)
		if err != nil {
			t.Fatalf("%s: %v", clause, err)
		}
		gliFxCheckBundle(t, set, frames, c.Session, pol, []fleet.Intent{in}, now, srcs, clause)
		gliFxWantSameDecision(t, set.Devices[0].Bundle, frames, c.Session, srcs, now, clause)
		// Three intents are answered in q.Intents order, not UID order (GLI-075 (c)).
		order := []fleet.Intent{z, in, a}
		set3, err := env.Query(now, pol, order...)
		if err != nil {
			t.Fatalf("%s: three intents: %v", clause, err)
		}
		gliFxCheckBundle(t, set3, frames, c.Session, pol, order, now, srcs, clause+" three intents")
	}
	last := env.One(gliFxAt(2).Add(5*time.Second), gliFxFresh)
	if last.Bundle.Snapshot.Completeness != fleet.CompletenessComplete {
		t.Errorf("last frame is complete, Snapshot.Completeness = %s", last.Bundle.Snapshot.Completeness)
	}
}

// GLI-073: an element is stamped with the profile ID only when its source equals
// a source of the profile for the matching capability; everything else gets the
// fixed stamp unmatched-source, never an empty stamp. CollectorTrust is the live
// profile with the configured sources.
func TestGLI073_StampsAndTrustProfile(t *testing.T) {
	parent := fleet.TrustedSource{Capability: fleet.TrustSysfsPhysicalParent, Source: model.SourceRef{Type: "agent", Name: gliFxParentSrc}}
	binding := fleet.TrustedSource{Capability: fleet.TrustNVIDIAUUIDBinding, Source: model.SourceRef{Type: "agent", Name: gliFxNVSrc}}
	rows := []struct {
		name          string
		srcs          []fleet.TrustedSource
		mut           func(p *ingestpb.HostSnapshotV1)
		edgeUnmatched int
		bindUnmatched int
	}{
		{"every source is trusted", ingest.DefaultTrustedSources(), func(p *ingestpb.HostSnapshotV1) {}, 0, 0},
		{"one edge with an unknown source name", ingest.DefaultTrustedSources(), func(p *ingestpb.HostSnapshotV1) { p.EdgeEvidence[0].SourceName = "path-agent/evil" }, 1, 0},
		{"one edge with the width source (wrong capability)", ingest.DefaultTrustedSources(), func(p *ingestpb.HostSnapshotV1) { p.EdgeEvidence[1].SourceName = gliFxWidthSrc }, 1, 0},
		{"one edge with another source type", ingest.DefaultTrustedSources(), func(p *ingestpb.HostSnapshotV1) { p.EdgeEvidence[2].SourceType = "gnmi" }, 1, 0},
		{"every edge with an unknown source", ingest.DefaultTrustedSources(), func(p *ingestpb.HostSnapshotV1) {
			for _, ev := range p.EdgeEvidence {
				ev.SourceName = "x"
			}
		}, 6, 0},
		{"binding with an unknown source name", ingest.DefaultTrustedSources(), func(p *ingestpb.HostSnapshotV1) { p.GpuBindings[0].SourceName = "path-agent/evil" }, 0, 1},
		{"binding with the parent source (wrong capability)", ingest.DefaultTrustedSources(), func(p *ingestpb.HostSnapshotV1) { p.GpuBindings[0].SourceName = gliFxParentSrc }, 0, 1},
		{"binding with another source type", ingest.DefaultTrustedSources(), func(p *ingestpb.HostSnapshotV1) { p.GpuBindings[0].SourceType = "gnmi" }, 0, 1},
		{"profile trusts the parent capability only", []fleet.TrustedSource{parent}, func(p *ingestpb.HostSnapshotV1) {}, 0, 1},
		{"profile trusts the binding capability only", []fleet.TrustedSource{binding}, func(p *ingestpb.HostSnapshotV1) {}, 6, 0},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			srcs := r.srcs
			env := gliFxStart(t, func(c *ingest.Config) { c.TrustedSources = srcs })
			c := env.ConnectDefault()
			p := gliPayload(c.UID, c.Boot, c.Session, 0, gliFxAt(0))
			r.mut(p)
			f := gliFxAcceptPayload(t, c, p, 0, gliFxAt(0), true)
			pol := gliFxPolicy(t, gliFxFresh)
			in := gliFxDefaultIntent(t)
			now := gliFxAt(0).Add(time.Second)
			set, err := env.Query(now, pol, in)
			if err != nil {
				t.Fatalf("NodeBundles: %v", err)
			}
			gliFxCheckBundle(t, set, []*ingestpb.SnapshotFrame{f}, c.Session, pol, []fleet.Intent{in}, now, srcs, "GLI-073")
			bd := set.Devices[0].Bundle
			edgeUn, bindUn := 0, 0
			for _, pv := range bd.Provenance {
				switch pv.CollectorProfileID {
				case gliFxProfile:
				case gliOrUnmatched:
					edgeUn++
				default:
					t.Errorf("provenance %s stamped %q, want the profile ID or %q (never empty)", pv.EvidenceID, pv.CollectorProfileID, gliOrUnmatched)
				}
			}
			for _, b := range bd.Bindings {
				switch b.CollectorProfileID {
				case gliFxProfile:
				case gliOrUnmatched:
					bindUn++
				default:
					t.Errorf("binding %s stamped %q, want the profile ID or %q (never empty)", b.EvidenceID, b.CollectorProfileID, gliOrUnmatched)
				}
			}
			if edgeUn != r.edgeUnmatched || bindUn != r.bindUnmatched {
				t.Errorf("unmatched stamps edge/binding = %d/%d, want %d/%d", edgeUn, bindUn, r.edgeUnmatched, r.bindUnmatched)
			}
			tr := bd.CollectorTrust
			if tr.ID != gliFxProfile || tr.Mode != fleet.TrustModeLive || tr.ClusterID != gliFxCluster || tr.NodeUID != gliFxNodeUID || tr.Session != c.Session {
				t.Errorf("CollectorTrust = %s, want the live profile of session %d", gliOrTrustString(tr), c.Session)
			}
			gliFxWantSameDecision(t, bd, []*ingestpb.SnapshotFrame{f}, c.Session, srcs, now, "GLI-073 decision")
		})
	}
}

// GLI-072: the evidence window is rebuilt from the last 16 accepted frames
// (partial frames included). The first frame (with a series of its own and the
// only expected-width observations) leaves the ring when the 17th frame is
// accepted: its series disappears from Bundle.Window and BaselineDigest changes,
// while it is unchanged for the 16 frames before. The topology is always the
// last frame's alone. Every step equals the independent expectation.
func TestGLI072_RingOfSixteenFramesBaselineDigestAndLastFrameTopology(t *testing.T) {
	env := gliFxStart(t)
	c := env.ConnectDefault()
	srcs := ingest.DefaultTrustedSources()
	pol := gliFxPolicy(t, 600*time.Second)
	in := gliFxDefaultIntent(t)
	xKey := gliFxFnKey("0000:09:00.0")
	var frames []*ingestpb.SnapshotFrame
	var d0, t0 string
	for k := 0; k <= 16; k++ {
		seq := uint64(k)
		at := gliFxAt(seq)
		p := gliPayload(c.UID, c.Boot, c.Session, seq, at)
		if k == 0 {
			o := gliFxObservation(c.Session, seq, at, "PCIeFunction", "pci-bdf:0000:09:00.0", "gli.first", gliFxWidthSrc, gliFxInt(1), "n", nil)
			o.ExpiresAt = gliFxTS(at.Add(gliFxTTL))
			p.Observations = append(p.Observations, o)
			gliFxSortPayload(p)
		} else {
			gliFxDropObs(p, gliFxSigExpect)
		}
		// Frame 7 is partial with a node-only topology; the next frame is whole again.
		complete := k != 7
		if k == 7 {
			p = gliFxNodeOnlyPayload(c, seq, at)
		}
		frames = append(frames, gliFxAcceptPayload(t, c, p, seq, at, complete))
		now := at.Add(time.Second)
		set, err := env.Query(now, pol, in)
		if err != nil {
			t.Fatalf("frame %d: %v", k, err)
		}
		gliFxCheckBundle(t, set, frames, c.Session, pol, []fleet.Intent{in}, now, srcs, fmt.Sprintf("GLI-072 after frame %d", k))
		bd := set.Devices[0].Bundle
		hasX := false
		for _, s := range gliOrSubjects(bd.Window) {
			hasX = hasX || s == xKey
		}
		switch {
		case k == 0:
			d0, t0 = bd.BaselineDigest, bd.TopologyDigest
		case k <= 15:
			if bd.BaselineDigest != d0 {
				t.Errorf("GLI-072 (d): BaselineDigest changed at frame %d although frame 0 is still in the ring", k)
			}
			if !hasX {
				t.Errorf("GLI-072 (a): the series of frame 0 left the window at frame %d (ring keeps 16 frames)", k)
			}
		default:
			if bd.BaselineDigest == d0 {
				t.Errorf("GLI-072 (d): BaselineDigest did not change after frame 0 left the ring (17th frame)")
			}
			if hasX {
				t.Errorf("GLI-072 (a): the series of frame 0 is still in the window after the 17th frame")
			}
		}
		switch k {
		case 7:
			if bd.TopologyDigest == t0 || bd.Snapshot.Completeness != fleet.CompletenessPartial {
				t.Errorf("GLI-072 (b): the partial frame's topology must stand alone (digest %s, completeness %s)", bd.TopologyDigest, bd.Snapshot.Completeness)
			}
		case 8:
			if bd.TopologyDigest != t0 {
				t.Errorf("GLI-072 (b): the topology after the partial frame must be the whole frame's again (no carried graph)")
			}
		}
	}
}

// GLI-076, GLI-072 (c): a partial last frame is returned as it is (never
// ErrNoObservation, no earlier complete graph attached) and S1 judges it like
// the oracle bundle: not qualified.
func TestGLI076_PartialLastFrameIsReturnedAsIs(t *testing.T) {
	env := gliFxStart(t)
	c := env.ConnectDefault()
	srcs := ingest.DefaultTrustedSources()
	pol := gliFxPolicy(t, gliFxFresh)
	in := gliFxDefaultIntent(t)
	f0 := c.AcceptSeq(0, gliFxAt(0), true)
	f1 := gliFxAcceptPayload(t, c, gliFxNodeOnlyPayload(c, 1, gliFxAt(1)), 1, gliFxAt(1), false)
	frames := []*ingestpb.SnapshotFrame{f0, f1}
	now := gliFxAt(1).Add(time.Second)
	set, err := env.Query(now, pol, in)
	if err != nil {
		t.Fatalf("NodeBundles after a partial frame: %v", err)
	}
	gliFxCheckBundle(t, set, frames, c.Session, pol, []fleet.Intent{in}, now, srcs, "GLI-076 partial last frame")
	bd := set.Devices[0].Bundle
	if len(bd.Provenance) != 0 || len(bd.Bindings) != 0 {
		t.Errorf("GLI-076: the partial frame has no edges or bindings; the bundle carries %d provenance and %d bindings of an earlier frame", len(bd.Provenance), len(bd.Bindings))
	}
	dec := gliFxWantSameDecision(t, bd, frames, c.Session, srcs, now, "GLI-076 partial")
	if dec.Qualification == fleet.QualificationQualified || dec.Phase == fleet.PhaseReady {
		t.Errorf("GLI-076: a partial last frame must not qualify (S1: Unknown); got phase %s qualification %s", dec.Phase, dec.Qualification)
	}
}

// GLI-072 (c), GLI-076: the head freshness of the window follows q.Now and the
// policy freshness; between frames only FindingsEvaluatedAt and what depends on
// it (findings and their records) change from one call to the next.
func TestGLI072_FreshnessAndQueryTimeDependence(t *testing.T) {
	env := gliFxStart(t)
	c := env.ConnectDefault()
	srcs := ingest.DefaultTrustedSources()
	in := gliFxDefaultIntent(t)
	var frames []*ingestpb.SnapshotFrame
	for seq := uint64(0); seq < 3; seq++ {
		frames = append(frames, gliFxAcceptPayload(t, c, gliFxWidthPayload(c, seq, gliFxAt(seq), 8), seq, gliFxAt(seq), true))
	}
	t2 := gliFxAt(2)
	query := func(fresh time.Duration, now time.Time) (app.LiveBundleSet, fleet.Policy) {
		pol := gliFxPolicy(t, fresh)
		set, err := env.Query(now, pol, in)
		if err != nil {
			t.Fatalf("NodeBundles(freshness %s, now %s): %v", fresh, now, err)
		}
		gliFxCheckBundle(t, set, frames, c.Session, pol, []fleet.Intent{in}, now, srcs, fmt.Sprintf("freshness %s now %s", fresh, gliOrT(now)))
		return set, pol
	}
	expect := func(fresh time.Duration, now time.Time) int {
		x, err := gliOrExpectFor(gliFxOFrames(t, frames, srcs), fresh, now)
		if err != nil {
			t.Fatalf("oracle: %v", err)
		}
		return len(x.Findings)
	}
	if expect(60*time.Second, t2) == 0 {
		t.Fatalf("test precondition: three degraded width pairs 30 s apart must give a PCIE_LINK_WIDTH_DEGRADED finding")
	}
	if expect(60*time.Second, t2.Add(30*time.Second)) == 0 || expect(20*time.Second, t2.Add(30*time.Second)) != 0 {
		t.Fatalf("test precondition: freshness 60 s keeps the finding 30 s after the last frame and freshness 20 s drops it")
	}
	set1, _ := query(60*time.Second, t2)
	set2, _ := query(60*time.Second, t2.Add(61*time.Second)) // head stale at 60 s
	a, b := set1.Devices[0].Bundle, set2.Devices[0].Bundle
	same := func(what, x, y string) {
		t.Helper()
		if x != y {
			t.Errorf("GLI-076: %s changed between two calls without a new frame\n first %s\nsecond %s", what, x, y)
		}
	}
	same("Snapshot", gliOrEnvString(a.Snapshot), gliOrEnvString(b.Snapshot))
	same("Admitted", gliOrCursorString(a.Admitted), gliOrCursorString(b.Admitted))
	same("TopologyDigest", a.TopologyDigest, b.TopologyDigest)
	same("BaselineDigest", a.BaselineDigest, b.BaselineDigest)
	same("Provenance", fmt.Sprint(gliOrProvStrings(a.Provenance)), fmt.Sprint(gliOrProvStrings(b.Provenance)))
	same("Bindings", fmt.Sprint(gliOrBindStrings(a.Bindings)), fmt.Sprint(gliOrBindStrings(b.Bindings)))
	same("Window", gliOrWindowSig(a.Window), gliOrWindowSig(b.Window))
	if gliOrT(a.FindingsEvaluatedAt) == gliOrT(b.FindingsEvaluatedAt) {
		t.Errorf("GLI-072 (c): FindingsEvaluatedAt must be each call's q.Now")
	}
	if len(set1.Devices[0].Findings) == 0 {
		t.Errorf("GLI-074: the degraded window gave no finding record")
	}
	if len(set2.Devices[0].Findings) != 0 {
		t.Errorf("GLI-076: once the head is stale the finding is gone; got %d records", len(set2.Devices[0].Findings))
	}
	// Freshness 60 s versus 20 s at the same query time (the window MaxAge is the policy freshness).
	long, _ := query(60*time.Second, t2.Add(30*time.Second))
	short, _ := query(20*time.Second, t2.Add(30*time.Second))
	if len(long.Devices[0].Findings) == 0 || len(short.Devices[0].Findings) != 0 {
		t.Errorf("GLI-072 (a): findings with freshness 60 s / 20 s = %d / %d, want >0 / 0", len(long.Devices[0].Findings), len(short.Devices[0].Findings))
	}
}

// GLI-075 (f): every call returns copies; a caller that rewrites the returned
// slices and records cannot change the next call's result.
func TestGLI075_ResultsAreDefensiveCopies(t *testing.T) {
	env := gliFxStart(t)
	c := env.ConnectDefault()
	for seq := uint64(0); seq < 3; seq++ {
		gliFxAcceptPayload(t, c, gliFxWidthPayload(c, seq, gliFxAt(seq), 8), seq, gliFxAt(seq), true)
	}
	pol := gliFxPolicy(t, gliFxFresh)
	in := gliFxDefaultIntent(t)
	now := gliFxAt(2)
	query := func() app.LiveBundleSet {
		set, err := env.Query(now, pol, in, gliFxExtraIntent(t, "gpu-node-1-gpu1", "1c6a3e5b-7f2d-4b9c-8a1e-5d3f7b9c2e4a", 1))
		if err != nil {
			t.Fatalf("NodeBundles: %v", err)
		}
		return set
	}
	want := gliFxSig(query())
	set := query()
	if len(set.Devices[0].Findings) == 0 || len(set.Devices[0].Evidence) == 0 || len(set.Devices[0].Bundle.Findings) == 0 {
		t.Fatalf("test precondition: the degraded node must carry findings and evidence records")
	}
	for _, d := range set.Devices {
		for i := range d.Bundle.Provenance {
			d.Bundle.Provenance[i].EvidenceID = "mutated"
			d.Bundle.Provenance[i].CollectorProfileID = "mutated"
		}
		for i := range d.Bundle.Bindings {
			d.Bundle.Bindings[i].BDF = "mutated"
			d.Bundle.Bindings[i].CollectorProfileID = "mutated"
		}
		for i := range d.Bundle.Findings {
			d.Bundle.Findings[i].ID = "mutated"
			if len(d.Bundle.Findings[i].Scope) > 0 {
				d.Bundle.Findings[i].Scope[0] = model.AssetRef{}
			}
		}
		for i := range d.Findings {
			d.Findings[i].ID = "mutated"
		}
		for i := range d.Evidence {
			d.Evidence[i].ID = "mutated"
			d.Evidence[i].Summary = "mutated"
		}
		if len(d.Bundle.CollectorTrust.Sources) > 0 {
			d.Bundle.CollectorTrust.Sources[0].Source.Name = "mutated"
		}
	}
	set.Devices[0], set.Devices[1] = set.Devices[1], set.Devices[0]
	if got := gliFxSig(query()); got != want {
		t.Errorf("GLI-075 (f): the result of a later call changed after a caller rewrote an earlier result\nbefore:\n%s\nafter:\n%s", want, got)
	}
}

// GLI-070, GLI-045, GLI-075 (e): one ingesting stream and several concurrent
// NodeBundles callers (run with -race). Every bundle a reader sees is one whole
// view: its revision is the canonical one of an accepted frame and the
// provenance, bindings and window revisions are the same frame's; sequences
// never go backwards; the call after an ack already shows the acked frame.
func TestGLI070_ConcurrentNodeBundlesWhileIngesting(t *testing.T) {
	env := gliFxStart(t)
	c := env.ConnectDefault()
	const frames = 40
	built := make([]*ingestpb.SnapshotFrame, frames)
	revs := map[uint64]string{}
	for i := range built {
		built[i] = c.Frame(uint64(i), gliFxAt(uint64(i)), true)
		revs[uint64(i)] = built[i].GetBundleRevision()
	}
	pol := gliFxPolicy(t, gliFxFresh)
	in := gliFxDefaultIntent(t)
	node := fleet.NodeRef{ClusterID: gliFxCluster, Name: gliFxNodeName, UID: gliFxNodeUID}
	done := make(chan struct{})
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			var last uint64
			for {
				select {
				case <-done:
					return
				default:
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				set, err := env.S.Server.BundleSource().NodeBundles(ctx, app.LiveBundleQuery{Node: node, Policy: pol, Intents: []fleet.Intent{in}, Now: gliFxAt(100)})
				cancel()
				if errors.Is(err, app.ErrNoObservation) {
					continue
				}
				if err != nil || len(set.Devices) != 1 {
					t.Errorf("reader %d: NodeBundles = %d devices, %v", r, len(set.Devices), err)
					return
				}
				b := set.Devices[0].Bundle
				seq := b.Snapshot.Sequence
				rev := b.Snapshot.BundleRevision
				if want, ok := revs[seq]; !ok || rev != want {
					t.Errorf("reader %d: revision %q of sequence %d is not the canonical revision of an accepted frame", r, rev, seq)
					return
				}
				if b.GraphRevision != rev || b.WindowRevision != rev || b.FindingsGraphRevision != rev || b.Admitted.BundleRevision != rev || b.Admitted.Sequence != seq {
					t.Errorf("reader %d: bundle mixes two views: revisions %q/%q/%q/%q, cursor sequence %d, snapshot sequence %d",
						r, b.GraphRevision, b.WindowRevision, b.FindingsGraphRevision, b.Admitted.BundleRevision, b.Admitted.Sequence, seq)
					return
				}
				for _, p := range b.Provenance {
					if p.BundleRevision != rev {
						t.Errorf("reader %d: provenance of revision %q in the view of %q", r, p.BundleRevision, rev)
						return
					}
				}
				for _, bi := range b.Bindings {
					if bi.BundleRevision != rev {
						t.Errorf("reader %d: binding of revision %q in the view of %q", r, bi.BundleRevision, rev)
						return
					}
				}
				if seq < last {
					t.Errorf("reader %d: sequence went back from %d to %d", r, last, seq)
					return
				}
				last = seq
			}
		}(r)
	}
	for i, f := range built {
		a := c.Ack(f)
		gliFxWantAck(t, a, ingestpb.AckCode_ACCEPTED, c.Session, uint64(i)+1, fmt.Sprintf("frame %d", i))
		b := env.One(gliFxAt(100), gliFxFresh)
		if b.Bundle.Snapshot.Sequence != uint64(i) {
			t.Errorf("GLI-045: the call after the ack of sequence %d sees sequence %d", i, b.Bundle.Snapshot.Sequence)
		}
	}
	close(done)
	wg.Wait()
}

// GLI-077 (b): the same UUID on two BDFs is structurally valid and passes the
// reducer untouched (every binding is mapped, none is filtered); S1 judges the
// observation-axis conflict: identity Conflict / IdentityConflict, Unknown.
func TestGLI077_SameUUIDOnTwoBDFsIsAnIdentityConflict(t *testing.T) {
	env := gliFxStart(t)
	c := env.ConnectDefault()
	srcs := ingest.DefaultTrustedSources()
	p := gliPayload(c.UID, c.Boot, c.Session, 0, gliFxAt(0))
	p.GpuBindings = append(p.GpuBindings, gliFxBinding(c.Session, 0, gliFxAt(0), gliFxUUID, gliFxNICBDF))
	gliFxSortPayload(p)
	f := gliFxAcceptPayload(t, c, p, 0, gliFxAt(0), true)
	pol := gliFxPolicy(t, gliFxFresh)
	in := gliFxDefaultIntent(t)
	now := gliFxAt(0).Add(time.Second)
	set, err := env.Query(now, pol, in)
	if err != nil {
		t.Fatalf("NodeBundles: %v", err)
	}
	gliFxCheckBundle(t, set, []*ingestpb.SnapshotFrame{f}, c.Session, pol, []fleet.Intent{in}, now, srcs, "GLI-077")
	if n := len(set.Devices[0].Bundle.Bindings); n != 2 {
		t.Fatalf("GLI-077 (b): %d bindings in the bundle, want both (the source never filters them)", n)
	}
	dec := gliFxWantSameDecision(t, set.Devices[0].Bundle, []*ingestpb.SnapshotFrame{f}, c.Session, srcs, now, "GLI-077")
	if dec.BindingState.String() != "Conflict" || dec.Reason != "IdentityConflict" || dec.Qualification != fleet.QualificationUnknown {
		t.Errorf("GLI-077/S-IDCONFLICT: binding %s reason %q qualification %s, want Conflict / IdentityConflict / Unknown",
			dec.BindingState, dec.Reason, dec.Qualification)
	}
}

// gliFxChain sends n frames built by mk, assesses the node after each frame at
// the frame time with the live assessor, and returns the live decisions with the
// decisions of the offline-style oracle replay (GFX-048, compared without the
// nic-lldp-remote coverage item) and of the replay with the live trust
// profile (an independent S1 call, which the nic-lldp-remote item is compared
// with).
func gliFxChain(t *testing.T, env *gliFxEnv, n int, mk func(c *gliFxConn, seq uint64, at time.Time) *ingestpb.HostSnapshotV1) (live, want, wantLive []fleet.DeviceDecision) {
	t.Helper()
	c := env.ConnectDefault()
	pol := gliFxPolicy(t, gliFxFresh)
	in := gliFxDefaultIntent(t)
	assessor := app.NewLiveAssessor(env.S.Server.BundleSource())
	var frames []*ingestpb.SnapshotFrame
	for k := 0; k < n; k++ {
		seq := uint64(k)
		at := gliFxAt(seq)
		frames = append(frames, gliFxAcceptPayload(t, c, mk(c, seq, at), seq, at, true))
		res, err := assessor.AssessNode(context.Background(), app.NodeAssessmentRequest{
			Node: fleet.NodeRef{ClusterID: gliFxCluster, Name: gliFxNodeName, UID: gliFxNodeUID}, FleetUID: gliFxFleetUID,
			Policy: pol, Selection: fleet.SelectionComplete, Intents: []fleet.Intent{in}, Now: at,
		})
		if err != nil {
			t.Fatalf("AssessNode after frame %d: %v (an evaluation failure would wrap fleet.ErrInvalidInput)", k, err)
		}
		if len(res.Devices) != 1 {
			t.Fatalf("AssessNode after frame %d returned %d decisions, want 1", k, len(res.Devices))
		}
		live = append(live, res.Devices[0].Decision)
	}
	rep, err := gliOrReplay(gliOrReplayIn{
		Frames: frames, Policy: pol, Intents: []fleet.Intent{in}, Cluster: gliFxCluster, NodeName: gliFxNodeName, FleetUID: gliFxFleetUID,
		Profile: fleet.CollectorTrustProfile{
			ID: "offline:gli-oracle", Mode: fleet.TrustModeOffline, ClusterID: gliFxCluster, NodeUID: gliFxNodeUID,
			Session: c.Session, Sources: ingest.DefaultTrustedSources(),
		},
	})
	if err != nil {
		t.Fatalf("oracle replay: %v", err)
	}
	repLive, err := gliOrReplay(gliOrReplayIn{
		Frames: frames, Policy: pol, Intents: []fleet.Intent{in}, Cluster: gliFxCluster, NodeName: gliFxNodeName, FleetUID: gliFxFleetUID,
		Profile: gliFxTrust(c.Session, ingest.DefaultTrustedSources()), LiveStamps: true,
	})
	if err != nil {
		t.Fatalf("live-profile oracle replay: %v", err)
	}
	return live, rep.DecAt[0], repLive.DecAt[0]
}

// L-SPOOF-SOURCE (GFL-137, GLI-073): an authenticated agent that sends edge
// evidence, a gpuBinding or width observations with a source the profile does
// not trust gets the S1 decision of the offline S-UNTRUSTED* replay of the same
// frames: no evaluation error, not Ready, not Qualified.
func TestGLI073_LSPOOFSOURCESpoofedSourcesNeverQualify(t *testing.T) {
	spoof := func(name string, mut func(p *ingestpb.HostSnapshotV1)) func(c *gliFxConn, seq uint64, at time.Time) *ingestpb.HostSnapshotV1 {
		return func(c *gliFxConn, seq uint64, at time.Time) *ingestpb.HostSnapshotV1 {
			p := gliPayload(c.UID, c.Boot, c.Session, seq, at)
			mut(p)
			return p
		}
	}
	const evil = "path-agent/evil"
	rows := []struct {
		name string
		mk   func(c *gliFxConn, seq uint64, at time.Time) *ingestpb.HostSnapshotV1
	}{
		{"edge evidence of an untrusted source", spoof("edge", func(p *ingestpb.HostSnapshotV1) {
			for _, ev := range p.EdgeEvidence {
				ev.SourceName = evil
			}
		})},
		{"gpuBinding of an untrusted source", spoof("binding", func(p *ingestpb.HostSnapshotV1) { p.GpuBindings[0].SourceName = evil })},
		{"width observations of an untrusted source", spoof("width", func(p *ingestpb.HostSnapshotV1) {
			for _, o := range p.Observations {
				if o.GetSignal() == gliFxSigCurrent || o.GetSignal() == gliFxSigExpect {
					o.Source.Name = evil
				}
			}
			gliFxSortPayload(p)
		})},
	}
	control, _, _ := gliFxChain(t, gliFxStart(t), 3, func(c *gliFxConn, seq uint64, at time.Time) *ingestpb.HostSnapshotV1 {
		return gliPayload(c.UID, c.Boot, c.Session, seq, at)
	})
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			live, want, wantLive := gliFxChain(t, gliFxStart(t), 3, r.mk)
			for k := range live {
				if g, w := gliOrDecisionStringNoNIC(live[k]), gliOrDecisionStringNoNIC(want[k]); g != w {
					t.Errorf("GLI-078/L-SPOOF-SOURCE frame %d: live decision differs from the offline-style decision (nic-lldp-remote left out)\n got %s\nwant %s", k, g, w)
				}
				if g, w := gliOrNICCoverage(live[k]), gliOrNICCoverage(wantLive[k]); g != w {
					t.Errorf("GLI-078 frame %d: nic-lldp-remote coverage differs from the S1 call with the live trust profile\n got %s\nwant %s", k, g, w)
				}
			}
			last := live[len(live)-1]
			if last.Phase == fleet.PhaseReady || last.Qualification == fleet.QualificationQualified {
				t.Errorf("L-SPOOF-SOURCE: untrusted source reached phase %s qualification %s; it must never be a Ready or gate-release basis", last.Phase, last.Qualification)
			}
			if c := control[len(control)-1]; c.Phase == fleet.PhaseReady && last.Phase == fleet.PhaseReady {
				t.Errorf("L-SPOOF-SOURCE: the spoofed frames reached the same Ready phase as the trusted control")
			}
		})
	}
}

// GLI-074: finding and evidence records. Findings carry the model String()
// values; Evidence holds a record for every edge provenance of the last frame,
// the gpuBinding, every width observation of the window (summary
// "<signal>=<value> <unit>", expiry min(payload expiry, observed + freshness))
// and every observation a finding refers to; no ID twice; summaries ASCII 1-512;
// every evidence ID of the S1 decision has a record (GKA-085 (b)).
func TestGLI074_FindingAndEvidenceRecords(t *testing.T) {
	env := gliFxStart(t)
	c := env.ConnectDefault()
	srcs := ingest.DefaultTrustedSources()
	var frames []*ingestpb.SnapshotFrame
	for seq := uint64(0); seq < 3; seq++ {
		frames = append(frames, gliFxAcceptPayload(t, c, gliFxWidthPayload(c, seq, gliFxAt(seq), 8), seq, gliFxAt(seq), true))
	}
	in := gliFxDefaultIntent(t)
	now := gliFxAt(2)
	for _, fresh := range []time.Duration{60 * time.Second, 600 * time.Second} {
		t.Run(fresh.String(), func(t *testing.T) {
			pol := gliFxPolicy(t, fresh)
			set, err := env.Query(now, pol, in)
			if err != nil {
				t.Fatalf("NodeBundles: %v", err)
			}
			d := set.Devices[0]
			x, err := gliOrExpectFor(gliFxOFrames(t, frames, srcs), fresh, now)
			if err != nil || len(x.Findings) == 0 {
				t.Fatalf("test precondition: oracle findings %d, %v", len(x.Findings), err)
			}
			byID := map[string]app.EvidenceRecord{}
			for _, r := range d.Evidence {
				if _, dup := byID[r.ID]; dup {
					t.Errorf("GLI-074: evidence record %q appears twice", r.ID)
				}
				byID[r.ID] = r
				if len(r.Summary) < 1 || len(r.Summary) > 512 {
					t.Errorf("GLI-074: summary of %q is %d bytes, want 1-512", r.ID, len(r.Summary))
				}
				for i := 0; i < len(r.Summary); i++ {
					if r.Summary[i] < 0x20 || r.Summary[i] > 0x7e {
						t.Errorf("GLI-074: summary of %q has the non-ASCII byte 0x%02x", r.ID, r.Summary[i])
						break
					}
				}
			}
			need := func(id, summary string, observed, expires time.Time) {
				t.Helper()
				r, ok := byID[id]
				if !ok {
					t.Errorf("GLI-074: no evidence record for %q (%s)", id, summary)
					return
				}
				if r.Summary != summary {
					t.Errorf("GLI-074: record %q summary %q, want %q", id, r.Summary, summary)
				}
				if !r.ObservedAt.Equal(observed) || !r.ExpiresAt.Equal(expires) {
					t.Errorf("GLI-074: record %q times %s/%s, want %s/%s", id, gliOrT(r.ObservedAt), gliOrT(r.ExpiresAt), gliOrT(observed), gliOrT(expires))
				}
			}
			for _, pv := range x.Last.Prov {
				need(pv.EvidenceID, fmt.Sprintf("%s %s %s %s", pv.Edge.From.Kind, pv.Edge.Relation, pv.Edge.To.Kind, strings.ToLower(pv.Edge.Origin.String())), pv.ObservedAt, pv.ExpiresAt)
			}
			for _, b := range x.Last.Binds {
				need(b.EvidenceID, "NVIDIA UUID binding "+b.BDF, b.ObservedAt, b.ExpiresAt)
			}
			for _, fr := range x.Ring {
				for _, o := range fr.Obs {
					if o.Signal != "pcie.link.width.current" && o.Signal != "pcie.link.width.expected" {
						continue
					}
					val := strings.TrimPrefix(gliOrValueString(o.Value), "int:")
					exp := o.ExpiresAt
					if alt := o.ObservedAt.Add(fresh); exp.IsZero() || alt.Before(exp) {
						exp = alt
					}
					need(o.ID, fmt.Sprintf("%s=%s %s", o.Signal, val, o.Unit), o.ObservedAt, exp)
				}
			}
			for _, f := range x.Findings {
				for _, ev := range f.Evidence {
					if _, ok := byID[ev.ObservationID]; !ok {
						t.Errorf("GLI-074: no record for the observation %q that finding %q refers to", ev.ObservationID, f.ID)
					}
				}
			}
			dec, err := fleet.EvaluateDevice(d.Bundle, nil, now)
			if err != nil {
				t.Fatalf("EvaluateDevice: %v", err)
			}
			for _, id := range dec.EvidenceIDs {
				if _, ok := byID[id]; !ok {
					t.Errorf("GKA-085 (b): decision evidence ID %q has no record", id)
				}
			}
			for _, id := range dec.FindingIDs {
				found := false
				for _, r := range d.Findings {
					found = found || r.ID == id
				}
				if !found {
					t.Errorf("GKA-085: decision finding ID %q has no record", id)
				}
			}
		})
	}
}

// gliFxManual is a server that the test serves and stops itself.
type gliFxManual struct {
	srv    *ingest.Server
	addr   string
	pki    *gliPKI
	clock  *gliClock
	cancel context.CancelFunc
	done   chan struct{} // closed when Serve has returned; err is valid then
	err    error
}

func gliFxNewManual(t *testing.T) *gliFxManual {
	t.Helper()
	pki := gliNewPKI(t)
	nodes := gliNewNodes()
	nodes.Put(gliFxNodeName, gliFxNodeUID)
	clock := gliNewClock(gliFxT0)
	srv, err := ingest.NewServer(ingest.Config{
		ClusterID: gliFxCluster, ServiceDNS: "localhost", CollectorProfileID: gliFxProfile,
		TrustedSources: ingest.DefaultTrustedSources(),
		TLS:            ingest.TLSFiles{CAFile: pki.CAFile, CertFile: pki.Server.CertFile, KeyFile: pki.Server.KeyFile},
		Nodes:          nodes, Sessions: gliNewSessions(), Leader: gliNewLeader(true), Clock: clock,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil || srv == nil {
		t.Fatalf("ingest.NewServer: %v", err)
	}
	return &gliFxManual{srv: srv, pki: pki, clock: clock}
}

func (m *gliFxManual) serve(t *testing.T) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	m.addr = lis.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	go func() {
		m.err = m.srv.Serve(ctx, lis)
		close(m.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-m.done:
		case <-time.After(20 * time.Second):
		}
	})
}

// GLI-075 (b)/(f), GLI-111: BundleSource is valid before Serve (no observation,
// no panic); a view committed while serving stays readable after Serve returned
// (until NodeRetention); the stream is ended Unavailable by the shutdown.
func TestGLI075_BundleSourceBeforeAndAfterServe(t *testing.T) {
	m := gliFxNewManual(t)
	src := m.srv.BundleSource()
	pol := gliFxPolicy(t, gliFxFresh)
	in := gliFxDefaultIntent(t)
	node := fleet.NodeRef{ClusterID: gliFxCluster, Name: gliFxNodeName, UID: gliFxNodeUID}
	q := app.LiveBundleQuery{Node: node, Policy: pol, Intents: []fleet.Intent{in}, Now: gliFxAt(0).Add(time.Second)}
	if set, err := src.NodeBundles(context.Background(), q); !errors.Is(err, app.ErrNoObservation) || len(set.Devices) != 0 {
		t.Fatalf("GLI-111: BundleSource before Serve: %d devices, %v; want ErrNoObservation", len(set.Devices), err)
	}
	m.serve(t)
	cert := m.pki.NodeCert(t, gliFxCluster, gliFxNodeUID)
	conn := gliFxDial(t, &gliServer{Addr: m.addr, PKI: m.pki}, cert)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := ingestpb.NewIngestClient(conn).Stream(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if err := st.Send(&ingestpb.ClientFrame{Frame: &ingestpb.ClientFrame_Hello{Hello: &ingestpb.ClientHello{
		Version: "v1alpha1", ClusterId: gliFxCluster, NodeName: gliFxNodeName, NodeUid: gliFxNodeUID, BootId: gliFxBootID,
	}}}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	sf, err := st.Recv()
	if err != nil || sf.GetHello() == nil {
		t.Fatalf("hello: %v / %v", sf, err)
	}
	session := sf.GetHello().GetSession()
	at := gliFxAt(0)
	f := gliFrame(t, gliPayload(gliFxNodeUID, gliFxBootID, session, 0, at), session, 0, at, true)
	m.clock.Advance(gliFxStep)
	if err := st.Send(gliFxSnapshotFrame(f)); err != nil {
		t.Fatalf("send frame: %v", err)
	}
	if sf, err = st.Recv(); err != nil || sf.GetAck().GetCode() != ingestpb.AckCode_ACCEPTED {
		t.Fatalf("baseline: %v / %v", sf, err)
	}
	before, err := src.NodeBundles(context.Background(), q)
	if err != nil || len(before.Devices) != 1 {
		t.Fatalf("while serving: %d devices, %v", len(before.Devices), err)
	}
	m.cancel()
	select {
	case <-m.done:
		if m.err != nil {
			t.Fatalf("GLI-111: Serve returned %v after cancellation, want nil", m.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("GLI-111: Serve did not return within 15 s of the cancellation")
	}
	if _, err := st.Recv(); status.Code(err) != codes.Unavailable {
		t.Errorf("GLI-100: the stream ended with %v at shutdown, want Unavailable", err)
	}
	after, err := src.NodeBundles(context.Background(), q)
	if err != nil || len(after.Devices) != 1 {
		t.Fatalf("GLI-075 (f): after Serve returned: %d devices, %v; the view must stay until NodeRetention", len(after.Devices), err)
	}
	if gliFxSig(after) != gliFxSig(before) {
		t.Errorf("GLI-075 (f): the view changed when Serve ended")
	}
}

// L-STALE, GLI-076, GLI-053: when the agent stops, the last view stays and is
// judged stale by S1 (never ErrNoObservation) until NodeRetention (fake clock)
// has passed since the last accepted frame; the discard needs no frame or hello
// to be noticed; the node can start again afterwards.
func TestGLI053_LSTALEStaleViewThenRetentionExpiry(t *testing.T) {
	env := gliFxStart(t, func(c *ingest.Config) { c.Limits.NodeRetention = time.Hour })
	c := env.ConnectDefault()
	srcs := ingest.DefaultTrustedSources()
	var frames []*ingestpb.SnapshotFrame
	for seq := uint64(0); seq < 3; seq++ {
		frames = append(frames, c.AcceptSeq(seq, gliFxAt(seq), true))
	}
	pol := gliFxPolicy(t, gliFxFresh)
	in := gliFxDefaultIntent(t)
	stale := gliFxAt(2).Add(10 * time.Minute) // far beyond the freshness of 60 s
	set, err := env.Query(stale, pol, in)
	if err != nil {
		t.Fatalf("L-STALE: a view older than the freshness must still be returned: %v", err)
	}
	gliFxCheckBundle(t, set, frames, c.Session, pol, []fleet.Intent{in}, stale, srcs, "L-STALE")
	dec := gliFxWantSameDecision(t, set.Devices[0].Bundle, frames, c.Session, srcs, stale, "L-STALE")
	if dec.Phase == fleet.PhaseReady || dec.Qualification == fleet.QualificationQualified {
		t.Errorf("L-STALE: stale evidence must not be Ready/Qualified; got %s/%s", dec.Phase, dec.Qualification)
	}
	env.S.Clock.Advance(time.Hour - time.Second)
	if _, err := env.Query(stale, pol, in); err != nil {
		t.Fatalf("GLI-053: one second before NodeRetention the view must still be there: %v", err)
	}
	env.S.Clock.Advance(2 * time.Second)
	env.WantNoObservation("GLI-053 after NodeRetention (lazy discard on NodeBundles)")
	c2 := env.ConnectDefault()
	env.WantNoObservation("after the hello that follows the discard")
	c2.AcceptSeq(0, gliFxAt(0), true)
	if _, err := env.Query(gliFxAt(0).Add(time.Second), pol, in); err != nil {
		t.Fatalf("a new session after the discard must work: %v", err)
	}
}

// L-RESTART, GLI-031/035: a new controller process (new server, the same
// persisted session store) gets a larger session on the next hello, passes
// after=0 because it has issued nothing yet, has no observation until the new
// baseline, and a process that stays up passes the largest session it issued as
// after.
func TestGLI031_LRESTARTNewProcessSameStore(t *testing.T) {
	first := gliFxStart(t)
	c1 := first.ConnectDefault()
	c1.AcceptSeq(0, gliFxAt(0), true)
	s1 := c1.Session
	c1b := first.ConnectDefault()
	calls := first.S.Sessions.Calls()
	if len(calls) != 2 || calls[0].After != 0 || calls[1].After != s1 || calls[1].Result <= s1 {
		t.Fatalf("GLI-031: SessionStore calls of one process = %+v; want after 0 then after %d with a larger result", calls, s1)
	}
	second := gliFxStart(t, func(c *ingest.Config) { c.Sessions = first.S.Sessions })
	c2 := second.ConnectDefault()
	if c2.Session <= c1b.Session {
		t.Fatalf("L-RESTART: the new process issued session %d, want > %d", c2.Session, c1b.Session)
	}
	calls = first.S.Sessions.Calls()
	if last := calls[len(calls)-1]; last.After != 0 || last.Result != c2.Session {
		t.Errorf("GLI-031: the new process called AllocateSession with after=%d (result %d), want after=0 (nothing issued yet)", last.After, last.Result)
	}
	second.WantNoObservation("L-RESTART before the new baseline")
	f := c2.AcceptSeq(0, gliFxAt(0), true)
	b := second.One(gliFxAt(0).Add(time.Second), gliFxFresh)
	if b.Bundle.Snapshot.Session != c2.Session || b.Bundle.Snapshot.BundleRevision != f.GetBundleRevision() {
		t.Errorf("L-RESTART: baseline of the new process %q session %d", b.Bundle.Snapshot.BundleRevision, b.Bundle.Snapshot.Session)
	}
	if first.S.Sessions.Stored(gliFxNodeUID) != c2.Session {
		t.Errorf("GLI-031: stored session %d, want %d", first.S.Sessions.Stored(gliFxNodeUID), c2.Session)
	}
}

// L-FIXTURE-NOT-LIVE, GLI-073, GLI-111: the profile of a live ingest comes from
// the controller configuration only. An offline: profile ID, the reserved stamp
// and malformed IDs are configuration errors (no server); and a session known
// from an offline artifact cannot be injected: only a session issued by the
// store is accepted.
func TestGLI111_LFIXTURENOTLIVEProfileComesFromTheControllerOnly(t *testing.T) {
	pki := gliNewPKI(t)
	mk := func(profile string) ingest.Config {
		return ingest.Config{
			ClusterID: gliFxCluster, ServiceDNS: "localhost", CollectorProfileID: profile,
			TrustedSources: ingest.DefaultTrustedSources(),
			TLS:            ingest.TLSFiles{CAFile: pki.CAFile, CertFile: pki.Server.CertFile, KeyFile: pki.Server.KeyFile},
			Nodes:          gliNewNodes(), Sessions: gliNewSessions(),
		}
	}
	for _, id := range []string{"offline:lab-a-basic", "offline:", "offline:x", "unmatched-source", "", "live default", "live:é", strings.Repeat("a", 129)} {
		srv, err := ingest.NewServer(mk(id))
		if !errors.Is(err, ingest.ErrInvalidConfig) || srv != nil {
			t.Errorf("CollectorProfileID %q: NewServer = %v, %v; want ErrInvalidConfig and a nil server", id, srv, err)
		}
	}
	for _, id := range []string{"live:default", "a", strings.Repeat("a", 128), "offline", "Offline:x", "unmatched-source-2", "live:parity"} {
		srv, err := ingest.NewServer(mk(id))
		if err != nil || srv == nil {
			t.Errorf("CollectorProfileID %q: NewServer = %v, %v; want a server", id, srv, err)
		}
	}
	env := gliFxStart(t)
	c := env.ConnectDefault()
	if c.Hello.GetCollectorProfileId() != gliFxProfile {
		t.Errorf("ServerHello profile %q, want the configured %q", c.Hello.GetCollectorProfileId(), gliFxProfile)
	}
	offlineSession := int64(7) // the session of every GFX-102 fixture
	if c.Session == offlineSession {
		offlineSession = 8
	}
	f := c.Frame(0, gliFxAt(0), true)
	f.Session = offlineSession
	gliReseal(t, f)
	c.Reject(f, ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition, "L-FIXTURE-NOT-LIVE: an artifact session is not a live session")
	env.WantNoObservation("L-FIXTURE-NOT-LIVE")
}

// GLI-072 (a): the ring also holds at most 8192 observations. Frames
// of 4096 observations (the per-frame limit): two of them (8192, exactly the cap)
// stay together; the third frame's acceptance drops the first. A small fourth
// frame (6 observations) makes the total 8198 and drops the second as well. The
// last frame always stays. Every observation of a frame has a series of its own,
// which keeps the test at the cost the cap exists to bound.
func TestGLI072_RingObservationCap(t *testing.T) {
	if testing.Short() {
		t.Skip("8192 distinct series; skipped with -short")
	}
	env := gliFxStart(t)
	c := env.ConnectDefault()
	srcs := ingest.DefaultTrustedSources()
	pol := gliFxPolicy(t, 600*time.Second)
	in := gliFxDefaultIntent(t)
	var frames []*ingestpb.SnapshotFrame
	domain := func(k int) string { return fmt.Sprintf("pci-bdf:%04x:", 0x100+k) }
	// fillers per frame: 4090 + the 6 BASE observations = 4096 for frames 0..2; frame 3 is BASE only.
	size := []int{4096, 4096, 4096, 6}
	for k, total := range size {
		seq := uint64(k)
		at := gliFxAt(seq)
		p := gliPayload(c.UID, c.Boot, c.Session, seq, at)
		for i := 0; len(p.Observations) < total; i++ {
			o := gliFxObservation(c.Session, seq, at, "PCIeFunction", "pci-bdf:"+gliFxBDF(0x100+k, i), "gli.pad", gliFxWidthSrc, gliFxInt(1), "n", nil)
			o.ExpiresAt = gliFxTS(at.Add(gliFxTTL))
			p.Observations = append(p.Observations, o)
		}
		gliFxSortPayload(p)
		frames = append(frames, gliFxAcceptPayload(t, c, p, seq, at, true))
		if k == 0 {
			continue
		}
		now := at.Add(time.Second)
		set, err := env.Query(now, pol, in)
		if err != nil {
			t.Fatalf("frame %d: %v", k, err)
		}
		if k >= 2 { // full comparison where the cap acts (each window build costs seconds)
			gliFxCheckBundle(t, set, frames, c.Session, pol, []fleet.Intent{in}, now, srcs, fmt.Sprintf("GLI-072 cap after frame %d", k))
		}
		present := make([]bool, 3)
		for _, sk := range gliOrSubjects(set.Devices[0].Bundle.Window) {
			for f := range present {
				present[f] = present[f] || strings.Contains(sk, domain(f))
			}
		}
		want := map[int][]bool{
			1: {true, true, false},  // 4096 + 4096 = 8192: exactly the cap, everything stays
			2: {false, true, true},  // 12288 > 8192: the first frame leaves
			3: {false, false, true}, // 4096 + 4096 + 6 = 8198 > 8192: the second leaves too
		}[k]
		for f := range present {
			if present[f] != want[f] {
				t.Errorf("GLI-072 (a) after frame %d: observations of frame %d in the window = %t, want %t (ring cap 8192)", k, f, present[f], want[f])
			}
		}
		if got := set.Devices[0].Bundle.Snapshot.Sequence; got != seq {
			t.Errorf("after frame %d the bundle is the view of sequence %d (the last frame always stays)", k, got)
		}
	}
}

// GLI-072 (a): the observation total is compared with 8192 exactly. With three
// frames whose totals are 8192, 8193 and 8197 the first stays only when the total
// is exactly 8192; above it the oldest frame leaves while the others stay.
func TestGLI072_RingObservationTotalBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("8192 distinct series; skipped with -short")
	}
	base := gliFxStart(t)
	cases := []struct {
		name  string
		sizes []int // observations per frame (each at most 4096, the BASE part is 6)
		first bool  // the first frame stays in the window
	}{
		{"total exactly 8192 keeps every frame", []int{4096, 4090, 6}, true},
		{"total 8193 drops the oldest frame", []int{4096, 4091, 6}, false},
		{"total 8197 drops the oldest frame", []int{4096, 4095, 6}, false},
	}
	for ci, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			domain := func(k int) string { return fmt.Sprintf("pci-bdf:%04x:", 0x300+4*ci+k) }
			for k, total := range tc.sizes {
				seq := uint64(k)
				at := gliFxAt(seq)
				p := gliPayload(c.UID, c.Boot, c.Session, seq, at)
				for i := 0; len(p.Observations) < total; i++ {
					o := gliFxObservation(c.Session, seq, at, "PCIeFunction", "pci-bdf:"+gliFxBDF(0x300+4*ci+k, i), "gli.pad", gliFxWidthSrc, gliFxInt(1), "n", nil)
					o.ExpiresAt = gliFxTS(at.Add(gliFxTTL))
					p.Observations = append(p.Observations, o)
				}
				gliFxSortPayload(p)
				gliFxAcceptPayload(t, c, p, seq, at, true)
			}
			last := uint64(len(tc.sizes) - 1)
			b := env.One(gliFxAt(last).Add(time.Second), 600*time.Second)
			if b.Bundle.Snapshot.Sequence != last {
				t.Fatalf("the bundle is the view of sequence %d, want %d", b.Bundle.Snapshot.Sequence, last)
			}
			var first, second bool
			for _, s := range gliOrSubjects(b.Bundle.Window) {
				first = first || strings.Contains(s, domain(0))
				second = second || strings.Contains(s, domain(1))
			}
			if first != tc.first || !second {
				t.Errorf("GLI-072 (a): observations of the first frame in the window = %t (want %t), of the second = %t (want true); totals %v", first, tc.first, second, tc.sizes)
			}
		})
	}
}

// GLI-074: every record Summary is ASCII 1-512 and longer text is cut to 512
// bytes. A width observation whose summary "<signal>=<value> <unit>" is 511, 512,
// 513 and the longest possible 539 bytes (unit of 512 bytes) must come back
// whole up to 512 and as its first 512 bytes beyond.
func TestGLI074_RecordSummaryIsCutAtFiveHundredTwelveBytes(t *testing.T) {
	base := gliFxStart(t)
	const prefix = "pcie.link.width.current=16 "
	for _, want := range []int{511, 512, 513, 539} {
		t.Run(fmt.Sprintf("summary of %d bytes", want), func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			at := gliFxAt(0)
			p := gliPayload(c.UID, c.Boot, c.Session, 0, at)
			unit := strings.Repeat("u", want-len(prefix))
			o := gliFxObservation(c.Session, 0, at, "PCIeFunction", "pci-bdf:0000:0b:00.0", gliFxSigCurrent, gliFxWidthSrc, gliFxInt(16), unit, nil)
			o.ExpiresAt = gliFxTS(at.Add(gliFxTTL))
			p.Observations = append(p.Observations, o)
			gliFxSortPayload(p)
			gliFxAcceptPayload(t, c, p, 0, at, true)
			b := env.One(at.Add(time.Second), 600*time.Second)
			var rec *app.EvidenceRecord
			for i := range b.Evidence {
				if b.Evidence[i].ID == o.Id {
					rec = &b.Evidence[i]
				}
			}
			if rec == nil {
				t.Fatalf("GLI-074: no evidence record for the width observation %q", o.Id)
			}
			full := prefix + unit
			exp := full
			if len(exp) > 512 {
				exp = full[:512]
			}
			if rec.Summary != exp {
				t.Errorf("GLI-074: summary is %d bytes (%.40q...), want %d bytes: the full text up to 512 bytes, else its first 512", len(rec.Summary), rec.Summary, len(exp))
			}
		})
	}
}
