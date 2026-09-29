package tests_test

// Follow-up gap tests for gpu-fleet-offline-explain (F-01..F-10). Each test
// closes a gap through which a wrong implementation of the clause named in its
// title would still pass the earlier tests. Expected values come from the
// clause text (GFX v1.0, GFO v1.2); wherever the case reaches the evaluation
// the output is also compared with the independent oracle (GFX-101) through
// gfxCase.Check. Inputs are built under t.TempDir() (GFX-104).

import (
	"fmt"
	"testing"
)

// gfxFollowCovState asserts the state (only) of one coverage element.
func gfxFollowCovState(t *testing.T, clause string, e *gfxExpl, name, state string) {
	t.Helper()
	c, ok := e.Cov(name)
	if !ok {
		t.Errorf("%s: coverage %q missing (have %d elements)", clause, name, len(e.Coverage))
		return
	}
	if c.State != state {
		t.Errorf("%s: coverage %s state = %s (reason %s), want %s", clause, name, c.State, c.Reason, state)
	}
}

// gfxFollowPhase renders phase/qualification/reason of a gpu explanation.
func gfxFollowPhase(e *gfxExpl) string { return e.Phase + "/" + e.Qualification + "/" + e.Reason }

// ---------------------------------------------------------------------------
// F-01 (GFX-031, GFX-033 (c), §0): ASCII is 0x21-0x7E, not the general
// character rule that also admits the space (0x20).
// ---------------------------------------------------------------------------

// TestF01_GFX031_GFX033c_ASCIIExcludesSpace: a single space inside a field
// defined as ASCII rejects the whole artifact (exit 6). Every case changes one
// value of the S-BASIC artifact and recomputes what the change invalidates, so
// only the ASCII rule is violated; the reseal-only controls are accepted and
// give the unedited output. A reader that checks these fields against the
// general printable range 0x20-0x7E accepts every edited case.
func TestF01_GFX031_GFX033c_ASCIIExcludesSpace(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	ed := gfxArtEditor{t, a}
	if len(a.Trust.Sources) != 4 || a.Trust.Sources[3] != (gfoTrustSource{"SysfsPhysicalParent", "agent", gfoParentSrc}) {
		t.Fatalf("fixture setup: trust.sources = %+v, want the parent source (SysfsPhysicalParent, agent, %s) at index 3 of 4", a.Trust.Sources, gfoParentSrc)
	}
	p := func(c *gfoArtifact) *gfoPayload { return &c.Frames[0].Payload }
	obs := func(c *gfoArtifact, subj, sig string) *gfoObs {
		return &p(c).Observations[gfxObsIndex(t, c, 0, subj, sig)]
	}
	cases := []gfxArtCase{
		{"control: payload resealed, nothing edited", ed.typed("digest", func(c *gfoArtifact) {}), 0, true},
		{"control: payload IDs and digest recomputed, nothing edited", ed.typed("ids", func(c *gfoArtifact) {}), 0, true},
		{"trust.profileID with a space", ed.typed("none", func(c *gfoArtifact) { c.Trust.ProfileID = "offline:lab a" }), 6, false},
		{"trust.sources[3].source.name with a space", ed.typed("none", func(c *gfoArtifact) {
			c.Trust.Sources[3].Name = "path-agent/sysfs-parent x"
		}), 6, false},
		{"trust.sources[3].source.type with a space", ed.typed("none", func(c *gfoArtifact) { c.Trust.Sources[3].Type = "agent x" }), 6, false},
		{"top-level and frame bootID with a space", ed.typed("none", func(c *gfoArtifact) {
			c.BootID = "boot a"
			c.Frames[0].BootID = "boot a"
		}), 6, false},
		{"GPU expected observation unit with a space", ed.typed("digest", func(c *gfoArtifact) {
			obs(c, gfxKGPU, gfoSigExpect).Unit = "lan es"
		}), 6, false},
		{"GPU expected observation dimension value with a space", ed.typed("digest", func(c *gfoArtifact) {
			gfxSetDim(obs(c, gfxKGPU, gfoSigExpect), "pcie.expected.provenance", "a b")
			gfxSortPayload(p(c))
		}), 6, false},
		{"NIC class observation source.name with a space", ed.typed("digest", func(c *gfoArtifact) {
			obs(c, gfxKNIC, gfoSigClass).SourceName = "path-agent/sysfs x"
			gfxSortPayload(p(c))
		}), 6, false},
		{"edgeEvidence[0].sourceType with a space", ed.typed("digest", func(c *gfoArtifact) {
			p(c).EdgeEvidence[0].SourceType = "agent x"
		}), 6, false},
		{"gpuBindings[0].sourceName with a space", ed.typed("digest", func(c *gfoArtifact) {
			p(c).GPUBindings[0].SourceName = "path-agent/nvidia smi"
		}), 6, false},
	}
	gfxRunArtCases(t, bins, a, "F-01/GFX-031/GFX-033(c)", cases)
}

