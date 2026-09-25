package tests_test

// Explanation model (GFX-060..069) and text form (GFX-070..078). Every run
// goes through gfxCase.Check, which also applies the structural rules, the
// JSON-to-text reconstruction and the oracle; the tests below add the
// clause-specific assertions.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestGFX060_TopLevelFields: field order and omission for gpu and node.
func TestGFX060_TopLevelFields(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	w := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSWidth(t)), gfxDefaultFleet())
	g := w.Check("gpu", gfxDevName, "GFX-060 gpu")
	gfxWant(t, "GFX-060 gpu", "keys", g.Tree.keys, gfxTopGPU)
	n := w.Check("node", gfoNodeName, "GFX-060 node")
	gfxWant(t, "GFX-060 node", "keys", n.Tree.keys, gfxTopNode)
	gfxWant(t, "GFX-060 node", "target", n.TargetName+"/"+n.TargetUID, gfoNodeName+"/"+gfoNodeUID)
	gfxWant(t, "GFX-060 gpu", "target/node", g.TargetName+"/"+g.TargetUID+"/"+g.NodeName+"/"+g.NodeUID, gfxDevName+"/"+gfxDevUID+"/"+gfoNodeName+"/"+gfoNodeUID)
	p := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSPartial0(t)), gfxDefaultFleet())
	pg := p.Check("gpu", gfxDevName, "GFX-060 gpu A empty")
	var want []string
	for _, k := range gfxTopGPU {
		if k != "graphRevision" {
			want = append(want, k)
		}
	}
	gfxWant(t, "GFX-060 gpu A empty", "keys", pg.Tree.keys, want)
}

// TestGFX061_ValueNotation: enum strings, T times, decimal strings for int64,
// numbers for counts, booleans, [] for empty lists.
func TestGFX061_ValueNotation(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	fl := gfxDefaultFleet()
	fl.Devices[0].Generation = 9223372036854775807
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSBasic(t)), fl)
	n := c.Check("node", gfoNodeName, "GFX-061 node")
	if len(n.Devices) == 1 {
		gfxWant(t, "GFX-061", "observedGeneration", n.Devices[0].Generation, "9223372036854775807")
	}
	if !bytes.Contains(n.Raw, []byte(`"observedGeneration":"9223372036854775807"`)) {
		t.Errorf("GFX-061: observedGeneration is not a decimal string in %s", gfoTail(n.Raw))
	}
	for _, k := range []string{`"truncated":false`, `"pathSegments":4`, `"affectedWorkloads":[]`, `"activeFindings":[]`} {
		if !bytes.Contains(n.Raw, []byte(k)) {
			t.Errorf("GFX-061: node output lacks %s", k)
		}
	}
	g := c.Check("gpu", gfxDevName, "GFX-061 gpu")
	for _, k := range []string{`"deviceSummaries":[]`, `"evidenceRefs":[]`, `"required":false`, `"required":true`} {
		if !bytes.Contains(g.Raw, []byte(k)) {
			t.Errorf("GFX-061: gpu output lacks %s", k)
		}
	}
}

// TestGFX062_Identity: Bound identity fields and order; other states carry
// only state and reason; the fleet serial is never output.
func TestGFX062_Identity(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	fl := gfxDefaultFleet()
	fl.Devices[0].Serial = "SN-ZQX-0001"
	c := gfxNewCase(t, bins, a, fl)
	e := c.Check("gpu", gfxDevName, "GFX-062 Bound with a fleet serial")
	var want []string
	for _, k := range gfxIdentityKeys {
		if k != "serial" {
			want = append(want, k)
		}
	}
	gfxWant(t, "GFX-062", "identity keys", e.IdentityKeys, want)
	if bytes.Contains(e.Raw, []byte("SN-ZQX-0001")) {
		t.Errorf("GFX-062: the fleet inventoryClaim.serial appears in the output")
	}
	text := gfxOK(t, c.Run("gpu", gfxDevName, "text"), "GFX-062 text")
	if !bytes.Contains(text, []byte(" serial=- ")) {
		t.Errorf("GFX-062/GFX-073: identity line lacks serial=-")
	}
	s := gfxDefaultFleet()
	s.Devices[0].UUID, s.Devices[0].Serial = "", "SN-1"
	se := gfxNewCase(t, bins, a, s).Check("gpu", gfxDevName, "GFX-062 serial-only claim")
	gfxWant(t, "GFX-062 serial-only claim", "identity keys", se.IdentityKeys, []string{"state", "reason"})
	if se.Identity["state"] == "Bound" {
		t.Errorf("GFX-062/GFX-022: a claim without uuid must not bind")
	}
}

