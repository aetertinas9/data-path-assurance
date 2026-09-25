package tests_test

// Normative scenarios of GFX-102 (BASE fixture and every variant row). The
// table values are asserted literally; S1-derived opaque values are compared
// with the independent oracle inside gfxCase.Check (GFX-101).

import (
	"bytes"
	"strings"
	"testing"
)

// Asset keys of the BASE topology.
var (
	gfxKGPU  = gfoFnKey(gfoGPU)
	gfxKNIC  = gfoFnKey(gfoNIC)
	gfxKSWD  = gfoSWKey(gfoSWD)
	gfxKSWU  = gfoSWKey(gfoSWU)
	gfxKRPA  = gfoRPKey(gfoRPA)
	gfxKRPB  = gfoRPKey(gfoRPB)
	gfxKNode = gfoNodeKey(gfoNodeUID)
)

func gfxEdgeStr(from, to string) string { return from + " -> " + to }

var gfxBaseSegments = []string{gfxEdgeStr(gfxKGPU, gfxKSWD), gfxEdgeStr(gfxKSWD, gfxKSWU), gfxEdgeStr(gfxKSWU, gfxKRPA), gfxEdgeStr(gfxKRPA, gfxKNode)}

// gfxWithoutSources removes the trust entries of the given capabilities.
func gfxWithoutSources(f *gfoFx, caps ...string) {
	var keep []gfoSrc
	for _, s := range f.M.Sources {
		drop := false
		for _, c := range caps {
			if s.Cap == c {
				drop = true
			}
		}
		if !drop {
			keep = append(keep, s)
		}
	}
	f.M.Sources = keep
}

// Scenario fixtures and artifacts (GFX-102 variant table).

func gfxSPartial0(t *testing.T) *gfoFx {
	t.Helper()
	f := gfxSReady(t)
	f.Write(gfoSys(0)+"/bus/pci/devices/.gitkeep", "")
	return f
}

func gfxSLastPartial(t *testing.T) *gfoFx {
	t.Helper()
	f := gfxSReady(t)
	f.Remove(gfxAttr(2, gfoPathGPU, "class"))
	return f
}

func gfxSStale(t *testing.T) *gfoFx {
	t.Helper()
	f := gfxSReady(t)
	f.M.Frames = []string{"2026-09-24T00:00:00Z", "2026-09-24T00:00:15Z", "2026-09-24T00:01:16Z"}
	f.Remove(gfxAttr(2, gfoPathGPU, "current_link_width"))
	return f
}

func gfxSCarry(t *testing.T) *gfoFx {
	t.Helper()
	f := gfxSReady(t)
	f.Remove(gfxAttr(2, gfoPathGPU, "current_link_width"))
	return f
}

func gfxSCarry4(t *testing.T) *gfoFx {
	t.Helper()
	f := gfxSReady(t)
	i := gfxAddFrame(f, "2026-09-24T00:00:45Z")
	f.Remove(gfxAttr(i, gfoPathGPU, "current_link_width"))
	return f
}

func gfxSRootless(t *testing.T) *gfoFx {
	t.Helper()
	f := gfoNewFx(t, 1)
	f.M.Baselines = []gfoBL{{gfoGPU, gfoSWD, "16"}}
	f.Dev(0, gfxPCI(gfoPathRPA, "0x060400", "0x8086", "16", "16"))
	f.Dev(0, gfxPCI(gfoPathSWU, "0x060400", "0x10b5", "16", "16"))
	f.Dev(0, gfxPCI(gfoPathSWD, "0x060400", "0x10b5", "16", "16"))
	f.Dev(0, gfxPCI([]string{gfoHB, gfoGPU}, "0x030200", "0x10de", "16", "16"))
	f.Dev(0, gfxPCI(gfoPathRPB, "0x060400", "0x8086", "16", "16"))
	f.Dev(0, gfxPCI(gfoPathNIC, "0x020000", "0x15b3", "16", "16"))
	f.Write(gfoCSV(0), gfxUUID+", 00000000:03:00.0\n")
	return f
}

func gfxSContra(t *testing.T) *gfoFx {
	t.Helper()
	f := gfxSBasic(t)
	f.Dev(0, gfoDev{BDF: "0000:05:00.0", Path: []string{gfoHB, gfoRPB, gfoSWD, "0000:05:00.0"}, Attrs: map[string]string{"class": "0x020000\n"}})
	return f
}

func gfxSNICBase(t *testing.T) *gfoFx {
	t.Helper()
	f := gfxSReady(t)
	for i := 1; i <= 2; i++ {
		f.Write(gfxAttr(i, gfoPathNIC, "current_link_width"), "8\n")
		f.Write(gfxAttr(i, gfoPathNIC, "max_link_width"), "8\n")
	}
	return f
}

