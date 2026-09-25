package tests_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// ---------------------------------------------------------------------------
// Expected artifact of the basic topology, built only from the spec.
// ---------------------------------------------------------------------------

type gfoBasicFrameSpec struct {
	seq, obsAt, expAt string
	gpuCur, nicCur    string
	completeness      string
}

func gfoAssetOf(kind, canonical string) gfoAsset {
	return gfoAsset{Kind: kind, Canonical: canonical, Aliases: []gfoAlias{}}
}

func gfoObsOf(session, seq, bdf, signal, src, value, unit string, dims []gfoKV, obsAt, expAt string) gfoObs {
	return gfoObs{
		ID: gfoObservationID(session, seq, gfoFnKey(bdf), signal), SourceType: "agent", SourceName: src,
		Subject: gfoAssetOf("PCIeFunction", "pci-bdf:"+bdf), Signal: signal, ValueKind: "int", ValueJSON: `"` + value + `"`,
		Unit: unit, Dims: dims, ObservedAt: obsAt, ReceivedAt: obsAt, ExpiresAt: expAt, Sequence: seq, Quality: "Good", RawDigest: "",
	}
}

// gfoExpectedBasicFrame returns the spec frame for the basic topology with
// the GPU baseline (peer switch downstream, 16) and adjacent NIC width 16.
func gfoExpectedBasicFrame(t *testing.T, session string, s gfoBasicFrameSpec) gfoFrame {
	t.Helper()
	node := gfoAssetOf("KubernetesNode", "kubernetes-node-uid:"+gfoNodeUID)
	p := gfoPayload{
		Assets: []gfoAsset{
			node,
			gfoAssetOf("PCIeFunction", "pci-bdf:"+gfoGPU), gfoAssetOf("PCIeFunction", "pci-bdf:"+gfoNIC),
			gfoAssetOf("PCIeRootPort", "pci-bdf:"+gfoRPA), gfoAssetOf("PCIeRootPort", "pci-bdf:"+gfoRPB),
			gfoAssetOf("PCIeSwitch", "pci-bdf:"+gfoSWU), gfoAssetOf("PCIeSwitch", "pci-bdf:"+gfoSWD),
		},
	}
	for _, e := range [][2]string{
		{gfoFnKey(gfoGPU), gfoSWKey(gfoSWD)}, {gfoFnKey(gfoNIC), gfoRPKey(gfoRPB)},
		{gfoRPKey(gfoRPA), node.Key()}, {gfoRPKey(gfoRPB), node.Key()},
		{gfoSWKey(gfoSWU), gfoRPKey(gfoRPA)}, {gfoSWKey(gfoSWD), gfoSWKey(gfoSWU)},
	} {
		p.Edges = append(p.Edges, gfoEdge{FromKey: e[0], Relation: "LOCATED_IN", ToKey: e[1], Origin: "Observed"})
	}
	for i, e := range p.Edges {
		p.EdgeEvidence = append(p.EdgeEvidence, gfoEdgeEvidence{
			EdgeIndex: i, Kind: "Observed", SourceType: "agent", SourceName: gfoParentSrc,
			ObservedAt: s.obsAt, ExpiresAt: s.expAt, EvidenceID: gfoEdgeEvidenceID(session, s.seq, e),
		})
	}
	gpuDims := []gfoKV{{"pcie.expected.provenance", gfoProvOp}, {"pcie.peer.canonical", "pci-bdf:" + gfoSWD},
		{"pcie.peer.kind", "PCIeSwitch"}, {"pcie.root.canonical", "pci-bdf:" + gfoRPA}}
	nicDims := []gfoKV{{"pcie.expected.provenance", gfoProvAdj}, {"pcie.peer.canonical", "pci-bdf:" + gfoRPB},
		{"pcie.peer.kind", "PCIeRootPort"}, {"pcie.root.canonical", "pci-bdf:" + gfoRPB}}
	p.Observations = []gfoObs{
		gfoObsOf(session, s.seq, gfoGPU, gfoSigClass, gfoParentSrc, "197120", "pci_class", []gfoKV{}, s.obsAt, s.expAt),
		gfoObsOf(session, s.seq, gfoGPU, gfoSigCurrent, gfoWidthSrc, s.gpuCur, "lanes", gpuDims, s.obsAt, s.expAt),
		gfoObsOf(session, s.seq, gfoGPU, gfoSigExpect, gfoWidthSrc, "16", "lanes", gpuDims, s.obsAt, s.expAt),
		gfoObsOf(session, s.seq, gfoNIC, gfoSigClass, gfoParentSrc, "131072", "pci_class", []gfoKV{}, s.obsAt, s.expAt),
		gfoObsOf(session, s.seq, gfoNIC, gfoSigCurrent, gfoWidthSrc, s.nicCur, "lanes", nicDims, s.obsAt, s.expAt),
		gfoObsOf(session, s.seq, gfoNIC, gfoSigExpect, gfoWidthSrc, "16", "lanes", nicDims, s.obsAt, s.expAt),
	}
	p.GPUBindings = []gfoBinding{{
		UUID: gfoUUIDA, Serial: "", BDF: gfoGPU, SourceType: "agent", SourceName: gfoNVIDIASrc,
		EvidenceID: gfoBindingID(session, s.seq, gfoUUIDA, gfoGPU), ObservedAt: s.obsAt, ExpiresAt: s.expAt,
	}}
	digest, err := gfoPayloadDigest(p)
	if err != nil {
		t.Fatalf("test bug: %v", err)
	}
	completeness := s.completeness
	if completeness == "" {
		completeness = "COMPLETE"
	}
	return gfoFrame{
		NodeUID: gfoNodeUID, BootID: gfoBootID, Session: session, Sequence: s.seq, Completeness: completeness, ObservedAt: s.obsAt,
		Payload: p, PayloadDigest: digest, BundleRevision: session + ":" + s.seq + ":" + digest,
		Diagnostics: []gfoDiag{}, DiagnosticsTruncated: false, DiagnosticsTotal: 0,
	}
}

