package tests_test

// Narrow rule checks: payload time notation (GFX-030, GFX-033 (e)), PARTIAL
// frame observations in the cumulative window (GFX-042), per-frame
// evaluation time (GFX-041, GFX-048), BaselineDigest inputs (GFX-045), the
// frame_partial range (GFX-080), the width_pair_absent condition (GFX-084)
// and sysfs_attr_nodata on classification attributes (GFX-085).

import (
	"testing"
)

// TestGFX033_PayloadTimeNotation: a payload time written in another
// notation of the same instant is rejected although the canonical payload
// bytes (and so payloadDigest and every ID) are unchanged; the unedited
// artifact is accepted.
func TestGFX033_PayloadTimeNotation(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	const at, exp = "2026-09-24T00:00:00Z", "2026-09-24T00:05:00Z"
	p := a.Frames[0].Payload
	if p.EdgeEvidence[0].ObservedAt != at || p.EdgeEvidence[0].ExpiresAt != exp || p.Observations[0].ReceivedAt != at ||
		p.Observations[0].ExpiresAt != exp || p.GPUBindings[0].ExpiresAt != exp {
		t.Fatalf("fixture setup: payload times are not %s / %s", at, exp)
	}
	ed := gfxArtEditor{t, a}
	pl := func(c *gfoArtifact) *gfoPayload { return &c.Frames[0].Payload }
	cases := []gfxArtCase{
		{"edgeEvidence expiresAt 00:05:00.000Z", ed.typed("none", func(c *gfoArtifact) { pl(c).EdgeEvidence[0].ExpiresAt = "2026-09-24T00:05:00.000Z" }), 6, false},
		{"edgeEvidence expiresAt 00:05:00+00:00", ed.typed("none", func(c *gfoArtifact) { pl(c).EdgeEvidence[0].ExpiresAt = "2026-09-24T00:05:00+00:00" }), 6, false},
		{"observation expiresAt 09:05:00+09:00", ed.typed("none", func(c *gfoArtifact) { pl(c).Observations[0].ExpiresAt = "2026-09-24T09:05:00+09:00" }), 6, false},
		{"gpuBinding expiresAt 00:05:00.0Z", ed.typed("none", func(c *gfoArtifact) { pl(c).GPUBindings[0].ExpiresAt = "2026-09-24T00:05:00.0Z" }), 6, false},
		{"edgeEvidence observedAt 00:00:00.000Z", ed.typed("none", func(c *gfoArtifact) { pl(c).EdgeEvidence[0].ObservedAt = "2026-09-24T00:00:00.000Z" }), 6, false},
		{"observation receivedAt 00:00:00-00:00", ed.typed("none", func(c *gfoArtifact) { pl(c).Observations[0].ReceivedAt = "2026-09-24T00:00:00-00:00" }), 6, false},
	}
	gfxRunArtCases(t, bins, a, "GFX-030/GFX-033 (e) payload time notation", cases)
}

// TestGFX042_PartialFrameObservationsAccumulate: frame 1 is PARTIAL and frame
// 2 has no GPU width pair; the frame 1 pair (50 s) is still fresh at t_2 =
// 100 s with Freshness 60 s, so width coverage is Normal from it.
func TestGFX042_PartialFrameObservationsAccumulate(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxSReady(t)
	f.M.Frames = []string{"2026-09-24T00:00:00Z", "2026-09-24T00:00:50Z", "2026-09-24T00:01:40Z"}
	f.Write(gfoSys(1)+"/bus/pci/devices/.gitkeep", "")
	f.Remove(gfxAttr(2, gfoPathGPU, "current_link_width"))
	a := gfxAgentArtifact(t, bins.Agent, f)
	fr1, fr2 := a.Frames[1].Payload, a.Frames[2].Payload
	if a.Frames[0].Completeness != "COMPLETE" || a.Frames[1].Completeness != "PARTIAL" || a.Frames[2].Completeness != "COMPLETE" ||
		len(fr1.Obs(gfxKGPU, gfoSigCurrent)) != 1 || len(fr1.Obs(gfxKGPU, gfoSigExpect)) != 1 || len(fr2.Obs(gfxKGPU, gfoSigCurrent)) != 0 {
		t.Fatalf("fixture setup: frames %s/%s/%s, frame 1 GPU pair %d/%d, frame 2 GPU current %d; want COMPLETE/PARTIAL/COMPLETE, 1/1, 0",
			a.Frames[0].Completeness, a.Frames[1].Completeness, a.Frames[2].Completeness,
			len(fr1.Obs(gfxKGPU, gfoSigCurrent)), len(fr1.Obs(gfxKGPU, gfoSigExpect)), len(fr2.Obs(gfxKGPU, gfoSigCurrent)))
	}
	const cl = "GFX-042 PARTIAL frame 1 pair carried to frame 2"
	e := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
	if cv, ok := e.Cov("pcie-width"); !ok || cv.State != "Normal" {
		t.Errorf("%s: pcie-width %s/%s, want Normal from the frame 1 pair", cl, cv.State, cv.Reason)
	}
	gfxWant(t, cl, "phase", e.Phase, "Validating")
	gfxWantLims(t, cl, e, []string{"frame_partial 1", "width_pair_absent " + gfxKGPU}, nil)
}