// gfxSRevert builds S-READY plus frame 3 with path-agent and then edits frame
// 1's GPU width pair provenance to adjacent_capability_min (GFO-084..087
// recomputed).
func gfxSRevert(t *testing.T, agent string) *gfoArtifact {
	t.Helper()
	f := gfxSReady(t)
	gfxAddFrame(f, "2026-09-24T00:00:45Z")
	a := gfxAgentArtifact(t, agent, f)
	for _, sig := range []string{gfoSigCurrent, gfoSigExpect} {
		o := &a.Frames[1].Payload.Observations[gfxObsIndex(t, a, 1, gfxKGPU, sig)]
		gfxSetDim(o, "pcie.expected.provenance", gfoProvAdj)
	}
	gfxSortPayload(&a.Frames[1].Payload)
	gfxSeal(t, a, true)
	return a
}

// gfxSIDConflict adds a second binding of the same UUID on the NIC BDF.
func gfxSIDConflict(t *testing.T, agent string) *gfoArtifact {
	t.Helper()
	a := gfxAgentArtifact(t, agent, gfxSBasic(t))
	p := &a.Frames[0].Payload
	b := p.GPUBindings[0]
	b.BDF = gfoNIC
	p.GPUBindings = append(p.GPUBindings, b)
	gfxSortPayload(p)
	gfxSeal(t, a, true)
	return a
}

// gfxSFloatBase rewrites frame 1's GPU expected value as the Float 16.
func gfxSFloatBase(t *testing.T, agent string) *gfoArtifact {
	t.Helper()
	a := gfxAgentArtifact(t, agent, gfxSReady(t))
	o := &a.Frames[1].Payload.Observations[gfxObsIndex(t, a, 1, gfxKGPU, gfoSigExpect)]
	if o.ValueKind != "int" || o.ValueJSON != `"16"` {
		t.Fatalf("fixture setup: frame 1 GPU expected value is %s %s, want int 16", o.ValueKind, o.ValueJSON)
	}
	o.ValueKind, o.ValueJSON = "float", `"16"`
	gfxSeal(t, a, true)
	return a
}

// gfxSContraF adds the observed edge GPU -> switch upstream.
func gfxSContraF(t *testing.T, agent string) *gfoArtifact {
	t.Helper()
	a := gfxAgentArtifact(t, agent, gfxSBasic(t))
	gfxAddEdge(t, a, 0, gfxKGPU, gfxKSWU)
	return a
}

// TestGFX102_SBasic: exact row of GFX-102 S-BASIC.
func TestGFX102_SBasic(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	c := gfxNewCase(t, bins, a, gfxDefaultFleet())
	const cl = "GFX-102 S-BASIC"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "phase/qualification/reason", e.Phase+"/"+e.Qualification+"/"+e.Reason, "Validating/Unknown/Validating")
	gfxWant(t, cl, "nodeEligibility", e.NodeEligibility, "Ineligible")
	gfxWant(t, cl, "graphRevision", e.GraphRevision, a.Frames[0].BundleRevision)
	gfxWant(t, cl, "observedAt/evaluatedAt", e.ObservedAt+"/"+e.EvaluatedAt, gfoT0+"/"+gfoT0)
	exp := gfoAddSeconds(t, gfoT0, 300)
	wantID := map[string]string{
		"state": "Bound", "reason": "Ready", "vendor": "NVIDIA", "uuid": gfxUUID, "nodeUID": gfoNodeUID, "bootID": gfoBootID,
		"bdf": gfoGPU, "functionKey": gfxKGPU, "source.type": "agent", "source.name": gfoNVIDIASrc,
		"evidenceID": gfoBindingID("7", "0", gfxUUID, gfoGPU), "observedAt": gfoT0, "expiresAt": exp,
	}
	for k, v := range wantID {
		gfxWant(t, cl+" GFX-062", "identity."+k, e.Identity[k], v)
	}
	gfxWantSegs(t, cl, e, gfxBaseSegments...)
	for i, s := range e.Segments {
		want := gfoEdgeEvidenceID("7", "0", gfoEdge{s.From, "LOCATED_IN", s.To, "Observed"})
		if s.EvidenceID != want || s.SourceType != "agent" || s.SourceName != gfoParentSrc || s.ObservedAt != gfoT0 || s.ExpiresAt != exp {
			t.Errorf("%s GFX-063: pathSegments[%d] = %+v, want evidence %s source agent/%s times %s/%s", cl, i, s, want, gfoParentSrc, gfoT0, exp)
		}
	}
	gfxWant(t, cl, "activeFindings", len(e.Findings), 0)
	gfxWant(t, cl, "coverage order", gfxCovNames(e), "[nic-lldp pcie-parent pcie-root pcie-width]")
	gfxWantCov(t, cl, e, "nic-lldp", "Unsupported", "Unsupported")
	for _, n := range []string{"pcie-parent", "pcie-root", "pcie-width"} {
		if cv, _ := e.Cov(n); cv.State != "Normal" {
			t.Errorf("%s: coverage %s state %s, want Normal", cl, n, cv.State)
		}
	}
	gfxWant(t, cl+" (exact limitations)", "limitations", gfxJoin(e.Lims()),
		gfxJoin([]string{"allocation_unavailable", "offline", "offline_trust_not_live offline:lab-a-basic", "traffic_path_unverified"}))
	gfxWant(t, cl, "diagnostics of R", len(a.Frames[0].Diagnostics), 0)
	gfxWant(t, cl, "totalCounts", gfxTotals(e), "pathSegments=4 activeFindings=0 coverage=4 affectedWorkloads=0 deviceSummaries=0 limitations=4")
}