// ---------------------------------------------------------------------------
// F-02 (GFX-031): trust.sources is strictly increasing in the byte order of
// the tuple (capability, type, name), compared field by field.
// ---------------------------------------------------------------------------

// TestF02_GFX031_TrustSourcesTupleOrder: the extra entry (SysfsPhysicalParent,
// agent-b, a) compares after (SysfsPhysicalParent, agent,
// path-agent/sysfs-parent) as a tuple ("agent" is a prefix of "agent-b"), but
// before it when the three fields are concatenated ('-' < 'p'). The artifact
// with the extra entry after the parent entry is valid and gives the unedited
// output; with the entry before it the artifact is rejected.
func TestF02_GFX031_TrustSourcesTupleOrder(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	ed := gfxArtEditor{t, a}
	if len(a.Trust.Sources) != 4 || a.Trust.Sources[3] != (gfoTrustSource{"SysfsPhysicalParent", "agent", gfoParentSrc}) {
		t.Fatalf("fixture setup: trust.sources = %+v, want the parent source (SysfsPhysicalParent, agent, %s) at index 3 of 4", a.Trust.Sources, gfoParentSrc)
	}
	extra := gfoTrustSource{"SysfsPhysicalParent", "agent-b", "a"}
	after := ed.typed("none", func(c *gfoArtifact) { c.Trust.Sources = append(c.Trust.Sources, extra) })
	before := ed.typed("none", func(c *gfoArtifact) {
		s := c.Trust.Sources
		out := append([]gfoTrustSource{}, s[:3]...)
		out = append(out, extra, s[3])
		c.Trust.Sources = out
	})
	cases := []gfxArtCase{
		{"(SysfsPhysicalParent, agent-b, a) after (SysfsPhysicalParent, agent, path-agent/sysfs-parent)", after, 0, true},
		{"(SysfsPhysicalParent, agent-b, a) before (SysfsPhysicalParent, agent, path-agent/sysfs-parent)", before, 6, false},
	}
	gfxRunArtCases(t, bins, a, "F-02/GFX-031", cases)

	// The accepted artifact is evaluated like any other (oracle comparison).
	acc, err := gfoParseArtifact(after)
	if err != nil {
		t.Fatalf("test bug: the edited artifact does not decode: %v", err)
	}
	gfxNewCase(t, bins, acc, gfxDefaultFleet()).Check("gpu", gfxDevName, "F-02/GFX-031 accepted entry after the parent entry")
}

// ---------------------------------------------------------------------------
// F-03 (GFX-045): a current observation is not a BaselineDigest input.
// ---------------------------------------------------------------------------