// TestGFX063_PathSegments: trusted hops from each bound function, union for
// node requests, shortest depth, one entry per edge, every contradictory
// branch, cycles.
func TestGFX063_PathSegments(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxSBasic(t)
	gpu2 := "0000:03:00.1"
	f.Dev(0, gfxPCI(append(append([]string{}, gfoPathSWD...), gpu2), "0x030200", "0x10de", "16", "16"))
	uuid2 := "GPU-00000000-0000-4000-8000-000000000002"
	f.Write(gfoCSV(0), gfxUUID+", 00000000:03:00.0\n"+uuid2+", 0000:03:00.1\n")
	fl := gfxDefaultFleet()
	d := gfxDefaultDevice()
	d.Name, d.UID, d.UUID, d.EvidenceID = "gpu-node-1-gpu1", "1-uid-gpu1", uuid2, "asset-db:gpu1"
	fl.Devices = append(fl.Devices, d)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), fl)
	k2 := gfoFnKey(gpu2)
	e := c.Check("gpu", "gpu-node-1-gpu1", "GFX-063 second GPU")
	gfxWantSegs(t, "GFX-063 second GPU", e, gfxEdgeStr(k2, gfxKSWD), gfxEdgeStr(gfxKSWD, gfxKSWU), gfxEdgeStr(gfxKSWU, gfxKRPA), gfxEdgeStr(gfxKRPA, gfxKNode))
	n := c.Check("node", gfoNodeName, "GFX-063 node union")
	gfxWantSegs(t, "GFX-063 node union", n, gfxEdgeStr(gfxKGPU, gfxKSWD), gfxEdgeStr(k2, gfxKSWD), gfxEdgeStr(gfxKSWD, gfxKSWU), gfxEdgeStr(gfxKSWU, gfxKRPA), gfxEdgeStr(gfxKRPA, gfxKNode))

	// A cycle: every asset has one outgoing edge and the path returns to F.
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	gfxRemoveEdge(t, a, 0, gfxKSWU, gfxKRPA)
	gfxAddEdge(t, a, 0, gfxKSWU, gfxKGPU)
	cy := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-063/GFX-083 cycle")
	gfxWantSegs(t, "GFX-063 cycle", cy, gfxEdgeStr(gfxKGPU, gfxKSWD), gfxEdgeStr(gfxKSWD, gfxKSWU), gfxEdgeStr(gfxKSWU, gfxKGPU))
	gfxWantLims(t, "GFX-083 cycle", cy, []string{"path_contradiction " + gfxKGPU}, nil)

	// Untrusted hops are not segments but make path_untrusted.
	u := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	p := &u.Frames[0].Payload
	for k, ev := range p.EdgeEvidence {
		if p.Edges[ev.EdgeIndex].FromKey == gfxKSWU {
			p.EdgeEvidence[k].SourceName = "zz-untrusted-parent"
		}
	}
	gfxSeal(t, u, true)
	ue := gfxNewCase(t, bins, u, gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-063 one untrusted hop")
	gfxWantSegs(t, "GFX-063 one untrusted hop", ue, gfxEdgeStr(gfxKGPU, gfxKSWD), gfxEdgeStr(gfxKSWD, gfxKSWU))
	gfxWantLims(t, "GFX-063 one untrusted hop", ue, []string{"path_untrusted " + gfxKGPU}, nil)
}

// gfxNICDegraded degrades the NIC (current 8, both max widths 16) on every
// frame of S-READY; the NIC finding belongs to no device.
func gfxNICDegraded(t *testing.T) *gfoFx {
	t.Helper()
	f := gfxSReady(t)
	for i := 0; i < 3; i++ {
		f.Write(gfxAttr(i, gfoPathNIC, "current_link_width"), "8\n")
	}
	return f
}

