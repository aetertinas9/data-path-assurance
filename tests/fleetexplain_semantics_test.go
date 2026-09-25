package tests_test

// Further evaluation and model edge cases: the untrusted-source limitation
// judged before truncation and with a PARTIAL result frame (GFX-064), the
// per-device minimum depth of the node segment union (GFX-063), the stale
// boundary of the cumulative window (GFX-041, GFX-042), the BaselineDigest
// value encoding and peer dimension (GFX-045) and the inputs TopologyDigest
// ignores (GFX-044).

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

type gfxSelFinding struct{ ID, Fn string }

// gfxSelectedFindings returns the R findings whose id is in some device's
// FindingIDs (every device of the fleet file), ordered by id, with the
// PCIeFunction key of each finding's scope.
func gfxSelectedFindings(rp *gfxReplay) []gfxSelFinding {
	sel := map[string]bool{}
	if len(rp.Cursors) > 0 {
		for _, d := range rp.Dec {
			for _, id := range d.FindingIDs {
				sel[id] = true
			}
		}
	}
	var out []gfxSelFinding
	for _, f := range rp.Frames[rp.R].Findings {
		if !sel[f.ID] {
			continue
		}
		x := gfxSelFinding{ID: f.ID}
		for _, s := range f.Scope {
			if strings.HasPrefix(s.Key(), "PCIeFunction/") {
				x.Fn = s.Key()
				break
			}
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// TestGFX064_UntrustedFindingBeyondBound: with 65 selected findings, only
// the one cut by the activeFindings bound (64) comes from a width source
// missing from the trust list; finding_source_untrusted is still reported
// because the judgement covers the whole list before truncation.
func TestGFX064_UntrustedFindingBeyondBound(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	holdHeavyFixture(t)
	f, fl := gfxManyGPUs(t, 65, 3, "8")
	a := gfxAgentArtifact(t, bins.Agent, f)
	const cl = "GFX-064 untrusted finding cut by the activeFindings bound"
	rp, err := gfxReplayOf(a, fl)
	if err != nil {
		t.Fatalf("%s: GFX-101 oracle could not replay the inputs: %v", cl, err)
	}
	sel := gfxSelectedFindings(rp)
	if len(sel) != 65 || sel[64].Fn == "" {
		t.Fatalf("%s: fixture precondition: the oracle selects %d findings, want 65 with a function scope", cl, len(sel))
	}
	target := sel[64]

	// Move the target function's width pair to an unlisted source in every
	// frame (the finding id does not depend on the source, PCIE-019).
	x := gfxClone(t, a)
	moved := 0
	for i := range x.Frames {
		p := &x.Frames[i].Payload
		for k := range p.Observations {
			o := &p.Observations[k]
			if o.Subject.Key() == target.Fn && (o.Signal == gfoSigCurrent || o.Signal == gfoSigExpect) {
				o.SourceName = "path-agent/sysfs-width-unlisted"
				moved++
			}
		}
		gfxSortPayload(p)
	}
	gfxSeal(t, x, true)
	if moved != 6 {
		t.Fatalf("fixture setup: moved %d width observations of %s, want 6", moved, target.Fn)
	}
	c := gfxNewCase(t, bins, x, fl)
	rp2, err := c.Oracle()
	if err != nil {
		t.Fatalf("%s: GFX-101 oracle could not replay the edited inputs: %v", cl, err)
	}
	sel2 := gfxSelectedFindings(rp2)
	pos := -1
	for i, s := range sel2 {
		if s.Fn == target.Fn {
			pos = i
		}
	}
	if len(sel2) != 65 || pos < 64 {
		t.Fatalf("%s: fixture precondition: after the edit the oracle selects %d findings with the target at %d, want 65 with the target cut (index 64)", cl, len(sel2), pos)
	}
	id := sel2[pos].ID

	n := c.Check("node", gfoNodeName, cl)
	gfxWant(t, cl, "activeFindings len/total/truncated", fmt.Sprintf("%d/%d/%v", len(n.Findings), n.Totals["activeFindings"], n.Truncated), "64/65/true")
	for _, fo := range n.Findings {
		if fo.ID == id {
			t.Errorf("%s: GFX-068: finding %s sorts last by id and must be cut", cl, id)
		}
	}
	gfxWantLims(t, cl, n, []string{"finding_source_untrusted " + id}, nil)
	gfxWant(t, cl, "finding_source_untrusted count", n.CountLim("finding_source_untrusted"), 1)

	dev := ""
	for i, d := range rp2.Dec {
		if d.BindingState == fleet.BindingBound && d.Binding.Function.Key() == target.Fn {
			dev = fl.Devices[i].Name
		}
	}
	if dev == "" {
		t.Fatalf("fixture setup: no device is bound to %s", target.Fn)
	}
	g := c.Check("gpu", dev, cl+" gpu "+dev)
	gfxWant(t, cl+" gpu", "activeFindings", len(g.Findings), 1)
	gfxWantLims(t, cl+" gpu", g, []string{"finding_source_untrusted " + id}, nil)
}

// TestGFX064_PartialResultFrameFindings: R is a PARTIAL frame after three
// degraded COMPLETE frames, so W_R holds an active finding built from
// earlier frames. The gpu request shows no path and no finding_not_applied
// (the device is not Bound); the node request shows every R finding either
// as active or as finding_not_applied, never both and never neither.
func TestGFX064_PartialResultFrameFindings(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	for _, x := range []struct {
		name     string
		keepPair bool
	}{
		{"frame 3 PARTIAL with the GPU width pair", true},
		{"frame 3 PARTIAL without the GPU width pair", false},
	} {
		cl := "GFX-064 " + x.name
		f := gfxSWidth(t)
		i := gfxAddFrame(f, "2026-09-24T00:00:45Z")
		f.Write(gfoSys(i)+"/bus/pci/devices/.gitkeep", "")
		if x.keepPair {
			f.Write(gfxAttr(i, gfoPathGPU, "current_link_width"), "8\n")
		} else {
			f.Remove(gfxAttr(i, gfoPathGPU, "current_link_width"))
		}
		a := gfxAgentArtifact(t, bins.Agent, f)
		if a.Frames[3].Completeness != "PARTIAL" || a.Frames[0].Completeness != "COMPLETE" {
			t.Fatalf("fixture setup: %s: frame 0/3 completeness %s/%s, want COMPLETE/PARTIAL", cl, a.Frames[0].Completeness, a.Frames[3].Completeness)
		}
		c := gfxNewCase(t, bins, a, gfxDefaultFleet())
		rp, err := c.Oracle()
		if err != nil {
			t.Fatalf("%s: GFX-101 oracle could not replay the inputs: %v", cl, err)
		}
		if rp.R != 3 || len(rp.Frames[3].Findings) == 0 {
			t.Fatalf("%s: fixture precondition: oracle R = %d with %d findings in W_R, want R = 3 with at least one", cl, rp.R, len(rp.Frames[rp.R].Findings))
		}

		g := c.Check("gpu", gfxDevName, cl+" gpu")
		gfxWant(t, cl+" GFX-062", "identity", g.Identity["state"]+"/"+g.Identity["reason"], "Unknown/Validating")
		gfxWant(t, cl, "graphRevision", g.GraphRevision, a.Frames[3].BundleRevision)
		gfxWantSegs(t, cl+" GFX-063", g)
		gfxWantLims(t, cl, g, []string{"frame_partial 3"}, []string{"finding_not_applied"})

		n := c.Check("node", gfoNodeName, cl+" node")
		gfxWantLims(t, cl+" node", n, []string{"frame_partial 3"}, nil)
		for _, rf := range rp.Frames[3].Findings {
			active := false
			for _, fo := range n.Findings {
				if fo.ID == rf.ID {
					active = true
				}
			}
			if na := n.HasLim("finding_not_applied", rf.ID); active == na {
				t.Errorf("%s node: R finding %s is active=%v and finding_not_applied=%v, want exactly one", cl, rf.ID, active, na)
			}
		}
	}
}

// TestGFX063_NodeUnionMinimumDepth: GPU A (under the switch downstream port)
// and GPU B (under the switch upstream port) reach the same edges at
// different hop counts; the node union orders segments by the smaller depth.
func TestGFX063_NodeUnionMinimumDepth(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	const bdfB = "0000:02:00.0"
	uuidB := gfxUUIDn(0xb)
	f := gfxSBasic(t)
	f.Dev(0, gfxPCI([]string{gfoHB, gfoRPA, gfoSWU, bdfB}, "0x030200", "0x10de", "16", "16"))
	f.Write(gfoCSV(0), gfxUUID+", 00000000:03:00.0\n"+uuidB+", 00000000:02:00.0\n")
	fl := gfxDefaultFleet()
	d := gfxDefaultDevice()
	d.Name, d.UID, d.UUID, d.RequestID, d.EvidenceID = "gpu-node-1-gpu1", "9d0e4f6a-1b2c-4d3e-8f4a-5b6c7d8e9f01", uuidB, "enroll-2", "asset-db:rack7-u12-gpu1"
	fl.Devices = append(fl.Devices, d)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), fl)
	kB := gfoFnKey(bdfB)
	const cl = "GFX-063 node union depth"

	ga := c.Check("gpu", gfxDevName, cl+" gpu A")
	gfxWant(t, cl+" gpu A", "identity.state", ga.Identity["state"], "Bound")
	gfxWantSegs(t, cl+" gpu A", ga, gfxBaseSegments...)
	gb := c.Check("gpu", d.Name, cl+" gpu B")
	gfxWant(t, cl+" gpu B", "identity.state", gb.Identity["state"], "Bound")
	gfxWantSegs(t, cl+" gpu B", gb, gfxEdgeStr(kB, gfxKSWU), gfxEdgeStr(gfxKSWU, gfxKRPA), gfxEdgeStr(gfxKRPA, gfxKNode))
	// Depths in the union: B->SWU 0, A->SWD 0, SWU->RPA min(2,1)=1,
	// SWD->SWU 1, RPA->node min(3,2)=2; ties ordered by From key.
	n := c.Check("node", gfoNodeName, cl+" node")
	gfxWantSegs(t, cl+" node", n,
		gfxEdgeStr(kB, gfxKSWU), gfxEdgeStr(gfxKGPU, gfxKSWD),
		gfxEdgeStr(gfxKSWU, gfxKRPA), gfxEdgeStr(gfxKSWD, gfxKSWU),
		gfxEdgeStr(gfxKRPA, gfxKNode))
}

// TestGFX042_StaleBoundary: the frame 1 width pair is the head at frame 2;
// with Freshness 60 s it is stale when t_2 = head.observedAt + Freshness and
// fresh 1 ns earlier (GFX-041, GFL-013, PCIE-013).
func TestGFX042_StaleBoundary(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	for _, x := range []struct{ at, state string }{
		{"2026-09-24T00:01:15Z", "Unknown"},
		{"2026-09-24T00:01:14.999999999Z", "Normal"},
	} {
		cl := "GFX-042 frame 2 at " + x.at + " (head at 00:00:15Z, freshness 60s)"
		f := gfxSReady(t)
		f.M.Frames = []string{"2026-09-24T00:00:00Z", "2026-09-24T00:00:15Z", x.at}
		f.Remove(gfxAttr(2, gfoPathGPU, "current_link_width"))
		c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet())
		e := c.Check("gpu", gfxDevName, cl)
		cv, ok := e.Cov("pcie-width")
		if !ok {
			t.Errorf("%s: coverage pcie-width missing", cl)
			continue
		}
		gfxWant(t, cl, "pcie-width state", cv.State, x.state)
		gfxWantLims(t, cl, e, []string{"width_pair_absent " + gfxKGPU}, nil)
	}
}

