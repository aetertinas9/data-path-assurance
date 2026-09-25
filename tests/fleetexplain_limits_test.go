package tests_test

// Reachable truncation of coverage evidenceRefs (GFX-090, GFX-068, GFX-077)
// and the payload count and canonical-length bounds of GFX-033 (a) at the
// bound and one past it.

import (
	"fmt"
	"strings"
	"testing"
)

// gfxChainBDF is the BDF of the i-th synthetic switch of a long chain.
func gfxChainBDF(i int) string { return fmt.Sprintf("0000:%02x:%02x.0", 0x10+i/32, i%32) }

// gfxAddEdges appends observed edges (each with a trusted parent-source
// edgeEvidence at the frame time) to frame i, restores the payload order and
// reseals the artifact once.
func gfxAddEdges(t *testing.T, a *gfoArtifact, i int, pairs [][2]string) {
	t.Helper()
	fr := &a.Frames[i]
	p := &fr.Payload
	exp := gfoAddSeconds(t, fr.ObservedAt, 300)
	if len(p.EdgeEvidence) > 0 {
		exp = p.EdgeEvidence[0].ExpiresAt
	}
	for _, pr := range pairs {
		p.Edges = append(p.Edges, gfoEdge{FromKey: pr[0], Relation: "LOCATED_IN", ToKey: pr[1], Origin: "Observed"})
		p.EdgeEvidence = append(p.EdgeEvidence, gfoEdgeEvidence{
			EdgeIndex: len(p.Edges) - 1, Kind: "Observed", SourceType: "agent", SourceName: gfoParentSrc,
			ObservedAt: fr.ObservedAt, ExpiresAt: exp,
		})
	}
	gfxSortPayload(p)
	gfxSeal(t, a, true)
}

