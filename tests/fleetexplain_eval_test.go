package tests_test

// Evaluation semantics (GFX-040..052): admission, result frame and time,
// cumulative window, per-frame topology, digests, findings, bundle, device
// chain, node aggregate and the normative ready-window and degradation
// examples.

import (
	"fmt"
	"strings"
	"testing"
)

// TestGFX040_AdmissionChain: A is every frame when frame 0 is COMPLETE and
// empty when frame 0 is PARTIAL.
func TestGFX040_AdmissionChain(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	for _, n := range []int{1, 3} {
		f := gfxBaseFixture(t, n)
		f.M.Profile = gfxProfileWidth
		f.Write(gfoSys(0)+"/bus/pci/devices/.gitkeep", "")
		c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet())
		cl := fmt.Sprintf("GFX-040 PARTIAL frame 0 of %d", n)
		e := c.Check("gpu", gfxDevName, cl)
		if e.HasGraphRevision {
			t.Errorf("%s: graphRevision present, want omitted (A empty)", cl)
		}
		gfxWantLims(t, cl, e, []string{"no_admitted_frame WrongSession"}, nil)
		gfxWant(t, cl, "reason", e.Reason, "UnadmittedSnapshot")
	}
	f := gfxSReady(t)
	for i := 1; i <= 2; i++ {
		f.Write(gfoSys(i)+"/bus/pci/devices/.gitkeep", "")
	}
	a := gfxAgentArtifact(t, bins.Agent, f)
	c := gfxNewCase(t, bins, a, gfxDefaultFleet())
	const cl = "GFX-040 PARTIAL frames after a COMPLETE frame 0"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "graphRevision", e.GraphRevision, a.Frames[2].BundleRevision)
	gfxWantLims(t, cl, e, []string{"frame_partial 1", "frame_partial 2"}, []string{"no_admitted_frame"})
}

// TestGFX041_ResultFrameAndTime: R is the last frame when A is not empty,
// frame 0 otherwise; observedAt and evaluatedAt are T(t_R).
func TestGFX041_ResultFrameAndTime(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxSReady(t)
	f.M.Frames = []string{"2026-09-24T09:00:00.100+09:00", "2026-09-24T00:00:15.000000001Z", "2026-09-23T23:00:30.500-01:00"}
	a := gfxAgentArtifact(t, bins.Agent, f)
	c := gfxNewCase(t, bins, a, gfxDefaultFleet())
	const cl = "GFX-041"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl+" GFX-061 T form", "observedAt/evaluatedAt", e.ObservedAt+"/"+e.EvaluatedAt, "2026-09-24T00:00:30.5Z/2026-09-24T00:00:30.5Z")
	gfxWant(t, cl, "graphRevision", e.GraphRevision, a.Frames[2].BundleRevision)
	n := c.Check("node", gfoNodeName, cl+" node")
	gfxWant(t, cl+" node", "observedAt", n.ObservedAt, "2026-09-24T00:00:30.5Z")

	p := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSPartial0(t)), gfxDefaultFleet())
	pe := p.Check("gpu", gfxDevName, cl+" A empty")
	gfxWant(t, cl+" A empty (X-OD-12)", "observedAt/evaluatedAt", pe.ObservedAt+"/"+pe.EvaluatedAt, gfoT0+"/"+gfoT0)
}