func gfxCovNames(e *gfxExpl) string {
	var n []string
	for _, c := range e.Coverage {
		n = append(n, c.Name)
	}
	return "[" + strings.Join(n, " ") + "]"
}

func gfxTotals(e *gfxExpl) string {
	var parts []string
	for _, k := range gfxTotalKeys {
		if k == "evidenceRefs" {
			continue
		}
		parts = append(parts, k+"="+gfxItoa(e.Totals[k]))
	}
	return strings.Join(parts, " ")
}

// TestGFX102_SWidth: GFX-051 and the normative §6/§7 examples (gpu and node).
func TestGFX102_SWidth(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSWidth(t))
	c := gfxNewCase(t, bins, a, gfxDefaultFleet())
	const cl = "GFX-102 S-WIDTH"
	e := c.Check("gpu", gfxDevName, cl)
	t30 := "2026-09-24T00:00:30Z"
	gfxWant(t, cl, "graphRevision", e.GraphRevision, a.Frames[2].BundleRevision)
	gfxWant(t, cl, "phase/qualification/reason/nodeEligibility", strings.Join([]string{e.Phase, e.Qualification, e.Reason, e.NodeEligibility}, "/"), "Degraded/Disqualified/Degraded/Ineligible")
	gfxWant(t, cl, "identity.evidenceID", e.Identity["evidenceID"], gfoBindingID("7", "2", gfxUUID, gfoGPU))
	gfxWant(t, cl, "identity times", e.Identity["observedAt"]+"/"+e.Identity["expiresAt"], t30+"/2026-09-24T00:05:30Z")
	gfxWantSegs(t, cl, e, gfxBaseSegments...)
	if len(e.Findings) != 1 {
		t.Fatalf("%s: activeFindings %d, want 1", cl, len(e.Findings))
	}
	f := e.Findings[0]
	wantFindingID := "pcie-width-v1:5043496546756e6374696f6e2f7063692d6264663a303030303a30333a30302e30:50434965526f6f74506f72742f7063692d6264663a303030303a30303a30312e30:504349655377697463682f7063692d6264663a303030303a30323a30382e30"
	gfxWant(t, cl, "finding id", f.ID, wantFindingID)
	gfxWant(t, cl, "finding type/severity/state/confidence", strings.Join([]string{f.Type, f.Severity, f.State, f.Confidence}, "/"), "PCIE_LINK_WIDTH_DEGRADED/Warning/Active/Medium")
	gfxWant(t, cl, "finding scope", f.Scope, []string{gfxKGPU, gfxKRPA, gfxKSWD})
	gfxWant(t, cl, "finding firstSeen/lastSeen", f.FirstSeen+"/"+f.LastSeen, gfoT0+"/"+t30)
	if len(f.Evidence) != 6 {
		t.Errorf("%s GFX-051: finding evidence %d, want 6", cl, len(f.Evidence))
	} else {
		for i, ev := range f.Evidence {
			seq := gfxItoa(i / 2)
			sig, sum := gfoSigCurrent, "pcie.link.width.current=8 lanes"
			if i%2 == 1 {
				sig, sum = gfoSigExpect, "pcie.link.width.expected=16 lanes"
			}
			gfxWant(t, cl+" GFX-051/X-T-14", "finding evidence["+gfxItoa(i)+"]", ev.ObservationID+" "+ev.Summary, gfoObservationID("7", seq, gfxKGPU, sig)+" "+sum)
		}
	}
	gfxWant(t, cl, "finding explanation", f.Explanation, `PCIe link width degraded: subject="PCIeFunction/pci-bdf:0000:03:00.0"; peer="PCIeSwitch/pci-bdf:0000:02:08.0"; root="PCIeRootPort/pci-bdf:0000:00:01.0"; current=8 lanes; expected=16 lanes; samples=3; first=2026-09-24T00:00:00Z; last=2026-09-24T00:00:30Z; source_type="agent"; source_name="path-agent/sysfs-width"; provenance="operator_verified_wiring"; basis=adapter-supplied operator-verified wiring width; limitation=operator verification is asserted by the adapter and not revalidated by this rule.`)
	gfxWant(t, cl, "finding suggestedStep", f.SuggestedStep, "Recheck the verified wiring baseline and inspect both link endpoints in audit mode.")
	gfxWantCov(t, cl, e, "nic-lldp", "Unsupported", "Unsupported")
	gfxWantCov(t, cl, e, "pcie-parent", "Normal", "Normal")
	gfxWantCov(t, cl, e, "pcie-root", "Normal", "Normal")
	gfxWantCov(t, cl+" GFX-051", e, "pcie-width", "Missing", "CoverageMissing")
	gfxWant(t, cl, "totalCounts", gfxTotals(e)+" evidenceRefs="+gfxItoa(e.Totals["evidenceRefs"]), "pathSegments=4 activeFindings=1 coverage=4 affectedWorkloads=0 deviceSummaries=0 limitations=4 evidenceRefs=15")
	gfxWant(t, cl, "limitations", gfxJoin(e.Lims()), gfxJoin([]string{"allocation_unavailable", "offline", "offline_trust_not_live " + gfxProfileWidth, "traffic_path_unverified"}))
	gfxWant(t, cl, "observedAt/evaluatedAt", e.ObservedAt+"/"+e.EvaluatedAt, t30+"/"+t30)
	text := gfxOK(t, c.Run("gpu", gfxDevName, "text"), cl+" text")
	if ok, line := gfxMatchExample(string(text), gfxExampleWidthGPUText); !ok {
		t.Errorf("%s: GFX-070..077: gpu text output does not match the normative example at line %d:\n%s", cl, line, gfxLine(string(text), line))
	}
	n := c.Check("node", gfoNodeName, cl+" node")
	gfxWant(t, cl+" node", "qualification/nodeEligibility/eligibilityReason", n.Qualification+"/"+n.NodeEligibility+"/"+n.EligibilityReason, "Disqualified/Ineligible/Degraded")
	gfxWant(t, cl+" node", "coverage", len(n.Coverage), 1)
	if len(n.Coverage) == 1 {
		cv := n.Coverage[0]
		gfxWant(t, cl+" node", "coverage[0]", strings.Join([]string{cv.Device, cv.Name, cv.PathKind, cv.State, cv.Reason}, "/"), gfxDevName+"/pcie-width/gpu-pcie-link-width-normal/Missing/CoverageMissing")
	}
	gfxWant(t, cl+" node", "deviceSummaries", n.Devices, []gfxDevSum{{gfxDevName, gfxDevUID, "InService", "1", "Disqualified", "Degraded", "Degraded"}})
	text = gfxOK(t, c.Run("node", gfoNodeName, "text"), cl+" node text")
	if ok, line := gfxMatchExample(string(text), gfxExampleWidthNodeText); !ok {
		t.Errorf("%s: GFX-070..077: node text output does not match the normative example at line %d:\n%s", cl, line, gfxLine(string(text), line))
	}
}