func gfoExpectedArtifact(frames ...gfoFrame) *gfoArtifact {
	a := &gfoArtifact{
		SchemaVersion: gfoSchemaOut, Mode: "offline", Limitations: []string{"offline"}, ClusterID: gfoCluster,
		NodeName: gfoNodeName, NodeUID: gfoNodeUID, BootID: gfoBootID,
		Trust: gfoTrust{ProfileID: gfoProfile, Mode: "Offline", Session: "7", Sources: []gfoTrustSource{
			{"NVIDIAUUIDBinding", "agent", gfoNVIDIASrc}, {"OperatorBaseline", "agent", gfoWidthSrc},
			{"SysfsPCIeWidth", "agent", gfoWidthSrc}, {"SysfsPhysicalParent", "agent", gfoParentSrc},
		}},
		Frames: frames,
	}
	a.Raw = gfoRenderArtifact(a)
	return a
}

func gfoCompareArtifacts(t *testing.T, got, want *gfoArtifact, clause string) {
	t.Helper()
	for i := range want.Frames {
		if i >= len(got.Frames) {
			t.Errorf("%s: missing frame %d", clause, i)
			break
		}
		g, w := got.Frames[i].Payload, want.Frames[i].Payload
		if strings.Join(g.AssetKeys(), " ") != strings.Join(w.AssetKeys(), " ") {
			t.Errorf("%s: frame %d assets\n got %v\nwant %v", clause, i, g.AssetKeys(), w.AssetKeys())
		}
		if strings.Join(g.EdgeStrings(), " | ") != strings.Join(w.EdgeStrings(), " | ") {
			t.Errorf("%s: frame %d edges\n got %v\nwant %v", clause, i, g.EdgeStrings(), w.EdgeStrings())
		}
		if gfoRenderPayload(g) != gfoRenderPayload(w) {
			t.Errorf("GFO-064/GFO-075/GFO-082/GFO-084: frame %d payload\n got %s\nwant %s", i, gfoRenderPayload(g), gfoRenderPayload(w))
		}
		if got.Frames[i].PayloadDigest != want.Frames[i].PayloadDigest {
			t.Errorf("GFO-086: frame %d payloadDigest %s, want %s", i, got.Frames[i].PayloadDigest, want.Frames[i].PayloadDigest)
		}
	}
	if !bytes.Equal(got.Raw, want.Raw) {
		at := gfoDiffAt(got.Raw, want.Raw)
		t.Errorf("%s: stdout differs from the spec-built artifact at byte %d\n got: ...%s...\nwant: ...%s...", clause, at, gfoAround(got.Raw, at), gfoAround(want.Raw, at))
	}
}

