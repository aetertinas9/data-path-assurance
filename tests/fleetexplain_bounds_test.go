package tests_test

// Bounds, truncation and the separation of truncation from judgement
// (GFX-090, GFX-091; GFX-068 totals; GFX-077 truncated row; GFX-083 under
// truncation).

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func gfxUUIDn(n int) string { return fmt.Sprintf("GPU-%08x-0000-4000-8000-%012x", n, n) }

// gfxManyGPUs builds n GPUs, each directly under its own root port with an
// operator baseline of 16 lanes and the given current width, on `frames`
// frames; it returns the fixture and the matching fleet file.
func gfxManyGPUs(t *testing.T, n, frames int, current string) (*gfoFx, gfxFleet) {
	t.Helper()
	f := gfoNewFx(t, frames)
	f.M.Profile = gfxProfileWidth
	fl := gfxDefaultFleet()
	fl.Devices = nil
	var csv strings.Builder
	for j := 0; j < n; j++ {
		rp := fmt.Sprintf("0000:00:%02x.%d", 1+j/8, j%8)
		gpu := fmt.Sprintf("0000:%02x:00.0", j+1)
		f.M.Baselines = append(f.M.Baselines, gfoBL{gpu, rp, "16"})
		fmt.Fprintf(&csv, "%s, 00000000:%02X:00.0\n", gfxUUIDn(j+1), j+1)
		for i := 0; i < frames; i++ {
			f.Dev(i, gfxPCI([]string{gfoHB, rp}, "0x060400", "0x8086", "16", "16"))
			f.Dev(i, gfxPCI([]string{gfoHB, rp, gpu}, "0x030200", "0x10de", current, "16"))
		}
		d := gfxDefaultDevice()
		d.Name, d.UID, d.UUID, d.EvidenceID = fmt.Sprintf("gpu-%03d", j), fmt.Sprintf("uid-%03d", j), gfxUUIDn(j+1), fmt.Sprintf("asset-db:gpu-%03d", j)
		fl.Devices = append(fl.Devices, d)
	}
	for i := 0; i < frames; i++ {
		f.Write(gfoCSV(i), csv.String())
	}
	return f, fl
}

// gfxChains builds n GPUs, each under a private switch pair and root port in
// its own PCI domain (4 observed hops per GPU), on one frame.
func gfxChains(t *testing.T, n int) (*gfoFx, gfxFleet) {
	t.Helper()
	f := gfoNewFx(t, 1)
	fl := gfxDefaultFleet()
	fl.Devices = nil
	var csv strings.Builder
	for j := 0; j < n; j++ {
		dom := fmt.Sprintf("%04x", j)
		hb := "pci" + dom + ":00"
		rp, swu, swd, gpu := dom+":00:01.0", dom+":01:00.0", dom+":02:00.0", dom+":03:00.0"
		f.Dev(0, gfxPCI([]string{hb, rp}, "0x060400", "0x8086", "16", "16"))
		f.Dev(0, gfxPCI([]string{hb, rp, swu}, "0x060400", "0x10b5", "16", "16"))
		f.Dev(0, gfxPCI([]string{hb, rp, swu, swd}, "0x060400", "0x10b5", "16", "16"))
		f.Dev(0, gfxPCI([]string{hb, rp, swu, swd, gpu}, "0x030200", "0x10de", "16", "16"))
		fmt.Fprintf(&csv, "%s, %08X:03:00.0\n", gfxUUIDn(j+1), j)
		d := gfxDefaultDevice()
		d.Name, d.UID, d.UUID, d.EvidenceID = fmt.Sprintf("chain-%03d", j), fmt.Sprintf("uid-%03d", j), gfxUUIDn(j+1), fmt.Sprintf("asset-db:chain-%03d", j)
		fl.Devices = append(fl.Devices, d)
	}
	f.Write(gfoCSV(0), csv.String())
	return f, fl
}