// TestGFX064_ActiveFindings: selection by FindingIDs, id order,
// finding_not_applied for unselected findings and finding_source_untrusted.
func TestGFX064_ActiveFindings(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxNICDegraded(t)), gfxDefaultFleet())
	g := c.Check("gpu", gfxDevName, "GFX-064 NIC finding, gpu")
	n := c.Check("node", gfoNodeName, "GFX-064 NIC finding, node")
	rp, err := c.Oracle()
	if err != nil {
		t.Fatalf("GFX-101 oracle: %v", err)
	}
	var nicID string
	for _, fnd := range rp.Frames[rp.R].Findings {
		for _, s := range fnd.Scope {
			if s.Key() == gfxKNIC {
				nicID = fnd.ID
			}
		}
	}
	if nicID == "" {
		t.Fatalf("fixture setup: the NIC degradation produced no finding in the oracle")
	}
	for _, e := range []*gfxExpl{g, n} {
		for _, f := range e.Findings {
			if f.ID == nicID {
				t.Errorf("GFX-064: %s lists the NIC finding %s as active", e.Kind, nicID)
			}
		}
	}
	gfxWantLims(t, "GFX-064 node", n, []string{"finding_not_applied " + nicID}, nil)
	gfxWantLims(t, "GFX-064 gpu (scope lacks the GPU)", g, nil, []string{"finding_not_applied"})

	// Two degraded GPUs: node activeFindings are ordered by id.
	f := gfxSWidth(t)
	gpu2 := "0000:03:00.1"
	uuid2 := "GPU-00000000-0000-4000-8000-000000000002"
	for i := 0; i < 3; i++ {
		f.Dev(i, gfxPCI(append(append([]string{}, gfoPathSWD...), gpu2), "0x030200", "0x10de", "8", "16"))
		f.Write(gfoCSV(i), gfxUUID+", 00000000:03:00.0\n"+uuid2+", 0000:03:00.1\n")
	}
	fl := gfxDefaultFleet()
	d := gfxDefaultDevice()
	d.Name, d.UID, d.UUID, d.EvidenceID = "gpu-node-1-gpu1", "1-uid-gpu1", uuid2, "asset-db:gpu1"
	fl.Devices = append(fl.Devices, d)
	two := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), fl)
	te := two.Check("node", gfoNodeName, "GFX-064 two degraded GPUs")
	gfxWant(t, "GFX-064 two degraded GPUs", "activeFindings", len(te.Findings), 2)
	two.Check("gpu", "gpu-node-1-gpu1", "GFX-064 second degraded GPU")
}

// TestGFX065_Coverage: policy items by name regardless of file order,
// synthetic elements, node elements only for unmet required coverage.
func TestGFX065_Coverage(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSWidth(t))
	fl := gfxDefaultFleet()
	cov := fl.Coverage
	fl.Coverage = []gfxCovReq{cov[3], cov[2], cov[1], cov[0], {"a-optional-parent", "gpu-pcie-parent", false}}
	c := gfxNewCase(t, bins, a, fl)
	e := c.Check("gpu", gfxDevName, "GFX-065 reversed policy")
	gfxWant(t, "GFX-065", "coverage order", gfxCovNames(e), "[a-optional-parent nic-lldp pcie-parent pcie-root pcie-width]")
	if cv, ok := e.Cov("a-optional-parent"); !ok || cv.Required || cv.PathKind != "gpu-pcie-parent" {
		t.Errorf("GFX-065: optional policy item %+v, want required=false pathKind gpu-pcie-parent", cv)
	}
	n := c.Check("node", gfoNodeName, "GFX-065 reversed policy node")
	gfxWant(t, "GFX-065 node", "coverage", gfxCovNames(n), "[pcie-width]")
	ready := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSReady(t)), gfxDefaultFleet())
	rn := ready.Check("node", gfoNodeName, "GFX-065 node all normal")
	gfxWant(t, "GFX-065 node all normal", "coverage", len(rn.Coverage), 0)
	text := gfxOK(t, ready.Run("node", gfoNodeName, "text"), "GFX-065 text")
	if !bytes.Contains(text, []byte("COVERAGE\n  (none)\nALLOCATION\n")) {
		t.Errorf("GFX-075: node text without unmet coverage lacks `  (none)` under COVERAGE")
	}
	// Two devices: node coverage is ordered by (device UID, name) and
	// carries synthetic elements of the unbound device.
	two := gfxNewCase(t, bins, a, gfxTwoDevices())
	te := two.Check("node", gfoNodeName, "GFX-065 two devices node")
	var got []string
	for _, cv := range te.Coverage {
		got = append(got, cv.Device+"/"+cv.Name+"/"+cv.State+"/"+cv.Reason)
	}
	gfxWant(t, "GFX-065 two devices node", "coverage", got, []string{
		"gpu-node-1-spare/pcie-parent/Unknown/UntrustedSource", "gpu-node-1-spare/pcie-root/Unknown/UntrustedSource",
		"gpu-node-1-spare/pcie-width/Unknown/UntrustedSource", gfxDevName + "/pcie-width/Missing/CoverageMissing"})
}