// GFO-080..087: the basic fixture's stdout equals, byte for byte, the
// artifact built independently from the spec (schema, key order, encoding,
// IDs, ordering, canonical digest, bundleRevision).
func TestGFO080_GFO087_BasicGoldenBytes(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	got := gfoNewBasic(t, 1).OK(bin, "GFO-080..087 golden")
	want := gfoExpectedArtifact(gfoExpectedBasicFrame(t, "7", gfoBasicFrameSpec{
		seq: "0", obsAt: gfoT0, expAt: "2026-09-24T00:05:00Z", gpuCur: "16", nicCur: "16",
	}))
	gfoCompareArtifacts(t, got, want, "GFO-080..087")
}

// GFO-081 [OD-01 A]: a replay of three frames (0s, 15s, 30s) is one session
// with sequences 0..2; IDs and digests follow each frame's sequence.
func TestGFO081_ReplayFrames(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 3)
	for i := 0; i < 3; i++ {
		f.Write(gfoDevDir(i, gfoPathGPU)+"/current_link_width", "8\n")
	}
	got := f.OK(bin, "GFO-081 replay")
	var frames []gfoFrame
	for i, at := range []string{"2026-09-24T00:00:00Z", "2026-09-24T00:00:15Z", "2026-09-24T00:00:30Z"} {
		exp := gfoAddSeconds(t, at, 300)
		frames = append(frames, gfoExpectedBasicFrame(t, "7", gfoBasicFrameSpec{seq: string(rune('0' + i)), obsAt: at, expAt: exp, gpuCur: "8", nicCur: "16"}))
	}
	gfoCompareArtifacts(t, got, gfoExpectedArtifact(frames...), "GFO-081")
	if got.Frames[0].PayloadDigest == got.Frames[1].PayloadDigest || got.Frames[1].PayloadDigest == got.Frames[2].PayloadDigest {
		t.Errorf("GFO-084/GFO-086: frames with different sequence/time must have different digests")
	}
}

// GFO-082: payload shape: exactly five collections, one Observed parent
// edgeEvidence per edge, no allocationBatch, enum String() values.
func TestGFO082_PayloadShape(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 1)
	res := f.Run(bin)
	a := gfoOK(t, res, &f.M, "GFO-082")
	p := a.Frames[0].Payload
	if len(p.Edges) != 6 || len(p.EdgeEvidence) != 6 {
		t.Errorf("GFO-082: %d edges and %d edgeEvidence, want 6 and 6", len(p.Edges), len(p.EdgeEvidence))
	}
	if bytes.Contains(res.Stdout, []byte("allocationBatch")) {
		t.Errorf("GFO-082/GFO-002: artifact contains allocationBatch")
	}
	for _, needle := range []string{`"relation":"LOCATED_IN"`, `"origin":"Observed"`, `"kind":"PCIeFunction"`, `"quality":"Good"`, `"kind":"Observed"`} {
		if !bytes.Contains(res.Stdout, []byte(needle)) {
			t.Errorf("GFO-082: stdout lacks %s", needle)
		}
	}
}

// GFO-083/GFO-087: compact JSON escaping only '"' and '\', decimal-string
// 64-bit integers, numbers for edgeIndex/diagnosticsTotal, no null.
func TestGFO083_GFO087_Encoding(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 1)
	f.M.NodeName = `a<b>&"c\d`
	f.M.Profile = `offline:<x>&"y\z`
	f.M.Sources[0].Name = `p<a>&"r\s`
	f.M.Session = "9223372036854775807"
	res := f.Run(bin)
	a := gfoOK(t, res, &f.M, "GFO-083")
	out := string(res.Stdout)
	for _, needle := range []string{
		`"name":"a<b>&\"c\\d"`, `"profileID":"offline:<x>&\"y\\z"`, `"name":"p<a>&\"r\\s"`,
		`"session":"9223372036854775807"`, `"sequence":"0"`, `"edgeIndex":0,`, `"diagnosticsTotal":0}`,
		`"dimensions":{}`, `"aliases":[]`, `"diagnostics":[]`, `"diagnosticsTruncated":false`, `"value":{"int":"197120"}`,
		`"limitations":["offline"]`,
	} {
		if !strings.Contains(out, needle) {
			t.Errorf("GFO-083: stdout lacks %s", needle)
		}
	}
	for _, bad := range []string{`\u003c`, `\u003e`, `\u0026`, `\u0022`, `\/`, "null", "\t", "\r"} {
		if strings.Contains(out, bad) {
			t.Errorf("GFO-083: stdout contains %q", bad)
		}
	}
	rev := a.Frames[0].BundleRevision
	if !strings.HasPrefix(rev, "9223372036854775807:0:") || len(rev) != 86 {
		t.Errorf("GFO-087: bundleRevision %q", rev)
	}
	for _, o := range a.Frames[0].Payload.Observations {
		if !strings.HasPrefix(o.ID, "ob:9223372036854775807:0:") {
			t.Errorf("GFO-084: observation ID %q must use the session", o.ID)
		}
	}
}