// TestGFX041_PerFrameEvaluationTime: with Freshness 60 s, ReadyFor 90 s and
// frames at 0/45/90 s, each frame is evaluated at its own time, so frame 0
// is a normal point and frame 2 is Ready (90 - 0 >= 90); evaluating the
// whole chain at E = 90 s would find frame 0's evidence stale.
func TestGFX041_PerFrameEvaluationTime(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxSReady(t)
	f.M.Frames = []string{"2026-09-24T00:00:00Z", "2026-09-24T00:00:45Z", "2026-09-24T00:01:30Z"}
	a := gfxAgentArtifact(t, bins.Agent, f)
	for i, fr := range a.Frames {
		if fr.Completeness != "COMPLETE" || len(fr.Payload.Obs(gfxKGPU, gfoSigCurrent)) != 1 {
			t.Fatalf("fixture setup: frame %d is %s with %d GPU current observations", i, fr.Completeness, len(fr.Payload.Obs(gfxKGPU, gfoSigCurrent)))
		}
	}
	fl := gfxDefaultFleet()
	fl.ReadyFor = 90
	const cl = "GFX-041/GFX-048 frames 0/45/90 s, Freshness 60 s, ReadyFor 90 s"
	e := gfxNewCase(t, bins, a, fl).Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "phase", e.Phase, "Ready")
}

// TestGFX045_UnitAndQualityNotInputs: changing only the unit or only the
// quality of the NIC expected observation in frame 1 keeps BaselineDigest,
// so the GPU stays Ready at frame 2 as in S-READY.
func TestGFX045_UnitAndQualityNotInputs(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	ready := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	for _, x := range []struct {
		name string
		edit func(o *gfoObs)
	}{
		{"unit", func(o *gfoObs) { o.Unit = "lane" }},
		{"quality", func(o *gfoObs) { o.Quality = "Degraded" }},
	} {
		cl := "GFX-045 NIC expected " + x.name + " changed in frame 1"
		c := gfxClone(t, ready)
		o := &c.Frames[1].Payload.Observations[gfxObsIndex(t, c, 1, gfxKNIC, gfoSigExpect)]
		if o.Unit != "lanes" || o.Quality != "Good" {
			t.Fatalf("fixture setup: frame 1 NIC expected unit %q quality %q", o.Unit, o.Quality)
		}
		x.edit(o)
		gfxSeal(t, c, true)
		if c.Frames[1].PayloadDigest == ready.Frames[1].PayloadDigest {
			t.Fatalf("test bug: %s: the edit did not change the frame 1 payload", cl)
		}
		e := gfxNewCase(t, bins, c, gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
		gfxWant(t, cl, "phase", e.Phase, "Ready")
	}
}

// TestGFX080_FramePartialRangeWithoutAdmission: frames 0 and 2 are PARTIAL,
// so A is empty and R is frame 0; frame_partial covers frames 0..R only.
func TestGFX080_FramePartialRangeWithoutAdmission(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxSReady(t)
	for _, i := range []int{0, 2} {
		f.Write(gfoSys(i)+"/bus/pci/devices/.gitkeep", "")
	}
	a := gfxAgentArtifact(t, bins.Agent, f)
	if a.Frames[0].Completeness != "PARTIAL" || a.Frames[1].Completeness != "COMPLETE" || a.Frames[2].Completeness != "PARTIAL" {
		t.Fatalf("fixture setup: frames %s/%s/%s, want PARTIAL/COMPLETE/PARTIAL", a.Frames[0].Completeness, a.Frames[1].Completeness, a.Frames[2].Completeness)
	}
	const cl = "GFX-080 frame_partial with A empty"
	e := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
	gfxWant(t, cl+" GFX-041", "observedAt (R = frame 0)", e.ObservedAt, a.Frames[0].ObservedAt)
	gfxWantLims(t, cl, e, []string{"frame_partial 0", "no_admitted_frame WrongSession"}, []string{"frame_partial 2"})
}

// TestGFX084_CurrentObservationDecides: width_pair_absent depends on the
// current observation of F(d) in R only: an R payload with the GPU expected
// but no current has it, one with the current but no expected does not.
func TestGFX084_CurrentObservationDecides(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	for _, x := range []struct {
		drop   string
		absent bool
	}{
		{gfoSigCurrent, true},
		{gfoSigExpect, false},
	} {
		cl := "GFX-084 R payload without the GPU " + x.drop
		c := gfxClone(t, a)
		p := &c.Frames[0].Payload
		k := gfxObsIndex(t, c, 0, gfxKGPU, x.drop)
		p.Observations = append(p.Observations[:k:k], p.Observations[k+1:]...)
		gfxSeal(t, c, true)
		e := gfxNewCase(t, bins, c, gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
		gfxWant(t, cl, "identity.state", e.Identity["state"], "Bound")
		if x.absent {
			gfxWantLims(t, cl, e, []string{"width_pair_absent " + gfxKGPU}, nil)
		} else {
			gfxWantLims(t, cl, e, nil, []string{"width_pair_absent"})
		}
	}
}

// TestGFX085_NodataOnClassificationAttributes: an R whose only diagnostic
// is sysfs_attr_nodata on a class, vendor or physfn attribute carries
// nvidia_classification_unknown. The frame is marked PARTIAL, as a
// classification input without data makes it (GFO-051).
func TestGFX085_NodataOnClassificationAttributes(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	if len(a.Frames[0].Diagnostics) != 0 {
		t.Fatalf("fixture setup: frame 0 has %d diagnostics, want 0", len(a.Frames[0].Diagnostics))
	}
	for _, attr := range []string{"class", "vendor", "physfn"} {
		subject := "devices/pci0000:00/0000:00:01.0/0000:01:00.0/0000:02:08.0/0000:03:00.0/" + attr
		c := gfxWithDiags(t, a, 0, gfoDiag{"sysfs_attr_nodata", subject})
		c.Frames[0].Completeness = "PARTIAL"
		c.Raw = gfoRenderArtifact(c)
		cl := "GFX-085 sysfs_attr_nodata on " + attr
		e := gfxNewCase(t, bins, c, gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
		gfxWantLims(t, cl, e, []string{"nvidia_classification_unknown", "collector:sysfs_attr_nodata " + subject}, nil)
	}
}