func gfxLine(text string, n int) string {
	lines := strings.Split(text, "\n")
	if n-1 >= 0 && n-1 < len(lines) {
		return lines[n-1]
	}
	return "(no such line)"
}

// TestGFX102_SReady: GFX-050 base case.
func TestGFX102_SReady(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSReady(t)), gfxDefaultFleet())
	const cl = "GFX-102 S-READY"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "phase/qualification/reason/nodeEligibility", strings.Join([]string{e.Phase, e.Qualification, e.Reason, e.NodeEligibility}, "/"), "Ready/Qualified/Ready/Eligible")
	n := c.Check("node", gfoNodeName, cl+" node")
	gfxWant(t, cl+" node", "nodeEligibility/qualification", n.NodeEligibility+"/"+n.Qualification, "Eligible/Qualified")
	gfxWant(t, cl+" node", "coverage", len(n.Coverage), 0)
}

// TestGFX102_SPartial0: A is empty and R is the rejected frame 0.
func TestGFX102_SPartial0(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSPartial0(t)), gfxDefaultFleet())
	const cl = "GFX-102 S-PARTIAL0"
	e := c.Check("gpu", gfxDevName, cl)
	if e.HasGraphRevision {
		t.Errorf("%s GFX-060: graphRevision %q present, want omitted (A empty)", cl, e.GraphRevision)
	}
	gfxWant(t, cl, "phase/qualification/reason", e.Phase+"/"+e.Qualification+"/"+e.Reason, "Pending/Unknown/UnadmittedSnapshot")
	gfxWant(t, cl+" GFX-062", "identity", gfxJoin(e.IdentityKeys)+" "+e.Identity["state"]+"/"+e.Identity["reason"], "[state, reason] Unknown/UnadmittedSnapshot")
	gfxWantSegs(t, cl, e)
	gfxWant(t, cl+" GFX-041/X-OD-12", "observedAt", e.ObservedAt, gfoT0)
	gfxWantSynthetic(t, cl, e, "UnadmittedSnapshot")
	gfxWantLims(t, cl, e, []string{"no_admitted_frame WrongSession", "frame_partial 0", "collector:sysfs_entry_name_invalid bus/pci/devices/.gitkeep", "diagnostics_outside_digest"}, nil)
	n := c.Check("node", gfoNodeName, cl+" node")
	if n.HasGraphRevision || len(n.Segments) != 0 || len(n.Findings) != 0 {
		t.Errorf("%s node: graphRevision/segments/findings %v/%d/%d, want omitted/0/0", cl, n.HasGraphRevision, len(n.Segments), len(n.Findings))
	}
}