// GFO-084: IDs are <p>:<session>:<sequence>:<H>; H does not depend on the
// session or the frame, only on the tuple.
func TestGFO084_DeterministicIDs(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f7 := gfoNewBasic(t, 2)
	a := f7.OK(bin, "GFO-084 session 7")
	f8 := gfoNewBasic(t, 2)
	f8.M.Session = "8"
	b := f8.OK(bin, "GFO-084 session 8")
	h := func(id string) string { return id[strings.LastIndex(id, ":")+1:] }
	for i := range a.Frames {
		pa, pb := a.Frames[i].Payload, b.Frames[i].Payload
		if len(pa.Observations) != len(pb.Observations) || len(pa.EdgeEvidence) != len(pb.EdgeEvidence) ||
			len(pa.GPUBindings) != 1 || len(pb.GPUBindings) != 1 || len(pa.Observations) != len(a.Frames[0].Payload.Observations) {
			t.Fatalf("GFO-084: frame %d payload shapes differ between sessions", i)
		}
		for k := range pa.Observations {
			x, y := pa.Observations[k].ID, pb.Observations[k].ID
			if h(x) != h(y) || !strings.HasPrefix(x, "ob:7:"+a.Frames[i].Sequence+":") || !strings.HasPrefix(y, "ob:8:"+b.Frames[i].Sequence+":") {
				t.Errorf("GFO-084: observation IDs %q / %q", x, y)
			}
			if h(x) != h(a.Frames[0].Payload.Observations[k].ID) {
				t.Errorf("GFO-084: H of %q differs between frames", x)
			}
			if len(h(x)) != 32 {
				t.Errorf("GFO-084: H %q is not 32 hex characters", h(x))
			}
		}
		for k := range pa.EdgeEvidence {
			if h(pa.EdgeEvidence[k].EvidenceID) != h(pb.EdgeEvidence[k].EvidenceID) || !strings.HasPrefix(pa.EdgeEvidence[k].EvidenceID, "pe:7:") {
				t.Errorf("GFO-084: edge evidence IDs %q / %q", pa.EdgeEvidence[k].EvidenceID, pb.EdgeEvidence[k].EvidenceID)
			}
		}
		if h(pa.GPUBindings[0].EvidenceID) != h(pb.GPUBindings[0].EvidenceID) || !strings.HasPrefix(pb.GPUBindings[0].EvidenceID, "nb:8:") {
			t.Errorf("GFO-084: binding IDs %q / %q", pa.GPUBindings[0].EvidenceID, pb.GPUBindings[0].EvidenceID)
		}
	}
}