// TestF03_GFX045_CurrentObservationNotBaselineInput: S-READY with the NIC
// current width 8 in frames 1 and 2 (or in frame 1 only) and max_link_width
// unchanged, so the NIC expected width stays 16 in every frame. Only the
// expected observations feed BaselineDigest, hence the ready window is not
// restarted and frame 2 is Ready. An implementation that also puts each
// subject's latest pcie.link.width.current into the baseline entries changes
// the digest at frame 1 and reports Validating.
func TestF03_GFX045_CurrentObservationNotBaselineInput(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	for _, x := range []struct {
		name   string
		frames []int
	}{
		{"NIC current width 8 in frames 1 and 2", []int{1, 2}},
		{"NIC current width 8 in frame 1 only", []int{1}},
	} {
		cl := "F-03/GFX-045 " + x.name
		f := gfxSReady(t)
		for _, i := range x.frames {
			f.Write(gfxAttr(i, gfoPathNIC, "current_link_width"), "8\n")
		}
		a := gfxAgentArtifact(t, bins.Agent, f)
		for i := range a.Frames {
			want := "16"
			for _, j := range x.frames {
				if j == i {
					want = "8"
				}
			}
			cur, exp, ok := a.Frames[i].Payload.WidthPair(gfoNIC)
			if !ok || cur.IntValue() != want || exp.IntValue() != "16" {
				t.Fatalf("fixture setup: %s: frame %d NIC current/expected = %s/%s (pair %v), want %s/16", cl, i, cur.IntValue(), exp.IntValue(), ok, want)
			}
		}
		c := gfxNewCase(t, bins, a, gfxDefaultFleet())
		e := c.Check("gpu", gfxDevName, cl)
		gfxWant(t, cl, "phase/qualification/reason", gfxFollowPhase(e), "Ready/Qualified/Ready")
		gfxWant(t, cl, "activeFindings", len(e.Findings), 0)
		n := c.Check("node", gfoNodeName, cl+" node")
		gfxWant(t, cl+" node", "nodeEligibility/qualification/activeFindings", n.NodeEligibility+"/"+n.Qualification+"/"+gfxItoa(len(n.Findings)), "Eligible/Qualified/0")
	}
}

// ---------------------------------------------------------------------------
// F-04 (GFX-033 (h), GFO-040): a binding bdf may have a 5..8 digit domain.
// ---------------------------------------------------------------------------

// TestF04_GFX033h_GFO040_WideDomainBinding: a GPU in PCI domain 0x10000 is
// bound through an NVIDIA row `00010000:01:00.0`; the artifact carries the
// binding bdf `10000:01:00.0` and pathctl must accept and explain it. The
// contrast bdfs with a device number above 0x1f or with an upper-case digit
// are still rejected. A reader that accepts only a 4-digit domain rejects the
// wide artifact; one that accepts any hex digits accepts the contrast bdfs.
func TestF04_GFX033h_GFO040_WideDomainBinding(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	const (
		wideUUID = "GPU-00000000-0000-4000-8000-00000000000e"
		wideBDF  = "10000:01:00.0"
		wideRP   = "10000:00:01.0"
		wideDev  = "gpu-node-1-gpu1"
		cl       = "F-04/GFX-033(h)/GFO-040 binding bdf with a 5-digit domain"
	)
	f := gfxSBasic(t)
	f.Dev(0, gfxPCI([]string{"pci10000:00", wideRP}, "0x060400", "0x8086", "16", "16"))
	f.Dev(0, gfxPCI([]string{"pci10000:00", wideRP, wideBDF}, "0x030200", "0x10de", "16", "16"))
	f.Write(gfoCSV(0), gfxUUID+", 00000000:03:00.0\n"+wideUUID+", 00010000:01:00.0\n")
	fl := gfxDefaultFleet()
	d := gfxDefaultDevice()
	d.Name, d.UID, d.UUID, d.RequestID, d.EvidenceID = wideDev, "9d0e4f6a-1b2c-4d3e-8f4a-5b6c7d8e9f01", wideUUID, "enroll-2", "asset-db:rack7-u12-gpu1"
	fl.Devices = append(fl.Devices, d)
	a := gfxAgentArtifact(t, bins.Agent, f)
	if b, ok := a.Frames[0].Payload.Binding(wideBDF); !ok || b.UUID != wideUUID {
		t.Fatalf("fixture setup: path-agent binding for %s = %+v (present %v), want uuid %s", wideBDF, b, ok, wideUUID)
	}
	c := gfxNewCase(t, bins, a, fl)
	e := c.Check("gpu", wideDev, cl)
	gfxWant(t, cl, "identity.state/bdf/functionKey", e.Identity["state"]+"/"+e.Identity["bdf"]+"/"+e.Identity["functionKey"], "Bound/"+wideBDF+"/"+gfoFnKey(wideBDF))
	gfxWantSegs(t, cl, e, gfxEdgeStr(gfoFnKey(wideBDF), gfoRPKey(wideRP)), gfxEdgeStr(gfoRPKey(wideRP), gfxKNode))
	for _, n := range []string{"pcie-parent", "pcie-root", "pcie-width"} {
		gfxFollowCovState(t, cl, e, n, "Normal")
	}
	c.Check("node", gfoNodeName, cl+" node")

	base := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	ed := gfxArtEditor{t, base}
	contrast := []gfxArtCase{
		{"binding bdf 0000:03:20.0 (device number above 0x1f)", ed.typed("ids", func(c *gfoArtifact) {
			c.Frames[0].Payload.GPUBindings[0].BDF = "0000:03:20.0"
		}), 6, false},
		{"binding bdf 0000:03:0A.0 (upper-case hex digit)", ed.typed("ids", func(c *gfoArtifact) {
			c.Frames[0].Payload.GPUBindings[0].BDF = "0000:03:0A.0"
		}), 6, false},
	}
	gfxRunArtCases(t, bins, base, "F-04/GFX-033(h)", contrast)
}