// gfxVal is an observation value: the single key of the value object and
// the compact JSON text of its member (GFO-082).
type gfxVal struct{ Kind, JSON string }

func gfxIntVal(decimal string) gfxVal { return gfxVal{"int", `"` + decimal + `"`} }

func gfxFloatVal(v float64) gfxVal {
	return gfxVal{"float", `"` + strconv.FormatFloat(v, 'g', -1, 64) + `"`}
}

// gfxNICExpectedAs rewrites the NIC expected-width value of the given frames
// of a copy of a (each originally the Int 16) and reseals it.
func gfxNICExpectedAs(t *testing.T, a *gfoArtifact, values map[int]gfxVal) *gfoArtifact {
	t.Helper()
	c := gfxClone(t, a)
	for i, v := range values {
		o := &c.Frames[i].Payload.Observations[gfxObsIndex(t, c, i, gfxKNIC, gfoSigExpect)]
		if o.ValueKind != "int" || o.ValueJSON != `"16"` {
			t.Fatalf("fixture setup: frame %d NIC expected value is %s %s, want int 16", i, o.ValueKind, o.ValueJSON)
		}
		o.ValueKind, o.ValueJSON = v.Kind, v.JSON
	}
	gfxSeal(t, c, true)
	return c
}

// gfxNICExpected rewrites the NIC expected-width value of the given frames
// of a copy of a as the given float text and reseals it.
func gfxNICExpected(t *testing.T, a *gfoArtifact, values map[int]string) *gfoArtifact {
	t.Helper()
	vs := map[int]gfxVal{}
	for i, v := range values {
		vs[i] = gfxVal{"float", `"` + v + `"`}
	}
	return gfxNICExpectedAs(t, a, vs)
}