// GFO-085: every payload collection is in byte order, including canonical
// BDFs whose byte order differs from numeric order (10000 < ffff).
func TestGFO085_ByteOrdering(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 1)
	// Created in reverse byte order on purpose.
	f.Dev(0, gfoBridge("pciffff:00", "ffff:00:01.0"))
	f.Dev(0, gfoGPUDev("pciffff:00", "ffff:00:01.0", "ffff:01:00.0"))
	f.Dev(0, gfoBridge("pci10000:00", "10000:00:01.0"))
	f.Dev(0, gfoGPUDev("pci10000:00", "10000:00:01.0", "10000:01:00.0"))
	f.Dev(0, gfoNICDev(gfoHB, gfoRPB, "0000:04:00.1"))
	f.Write(gfoCSV(0), "GPU-00000000-0000-0000-0000-00000000000f, FFFF:01:00.0\n"+
		"GPU-00000000-0000-0000-0000-00000000000e, 00010000:01:00.0\n"+gfoUUIDA+", 0000:03:00.0\n")
	a := f.OK(bin, "GFO-085")
	p := a.Frames[0].Payload
	idx := map[string]int{}
	for i, k := range p.AssetKeys() {
		idx[k] = i
	}
	if idx[gfoFnKey("10000:01:00.0")] > idx[gfoFnKey("ffff:01:00.0")] || idx[gfoRPKey("10000:00:01.0")] > idx[gfoRPKey("ffff:00:01.0")] {
		t.Errorf("GFO-085: assets must be in Key() byte order (10000 before ffff): %v", p.AssetKeys())
	}
	var bdfs []string
	for _, b := range p.GPUBindings {
		bdfs = append(bdfs, b.BDF)
	}
	if strings.Join(bdfs, ",") != "0000:03:00.0,10000:01:00.0,ffff:01:00.0" {
		t.Errorf("GFO-085: gpuBindings order %v", bdfs)
	}
}

// GFO-086 [OD-02 A]: the digest is over canonical bytes (not JSON); it
// ignores diagnostics and frame/manifest fields outside the payload, and
// changes with payload content.
func TestGFO086_DigestDefinition(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	base := gfoNewBasic(t, 1).OK(bin, "GFO-086 base")
	d := base.Frames[0].PayloadDigest
	jsonSum := sha256.Sum256([]byte(gfoRenderPayload(base.Frames[0].Payload)))
	if d == hex.EncodeToString(jsonSum[:]) {
		t.Errorf("GFO-086 [OD-02]: payloadDigest equals SHA-256 of the payload JSON; it must be over canonical bytes")
	}

	other := gfoNewBasic(t, 1)
	other.M.NodeName = "renamed-node"
	other.M.Cluster = "lab-b"
	other.M.BootID = "another-boot"
	other.M.Profile = "offline:other"
	other.M.Sources = []gfoSrc{{"SysfsPhysicalParent", "agent", "x"}}
	other.Write(gfoSys(0)+"/bus/pci/devices/.gitkeep", "")
	o := other.OK(bin, "GFO-086 non-payload changes")
	if o.Frames[0].PayloadDigest != d {
		t.Errorf("GFO-086: digest changed with diagnostics/identity/trust only (%s vs %s)", o.Frames[0].PayloadDigest, d)
	}

	changed := gfoNewBasic(t, 1)
	changed.Write(gfoDevDir(0, gfoPathGPU)+"/current_link_width", "8\n")
	if c := changed.OK(bin, "GFO-086 payload change"); c.Frames[0].PayloadDigest == d {
		t.Errorf("GFO-086: digest did not change with a payload value")
	}
}

// gfoBuildDeterminismFixture builds the same fixture content in forward or
// reverse creation order.
func gfoBuildDeterminismFixture(t *testing.T, reverse bool) *gfoFx {
	t.Helper()
	f := gfoNewFx(t, 2)
	f.M.Baselines = []gfoBL{{gfoGPU, gfoSWD, "16"}}
	devs := []gfoDev{
		gfoBridge(gfoPathRPA...), gfoBridge(gfoPathSWU...), gfoBridge(gfoPathSWD...), gfoGPUDev(gfoPathGPU...),
		gfoBridge(gfoPathRPB...), gfoNICDev(gfoPathNIC...), gfoNICDev(gfoHB, gfoRPB, "0000:04:00.1"),
		gfoGPUDev(gfoHB, "0000:30:00.0"),
	}
	for frame := 0; frame < 2; frame++ {
		order := append([]gfoDev(nil), devs...)
		if reverse {
			for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
				order[i], order[j] = order[j], order[i]
			}
			f.Write(gfoCSV(frame), gfoUUIDA+", 00000000:03:00.0\n")
		}
		for _, d := range order {
			f.Dev(frame, d)
		}
		f.Write(gfoSys(frame)+"/bus/pci/devices/.gitkeep", "")
		if !reverse {
			f.Write(gfoCSV(frame), gfoUUIDA+", 00000000:03:00.0\n")
		}
	}
	f.save()
	return f
}