// TestGFX042_CumulativeWindow: the window accumulates every frame's
// observations with MaxAge = Freshness and MaxSamples 16.
func TestGFX042_CumulativeWindow(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	carry := gfxAgentArtifact(t, bins.Agent, gfxSCarry(t))
	c := gfxNewCase(t, bins, carry, gfxDefaultFleet())
	e := c.Check("gpu", gfxDevName, "GFX-042 S-CARRY")
	if cv, _ := e.Cov("pcie-width"); cv.State != "Normal" {
		t.Errorf("GFX-042: S-CARRY pcie-width %s, want Normal from the frame 1 pair", cv.State)
	}
	// MaxAge follows Freshness: with a 10-second freshness the frame 1 pair
	// is stale at frame 2.
	short := gfxDefaultFleet()
	short.Freshness = 10
	sc := gfxNewCase(t, bins, carry, short)
	se := sc.Check("gpu", gfxDevName, "GFX-042 S-CARRY with freshness 10s")
	if cv, _ := se.Cov("pcie-width"); cv.State == "Normal" {
		t.Errorf("GFX-042: with freshness 10s the 15-second-old pair must not give Normal width")
	}
	// Sixteen frames 15 s apart fill the window without eviction.
	f := gfxBaseFixture(t, 16)
	f.M.Profile = gfxProfileWidth
	for i := 0; i < 16; i++ {
		f.Write(gfxAttr(i, gfoPathGPU, "current_link_width"), "8\n")
	}
	w := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet())
	we := w.Check("gpu", gfxDevName, "GFX-042 16 frames")
	if len(we.Findings) != 1 || len(we.Findings[0].Evidence) != 32 {
		n := -1
		if len(we.Findings) == 1 {
			n = len(we.Findings[0].Evidence)
		}
		t.Errorf("GFX-042/GFX-090: 16 degraded frames give %d findings with %d evidence refs, want 1 with 32 (2 per frame)", len(we.Findings), n)
	}
	// A frame span longer than one day keeps the persistence suffix.
	g := gfxBaseFixture(t, 4)
	g.M.Profile = gfxProfileWidth
	g.M.Frames = []string{"2026-09-24T00:00:00Z", "2026-09-26T00:00:00Z", "2026-09-26T00:00:15Z", "2026-09-26T00:00:30Z"}
	for i := 0; i < 4; i++ {
		g.Write(gfxAttr(i, gfoPathGPU, "current_link_width"), "8\n")
	}
	h := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, g), gfxDefaultFleet())
	he := h.Check("gpu", gfxDevName, "GFX-042 span beyond the horizon")
	gfxWant(t, "GFX-042 span beyond the horizon", "phase", he.Phase, "Degraded")
}

// TestGFX043_TopologyPerFrame: each frame's topology comes from its own
// payload only; a PARTIAL R does not borrow earlier edges.
func TestGFX043_TopologyPerFrame(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSLastPartial(t)), gfxDefaultFleet())
	e := c.Check("node", gfoNodeName, "GFX-043 S-LASTPARTIAL node")
	gfxWantSegs(t, "GFX-043/X-T-15", e)
	// An edge present only in frame 1 does not appear when R is frame 2.
	a := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	gfxAddEdge(t, a, 1, gfxKGPU, gfxKSWU)
	x := gfxNewCase(t, bins, a, gfxDefaultFleet())
	xe := x.Check("gpu", gfxDevName, "GFX-043 edge only in frame 1")
	gfxWantSegs(t, "GFX-043", xe, gfxBaseSegments...)
	gfxWantLims(t, "GFX-043", xe, nil, []string{"path_contradiction"})
}

// TestGFX044_TopologyDigest: asset, edge and provenance-source changes reset
// the ready window; sequence, time and revision changes do not.
func TestGFX044_TopologyDigest(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	control := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSReady(t)), gfxDefaultFleet())
	gfxWant(t, "GFX-044 control", "phase", control.Check("gpu", gfxDevName, "GFX-044 control").Phase, "Ready")

	// GFX-050 (c): an extra NIC in frame 1 only.
	f := gfxSReady(t)
	f.Dev(1, gfxPCI([]string{gfoHB, gfoRPB, "0000:06:00.0"}, "0x020000", "0x15b3", "16", "16"))
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet())
	gfxWant(t, "GFX-044/GFX-050 (c)", "phase", c.Check("gpu", gfxDevName, "GFX-050 (c)").Phase, "Validating")

	// An unrelated extra asset in frame 1 only.
	a := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	p := &a.Frames[1].Payload
	p.Assets = append(p.Assets, gfoAsset{"Site", "site:rack-7", []gfoAlias{}})
	gfxSortPayload(p)
	gfxSeal(t, a, true)
	ac := gfxNewCase(t, bins, a, gfxDefaultFleet())
	gfxWant(t, "GFX-044 asset only in frame 1", "phase", ac.Check("gpu", gfxDevName, "GFX-044 asset only in frame 1").Phase, "Validating")

	// A trusted but different provenance source in frame 1 only.
	g := gfxSReady(t)
	g.M.Sources = append(g.M.Sources, gfoSrc{"SysfsPhysicalParent", "agent", "path-agent/sysfs-parent-alt"})
	b := gfxAgentArtifact(t, bins.Agent, g)
	for k := range b.Frames[1].Payload.EdgeEvidence {
		b.Frames[1].Payload.EdgeEvidence[k].SourceName = "path-agent/sysfs-parent-alt"
	}
	gfxSeal(t, b, true)
	bc := gfxNewCase(t, bins, b, gfxDefaultFleet())
	b1 := gfxNewCase(t, bins, gfxFrames(t, b, 2), gfxDefaultFleet())
	b1.Check("gpu", gfxDevName, "GFX-044 frames 0..1 with another trusted source")
	gfxWant(t, "GFX-044 provenance source only in frame 1", "phase", bc.Check("gpu", gfxDevName, "GFX-044 provenance source only in frame 1").Phase, "Validating")
}