// gfxWantSynthetic asserts GFX-065 synthetic coverage for every policy item.
func gfxWantSynthetic(t *testing.T, cl string, e *gfxExpl, reason string) {
	t.Helper()
	want := map[string]string{"nic-lldp": "nic-lldp-remote", "pcie-parent": "gpu-pcie-parent", "pcie-root": "gpu-pcie-root", "pcie-width": "gpu-pcie-link-width-normal"}
	gfxWant(t, cl+" GFX-065", "coverage names", gfxCovNames(e), "[nic-lldp pcie-parent pcie-root pcie-width]")
	for _, cv := range e.Coverage {
		if cv.State != "Unknown" || cv.Reason != reason || cv.PathKind != want[cv.Name] || len(cv.EvidenceRefs) != 0 || cv.HasObservedAt || cv.HasLatest || cv.HasExpires {
			t.Errorf("%s GFX-065/X-T-16: coverage %s = %+v, want synthetic Unknown/%s with policy pathKind, [] refs and no times", cl, cv.Name, cv, reason)
		}
	}
}

// TestGFX102_SLastPartial: R is a PARTIAL admitted frame.
func TestGFX102_SLastPartial(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSLastPartial(t)), gfxDefaultFleet())
	const cl = "GFX-102 S-LASTPARTIAL"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "phase/qualification/reason", e.Phase+"/"+e.Qualification+"/"+e.Reason, "Pending/Unknown/Validating")
	gfxWant(t, cl+" GFX-062", "identity.reason", e.Identity["reason"], "Validating")
	gfxWantSegs(t, cl, e)
	gfxWantSynthetic(t, cl, e, "Validating")
	classSubject := gfoAttrSubject(gfoPathGPU, "class")
	gfxWantLims(t, cl, e, []string{"frame_partial 2", "collector:sysfs_attr_missing " + classSubject, "collector:nvidia_bdf_not_gpu " + gfoGPU, "nvidia_classification_unknown"}, []string{"frame_partial 0", "frame_partial 1"})
}

// TestGFX102_SStale: the width head from an earlier frame is stale at t_R.
func TestGFX102_SStale(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSStale(t)), gfxDefaultFleet())
	const cl = "GFX-102 S-STALE"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWantCov(t, cl, e, "pcie-width", "Unknown", "Validating")
	gfxWant(t, cl, "phase/qualification/reason", e.Phase+"/"+e.Qualification+"/"+e.Reason, "Pending/Unknown/Validating")
	gfxWantLims(t, cl, e, []string{"width_pair_absent " + gfxKGPU, "collector:width_omitted " + gfoGPU}, nil)
}

// TestGFX102_SCarry: the previous frame's fresh pair keeps width Normal.
func TestGFX102_SCarry(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSCarry(t)), gfxDefaultFleet())
	const cl = "GFX-102 S-CARRY"
	e := c.Check("gpu", gfxDevName, cl)
	if cv, _ := e.Cov("pcie-width"); cv.State != "Normal" {
		t.Errorf("%s: pcie-width %s/%s, want Normal", cl, cv.State, cv.Reason)
	}
	gfxWant(t, cl, "phase", e.Phase, "Validating")
	gfxWantLims(t, cl, e, []string{"width_pair_absent " + gfxKGPU}, nil)
}