// TestGFX045_BaselineValueEncoding: an unrelated function's expected value
// feeds BaselineDigest. A Float equal to the Int is the same baseline; a
// non-integral Float and Floats outside the int64 range use the GFO-086
// value encoding (else branch), so a change among them restarts the ready
// window even where a conversion to int64 would give equal values.
func TestGFX045_BaselineValueEncoding(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	ready := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	ft := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	for _, x := range []struct {
		name   string
		values map[int]string
		phase  string
	}{
		{"Float 16 in frames 1..2 (same integer)", map[int]string{1: "16", 2: "16"}, "Ready"},
		{"Float 16.5 in frames 1..2 (non-integral)", map[int]string{1: ft(16.5), 2: ft(16.5)}, "Validating"},
		{"Float -1e+19 in every frame (outside int64, unchanged)", map[int]string{0: ft(-1e19), 1: ft(-1e19), 2: ft(-1e19)}, "Ready"},
		{"Float -1e+19 then -2e+19 (outside int64, changed at frame 1)", map[int]string{0: ft(-1e19), 1: ft(-2e19), 2: ft(-2e19)}, "Validating"},
	} {
		cl := "GFX-045 NIC expected " + x.name
		e := gfxNewCase(t, bins, gfxNICExpected(t, ready, x.values), gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
		gfxWant(t, cl, "phase", e.Phase, x.phase)
	}

	// Only the peer dimension of the NIC width pair changes from frame 1.
	peer := gfxClone(t, ready)
	for i := 1; i <= 2; i++ {
		for _, sig := range []string{gfoSigCurrent, gfoSigExpect} {
			o := &peer.Frames[i].Payload.Observations[gfxObsIndex(t, peer, i, gfxKNIC, sig)]
			if o.Dim("pcie.peer.canonical") != "pci-bdf:"+gfoRPB {
				t.Fatalf("fixture setup: frame %d NIC %s peer is %q", i, sig, o.Dim("pcie.peer.canonical"))
			}
			gfxSetDim(o, "pcie.peer.canonical", "pci-bdf:0000:00:03.0")
		}
		gfxSortPayload(&peer.Frames[i].Payload)
	}
	gfxSeal(t, peer, true)
	const pc = "GFX-045 NIC peer dimension changed at frame 1"
	gfxWant(t, pc, "phase", gfxNewCase(t, bins, peer, gfxDefaultFleet()).Check("gpu", gfxDevName, pc).Phase, "Validating")
}

// TestGFX045_BaselineValueBoundaries: the integer branch of the value
// encoding covers exactly the int64 range (-2^63 inclusive, 2^63 exclusive),
// and String and Bool expected values keep their own GFO-086 encoding, so a
// change between them and an Int restarts the ready window while the same
// String or Bool in every frame does not.
func TestGFX045_BaselineValueBoundaries(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	ready := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	str := gfxVal{"string", gfoQuoteOut("16")}
	yes := gfxVal{"bool", "true"}
	for _, x := range []struct {
		name   string
		values map[int]gfxVal
		phase  string
	}{
		{"Int -2^63 then Float -2^63 (inside int64)", map[int]gfxVal{0: gfxIntVal("-9223372036854775808"), 1: gfxFloatVal(-0x1p63), 2: gfxFloatVal(-0x1p63)}, "Ready"},
		{"Int 2^63-1 then Float 2^63 (outside int64)", map[int]gfxVal{0: gfxIntVal("9223372036854775807"), 1: gfxFloatVal(0x1p63), 2: gfxFloatVal(0x1p63)}, "Validating"},
		{"String \"16\" in every frame", map[int]gfxVal{0: str, 1: str, 2: str}, "Ready"},
		{"Int 16 then String \"16\"", map[int]gfxVal{1: str, 2: str}, "Validating"},
		{"Bool true in every frame", map[int]gfxVal{0: yes, 1: yes, 2: yes}, "Ready"},
		{"Int 1 then Bool true", map[int]gfxVal{0: gfxIntVal("1"), 1: yes, 2: yes}, "Validating"},
	} {
		cl := "GFX-045 NIC expected " + x.name
		e := gfxNewCase(t, bins, gfxNICExpectedAs(t, ready, x.values), gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
		gfxWant(t, cl, "phase", e.Phase, x.phase)
	}
}

// TestGFX064_ConditionIIOnlyForOperatorBaseline: condition (ii) of
// finding_source_untrusted concerns operator_verified_wiring evidence only.
// With adjacent_capability_min baselines, a trusted width source and no
// OperatorBaseline trust entry, the degraded finding is not marked.
func TestGFX064_ConditionIIOnlyForOperatorBaseline(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxSWidth(t)
	f.M.Baselines = nil
	gfxWithoutSources(f, "OperatorBaseline")
	a := gfxAgentArtifact(t, bins.Agent, f)
	for i := range a.Frames {
		for _, sig := range []string{gfoSigCurrent, gfoSigExpect} {
			o := a.Frames[i].Payload.Observations[gfxObsIndex(t, a, i, gfxKGPU, sig)]
			if o.Dim("pcie.expected.provenance") != gfoProvAdj || o.SourceName != gfoWidthSrc {
				t.Fatalf("fixture setup: frame %d GPU %s has provenance %q and source %q, want %s from %s", i, sig, o.Dim("pcie.expected.provenance"), o.SourceName, gfoProvAdj, gfoWidthSrc)
			}
		}
	}
	const cl = "GFX-064 condition (ii) with adjacent_capability_min and no OperatorBaseline trust"
	e := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
	if len(e.Findings) != 1 {
		t.Fatalf("%s: fixture precondition: %d active findings, want the degraded width finding", cl, len(e.Findings))
	}
	gfxWantLims(t, cl, e, nil, []string{"finding_source_untrusted"})
}

// TestGFX044_AliasAndProvenanceExpiryNotInputs: frames that differ only in
// asset aliases or in edge provenance expiresAt keep TopologyDigest, so the
// ready window is not restarted (the payload digests do differ).
func TestGFX044_AliasAndProvenanceExpiryNotInputs(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	ready := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	alias := func(c *gfoArtifact) {
		p := &c.Frames[1].Payload
		for k := range p.Assets {
			if key := p.Assets[k].Key(); key == gfxKRPB || key == gfxKNIC {
				p.Assets[k].Aliases = []gfoAlias{{"test-ns", "alias-1"}}
			}
		}
	}
	expiry := func(c *gfoArtifact) {
		p := &c.Frames[1].Payload
		for k := range p.EdgeEvidence {
			p.EdgeEvidence[k].ExpiresAt = gfoAddSeconds(t, c.Frames[1].ObservedAt, 120)
		}
	}
	for _, x := range []struct {
		name  string
		edits []func(*gfoArtifact)
	}{
		{"aliases only", []func(*gfoArtifact){alias}},
		{"provenance expiresAt only", []func(*gfoArtifact){expiry}},
		{"aliases and provenance expiresAt", []func(*gfoArtifact){alias, expiry}},
	} {
		cl := "GFX-044 frame 1 differs in " + x.name
		c := gfxClone(t, ready)
		for _, ed := range x.edits {
			ed(c)
		}
		gfxSeal(t, c, true)
		if c.Frames[1].PayloadDigest == ready.Frames[1].PayloadDigest {
			t.Fatalf("test bug: %s: the edit did not change the frame 1 payload", cl)
		}
		e := gfxNewCase(t, bins, c, gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
		gfxWant(t, cl, "phase/qualification", e.Phase+"/"+e.Qualification, "Ready/Qualified")
	}
}

// TestGFX063_HopTrustedOnlyUnderOtherCapability: condition (ii) of a trusted
// hop needs a SysfsPhysicalParent entry for the provenance source; a source
// listed only under other capabilities (here the width source) does not
// make the hop trusted, so it is no segment and path_untrusted is reported.
func TestGFX063_HopTrustedOnlyUnderOtherCapability(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	listed := false
	for _, s := range a.Trust.Sources {
		if s.Type == "agent" && s.Name == gfoWidthSrc {
			if s.Capability == "SysfsPhysicalParent" {
				t.Fatalf("fixture setup: %s is trusted as SysfsPhysicalParent", gfoWidthSrc)
			}
			listed = true
		}
	}
	if !listed {
		t.Fatalf("fixture setup: %s is not in the trust list", gfoWidthSrc)
	}
	p := &a.Frames[0].Payload
	moved := 0
	for k, ev := range p.EdgeEvidence {
		if p.Edges[ev.EdgeIndex].FromKey == gfxKSWU {
			p.EdgeEvidence[k].SourceName = gfoWidthSrc
			moved++
		}
	}
	if moved != 1 {
		t.Fatalf("fixture setup: %d edgeEvidence leave %s, want 1", moved, gfxKSWU)
	}
	gfxSeal(t, a, true)
	const cl = "GFX-063 hop source trusted only as SysfsPCIeWidth/OperatorBaseline"
	e := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
	gfxWantSegs(t, cl, e, gfxEdgeStr(gfxKGPU, gfxKSWD), gfxEdgeStr(gfxKSWD, gfxKSWU))
	gfxWantLims(t, cl, e, []string{"path_untrusted " + gfxKGPU}, nil)
}

// TestGFX080_LimitationByteOrder: with frames 2 and 10 of eleven PARTIAL
// (frame 0 COMPLETE), the frame_partial limitations follow the byte order
// of (code, subject): "10" sorts before "2".
func TestGFX080_LimitationByteOrder(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxBaseFixture(t, 11)
	for _, i := range []int{2, 10} {
		f.Write(gfoSys(i)+"/bus/pci/devices/.gitkeep", "")
	}
	a := gfxAgentArtifact(t, bins.Agent, f)
	for i, fr := range a.Frames {
		want := "COMPLETE"
		if i == 2 || i == 10 {
			want = "PARTIAL"
		}
		if fr.Completeness != want {
			t.Fatalf("fixture setup: frame %d is %s, want %s", i, fr.Completeness, want)
		}
	}
	const cl = "GFX-080/GFX-068 frame_partial subjects 2 and 10"
	e := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
	gfxWantLims(t, cl, e, []string{"frame_partial 2", "frame_partial 10"}, nil)
	at := map[string]int{}
	for i, l := range e.Limitations {
		at[l.String()] = i
	}
	i10, ok10 := at["frame_partial 10"]
	i2, ok2 := at["frame_partial 2"]
	if ok10 && ok2 && i10 >= i2 {
		t.Errorf("%s: frame_partial 10 at %d must come before frame_partial 2 at %d (byte order): %v", cl, i10, i2, e.Lims())
	}
}

// gfxTruncatedDiags returns a copy of a whose frame i is PARTIAL and carries
// 256 sysfs_list_failed diagnostics out of a total of 300 (truncated).
func gfxTruncatedDiags(t *testing.T, a *gfoArtifact, i int) *gfoArtifact {
	t.Helper()
	c := gfxClone(t, a)
	fr := &c.Frames[i]
	fr.Diagnostics = nil
	for k := 0; k < 256; k++ {
		fr.Diagnostics = append(fr.Diagnostics, gfoDiag{"sysfs_list_failed", fmt.Sprintf("bus/pci/devices/zz-%03d", k)})
	}
	fr.DiagnosticsTotal, fr.DiagnosticsTruncated = 300, true
	fr.Completeness = "PARTIAL"
	c.Raw = gfoRenderArtifact(c)
	return c
}

// TestGFX081_NonResultFrameDiagnostics: diagnostics of a frame other than R
// (here frame 1 of S-READY, truncated, with sysfs_list_failed) give no
// collector:*, nvidia_classification_unknown, diagnostics_truncated or
// diagnostics_outside_digest when R has no diagnostics; the same
// diagnostics on R give all of them.
func TestGFX081_NonResultFrameDiagnostics(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	ready := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	if len(ready.Frames[2].Diagnostics) != 0 {
		t.Fatalf("fixture setup: frame 2 has %d diagnostics, want 0", len(ready.Frames[2].Diagnostics))
	}
	derived := []string{"collector:sysfs_list_failed", "nvidia_classification_unknown", "diagnostics_truncated", "diagnostics_outside_digest"}
	const cl = "GFX-081 diagnostics only in frame 1"
	e := gfxNewCase(t, bins, gfxTruncatedDiags(t, ready, 1), gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
	gfxWantLims(t, cl, e, []string{"frame_partial 1"}, derived)
	for _, l := range e.Limitations {
		if strings.HasPrefix(l.Code, "collector:") {
			t.Errorf("%s: unexpected %s", cl, l)
		}
	}
	const rc = "GFX-081 the same diagnostics on R"
	r := gfxNewCase(t, bins, gfxTruncatedDiags(t, ready, 2), gfxDefaultFleet()).Check("gpu", gfxDevName, rc)
	gfxWantLims(t, rc, r, []string{"collector:sysfs_list_failed bus/pci/devices/zz-000", "nvidia_classification_unknown",
		"diagnostics_truncated 300", "diagnostics_outside_digest"}, nil)
}

// TestGFX035_BindingFunctionNotInPayload: the gpuBinding Function is a key
// conversion and needs no payload asset (GFX-035 requires payload assets
// for edge endpoints only), so an artifact whose binding names a function
// absent from the topology is accepted and explained.
func TestGFX035_BindingFunctionNotInPayload(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	const bdf = "0000:40:00.0"
	p := &a.Frames[0].Payload
	if len(p.GPUBindings) != 1 || p.HasAsset(gfoFnKey(bdf)) {
		t.Fatalf("fixture setup: %d bindings, asset %s present=%v", len(p.GPUBindings), gfoFnKey(bdf), p.HasAsset(gfoFnKey(bdf)))
	}
	p.GPUBindings[0].BDF = bdf
	gfxSeal(t, a, true)
	gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-035 binding function outside the payload assets")
}