// TestGFX045_BaselineDigest: GFX-050 (f)/(g), S-REVERT and S-FLOATBASE are
// the observable contract; an unrelated function's baseline change resets
// every device's window (X-OD-07 A).
func TestGFX045_BaselineDigest(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	cases := []struct {
		name  string
		a     *gfoArtifact
		phase string
	}{
		{"S-CARRY4 (width read failure keeps the baseline)", gfxAgentArtifact(t, bins.Agent, gfxSCarry4(t)), "Ready"},
		{"S-NICBASE (NIC baseline change)", gfxAgentArtifact(t, bins.Agent, gfxSNICBase(t)), "Validating"},
		{"S-REVERT (provenance returns)", gfxSRevert(t, bins.Agent), "Validating"},
		{"S-FLOATBASE (Int/Float normalization)", gfxSFloatBase(t, bins.Agent), "Ready"},
	}
	for _, x := range cases {
		c := gfxNewCase(t, bins, x.a, gfxDefaultFleet())
		gfxWant(t, "GFX-045 "+x.name, "phase", c.Check("gpu", gfxDevName, "GFX-045 "+x.name).Phase, x.phase)
	}
}

// TestGFX046_FindingsPerFrame: findings are evaluated per frame on the
// cumulative window at t_k.
func TestGFX046_FindingsPerFrame(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxSWidth(t)
	i := gfxAddFrame(f, "2026-09-24T00:00:45Z")
	f.Write(gfxAttr(i, gfoPathGPU, "current_link_width"), "8\n")
	a := gfxAgentArtifact(t, bins.Agent, f)
	for n, want := range map[int]int{1: 0, 2: 0, 3: 1, 4: 1} {
		c := gfxNewCase(t, bins, gfxFrames(t, a, n), gfxDefaultFleet())
		cl := fmt.Sprintf("GFX-046 S-WIDTH frames 0..%d", n-1)
		e := c.Check("gpu", gfxDevName, cl)
		gfxWant(t, cl, "activeFindings", len(e.Findings), want)
		if want == 1 && len(e.Findings) == 1 {
			gfxWant(t, cl, "lastSeen", e.Findings[0].LastSeen, e.ObservedAt)
			gfxWant(t, cl, "evidence", len(e.Findings[0].Evidence), 2*n)
		}
	}
}

// TestGFX047_Bundle: bundle assembly for A empty and for several devices.
func TestGFX047_Bundle(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	p := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSPartial0(t)), gfxDefaultFleet())
	e := p.Check("gpu", gfxDevName, "GFX-047 A empty")
	gfxWant(t, "GFX-047/X-T-07", "phase/qualification/reason", e.Phase+"/"+e.Qualification+"/"+e.Reason, "Pending/Unknown/UnadmittedSnapshot")
	fl := gfxTwoDevices()
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSReady(t)), fl)
	c.Check("gpu", gfxDevName, "GFX-047 two devices (bound)")
	c.Check("gpu", "gpu-node-1-spare", "GFX-047 two devices (unobserved)")
	c.Check("node", gfoNodeName, "GFX-047 two devices (node)")
}