// GFO-004/GFO-088: stdout is byte-identical across fixture path, cwd,
// creation order, mtimes, readable modes, TZ/LANG/GOMAXPROCS/hostname env,
// wall-clock time and repeated runs.
func TestGFO004_GFO088_ByteDeterminism(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	a := gfoBuildDeterminismFixture(t, false)
	b := gfoBuildDeterminismFixture(t, true)

	// b: old mtimes, read-only files and directories.
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	var files, dirs []string
	err := filepath.WalkDir(b.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if err := os.Chtimes(p, old, old); err != nil {
			return err
		}
		rel, _ := filepath.Rel(b.Root, p)
		if d.IsDir() {
			dirs = append(dirs, rel)
		} else {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range files {
		b.Chmod(p, 0o444)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, p := range dirs {
		b.Chmod(p, 0o555)
	}

	env := func(extra ...string) []string {
		out := []string{}
		for _, kv := range os.Environ() {
			k := kv[:strings.IndexByte(kv, '=')+1]
			skip := false
			for _, e := range extra {
				if strings.HasPrefix(e, k) {
					skip = true
				}
			}
			if !skip {
				out = append(out, kv)
			}
		}
		return append(out, extra...)
	}
	runs := []struct {
		name string
		root string
		opt  gfoRunOpt
	}{
		{"a_default", a.Root, gfoRunOpt{}},
		{"b_reverse_readonly_seoul", b.Root, gfoRunOpt{Dir: t.TempDir(), Env: env("TZ=Asia/Seoul", "LANG=ko_KR.UTF-8", "LC_ALL=ko_KR.UTF-8", "GOMAXPROCS=1", "HOSTNAME=other-host")}},
		{"a_relative_root_newfoundland", "fixture", gfoRunOpt{Dir: filepath.Dir(a.Root), Env: env("TZ=America/St_Johns", "GOMAXPROCS=7", "LANG=C")}},
		{"b_minimal_env", b.Root, gfoRunOpt{Env: []string{"PATH=" + os.Getenv("PATH")}}},
	}
	var want []byte
	for i, r := range runs {
		if i == 3 {
			time.Sleep(1100 * time.Millisecond) // a different wall-clock second
		}
		res := gfoExec(t, bin, r.opt, "--fixture-root", r.root)
		gfoOK(t, res, &a.M, "GFO-088 "+r.name)
		if want == nil {
			want = res.Stdout
			continue
		}
		if !bytes.Equal(res.Stdout, want) {
			at := gfoDiffAt(res.Stdout, want)
			t.Errorf("GFO-004/GFO-088: %s stdout differs at byte %d: ...%s...", r.name, at, gfoAround(res.Stdout, at))
		}
	}
	again := gfoExec(t, bin, gfoRunOpt{}, "--fixture-root", a.Root)
	if !bytes.Equal(again.Stdout, want) {
		t.Errorf("GFO-088: repeated run differs")
	}
}

// GFO-089: the payload alone carries every TopologyDigest/BaselineDigest
// input: assets, edges, their evidence and expected widths with provenance.
func TestGFO089_DigestInputsInPayload(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	a := gfoNewBasic(t, 1).OK(bin, "GFO-089")
	p := a.Frames[0].Payload
	if len(p.Assets) == 0 || len(p.Edges) == 0 || len(p.EdgeEvidence) != len(p.Edges) {
		t.Fatalf("GFO-089: topology inputs missing")
	}
	for _, bdf := range []string{gfoGPU, gfoNIC} {
		_, exp, ok := p.WidthPair(bdf)
		if !ok || exp.IntValue() == "" || exp.Dim("pcie.expected.provenance") == "" {
			t.Errorf("GFO-089: expected width value/provenance of %s missing", bdf)
		}
	}
}

// gfoCheckPCIeSchema checks width pairs against pcie-rules PCIE-006..009 and
// PCIE-011 using the ratified model/identity API.
func gfoCheckPCIeSchema(t *testing.T, r gfoRatified, clause string) {
	t.Helper()
	type key struct{ subject string }
	pairs := map[key][]model.Observation{}
	for _, o := range r.Observations {
		if o.Signal != model.SignalRef(gfoSigCurrent) && o.Signal != model.SignalRef(gfoSigExpect) {
			continue
		}
		pairs[key{o.Subject.Key()}] = append(pairs[key{o.Subject.Key()}], o)
		if len(o.Dimensions) != 4 {
			t.Errorf("%s: PCIE-006 dimensions %v", clause, o.Dimensions)
		}
		root, peer, kind, prov := o.Dimensions["pcie.root.canonical"], o.Dimensions["pcie.peer.canonical"], o.Dimensions["pcie.peer.kind"], o.Dimensions["pcie.expected.provenance"]
		rootID, err := identity.ParseCanonical(root)
		if err == nil {
			_, err = model.NewAssetRef(model.KindPCIeRootPort, rootID)
		}
		if err != nil {
			t.Errorf("%s: PCIE-006 root canonical %q: %v", clause, root, err)
		}
		pk, ok := gfoKindOf(kind)
		if !ok || (kind != "PCIeRootPort" && kind != "PCIeSwitch" && kind != "PCIeFunction") {
			t.Errorf("%s: PCIE-006 peer kind %q", clause, kind)
		} else {
			peerID, err := identity.ParseCanonical(peer)
			var ref model.AssetRef
			if err == nil {
				ref, err = model.NewAssetRef(pk, peerID)
			}
			if err != nil || ref.Key() == o.Subject.Key() {
				t.Errorf("%s: PCIE-006 peer %q (%v) invalid or equal to the subject", clause, peer, err)
			}
			if kind == "PCIeRootPort" && peer != root {
				t.Errorf("%s: PCIE-006 root-kind peer %q must equal root %q", clause, peer, root)
			}
		}
		if prov != gfoProvOp && prov != gfoProvAdj {
			t.Errorf("%s: PCIE-007 provenance %q", clause, prov)
		}
		if v, ok := o.Value.Int(); !ok || v <= 0 || o.Unit != "lanes" || o.Quality != model.QualityGood {
			t.Errorf("%s: PCIE-008/009 value %v unit %q quality %v", clause, o.Value, o.Unit, o.Quality)
		}
	}
	for k, obs := range pairs {
		if len(obs) != 2 || obs[0].Source != obs[1].Source || !obs[0].ObservedAt.Equal(obs[1].ObservedAt) ||
			len(obs[0].Dimensions) != len(obs[1].Dimensions) {
			t.Errorf("%s: PCIE-011 %s is not one current/expected pair with equal source/time/dimensions", clause, k.subject)
			continue
		}
		for dk, dv := range obs[0].Dimensions {
			if obs[1].Dimensions[dk] != dv {
				t.Errorf("%s: PCIE-011 %s dimension %s differs", clause, k.subject, dk)
			}
		}
	}
}

// GFO-090/GFO-091: M maps the artifact to ratified values field by field and
// everything validates (the generic checker also runs M on every exit-0
// artifact of every GFO test).
func TestGFO090_GFO091_RatifiedValues(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	a := gfoNewBasic(t, 1).OK(bin, "GFO-090")
	r, err := gfoApplyM(a, 0)
	if err != nil {
		t.Fatalf("GFO-090: %v", err)
	}
	fr := a.Frames[0]
	env := r.Envelope
	if env.NodeUID != gfoNodeUID || env.BootID != gfoBootID || env.PayloadDigest != fr.PayloadDigest || env.BundleRevision != fr.BundleRevision ||
		env.Session != 7 || env.Sequence != 0 || env.Completeness != fleet.CompletenessComplete || !env.ObservedAt.Equal(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("GFO-090: envelope %+v", env)
	}
	if r.Profile.ID != gfoProfile || r.Profile.Mode != fleet.TrustModeOffline || r.Profile.ClusterID != gfoCluster ||
		r.Profile.NodeUID != gfoNodeUID || r.Profile.Session != 7 || len(r.Profile.Sources) != 4 {
		t.Errorf("GFO-090: trust profile %+v", r.Profile)
	}
	caps := map[fleet.TrustCapability]string{}
	for _, s := range r.Profile.Sources {
		caps[s.Capability] = s.Source.Name
	}
	if caps[fleet.TrustSysfsPhysicalParent] != gfoParentSrc || caps[fleet.TrustSysfsPCIeWidth] != gfoWidthSrc ||
		caps[fleet.TrustOperatorBaseline] != gfoWidthSrc || caps[fleet.TrustNVIDIAUUIDBinding] != gfoNVIDIASrc {
		t.Errorf("GFO-090: capabilities %v", caps)
	}
	if len(r.Assets) != 7 || len(r.Edges) != 6 || len(r.Provenance) != 6 || len(r.Observations) != 6 || len(r.Bindings) != 1 {
		t.Fatalf("GFO-090: counts %d/%d/%d/%d/%d", len(r.Assets), len(r.Edges), len(r.Provenance), len(r.Observations), len(r.Bindings))
	}
	for i, pv := range r.Provenance {
		if pv.Edge.From.Key() != r.Edges[i].From.Key() || pv.Edge.To.Key() != r.Edges[i].To.Key() || pv.Kind != fleet.EdgeEvidenceObserved ||
			pv.CollectorProfileID != gfoProfile || pv.BundleRevision != fr.BundleRevision {
			t.Errorf("GFO-090: provenance %d %+v", i, pv)
		}
	}
	b := r.Bindings[0]
	if b.Function.Key() != gfoFnKey(gfoGPU) || b.Function.Canonical != "pci-bdf:"+b.BDF || b.Claim.UUID != gfoUUIDA || b.Claim.Serial != "" ||
		b.Claim.Source != gfoNVIDIASrc || b.Node.Name != gfoNodeName || b.BootID != gfoBootID {
		t.Errorf("GFO-090: binding %+v", b)
	}
	gfoCheckPCIeSchema(t, r, "GFO-091")

	// A PARTIAL frame with contradictions, OD-08 and an unresolved closure
	// must still produce valid ratified values.
	f := gfoNewBasic(t, 1)
	f.Dev(0, gfoGPUDev(gfoHB, "0000:30:00.0"))
	f.Dev(0, gfoBridge(gfoHB, "0000:50:00.0"))
	f.Dev(0, gfoBridge(gfoHB, "0000:4f:00.0"))
	f.Dev(0, gfoBridge(gfoHB, "0000:4f:00.0", "0000:51:00.0"))
	f.Mkdir(gfoDevDir(0, []string{gfoHB, "0000:50:00.0", "0000:51:00.0"}))
	f.Dev(0, gfoGPUDev(gfoHB, "0000:50:00.0", "0000:51:00.0", "0000:52:00.0"))
	f.Dev(0, gfoNICDev(gfoHB, "0000:70:00.0", "0000:71:00.0"))
	c := f.OK(bin, "GFO-091 complex")
	rc, err := gfoApplyM(c, 0)
	if err != nil {
		t.Fatalf("GFO-090: %v", err)
	}
	gfoCheckPCIeSchema(t, rc, "GFO-091 complex")
}

// GFO-092: frame 0 COMPLETE is admitted as the baseline and every later frame
// is Accepted in order regardless of completeness; a PARTIAL frame 0 is
// WrongSession while the artifact still exits 0.
func TestGFO092_Admission(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 3)
	f.Write(gfoSys(1)+"/bus/pci/devices/.gitkeep", "")
	a := f.OK(bin, "GFO-092")
	gfoAssertCompleteness(t, a.Frames[1], "PARTIAL", "GFO-092 frame 1")
	var cursor *fleet.SnapshotCursor
	for i := range a.Frames {
		r, err := gfoApplyM(a, i)
		if err != nil {
			t.Fatalf("GFO-090: %v", err)
		}
		next, order, err := fleet.AdmitSnapshot(7, cursor, r.Envelope)
		if err != nil || order != fleet.SnapshotAccepted {
			t.Fatalf("GFO-092: frame %d admission = %v, %v; want Accepted", i, order, err)
		}
		if !next.Baseline {
			t.Errorf("GFO-092: frame %d cursor has Baseline=false", i)
		}
		cursor = &next
	}

	p := gfoNewBasic(t, 1)
	p.Write(gfoSys(0)+"/bus/pci/devices/.gitkeep", "")
	b := p.OK(bin, "GFO-092 partial frame 0")
	r, err := gfoApplyM(b, 0)
	if err != nil {
		t.Fatalf("GFO-090: %v", err)
	}
	if _, order, _ := fleet.AdmitSnapshot(7, nil, r.Envelope); order != fleet.SnapshotWrongSession {
		t.Errorf("GFO-092: PARTIAL frame 0 admission = %v, want WrongSession", order)
	}
}