// TestGFX066_Allocation: offline allocation is Unknown with empty lists.
func TestGFX066_Allocation(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSBasic(t)), gfxDefaultFleet())
	e := c.Check("gpu", gfxDevName, "GFX-066")
	alloc, _ := gfxGet(e.Tree, "allocation")
	gfxWant(t, "GFX-066", "allocation", gfxEncode(alloc), `{"state":"Unknown","reason":"AllocationUnknown","evidenceRefs":[],"affectedWorkloads":[]}`)
	text := gfxOK(t, c.Run("gpu", gfxDevName, "text"), "GFX-066 text")
	if !bytes.Contains(text, []byte("\nALLOCATION\n  allocation state=Unknown reason=AllocationUnknown profile=- observed=- expires=-\n  workload (none)\nLIMITATIONS\n")) {
		t.Errorf("GFX-076: ALLOCATION section differs from the offline form")
	}
	nt := gfxOK(t, c.Run("node", gfoNodeName, "text"), "GFX-066 node text")
	if !bytes.Contains(nt, []byte("\nALLOCATION\n  workload (none)\nLIMITATIONS\n")) {
		t.Errorf("GFX-076: node ALLOCATION section must hold only the workload line")
	}
}

// TestGFX067_DeviceSummaries: every device in UID order with decision values.
func TestGFX067_DeviceSummaries(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	fl := gfxDefaultFleet()
	for i, name := range []string{"a-first-name", "z-last-name", "m-middle-name"} {
		d := gfxDefaultDevice()
		d.Name, d.UID, d.UUID, d.EvidenceID = name, []string{"uid-3", "uid-1", "uid-2"}[i], fmt.Sprintf("GPU-00000000-0000-4000-8000-00000000001%d", i), "asset-db:"+name
		d.Generation = int64(10 + i)
		if i == 1 {
			d.Desired = "Retired"
		}
		fl.Devices = append(fl.Devices, d)
	}
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSReady(t)), fl)
	n := c.Check("node", gfoNodeName, "GFX-067")
	var got []string
	for _, d := range n.Devices {
		got = append(got, d.UID+"/"+d.Name+"/"+d.Desired+"/"+d.Generation)
	}
	gfxWant(t, "GFX-067", "deviceSummaries", got, []string{"0b5f3c9a-6e2d-4f8b-a1c7-3d9e5f2a7b41/gpu-node-1-gpu0/InService/1",
		"uid-1/z-last-name/Retired/11", "uid-2/m-middle-name/InService/12", "uid-3/a-first-name/InService/10"})
	gfxWantLims(t, "GFX-067/GFX-080", n, []string{"fence_unavailable"}, nil)
}

// TestGFX068_SortAndDeduplicate: limitations are ordered by (code, subject)
// and repeated conditions appear once.
func TestGFX068_SortAndDeduplicate(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	c := gfxClone(t, a)
	c.Frames[0].Diagnostics = []gfoDiag{{"sysfs_attr_missing", "devices/x/class"}, {"sysfs_attr_missing", "devices/y/vendor"}, {"sysfs_link_invalid", "bus/pci/devices/0000:0f:00.0"}}
	c.Frames[0].DiagnosticsTotal = 3
	c.Raw = gfoRenderArtifact(c)
	fl := gfxDefaultFleet()
	for i := 0; i < 2; i++ {
		d := gfxDefaultDevice()
		d.Name, d.UID, d.UUID, d.EvidenceID, d.Desired = fmt.Sprintf("maint-%d", i), fmt.Sprintf("m-uid-%d", i), fmt.Sprintf("GPU-00000000-0000-4000-8000-00000000002%d", i), "asset-db:m", "Maintenance"
		fl.Devices = append(fl.Devices, d)
	}
	e := gfxNewCase(t, bins, c, fl).Check("node", gfoNodeName, "GFX-068")
	gfxWant(t, "GFX-068", "fence_unavailable count", e.CountLim("fence_unavailable"), 1)
	gfxWant(t, "GFX-068", "nvidia_classification_unknown count", e.CountLim("nvidia_classification_unknown"), 1)
	gfxWant(t, "GFX-068", "collector:sysfs_attr_missing count", e.CountLim("collector:sysfs_attr_missing"), 2)
}