// TestGFX090_FindingAndCoverageTruncation: 65 degraded GPUs give 65 active
// findings (bound 64) and 65 unmet coverage elements (bound 32) for a node
// request; totals keep the pre-truncation counts.
func TestGFX090_FindingAndCoverageTruncation(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	holdHeavyFixture(t)
	f, fl := gfxManyGPUs(t, 65, 3, "8")
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), fl)
	const cl = "GFX-090 65 degraded GPUs"
	n := c.Check("node", gfoNodeName, cl)
	gfxWant(t, cl, "truncated", n.Truncated, true)
	gfxWant(t, cl, "activeFindings len/total", fmt.Sprintf("%d/%d", len(n.Findings), n.Totals["activeFindings"]), "64/65")
	gfxWant(t, cl, "coverage len/total", fmt.Sprintf("%d/%d", len(n.Coverage), n.Totals["coverage"]), "32/65")
	gfxWant(t, cl, "deviceSummaries", len(n.Devices), 65)
	if len(n.Coverage) == 32 {
		gfxWant(t, cl+" GFX-068", "last kept coverage device", n.Coverage[31].Device, "gpu-031")
	}
	lim := gfxLimitationsBody(t, string(n.Text), cl)
	wantRow := fmt.Sprintf("  truncated pathSegments=%d activeFindings=65 coverage=65 affectedWorkloads=0 deviceSummaries=65 evidenceRefs=%d limitations=%d\n",
		n.Totals["pathSegments"], n.Totals["evidenceRefs"], n.Totals["limitations"])
	if !strings.HasPrefix(lim, wantRow) {
		t.Errorf("%s: GFX-077: LIMITATIONS does not start with %q", cl, wantRow)
	}
	g := c.Check("gpu", "gpu-064", cl+" gpu")
	gfxWant(t, cl+" gpu", "truncated/findings", fmt.Sprintf("%v/%d", g.Truncated, len(g.Findings)), "false/1")

	// GFX-091: judgement uses every device; truncation is deterministic.
	gfxWant(t, cl+" GFX-091", "node qualification/eligibility", n.Qualification+"/"+n.NodeEligibility, "Disqualified/Ineligible")
	for _, d := range n.Devices {
		if d.Qualification != "Disqualified" || d.Phase != "Degraded" {
			t.Errorf("%s GFX-091: device %s = %s/%s, want Disqualified/Degraded", cl, d.Name, d.Qualification, d.Phase)
			break
		}
	}
	again := gfxOK(t, c.Run("node", gfoNodeName, "json"), cl+" repeat")
	if !bytes.Equal(again, n.Raw) {
		t.Errorf("%s GFX-091/GFX-004: repeated run differs", cl)
	}
}

// TestGFX090_PathSegmentsTruncation: 129 private chains give 516 trusted
// hops (bound 512); a contradiction whose branch is cut keeps its
// limitation (GFX-083).
func TestGFX090_PathSegmentsTruncation(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	holdHeavyFixture(t)
	f, fl := gfxChains(t, 129)
	a := gfxAgentArtifact(t, bins.Agent, f)
	c := gfxNewCase(t, bins, a, fl)
	const cl = "GFX-090 129 chains"
	n := c.Check("node", gfoNodeName, cl)
	gfxWant(t, cl, "pathSegments len/total/truncated", fmt.Sprintf("%d/%d/%v", len(n.Segments), n.Totals["pathSegments"], n.Truncated), "512/516/true")
	if len(n.Segments) == 512 {
		gfxWant(t, cl+" GFX-068", "first segment", n.Segments[0].From, gfoFnKey("0000:03:00.0"))
		gfxWant(t, cl+" GFX-068", "last kept segment", n.Segments[511].From+" -> "+n.Segments[511].To, gfoRPKey("007c:00:01.0")+" -> "+gfxKNode)
	}
	g := c.Check("gpu", "chain-128", cl+" gpu")
	gfxWant(t, cl+" gpu", "segments/truncated", fmt.Sprintf("%d/%v", len(g.Segments), g.Truncated), "4/false")

	x := gfxClone(t, a)
	from, to := gfoRPKey("0080:00:01.0"), gfoSWKey("0000:01:00.0")
	gfxAddEdge(t, x, 0, from, to)
	xc := gfxNewCase(t, bins, x, fl)
	xn := xc.Check("node", gfoNodeName, cl+" with a cut contradiction")
	gfxWant(t, cl+" with a cut contradiction", "pathSegments total", xn.Totals["pathSegments"], 517)
	gfxWantLims(t, cl+" GFX-083", xn, []string{"path_contradiction " + gfoFnKey("0080:03:00.0")}, nil)
	for _, s := range xn.Segments {
		if s.From == from && s.To == to {
			t.Errorf("%s: the contradictory branch %s -> %s sorts last and must be cut", cl, from, to)
		}
	}
}

// gfxManyNICs adds 2048 degraded NIC functions under eight root ports to
// every frame of S-READY; each NIC gets a finding that no device selects.
func gfxManyNICs(t *testing.T) *gfoFx {
	t.Helper()
	f := gfxSReady(t)
	for i := 0; i < 3; i++ {
		for r := 0; r < 8; r++ {
			rp := fmt.Sprintf("0000:00:10.%d", r)
			f.Dev(i, gfxPCI([]string{gfoHB, rp}, "0x060400", "0x8086", "16", "16"))
			for k := 0; k < 256; k++ {
				nic := fmt.Sprintf("0000:%02x:%02x.%d", 0x20+r, k/8, k%8)
				f.Dev(i, gfxPCI([]string{gfoHB, rp, nic}, "0x020000", "0x15b3", "8", "16"))
			}
		}
	}
	return f
}