// TestGFX102_SCarry4: GFX-050 (f).
func TestGFX102_SCarry4(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSCarry4(t))
	c := gfxNewCase(t, bins, a, gfxDefaultFleet())
	const cl = "GFX-102 S-CARRY4 / GFX-050 (f)"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "phase/qualification/nodeEligibility", e.Phase+"/"+e.Qualification+"/"+e.NodeEligibility, "Ready/Qualified/Eligible")
	gfxWantLims(t, cl, e, []string{"width_pair_absent " + gfxKGPU, "collector:width_omitted " + gfoGPU}, nil)
	n := c.Check("node", gfoNodeName, cl+" node")
	gfxWant(t, cl+" node", "nodeEligibility/qualification", n.NodeEligibility+"/"+n.Qualification, "Eligible/Qualified")
	c3 := gfxNewCase(t, bins, gfxFrames(t, a, 3), gfxDefaultFleet())
	e3 := c3.Check("gpu", gfxDevName, cl+" frames 0..2")
	gfxWant(t, cl+" frames 0..2", "phase", e3.Phase, "Ready")
}

// TestGFX102_SNICBase: GFX-050 (g).
func TestGFX102_SNICBase(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSNICBase(t)), gfxDefaultFleet())
	const cl = "GFX-102 S-NICBASE / GFX-050 (g)"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "phase/qualification", e.Phase+"/"+e.Qualification, "Validating/Unknown")
}

// TestGFX102_SRevert: a provenance that returns to the earlier value restarts
// the ready window.
func TestGFX102_SRevert(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxSRevert(t, bins.Agent), gfxDefaultFleet())
	const cl = "GFX-102 S-REVERT"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "frame 3 phase", e.Phase, "Validating")
}

// TestGFX102_SRootless: a function directly under the host bridge.
func TestGFX102_SRootless(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSRootless(t)), gfxDefaultFleet())
	const cl = "GFX-102 S-ROOTLESS"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWantSegs(t, cl, e, gfxEdgeStr(gfxKGPU, gfxKNode))
	gfxWantCov(t, cl, e, "pcie-parent", "Unknown", "TopologyConflict")
	gfxWantCov(t, cl, e, "pcie-root", "Unknown", "TopologyConflict")
	gfxWantCov(t, cl, e, "pcie-width", "Unknown", "Validating")
	gfxWantLims(t, cl, e, []string{"root_port_absent " + gfxKGPU, "width_pair_absent " + gfxKGPU, "collector:width_omitted " + gfoGPU}, []string{"path_contradiction"})
	gfxWant(t, cl, "phase/qualification", e.Phase+"/"+e.Qualification, "Pending/Unknown")
}

// TestGFX102_SContra: two observed parents; every branch is shown.
func TestGFX102_SContra(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSContra(t)), gfxDefaultFleet())
	const cl = "GFX-102 S-CONTRA"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWantSegs(t, cl, e, gfxEdgeStr(gfxKGPU, gfxKSWD), gfxEdgeStr(gfxKSWD, gfxKRPB), gfxEdgeStr(gfxKSWD, gfxKSWU), gfxEdgeStr(gfxKRPB, gfxKNode), gfxEdgeStr(gfxKSWU, gfxKRPA), gfxEdgeStr(gfxKRPA, gfxKNode))
	gfxWantLims(t, cl, e, []string{"path_contradiction " + gfxKGPU, "width_pair_absent " + gfxKGPU}, nil)
	gfxWantCov(t, cl, e, "pcie-parent", "Normal", e.coverageReason("pcie-parent"))
	gfxWantCov(t, cl, e, "pcie-root", "Unknown", "TopologyConflict")
	gfxWantCov(t, cl, e, "pcie-width", "Unknown", "Validating")
	gfxWant(t, cl, "phase/qualification", e.Phase+"/"+e.Qualification, "Pending/Unknown")
}

func (e *gfxExpl) coverageReason(name string) string {
	c, _ := e.Cov(name)
	return c.Reason
}