// TestGFX069_Encoding: compact JSON escaping only '"' and '\'.
func TestGFX069_Encoding(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	fl := gfxDefaultFleet()
	fl.Devices[0].UID = `a"b\c<&>/'`
	fl.UID = `f"\`
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSBasic(t)), fl)
	e := c.Check("gpu", gfxDevName, "GFX-069")
	if !bytes.Contains(e.Raw, []byte(`"targetUID":"a\"b\\c<&>/'"`)) {
		t.Errorf("GFX-069: targetUID is not encoded as \"a\\\"b\\\\c<&>/'\": %s", gfoTail(e.Raw))
	}
	text := gfxOK(t, c.Run("gpu", gfxDevName, "text"), "GFX-069 text")
	if !bytes.Contains(text, []byte(` uid=a"b\c<&>/' `)) {
		t.Errorf("GFX-071: text target uid token differs from the decoded value")
	}
	n := c.Check("node", gfoNodeName, "GFX-069 node")
	if !bytes.Contains(n.Raw, []byte(`"uid":"a\"b\\c<&>/'"`)) {
		t.Errorf("GFX-069: deviceSummaries uid is not encoded with only '\"' and '\\' escaped")
	}
}

// TestGFX070_TextIsFunctionOfJSON: text equals its reconstruction from the
// JSON output across scenario shapes (asserted inside Check).
func TestGFX070_TextIsFunctionOfJSON(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	for name, f := range map[string]*gfoFx{"S-BASIC": gfxSBasic(t), "S-WIDTH": gfxSWidth(t), "S-PARTIAL0": gfxSPartial0(t),
		"S-CONTRA": gfxSContra(t), "S-LASTPARTIAL": gfxSLastPartial(t), "NIC degraded": gfxNICDegraded(t)} {
		c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxTwoDevices())
		c.Check("gpu", gfxDevName, "GFX-070 "+name+" gpu")
		c.Check("gpu", "gpu-node-1-spare", "GFX-070 "+name+" spare gpu")
		c.Check("node", gfoNodeName, "GFX-070 "+name+" node")
	}
}

// TestGFX071_TokenEscaping: '%' and bytes outside 0x21..0x7e become %XX, a
// value of exactly "-" becomes %2D, omitted and empty values are "-".
func TestGFX071_TokenEscaping(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxSBasic(t)
	f.M.NodeName = "-"
	f.Mkdir(gfoSys(0) + "/bus/pci/devices")
	f.Write(gfoSys(0)+"/bus/pci/devices/odd%name x", "")
	fl := gfxDefaultFleet()
	fl.Devices[0].NodeName = "-"
	fl.Devices[0].UID = "%41-"
	fl.Coverage = append(fl.Coverage, gfxCovReq{"-", "gpu-pcie-parent", true})
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), fl)
	e := c.Check("gpu", gfxDevName, "GFX-071")
	text := string(gfxOK(t, c.Run("gpu", gfxDevName, "text"), "GFX-071 text"))
	for _, want := range []string{" uid=%2541- ", " node=%2D ", "\n  coverage %2D pathKind=gpu-pcie-parent ", " serial=- "} {
		if !strings.Contains(text, want) {
			t.Errorf("GFX-071: text lacks %q", want)
		}
	}
	var sub string
	for _, l := range e.Limitations {
		if l.Code == "collector:sysfs_entry_name_invalid" {
			sub = l.Subject
		}
	}
	if sub == "" || !strings.Contains(sub, "%") {
		t.Errorf("fixture setup: expected a %%-escaped collector subject, got %q", sub)
	} else if !strings.Contains(text, "  collector:sysfs_entry_name_invalid "+strings.ReplaceAll(sub, "%", "%25")+"\n") {
		t.Errorf("GFX-071: collector subject %q is not re-escaped with %%25 in text", sub)
	}
	for _, l := range gfxTextRules([]byte(text)) {
		t.Errorf("GFX-071: %s", l)
	}
}