// ---------------------------------------------------------------------------
// F-05 (GFX-033 (g)): exactly one KubernetesNode asset.
// ---------------------------------------------------------------------------

// TestF05_GFX033g_ZeroKubernetesNodeAsset: an S-BASIC artifact without any
// KubernetesNode asset (the edges that end at it, their edgeEvidence removed,
// edgeIndex renumbered, everything resealed) is rejected with exit 6. A
// reader that only rejects two or more node assets accepts it.
func TestF05_GFX033g_ZeroKubernetesNodeAsset(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	ed := gfxArtEditor{t, a}
	nodeKey := gfoNodeKey(gfoNodeUID)
	droppedEdges, droppedAssets := 0, 0
	raw := ed.typed("ids", func(c *gfoArtifact) {
		p := &c.Frames[0].Payload
		var ends [][2]string
		for _, e := range p.Edges {
			if e.FromKey == nodeKey || e.ToKey == nodeKey {
				ends = append(ends, [2]string{e.FromKey, e.ToKey})
			}
		}
		for _, e := range ends {
			gfxRemoveEdge(t, c, 0, e[0], e[1])
		}
		droppedEdges = len(ends)
		p = &c.Frames[0].Payload
		var keep []gfoAsset
		for _, as := range p.Assets {
			if as.Kind == "KubernetesNode" {
				droppedAssets++
				continue
			}
			keep = append(keep, as)
		}
		p.Assets = keep
	})
	if droppedEdges < 1 || droppedAssets != 1 {
		t.Fatalf("fixture setup: dropped %d edges and %d KubernetesNode assets, want at least 1 edge and exactly 1 asset", droppedEdges, droppedAssets)
	}
	cases := []gfxArtCase{
		{"control: resealed, nothing edited", ed.typed("ids", func(c *gfoArtifact) {}), 0, true},
		{"no KubernetesNode asset (edges to it and their edgeEvidence removed, edgeIndex renumbered, resealed)", raw, 6, false},
	}
	gfxRunArtCases(t, bins, a, "F-05/GFX-033(g)", cases)
}

// ---------------------------------------------------------------------------
// F-06 (GFX-042, PCIE-013): the window MaxAge is Policy.Freshness.
// ---------------------------------------------------------------------------