// gfxUniq returns the sorted distinct items.
func gfxUniq(items []string) []string {
	out := []string{}
	for _, s := range gfxSorted(items) {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// gfxLimitationsBody returns the text after the LIMITATIONS header.
func gfxLimitationsBody(t *testing.T, text, clause string) string {
	t.Helper()
	const h = "\nLIMITATIONS\n"
	i := strings.Index(text, h)
	if i < 0 {
		t.Fatalf("%s: GFX-077: text output has no LIMITATIONS section", clause)
	}
	return text[i+len(h):]
}

// TestGFX090_CoverageEvidenceRefsTruncation: 300 switches between the
// switch upstream port and the root port give the pcie-root coverage more
// than 256 evidence IDs; the list keeps the first 256 in byte order,
// truncated is true, totalCounts.evidenceRefs counts the lengths before
// truncation and the text LIMITATIONS section starts with the truncated row.
func TestGFX090_CoverageEvidenceRefsTruncation(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	holdHeavyFixture(t)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	const n = 300
	p := &a.Frames[0].Payload
	for i := 0; i < n; i++ {
		p.Assets = append(p.Assets, gfoAsset{"PCIeSwitch", "pci-bdf:" + gfxChainBDF(i), []gfoAlias{}})
	}
	gfxRemoveEdge(t, a, 0, gfxKSWU, gfxKRPA)
	pairs := [][2]string{{gfxKSWU, gfoSWKey(gfxChainBDF(0))}}
	for i := 1; i < n; i++ {
		pairs = append(pairs, [2]string{gfoSWKey(gfxChainBDF(i - 1)), gfoSWKey(gfxChainBDF(i))})
	}
	pairs = append(pairs, [2]string{gfoSWKey(gfxChainBDF(n - 1)), gfxKRPA})
	gfxAddEdges(t, a, 0, pairs)

	c := gfxNewCase(t, bins, a, gfxDefaultFleet())
	const cl = "GFX-090 coverage evidenceRefs (300-switch chain)"
	rp, err := c.Oracle()
	if err != nil {
		t.Fatalf("%s: GFX-101 oracle could not replay the inputs: %v", cl, err)
	}
	pre := map[string][]string{}
	for _, cv := range rp.Dec[0].Coverage {
		pre[cv.Name] = gfxUniq(cv.EvidenceIDs)
	}
	want := pre["pcie-root"]
	if len(want) <= 256 {
		t.Fatalf("%s: fixture precondition: the GFX-101 oracle gives %d pcie-root evidence IDs, want more than 256", cl, len(want))
	}

	e := c.Check("gpu", gfxDevName, cl)
	gfxWant(t, cl, "truncated", e.Truncated, true)
	gfxWant(t, cl+" (no pathSegments truncation)", "pathSegments len/total", fmt.Sprintf("%d/%d", len(e.Segments), e.Totals["pathSegments"]), fmt.Sprintf("%d/%d", n+4, n+4))
	cv, ok := e.Cov("pcie-root")
	if !ok {
		t.Fatalf("%s: coverage pcie-root missing", cl)
	}
	gfxWant(t, cl, "pcie-root evidenceRefs length", len(cv.EvidenceRefs), 256)
	if len(cv.EvidenceRefs) == 256 && strings.Join(cv.EvidenceRefs, ",") != strings.Join(want[:256], ",") {
		t.Errorf("%s: GFX-068: kept pcie-root evidenceRefs are not the first 256 distinct IDs in byte order", cl)
	}
	for i := 1; i < len(cv.EvidenceRefs); i++ {
		if cv.EvidenceRefs[i-1] >= cv.EvidenceRefs[i] {
			t.Errorf("%s: GFX-068: evidenceRefs not strictly increasing at %d: %q, %q", cl, i, cv.EvidenceRefs[i-1], cv.EvidenceRefs[i])
			break
		}
	}
	sum := 0
	for _, f := range e.Findings {
		sum += len(f.Evidence)
	}
	for _, x := range e.Coverage {
		sum += len(pre[x.Name])
	}
	sum += len(e.AllocationEvidence)
	gfxWant(t, cl+" GFX-068", "totalCounts.evidenceRefs (lengths before truncation)", e.Totals["evidenceRefs"], sum)

	row := fmt.Sprintf("  truncated pathSegments=%d activeFindings=%d coverage=%d affectedWorkloads=0 deviceSummaries=0 evidenceRefs=%d limitations=%d\n",
		n+4, e.Totals["activeFindings"], e.Totals["coverage"], sum, e.Totals["limitations"])
	if body := gfxLimitationsBody(t, string(e.Text), cl); !strings.HasPrefix(body, row) {
		t.Errorf("%s: GFX-077: LIMITATIONS does not start with %q:\n%s", cl, row, gfoTail([]byte(body)))
	}
}

// ---------------------------------------------------------------------------
// GFX-033 (a) count and canonical-length bounds.
// ---------------------------------------------------------------------------

// gfxWithEdges returns a copy of the one-frame artifact a whose frame 0 has
// exactly total edges and as many edgeEvidence. The extra edges leave
// synthetic switch assets that no GPU function reaches, three per asset.
func gfxWithEdges(t *testing.T, a *gfoArtifact, total int) *gfoArtifact {
	t.Helper()
	c := gfxClone(t, a)
	fr := &c.Frames[0]
	p := &fr.Payload
	exp := p.EdgeEvidence[0].ExpiresAt
	targets := []string{gfxKNode, gfxKRPA, gfxKRPB}
	for k := 0; len(p.Edges) < total; k++ {
		canonical := fmt.Sprintf("pci-bdf:0001:%02x:%02x.%d", k/256, (k/8)%32, k%8)
		p.Assets = append(p.Assets, gfoAsset{"PCIeSwitch", canonical, []gfoAlias{}})
		for _, to := range targets {
			if len(p.Edges) == total {
				break
			}
			p.Edges = append(p.Edges, gfoEdge{FromKey: "PCIeSwitch/" + canonical, Relation: "LOCATED_IN", ToKey: to, Origin: "Observed"})
			p.EdgeEvidence = append(p.EdgeEvidence, gfoEdgeEvidence{
				EdgeIndex: len(p.Edges) - 1, Kind: "Observed", SourceType: "agent", SourceName: gfoParentSrc,
				ObservedAt: fr.ObservedAt, ExpiresAt: exp,
			})
		}
	}
	if len(p.Assets) > 4096 {
		t.Fatalf("test bug: %d assets exceed the asset bound", len(p.Assets))
	}
	gfxSortPayload(p)
	gfxSeal(t, c, true)
	return c
}

// gfxPadObservation returns a string observation of the GPU function with
// signal test.pad.<k> (no width or class signal) and a value of size bytes;
// its ID is already the GFO-084 value for frame 0.
func gfxPadObservation(t *testing.T, a *gfoArtifact, k, size int) gfoObs {
	t.Helper()
	fr := a.Frames[0]
	o := fr.Payload.Observations[gfxObsIndex(t, a, 0, gfxKGPU, gfoSigClass)]
	o.Signal = fmt.Sprintf("test.pad.%05d", k)
	o.ValueKind, o.ValueJSON = "string", gfoQuoteOut(strings.Repeat("v", size))
	o.Unit = "text"
	o.Dims = []gfoKV{}
	o.ID = gfoObservationID(fr.Session, fr.Sequence, o.Subject.Key(), o.Signal)
	return o
}

// gfxWithObservations returns a copy whose frame 0 has exactly total
// observations (the extra ones are one-byte padding observations).
func gfxWithObservations(t *testing.T, a *gfoArtifact, total int) *gfoArtifact {
	t.Helper()
	c := gfxClone(t, a)
	p := &c.Frames[0].Payload
	for k := 0; len(p.Observations) < total; k++ {
		p.Observations = append(p.Observations, gfxPadObservation(t, a, k, 1))
	}
	gfxSortPayload(p)
	gfxSeal(t, c, true)
	return c
}

// gfxWithBindings returns a copy whose frame 0 has exactly total gpuBindings;
// each extra binding has its own UUID and its own PCIeFunction asset.
func gfxWithBindings(t *testing.T, a *gfoArtifact, total int) *gfoArtifact {
	t.Helper()
	c := gfxClone(t, a)
	p := &c.Frames[0].Payload
	tmpl := p.GPUBindings[0]
	for k := 0; len(p.GPUBindings) < total; k++ {
		bdf := fmt.Sprintf("0000:%02x:%02x.%d", 0x40+k/256, (k/8)%32, k%8)
		p.Assets = append(p.Assets, gfoAsset{"PCIeFunction", "pci-bdf:" + bdf, []gfoAlias{}})
		b := tmpl
		b.BDF, b.UUID = bdf, gfxUUIDn(0x2000+k)
		p.GPUBindings = append(p.GPUBindings, b)
	}
	gfxSortPayload(p)
	gfxSeal(t, c, true)
	return c
}

// gfxCPLen returns the GFO-086 canonical payload length.
func gfxCPLen(t *testing.T, p gfoPayload) int {
	t.Helper()
	b, err := gfoCanonicalPayload(p)
	if err != nil {
		t.Fatalf("test bug: canonical payload: %v", err)
	}
	return len(b)
}

// gfxWithCPLength returns a copy whose frame 0 canonical payload (GFO-086) is
// exactly target bytes long, padded with string observations of at most 4096
// bytes each.
func gfxWithCPLength(t *testing.T, a *gfoArtifact, target int) *gfoArtifact {
	t.Helper()
	c := gfxClone(t, a)
	p := &c.Frames[0].Payload
	base := gfxCPLen(t, *p)
	probe := *p
	probe.Observations = append(append([]gfoObs{}, p.Observations...), gfxPadObservation(t, a, 0, 0))
	o0 := gfxCPLen(t, probe) - base // bytes of one padding observation with an empty value
	rem := target - base
	if o0 <= 0 || o0 > 4096 || rem < o0 {
		t.Fatalf("test bug: padding overhead %d, remaining %d", o0, rem)
	}
	var sizes []int
	for rem > 0 {
		switch {
		case rem >= 2*o0+4096:
			sizes = append(sizes, 4096)
			rem -= o0 + 4096
		case rem <= o0+4096:
			sizes = append(sizes, rem-o0)
			rem = 0
		default:
			sizes = append(sizes, rem-2*o0)
			rem = o0
		}
	}
	for k, s := range sizes {
		p.Observations = append(p.Observations, gfxPadObservation(t, a, k, s))
	}
	gfxSortPayload(p)
	gfxSeal(t, c, true)
	if got := gfxCPLen(t, c.Frames[0].Payload); got != target {
		t.Fatalf("test bug: canonical payload is %d bytes, want %d", got, target)
	}
	if len(p.Observations) > 32768 || len(c.Raw) > gfxArtifactMax {
		t.Fatalf("test bug: padding exceeds another bound (%d observations, %d artifact bytes)", len(p.Observations), len(c.Raw))
	}
	return c
}

// TestGFX033_PayloadCountBounds: one past each GFO-095 count (edges with as
// many edgeEvidence, observations, gpuBindings) and one past the frame CP
// length is rejected with exit 6 and an empty stdout; the same constructions
// at the bound (or, for observations, at a small count) are accepted. The
// accepted edge and CP cases at the exact bound are skipped in -short mode.
func TestGFX033_PayloadCountBounds(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	holdHeavyFixture(t)
	base := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	fl := gfxDefaultFleet()
	reject := func(name string, a *gfoArtifact) {
		t.Helper()
		in := gfxWriteInputs(t, a.Raw, fl.JSON())
		res := gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "json")...)
		gfxFail(t, res, 6, "GFX-033 (a) "+name, in.Dir)
	}
	accept := func(name string, a *gfoArtifact) {
		t.Helper()
		gfxNewCase(t, bins, a, fl).Check("gpu", gfxDevName, "GFX-033 (a) accepted "+name)
	}

	e := gfxWithEdges(t, base, 8193)
	if n, m := len(e.Frames[0].Payload.Edges), len(e.Frames[0].Payload.EdgeEvidence); n != 8193 || m != 8193 {
		t.Fatalf("test bug: %d edges and %d edgeEvidence, want 8193 each", n, m)
	}
	reject("8193 edges with 8193 edgeEvidence", e)
	reject("32769 observations", gfxWithObservations(t, base, 32769))
	reject("257 gpuBindings", gfxWithBindings(t, base, 257))
	reject("frame CP of 16777217 bytes", gfxWithCPLength(t, base, 16777217))

	accept("256 gpuBindings", gfxWithBindings(t, base, 256))
	accept("64 padding observations (construction control)", gfxWithObservations(t, base, len(base.Frames[0].Payload.Observations)+64))
	if testing.Short() {
		t.Log("GFX-033 (a): accepted 8192 edges and 16777216-byte CP cases skipped in -short mode")
		return
	}
	accept("8192 edges with 8192 edgeEvidence", gfxWithEdges(t, base, 8192))
	accept("frame CP of 16777216 bytes", gfxWithCPLength(t, base, 16777216))
}