// TestGFX072_FirstLine: first line of gpu and node with mode=offline and "-"
// for an absent revision.
func TestGFX072_FirstLine(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	w := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSWidth(t)), gfxDefaultFleet())
	g := w.Check("gpu", gfxDevName, "GFX-072")
	first := func(b []byte) string { return strings.SplitN(string(b), "\n", 2)[0] }
	gfxWant(t, "GFX-072 gpu", "first line", first(gfxOK(t, w.Run("gpu", gfxDevName, "text"), "GFX-072")),
		"gpu/gpu-node-1-gpu0 phase=Degraded qualification=Disqualified revision="+g.GraphRevision+" reason=Degraded mode=offline")
	gfxWant(t, "GFX-072 node", "first line", first(gfxOK(t, w.Run("node", gfoNodeName, "text"), "GFX-072")),
		"node/gpu-node-1 eligibility=Ineligible qualification=Disqualified revision="+g.GraphRevision+" eligibilityReason=Degraded mode=offline")
	p := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSPartial0(t)), gfxDefaultFleet())
	p.Check("gpu", gfxDevName, "GFX-072 A empty")
	gfxWant(t, "GFX-072 A empty", "first line", first(gfxOK(t, p.Run("gpu", gfxDevName, "text"), "GFX-072")),
		"gpu/gpu-node-1-gpu0 phase=Pending qualification=Unknown revision=- reason=UnadmittedSnapshot mode=offline")
}

// TestGFX073_PathSection: target, identity, device and segment rows with
// their (none) forms.
func TestGFX073_PathSection(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	empty := gfxDefaultFleet()
	empty.Devices = nil
	ec := gfxNewCase(t, bins, a, empty)
	ec.Check("node", gfoNodeName, "GFX-073 no devices")
	text := string(gfxOK(t, ec.Run("node", gfoNodeName, "text"), "GFX-073"))
	if !strings.Contains(text, "\nPATH\n  target uid="+gfoNodeUID+" assessment=") || !strings.Contains(text, "\n  device (none)\n  segment (none)\nFINDINGS\n  (none)\n") {
		t.Errorf("GFX-073/GFX-074: node text without devices:\n%s", text)
	}
	f := gfxSBasic(t)
	gfxWithoutSources(f, "NVIDIAUUIDBinding")
	uc := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet())
	uc.Check("gpu", gfxDevName, "GFX-073 unbound")
	ut := string(gfxOK(t, uc.Run("gpu", gfxDevName, "text"), "GFX-073 unbound"))
	want := "\n  identity state=Unknown reason=UntrustedSource vendor=- uuid=- serial=- nodeUID=- bootID=- bdf=- function=- sourceType=- sourceName=- evidence=- observed=- expires=-\n  segment (none)\nFINDINGS\n"
	if !strings.Contains(ut, want) {
		t.Errorf("GFX-073: unbound identity/segment rows differ:\n%s", ut)
	}
}

// TestGFX074_FindingsSection: finding rows with scope, evidence and the
// optional explanation rows (asserted by the normative example in
// TestGFX102_SWidth); no finding gives one (none) row.
func TestGFX074_FindingsSection(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSWidth(t)), gfxDefaultFleet())
	c.Check("gpu", gfxDevName, "GFX-074")
	text := string(gfxOK(t, c.Run("gpu", gfxDevName, "text"), "GFX-074"))
	sec := gfxSection(text, "FINDINGS", "COVERAGE")
	lines := strings.Split(strings.TrimSuffix(sec, "\n"), "\n")
	var kinds []string
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) > 0 {
			kinds = append(kinds, f[0])
		}
	}
	gfxWant(t, "GFX-074", "row kinds", kinds, []string{"finding", "scope", "scope", "scope", "evidence", "evidence", "evidence", "evidence", "evidence", "evidence", "explanation", "suggestedStep"})
}

// gfxSection returns the lines between two section heads.
func gfxSection(text, from, to string) string {
	i := strings.Index(text, "\n"+from+"\n")
	j := strings.Index(text, "\n"+to+"\n")
	if i < 0 || j < 0 || j < i {
		return ""
	}
	return text[i+len(from)+2 : j+1]
}