// TestF06_GFX042_WindowMaxAgeIsFreshness: S-WIDTH (frames at 0/15/30 s, GPU
// current width 8) plus a frame 3 without the GPU current width. The head of
// the GPU width series is the frame 2 pair (30 s); with Freshness 60 s it is
// stale at 100 s and at exactly 90 s (head + Freshness), so the persistent
// degradation is not evaluated: no finding, width Unknown/Validating, phase
// Pending/Unknown/Validating. A window whose MaxAge is longer than Freshness
// (for example the 86400 s horizon) keeps the head fresh and still reports
// the degraded finding. The last row (1 ns before head + Freshness) has no
// literal expectation; it is compared with the oracle so a MaxAge shorter
// than Freshness is caught as well.
func TestF06_GFX042_WindowMaxAgeIsFreshness(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	for _, x := range []struct {
		name       string
		at         string
		oracleOnly bool
	}{
		{"frame 3 at 00:01:40Z (head 30 s, 70 s old)", "2026-09-24T00:01:40Z", false},
		{"P1b frame 3 at 00:01:30Z (head + Freshness)", "2026-09-24T00:01:30Z", false},
		{"P1c frame 3 at 00:01:29.999999999Z (1 ns before head + Freshness, oracle only)", "2026-09-24T00:01:29.999999999Z", true},
	} {
		cl := "F-06/GFX-042 " + x.name
		f := gfxSWidth(t)
		i := gfxAddFrame(f, x.at)
		f.Remove(gfxAttr(i, gfoPathGPU, "current_link_width"))
		a := gfxAgentArtifact(t, bins.Agent, f)
		if len(a.Frames) != 4 || len(a.Frames[3].Payload.Obs(gfxKGPU, gfoSigCurrent)) != 0 || len(a.Frames[2].Payload.Obs(gfxKGPU, gfoSigCurrent)) != 1 {
			t.Fatalf("fixture setup: %s: want 4 frames, no GPU current observation in frame 3 and one in frame 2", cl)
		}
		c := gfxNewCase(t, bins, a, gfxDefaultFleet())
		e := c.Check("gpu", gfxDevName, cl)
		n := c.Check("node", gfoNodeName, cl+" node")
		if x.oracleOnly {
			continue
		}
		gfxWant(t, cl, "phase/qualification/reason", gfxFollowPhase(e), "Pending/Unknown/Validating")
		gfxWant(t, cl, "activeFindings", len(e.Findings), 0)
		gfxWantCov(t, cl, e, "pcie-width", "Unknown", "Validating")
		gfxWantLims(t, cl, e, []string{"width_pair_absent " + gfxKGPU, "collector:width_omitted " + gfoGPU}, nil)
		gfxWant(t, cl+" node", "activeFindings", len(n.Findings), 0)
		gfxWantCov(t, cl+" node", n, "pcie-width", "Unknown", "Validating")
		gfxWantLims(t, cl+" node", n, []string{"width_pair_absent " + gfxKGPU, "collector:width_omitted " + gfoGPU}, nil)
	}
}

// ---------------------------------------------------------------------------
// F-07 (GFX-044): the provenance Source.Type is a TopologyDigest input.
// ---------------------------------------------------------------------------

// TestF07_GFX044_ProvenanceSourceTypeIsTopologyInput: S-READY built with an
// extra trusted parent source (SysfsPhysicalParent, agentx,
// path-agent/sysfs-parent). With no edit the result is Ready. When only the
// sourceType of every frame 1 edgeEvidence becomes agentx (frame 0 and 2 keep
// agent), TopologyDigest changes at frame 1, the ready window restarts and
// frame 2 is Validating while coverage stays Normal. An implementation that
// leaves Source.Type out of the provenance element keeps the digest and
// reports Ready.
func TestF07_GFX044_ProvenanceSourceTypeIsTopologyInput(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxSReady(t)
	f.M.Sources = append(f.M.Sources, gfoSrc{"SysfsPhysicalParent", "agentx", gfoParentSrc})
	a := gfxAgentArtifact(t, bins.Agent, f)
	const cl = "F-07/GFX-044"
	ctl := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("gpu", gfxDevName, cl+" control (extra trust entry, no edit)")
	gfxWant(t, cl+" control", "phase/qualification/reason", gfxFollowPhase(ctl), "Ready/Qualified/Ready")

	x := gfxClone(t, a)
	p := &x.Frames[1].Payload
	if len(p.EdgeEvidence) == 0 {
		t.Fatalf("fixture setup: frame 1 has no edgeEvidence")
	}
	for k, ev := range p.EdgeEvidence {
		if ev.SourceType != "agent" || ev.SourceName != gfoParentSrc {
			t.Fatalf("fixture setup: frame 1 edgeEvidence[%d] source = %s/%s, want agent/%s", k, ev.SourceType, ev.SourceName, gfoParentSrc)
		}
		p.EdgeEvidence[k].SourceType = "agentx"
	}
	gfxSeal(t, x, true)
	e := gfxNewCase(t, bins, x, gfxDefaultFleet()).Check("gpu", gfxDevName, cl+" frame 1 edgeEvidence sourceType agentx")
	gfxWant(t, cl, "phase/qualification/reason", gfxFollowPhase(e), "Validating/Unknown/Validating")
	for _, n := range []string{"pcie-parent", "pcie-root", "pcie-width"} {
		gfxFollowCovState(t, cl, e, n, "Normal")
	}
}