// TestGFX102_SUntrusted covers S-UNTRUSTED and S-UNTRUSTED-PARENT.
func TestGFX102_SUntrusted(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxSBasic(t)
	gfxWithoutSources(f, "NVIDIAUUIDBinding")
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet())
	const cl = "GFX-102 S-UNTRUSTED"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "identity", e.Identity["state"]+"/"+e.Identity["reason"], "Unknown/UntrustedSource")
	gfxWantSegs(t, cl, e)
	gfxWantSynthetic(t, cl, e, "UntrustedSource")
	gfxWant(t, cl, "qualification", e.Qualification, "Unknown")

	f = gfxSBasic(t)
	gfxWithoutSources(f, "SysfsPhysicalParent")
	c = gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet())
	const cp = "GFX-102 S-UNTRUSTED-PARENT"
	e = c.Check("gpu", gfxDevName, cp)
	gfxWant(t, cp, "identity.state", e.Identity["state"], "Bound")
	gfxWantSegs(t, cp, e)
	gfxWantLims(t, cp, e, []string{"path_untrusted " + gfxKGPU}, nil)
	for _, n := range []string{"pcie-parent", "pcie-root", "pcie-width"} {
		gfxWantCov(t, cp, e, n, "Unknown", "UntrustedSource")
	}
	gfxWant(t, cp, "phase/qualification", e.Phase+"/"+e.Qualification, "Pending/Unknown")
}

// TestGFX102_SUntrustedWidth covers the three width-trust rows.
func TestGFX102_SUntrustedWidth(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	rows := []struct {
		name       string
		drop       []string
		widthState string
		widthRsn   string
	}{
		{"S-UNTRUSTED-WIDTH", []string{"SysfsPCIeWidth", "OperatorBaseline"}, "Unsupported", "Unsupported"},
		{"S-UNTRUSTED-WIDTHONLY", []string{"SysfsPCIeWidth"}, "Unsupported", "Unsupported"},
		{"S-UNTRUSTED-BASELINE", []string{"OperatorBaseline"}, "Unknown", "UntrustedSource"},
	}
	for _, r := range rows {
		f := gfxSWidth(t)
		gfxWithoutSources(f, r.drop...)
		c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet())
		cl := "GFX-102 " + r.name
		e := c.Check("gpu", gfxDevName, cl)
		if len(e.Findings) != 1 {
			t.Errorf("%s: activeFindings %d, want 1", cl, len(e.Findings))
			continue
		}
		gfxWantLims(t, cl+" GFX-064/X-T-18", e, []string{"finding_source_untrusted " + e.Findings[0].ID}, []string{"finding_not_applied"})
		gfxWantCov(t, cl, e, "pcie-width", r.widthState, r.widthRsn)
		if r.name == "S-UNTRUSTED-BASELINE" {
			gfxWantCov(t, cl, e, "pcie-parent", "Normal", e.coverageReason("pcie-parent"))
			gfxWantCov(t, cl, e, "pcie-root", "Normal", e.coverageReason("pcie-root"))
		}
		gfxWant(t, cl, "phase/qualification/reason", e.Phase+"/"+e.Qualification+"/"+e.Reason, "Pending/Unknown/Validating")
	}
}

// TestGFX102_SIntent covers S-INTENT (Maintenance and Retired).
func TestGFX102_SIntent(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	for desired, phase := range map[string]string{"Maintenance": "MaintenancePending", "Retired": "Retiring"} {
		fl := gfxDefaultFleet()
		fl.Devices[0].Desired = desired
		c := gfxNewCase(t, bins, a, fl)
		cl := "GFX-102 S-INTENT " + desired
		e := c.Check("gpu", gfxDevName, cl)
		gfxWant(t, cl, "phase/reason", e.Phase+"/"+e.Reason, phase+"/AllocationUnknown")
		gfxWantLims(t, cl, e, []string{"fence_unavailable", "allocation_unavailable"}, nil)
		n := c.Check("node", gfoNodeName, cl+" node")
		gfxWantLims(t, cl+" node", n, []string{"fence_unavailable"}, nil)
	}
}

// TestGFX102_SLateIntent: intent after every frame.
func TestGFX102_SLateIntent(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	fl := gfxDefaultFleet()
	fl.Devices[0].IntentAt = "2026-09-24T00:00:01Z"
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSBasic(t)), fl)
	const cl = "GFX-102 S-LATEINTENT"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "identity", e.Identity["state"]+"/"+e.Identity["reason"], "Unknown/UntrustedSource")
	gfxWant(t, cl, "qualification", e.Qualification, "Unknown")
}