// TestGFX075_CoverageSection: node rows carry device=, every evidence ref
// has a row.
func TestGFX075_CoverageSection(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSWidth(t)), gfxDefaultFleet())
	n := c.Check("node", gfoNodeName, "GFX-075")
	text := string(gfxOK(t, c.Run("node", gfoNodeName, "text"), "GFX-075"))
	sec := gfxSection(text, "COVERAGE", "ALLOCATION")
	if !strings.HasPrefix(sec, "  coverage pcie-width device=gpu-node-1-gpu0 pathKind=gpu-pcie-link-width-normal required=true state=Missing reason=CoverageMissing ") {
		t.Errorf("GFX-075: node coverage row differs: %q", sec)
	}
	if len(n.Coverage) == 1 {
		gfxWant(t, "GFX-075", "evidence rows", strings.Count(sec, "\n    evidence "), len(n.Coverage[0].EvidenceRefs))
	}
}

// TestGFX076_AllocationSection is covered by TestGFX066_Allocation; this
// test asserts the gpu/node difference of the section once more on S-WIDTH.
func TestGFX076_AllocationSection(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSWidth(t)), gfxDefaultFleet())
	g := gfxSection(string(gfxOK(t, c.Run("gpu", gfxDevName, "text"), "GFX-076")), "ALLOCATION", "LIMITATIONS")
	n := gfxSection(string(gfxOK(t, c.Run("node", gfoNodeName, "text"), "GFX-076")), "ALLOCATION", "LIMITATIONS")
	gfxWant(t, "GFX-076 gpu", "section", g, "  allocation state=Unknown reason=AllocationUnknown profile=- observed=- expires=-\n  workload (none)\n")
	gfxWant(t, "GFX-076 node", "section", n, "  workload (none)\n")
}

// TestGFX077_LimitationsSection: one row per limitation in JSON order and no
// truncated row when nothing is truncated (the truncated row is asserted by
// the GFX-090 tests).
func TestGFX077_LimitationsSection(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSPartial0(t)), gfxDefaultFleet())
	e := c.Check("gpu", gfxDevName, "GFX-077")
	text := string(gfxOK(t, c.Run("gpu", gfxDevName, "text"), "GFX-077"))
	i := strings.Index(text, "\nLIMITATIONS\n")
	if i < 0 {
		t.Fatalf("GFX-077: no LIMITATIONS section")
	}
	rows := strings.Split(strings.TrimSuffix(text[i+len("\nLIMITATIONS\n"):], "\n"), "\n")
	var want []string
	for _, l := range e.Limitations {
		want = append(want, "  "+gfxTok(l.Code, true)+map[bool]string{true: " " + gfxTok(l.Subject, true), false: ""}[l.HasSubject])
	}
	gfxWant(t, "GFX-077", "rows", rows, want)
}

// TestGFX078_ReasonsNeverOmitted: the reasons of the verdict, identity,
// coverage and allocation are present in every state.
func TestGFX078_ReasonsNeverOmitted(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	for name, f := range map[string]*gfoFx{"S-PARTIAL0": gfxSPartial0(t), "S-LASTPARTIAL": gfxSLastPartial(t), "S-BASIC": gfxSBasic(t)} {
		c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet())
		e := c.Check("gpu", gfxDevName, "GFX-078 "+name)
		if e.Reason == "" || e.Identity["reason"] == "" || e.Allocation["reason"] == "" || len(e.Coverage) != 4 {
			t.Errorf("GFX-078 %s: reason %q identity.reason %q allocation.reason %q coverage %d", name, e.Reason, e.Identity["reason"], e.Allocation["reason"], len(e.Coverage))
		}
		text := string(gfxOK(t, c.Run("gpu", gfxDevName, "text"), "GFX-078"))
		if strings.Contains(text, " reason=- ") || strings.Contains(text, " reason=-\n") {
			t.Errorf("GFX-078 %s: a reason is rendered as '-'", name)
		}
		n := c.Check("node", gfoNodeName, "GFX-078 "+name+" node")
		if n.EligibilityReason == "" {
			t.Errorf("GFX-078 %s: node eligibilityReason empty", name)
		}
	}
}