// ---------------------------------------------------------------------------
// F-08 (GFX-045, X-T-19): the latest expected observation stays in the
// baseline even when its series is stale.
// ---------------------------------------------------------------------------

// TestF08_GFX045_LatestExpectedKeptWhenStale: S-READY plus frames 3..5 (45,
// 60, 75 s) where frames 1..5 lack the NIC current width, so the NIC expected
// observation exists in frame 0 only and its series head is stale from 60 s
// on (Freshness 60 s). The NIC baseline entry is the most recent expected
// value, so BaselineDigest never changes and the GPU stays Ready for the
// artifacts of frames 0..3, 0..4 and 0..5. An implementation that drops the
// stale expected series from the baseline changes the digest at frame 4.
func TestF08_GFX045_LatestExpectedKeptWhenStale(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxSReady(t)
	for _, at := range []string{"2026-09-24T00:00:45Z", "2026-09-24T00:01:00Z", "2026-09-24T00:01:15Z"} {
		gfxAddFrame(f, at)
	}
	for i := 1; i <= 5; i++ {
		f.Remove(gfxAttr(i, gfoPathNIC, "current_link_width"))
	}
	a := gfxAgentArtifact(t, bins.Agent, f)
	for i, fr := range a.Frames {
		want := 0
		if i == 0 {
			want = 1
		}
		if got := len(fr.Payload.Obs(gfxKNIC, gfoSigExpect)); got != want || fr.Completeness != "COMPLETE" {
			t.Fatalf("fixture setup: frame %d has %d NIC expected observations and is %s, want %d and COMPLETE", i, got, fr.Completeness, want)
		}
	}
	for _, n := range []int{4, 5, 6} {
		cl := fmt.Sprintf("F-08/GFX-045 frames 0..%d (NIC expected only in frame 0)", n-1)
		e := gfxNewCase(t, bins, gfxFrames(t, a, n), gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
		gfxWant(t, cl, "phase/qualification/reason", gfxFollowPhase(e), "Ready/Qualified/Ready")
		gfxWant(t, cl, "nodeEligibility", e.NodeEligibility, "Eligible")
	}
}

// ---------------------------------------------------------------------------
// F-09 (GFX-045, D4): the baseline covers every subject of the window.
// ---------------------------------------------------------------------------

// TestF09_GFX045_BaselineSubjectsNotLimitedToPayloadAssets: frame 1 of S-READY
// gets a copy of the NIC expected-width observation whose subject is
// PCIeFunction/pci-bdf:0000:09:00.0, which is not an asset of the payload. The
// subject is in the window's Subjects(), so a new baseline entry appears at
// frame 1, the ready window restarts and frame 2 is Validating. An
// implementation that only considers subjects that are assets of frame k
// ignores the copy and reports Ready.
func TestF09_GFX045_BaselineSubjectsNotLimitedToPayloadAssets(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	const ghost = "0000:09:00.0"
	const cl = "F-09/GFX-045 expected observation of a subject that is no payload asset"
	x := gfxClone(t, a)
	p := &x.Frames[1].Payload
	if p.HasAsset(gfoFnKey(ghost)) {
		t.Fatalf("fixture setup: frame 1 has the asset %s", gfoFnKey(ghost))
	}
	src := p.Obs(gfxKNIC, gfoSigExpect)
	if len(src) != 1 {
		t.Fatalf("fixture setup: frame 1 has %d NIC expected observations, want 1", len(src))
	}
	o := src[0]
	o.Subject = gfoAsset{Kind: "PCIeFunction", Canonical: "pci-bdf:" + ghost, Aliases: []gfoAlias{}}
	o.Dims = append([]gfoKV{}, o.Dims...)
	p.Observations = append(p.Observations, o)
	gfxSortPayload(p)
	gfxSeal(t, x, true)
	e := gfxNewCase(t, bins, x, gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "phase/qualification/reason", gfxFollowPhase(e), "Validating/Unknown/Validating")
}

// ---------------------------------------------------------------------------
// F-10 (GFX-064): finding evidence observations are looked up in W_R by ID.
// ---------------------------------------------------------------------------

// TestF10_GFX064_FindingEvidenceLookedUpInResultWindow: S-WIDTH plus a frame 3
// (45 s) without the GPU current width. R is frame 3, whose payload has no
// GPU width pair, but the degraded finding of frames 0..2 is still active in
// W_R and its six evidence observations (ob:7:0:, ob:7:1:, ob:7:2:) are found
// in the window. With the width source, the operator baseline or both removed
// from the trust list, finding_source_untrusted is reported for the finding
// (gpu and node). An implementation that looks the evidence IDs up in the
// payload of R only finds none and reports nothing.
func TestF10_GFX064_FindingEvidenceLookedUpInResultWindow(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	want := map[string]bool{}
	for seq := 0; seq < 3; seq++ {
		for _, sig := range []string{gfoSigCurrent, gfoSigExpect} {
			want[gfoObservationID("7", gfxItoa(seq), gfxKGPU, sig)] = true
		}
	}
	for _, x := range []struct {
		name string
		drop []string
	}{
		{"SysfsPCIeWidth removed from the trust list", []string{"SysfsPCIeWidth"}},
		{"OperatorBaseline removed from the trust list", []string{"OperatorBaseline"}},
		{"SysfsPCIeWidth and OperatorBaseline removed from the trust list", []string{"SysfsPCIeWidth", "OperatorBaseline"}},
	} {
		cl := "F-10/GFX-064 " + x.name
		f := gfxSWidth(t)
		i := gfxAddFrame(f, "2026-09-24T00:00:45Z")
		f.Remove(gfxAttr(i, gfoPathGPU, "current_link_width"))
		gfxWithoutSources(f, x.drop...)
		a := gfxAgentArtifact(t, bins.Agent, f)
		if len(a.Frames) != 4 || len(a.Frames[3].Payload.Obs(gfxKGPU, gfoSigCurrent)) != 0 || a.Trust.ProfileID != gfxProfileWidth {
			t.Fatalf("fixture setup: %s: want 4 frames, no GPU current observation in frame 3 and profile %s", cl, gfxProfileWidth)
		}
		c := gfxNewCase(t, bins, a, gfxDefaultFleet())
		check := func(e *gfxExpl, what string) {
			t.Helper()
			if len(e.Findings) != 1 {
				t.Errorf("%s: activeFindings %d, want 1", what, len(e.Findings))
				return
			}
			fo := e.Findings[0]
			got := map[string]bool{}
			for _, ev := range fo.Evidence {
				got[ev.ObservationID] = true
				if !want[ev.ObservationID] {
					t.Errorf("%s: finding evidence %s is not an observation of frames 0..2 of the GPU width pair", what, ev.ObservationID)
				}
			}
			if len(fo.Evidence) != 6 || len(got) != 6 {
				t.Errorf("%s: finding has %d evidence refs (%d distinct), want the 6 observations of frames 0..2", what, len(fo.Evidence), len(got))
			}
			gfxWantLims(t, what, e, []string{"finding_source_untrusted " + fo.ID, "width_pair_absent " + gfxKGPU}, nil)
		}
		g := c.Check("gpu", gfxDevName, cl)
		check(g, cl)
		gfxWant(t, cl, "phase/qualification/reason", gfxFollowPhase(g), "Pending/Unknown/Validating")
		n := c.Check("node", gfoNodeName, cl+" node")
		check(n, cl+" node")
	}
}