// gfxTwoDevices adds a device whose UUID is never observed.
func gfxTwoDevices() gfxFleet {
	fl := gfxDefaultFleet()
	d := gfxDefaultDevice()
	d.Name, d.UID, d.UUID, d.RequestID, d.EvidenceID = "gpu-node-1-spare", "00-spare-uid", "GPU-00000000-0000-4000-8000-00000000000a", "enroll-2", "asset-db:spare"
	fl.Devices = append(fl.Devices, d)
	return fl
}

// TestGFX048_EveryDeviceEvaluated: a gpu request still evaluates every
// device (nodeEligibility reflects all of them).
func TestGFX048_EveryDeviceEvaluated(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSReady(t)), gfxTwoDevices())
	e := c.Check("gpu", gfxDevName, "GFX-048")
	gfxWant(t, "GFX-048", "target phase/qualification", e.Phase+"/"+e.Qualification, "Ready/Qualified")
	gfxWant(t, "GFX-048/GFL-053", "nodeEligibility", e.NodeEligibility, "Ineligible")
	s := c.Check("gpu", "gpu-node-1-spare", "GFX-048 spare")
	gfxWant(t, "GFX-048 spare", "identity", s.Identity["state"]+"/"+s.Identity["reason"], "Unknown/UntrustedSource")
	n := c.Check("node", gfoNodeName, "GFX-048 node")
	if len(n.Devices) != 2 || n.Devices[0].UID != "00-spare-uid" {
		t.Errorf("GFX-048/GFX-067: deviceSummaries %v, want both devices ordered by uid", n.Devices)
	}
}

// TestGFX049_NodeAggregate: SelectionComplete for one or more devices,
// SelectionNoDevices for none; the gpu nodeEligibility is the node's.
func TestGFX049_NodeAggregate(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	c := gfxNewCase(t, bins, a, gfxDefaultFleet())
	n := c.Check("node", gfoNodeName, "GFX-049 S-READY node")
	gfxWant(t, "GFX-049", "nodeEligibility/qualification", n.NodeEligibility+"/"+n.Qualification, "Eligible/Qualified")
	g := c.Check("gpu", gfxDevName, "GFX-049 S-READY gpu")
	gfxWant(t, "GFX-049", "gpu nodeEligibility equals node", g.NodeEligibility+"/"+g.AssessmentRevision, n.NodeEligibility+"/"+n.AssessmentRevision)
	empty := gfxDefaultFleet()
	empty.Devices = nil
	ec := gfxNewCase(t, bins, a, empty)
	ee := ec.Check("node", gfoNodeName, "GFX-049 no devices")
	gfxWant(t, "GFX-049/GFL-053A", "nodeEligibility/qualification", ee.NodeEligibility+"/"+ee.Qualification, "Unknown/Unknown")
	gfxWantLims(t, "GFX-049/GFX-080", ee, []string{"no_devices"}, []string{"allocation_unavailable"})
	m := gfxDefaultFleet()
	m.Devices[0].Desired = "Maintenance"
	mc := gfxNewCase(t, bins, a, m)
	me := mc.Check("node", gfoNodeName, "GFX-049 maintenance device")
	gfxWant(t, "GFX-049/GFL-053", "nodeEligibility", me.NodeEligibility, "Ineligible")
}