// TestGFX102_SNameDiff: only the limitation and its count change.
func TestGFX102_SNameDiff(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	base := gfxNewCase(t, bins, a, gfxDefaultFleet())
	b := base.Check("gpu", gfxDevName, "GFX-102 S-NAMEDIFF reference")
	fl := gfxDefaultFleet()
	fl.Devices[0].NodeName = "gpu-node-1-old"
	c := gfxNewCase(t, bins, a, fl)
	const cl = "GFX-102 S-NAMEDIFF"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWantLims(t, cl, e, []string{"node_name_differs " + gfxDevName}, nil)
	gfxWant(t, cl, "totalCounts.limitations", e.Totals["limitations"], 5)
	// Everything else equals S-BASIC.
	want := gfxClone2(b.Tree)
	lims, _ := gfxGet(want, "limitations")
	var out []*gfoJ
	inserted := false
	for _, l := range lims.elems {
		code, _ := gfxGet(l, "code")
		if !inserted && code.str > "node_name_differs" {
			out = append(out, gfxObj(gfxKV{"code", gfxS("node_name_differs")}, gfxKV{"subject", gfxS(gfxDevName)}))
			inserted = true
		}
		out = append(out, l)
	}
	lims.elems = out
	tc, _ := gfxGet(want, "totalCounts")
	lc, _ := gfxGet(tc, "limitations")
	lc.num = "5"
	if d := gfxDiffJ(e.Tree, want, "$"); d != "" {
		t.Errorf("%s: output differs from S-BASIC beyond the limitation: %s", cl, d)
	}
}

// gfxClone2 deep-copies a JSON tree.
func gfxClone2(j *gfoJ) *gfoJ {
	c := *j
	c.keys = append([]string{}, j.keys...)
	c.vals = nil
	for _, v := range j.vals {
		c.vals = append(c.vals, gfxClone2(v))
	}
	c.elems = nil
	for _, e := range j.elems {
		c.elems = append(c.elems, gfxClone2(e))
	}
	if j.kind == '[' && c.elems == nil {
		c.elems = []*gfoJ{}
	}
	return &c
}

// TestGFX102_SIDConflict: the same UUID observed on two BDFs.
func TestGFX102_SIDConflict(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxSIDConflict(t, bins.Agent), gfxDefaultFleet())
	const cl = "GFX-102 S-IDCONFLICT"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "identity", gfxJoin(e.IdentityKeys)+" "+e.Identity["state"]+"/"+e.Identity["reason"], "[state, reason] Conflict/IdentityConflict")
	gfxWantSegs(t, cl, e)
	gfxWantSynthetic(t, cl, e, "IdentityConflict")
	gfxWant(t, cl, "qualification", e.Qualification, "Unknown")
}

// TestGFX102_SFloatBase: Int and Float of the same integer are the same
// baseline (PCIE-015).
func TestGFX102_SFloatBase(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxSFloatBase(t, bins.Agent), gfxDefaultFleet())
	const cl = "GFX-102 S-FLOATBASE"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "phase/qualification", e.Phase+"/"+e.Qualification, "Ready/Qualified")
}

// TestGFX102_SContraF: a second observed parent of the function itself.
func TestGFX102_SContraF(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxSContraF(t, bins.Agent), gfxDefaultFleet())
	const cl = "GFX-102 S-CONTRA-F"
	e := c.Check("gpu", gfxDevName, cl)
	gfxWantLims(t, cl, e, []string{"path_contradiction " + gfxKGPU}, nil)
	gfxWantSegs(t, cl, e, gfxEdgeStr(gfxKGPU, gfxKSWU), gfxEdgeStr(gfxKGPU, gfxKSWD), gfxEdgeStr(gfxKSWU, gfxKRPA), gfxEdgeStr(gfxKSWD, gfxKSWU), gfxEdgeStr(gfxKRPA, gfxKNode))
	gfxWantCov(t, cl, e, "pcie-parent", "Unknown", "TopologyConflict")
	gfxWantCov(t, cl, e, "pcie-root", "Unknown", "TopologyConflict")
}

// TestGFX102_ErrorRow: the error row of the GFX-102 table.
func TestGFX102_ErrorRow(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	tampered := bytes.Replace(a.Raw, []byte(`"unit":"lanes"`), []byte(`"unit":"lanez"`), 1)
	in := gfxWriteInputs(t, tampered, gfxDefaultFleet().JSON())
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "")...), 6, "GFX-102 error row: payload byte changed")
	fl := gfxDefaultFleet()
	fl.ClusterID = "lab-b"
	in = gfxWriteInputs(t, a.Raw, fl.JSON())
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "")...), 7, "GFX-102 error row: clusterID lab-b")
	in = gfxWriteInputs(t, a.Raw, gfxDefaultFleet().JSON())
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", "gpu-node-1-gpu9", in, "")...), 4, "GFX-102 error row: unknown name")
}