// TestGFX090_LimitationsTruncation: 2048 unselected findings push a node
// request past 2048 limitations; the first line keeps mode=offline.
func TestGFX090_LimitationsTruncation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("GFX-090: large fixture (2048 NIC functions x 3 frames) skipped in -short mode")
	}
	bins := gfxBuild(t, true)
	holdHeavyFixture(t)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxManyNICs(t)), gfxDefaultFleet())
	const cl = "GFX-090 2048 unselected findings"
	n := c.Check("node", gfoNodeName, cl)
	gfxWant(t, cl, "limitations len/truncated", fmt.Sprintf("%d/%v", len(n.Limitations), n.Truncated), "2048/true")
	if n.Totals["limitations"] <= 2048 {
		t.Errorf("%s: totalCounts.limitations %d, want > 2048", cl, n.Totals["limitations"])
	}
	gfxWant(t, cl, "finding_not_applied kept", n.CountLim("finding_not_applied"), 2047)
	if first := strings.SplitN(string(n.Text), "\n", 2)[0]; !strings.HasSuffix(first, " mode=offline") {
		t.Errorf("%s GFX-072: first line %q must end with mode=offline even when the offline limitation is cut", cl, first)
	}
	g := c.Check("gpu", gfxDevName, cl+" gpu")
	gfxWant(t, cl+" gpu", "truncated", g.Truncated, false)
	gfxWantLims(t, cl+" gpu", g, nil, []string{"finding_not_applied"})
}

// TestGFX090_DeviceBounds: 256 devices are accepted and all summarized; 257
// are rejected; unbound devices' synthetic coverage is truncated at 32.
func TestGFX090_DeviceBounds(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	build := func(n int) gfxFleet {
		fl := gfxDefaultFleet()
		for j := 1; j < n; j++ {
			d := gfxDefaultDevice()
			d.Name, d.UID, d.UUID, d.EvidenceID = fmt.Sprintf("spare-%03d", j), fmt.Sprintf("spare-uid-%03d", j), gfxUUIDn(0x1000+j), "asset-db:spare"
			fl.Devices = append(fl.Devices, d)
		}
		return fl
	}
	c := gfxNewCase(t, bins, a, build(256))
	const cl = "GFX-090 256 devices"
	n := c.Check("node", gfoNodeName, cl)
	gfxWant(t, cl, "deviceSummaries len/total", fmt.Sprintf("%d/%d", len(n.Devices), n.Totals["deviceSummaries"]), "256/256")
	gfxWant(t, cl, "coverage len/total/truncated", fmt.Sprintf("%d/%d/%v", len(n.Coverage), n.Totals["coverage"], n.Truncated), "32/765/true")
	c.Check("gpu", "spare-255", cl+" gpu")
	in := gfxWriteInputs(t, a.Raw, build(257).JSON())
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("node", gfoNodeName, in, "")...), 7, "GFX-090 257 devices")
	over := gfxDefaultFleet()
	for i := len(over.Coverage); i < 33; i++ {
		over.Coverage = append(over.Coverage, gfxCovReq{fmt.Sprintf("extra-%02d", i), "nic-lldp-remote", false})
	}
	in = gfxWriteInputs(t, a.Raw, over.JSON())
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "")...), 7, "GFX-090 33 coverage requirements")
}

// TestGFX091_TruncationIsNotJudgement: with pathSegments truncated, the
// devices whose root-port hops are cut from the output keep verdicts computed
// from the whole bundle (no unmet coverage, same verdict as their own gpu
// request), and the truncation is deterministic.
func TestGFX091_TruncationIsNotJudgement(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	holdHeavyFixture(t)
	f, fl := gfxChains(t, 129)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), fl)
	n := c.Check("node", gfoNodeName, "GFX-091 129 chains")
	if !n.Truncated {
		t.Fatalf("GFX-091: the fixture must truncate pathSegments")
	}
	gfxWant(t, "GFX-091", "node coverage (all devices fully covered)", len(n.Coverage), 0)
	for _, name := range []string{"chain-125", "chain-128"} {
		g := c.Check("gpu", name, "GFX-091 "+name)
		var sum gfxDevSum
		for _, d := range n.Devices {
			if d.Name == name {
				sum = d
			}
		}
		gfxWant(t, "GFX-091 "+name, "node summary vs gpu verdict", sum.Phase+"/"+sum.Qualification+"/"+sum.Reason, g.Phase+"/"+g.Qualification+"/"+g.Reason)
		for _, cv := range g.Coverage {
			if cv.Required && cv.State != "Normal" {
				t.Errorf("GFX-091 %s: coverage %s is %s although the full bundle covers it", name, cv.Name, cv.State)
			}
		}
	}
	for i := 0; i < 2; i++ {
		again := gfxOK(t, c.Run("node", gfoNodeName, "json"), "GFX-091 repeat")
		if !bytes.Equal(again, n.Raw) {
			t.Errorf("GFX-091: repeated run %d differs (truncation must be deterministic)", i)
		}
	}
}