// TestGFX050_ReadyWindow: the normative example and its variants (a)..(e);
// (f) and (g) are TestGFX102_SCarry4 and TestGFX102_SNICBase.
func TestGFX050_ReadyWindow(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	ready := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	c := gfxNewCase(t, bins, ready, gfxDefaultFleet())
	e := c.Check("gpu", gfxDevName, "GFX-050")
	gfxWant(t, "GFX-050", "phase/qualification/reason/nodeEligibility", strings.Join([]string{e.Phase, e.Qualification, e.Reason, e.NodeEligibility}, "/"), "Ready/Qualified/Ready/Eligible")

	slow := gfxDefaultFleet()
	slow.ReadyFor = 31
	gfxWant(t, "GFX-050 (a)", "phase", gfxNewCase(t, bins, ready, slow).Check("gpu", gfxDevName, "GFX-050 (a)").Phase, "Validating")
	gfxWant(t, "GFX-050 (b)", "phase", gfxNewCase(t, bins, gfxFrames(t, ready, 1), gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-050 (b)").Phase, "Validating")
	for n := 1; n <= 2; n++ {
		cl := fmt.Sprintf("GFX-050 frames 0..%d", n-1)
		gfxWant(t, cl, "phase", gfxNewCase(t, bins, gfxFrames(t, ready, n), gfxDefaultFleet()).Check("gpu", gfxDevName, cl).Phase, "Validating")
	}

	f := gfxSReady(t)
	f.Write(gfoSys(1)+"/bus/pci/devices/.gitkeep", "")
	d := gfxAgentArtifact(t, bins.Agent, f)
	d1 := gfxNewCase(t, bins, gfxFrames(t, d, 2), gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-050 (d) frames 0..1")
	gfxWant(t, "GFX-050 (d) frame 1", "phase/qualification/reason", d1.Phase+"/"+d1.Qualification+"/"+d1.Reason, "Pending/Unknown/Validating")
	d2 := gfxNewCase(t, bins, d, gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-050 (d)")
	gfxWant(t, "GFX-050 (d) frame 2", "phase", d2.Phase, "Validating")
	gfxWantLims(t, "GFX-050 (d)", d2, []string{"frame_partial 1"}, nil)

	g := gfxSReady(t)
	g.M.Frames = []string{"2026-09-24T00:00:00Z", "2026-09-24T00:01:01Z", "2026-09-24T00:02:02Z"}
	gfxWant(t, "GFX-050 (e)", "phase", gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, g), gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-050 (e)").Phase, "Validating")
}

// TestGFX051_WidthDegradation: persistent width degradation at frame 2 and
// the two-frame prefix without a finding.
func TestGFX051_WidthDegradation(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSWidth(t))
	e := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-051")
	gfxWant(t, "GFX-051", "phase/qualification/reason/nodeEligibility", strings.Join([]string{e.Phase, e.Qualification, e.Reason, e.NodeEligibility}, "/"), "Degraded/Disqualified/Degraded/Ineligible")
	gfxWantCov(t, "GFX-051", e, "pcie-width", "Missing", "CoverageMissing")
	if len(e.Findings) != 1 || len(e.Findings[0].Evidence) != 6 {
		t.Errorf("GFX-051: findings %d, want 1 with 6 evidence refs", len(e.Findings))
	} else {
		prior := 0
		for _, ev := range e.Findings[0].Evidence {
			if strings.HasPrefix(ev.ObservationID, "ob:7:0:") || strings.HasPrefix(ev.ObservationID, "ob:7:1:") {
				prior++
			}
		}
		gfxWant(t, "GFX-051/X-T-14", "evidence from earlier frames", prior, 4)
	}
	n := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("node", gfoNodeName, "GFX-051 node")
	gfxWant(t, "GFX-051 node", "qualification", n.Qualification, "Disqualified")
	two := gfxNewCase(t, bins, gfxFrames(t, a, 2), gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-051 frames 0..1")
	gfxWant(t, "GFX-051 frames 0..1", "phase/qualification/reason/findings", fmt.Sprintf("%s/%s/%s/%d", two.Phase, two.Qualification, two.Reason, len(two.Findings)), "Pending/Disqualified/CoverageMissing/0")
}

// TestGFX052_MaintenanceRetired: offline intents never complete.
func TestGFX052_MaintenanceRetired(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	for desired, phase := range map[string]string{"Maintenance": "MaintenancePending", "Retired": "Retiring"} {
		fl := gfxDefaultFleet()
		fl.Devices[0].Desired = desired
		cl := "GFX-052 " + desired
		e := gfxNewCase(t, bins, a, fl).Check("gpu", gfxDevName, cl)
		gfxWant(t, cl, "phase", e.Phase, phase)
		gfxWant(t, cl, "allocation.state", e.Allocation["state"], "Unknown")
		gfxWantLims(t, cl, e, []string{"fence_unavailable"}, nil)
	}
	e := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-052 InService")
	gfxWantLims(t, "GFX-052 InService", e, nil, []string{"fence_unavailable"})
}
