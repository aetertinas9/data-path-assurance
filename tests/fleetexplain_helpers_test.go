package tests_test

// Shared helpers for the gpu-fleet-offline-explain (GFX) black-box tests.
//
// Source of truth: the gpu-fleet-offline-explain specification (v1.0) and the
// ratified contracts it builds on. The helpers build the pathctl and
// path-agent binaries, create fixture trees, artifacts and fleet files under
// t.TempDir(), run pathctl and decode its output. They never read
// implementation sources.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Fixed values of the normative fleet file (GFX §3) and BASE fixture (GFX-102).
const (
	gfxSchemaFleet   = "dpa.offline-fleet/v1"
	gfxUUID          = "GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d"
	gfxDevName       = "gpu-node-1-gpu0"
	gfxDevUID        = "0b5f3c9a-6e2d-4f8b-a1c7-3d9e5f2a7b41"
	gfxFleetName     = "lab-a-gpus"
	gfxFleetUID      = "5d7c1b7e-2f0a-4c11-9a51-0b6e3c2d4f10"
	gfxPolicyRev     = "gpu-path-v1"
	gfxIntentAt      = "2026-09-24T00:00:00Z"
	gfxClaimSource   = "operator/asset-db"
	gfxClaimEvidence = "asset-db:rack7-u12-gpu0"
	gfxProfileWidth  = "offline:lab-a-width-replay"
	gfxArtifactMax   = 67108864
	gfxFleetMax      = 1048576
	// gfxQuick is the observable limit for the "1 second" bounds of GFX-013
	// and GFX-014: one second plus the two seconds of slack required for runs
	// under -race and parallel load.
	gfxQuick = 3 * time.Second
	// gfxHang is the hard kill deadline for runs that must not block.
	gfxHang = 20 * time.Second
)

// gfxTokens maps each pathctl exit code to its GFX-011 stderr token.
var gfxTokens = map[int]string{
	1: "internal",
	2: "usage",
	4: "not_found",
	5: "live_unsupported",
	6: "artifact_invalid",
	7: "fleet_invalid",
	8: "bound_exceeded",
}

// ---------------------------------------------------------------------------
// Build and execution.
// ---------------------------------------------------------------------------

type gfxBins struct{ Pathctl, Agent string }

func gfxGoBuild(t *testing.T, out, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = gfoRepoRoot(t)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("GFX-101: go build %s failed: %v\n%s", pkg, err, b)
	}
}

// gfxBuild builds ./cmd/pathctl (and ./cmd/path-agent when agent is true)
// into t.TempDir().
func gfxBuild(t *testing.T, agent bool) gfxBins {
	t.Helper()
	dir := t.TempDir()
	b := gfxBins{Pathctl: filepath.Join(dir, "pathctl")}
	gfxGoBuild(t, b.Pathctl, "./cmd/pathctl")
	if agent {
		b.Agent = filepath.Join(dir, "path-agent")
		gfxGoBuild(t, b.Agent, "./cmd/path-agent")
	}
	return b
}

func gfxRun(t *testing.T, bin string, opt gfoRunOpt, args ...string) gfoResult {
	t.Helper()
	if opt.Timeout == 0 {
		opt.Timeout = 180 * time.Second
	}
	return gfoExec(t, bin, opt, args...)
}

var gfxStderrRE = regexp.MustCompile(`^pathctl: error: ([a-z_]+): [\x20-\x7e]*\n$`)

// gfxFail asserts a failing pathctl run: exit code, the single GFX-012
// stderr line with the GFX-011 token, an empty stdout and the absence of every
// forbidden string in stderr.
func gfxFail(t *testing.T, res gfoResult, want int, clause string, forbidden ...string) {
	t.Helper()
	if res.Killed {
		t.Errorf("%s: pathctl did not finish (killed after %v)", clause, res.Elapsed)
		return
	}
	if res.Exit != want {
		t.Errorf("%s: GFX-011: exit %d, want %d; stderr=%q stdout=%d bytes", clause, res.Exit, want, gfoTail(res.Stderr), len(res.Stdout))
		return
	}
	if len(res.Stdout) != 0 {
		t.Errorf("%s: GFX-012/GFX-015: exit %d wrote %d stdout bytes, want 0", clause, res.Exit, len(res.Stdout))
	}
	m := gfxStderrRE.FindSubmatch(res.Stderr)
	switch {
	case m == nil:
		t.Errorf("%s: GFX-012: stderr %q is not exactly one line `pathctl: error: <token>: <message>\\n` of 0x20-0x7e bytes", clause, gfoTail(res.Stderr))
	case string(m[1]) != gfxTokens[want]:
		t.Errorf("%s: GFX-011: stderr token %q, want %q for exit %d", clause, m[1], gfxTokens[want], want)
	}
	if len(res.Stderr) > 8192 {
		t.Errorf("%s: GFX-012: stderr is %d bytes, want <= 8192", clause, len(res.Stderr))
	}
	for _, f := range forbidden {
		if f != "" && bytes.Contains(res.Stderr, []byte(f)) {
			t.Errorf("%s: GFX-012: stderr contains the forbidden string %q: %q", clause, f, gfoTail(res.Stderr))
		}
	}
}

// gfxWarm runs the binary once with -h before any timed run so that the
// first-execution cost of a freshly built binary is not charged to a timing
// judgement. The result is not asserted.
func gfxWarm(t *testing.T, bin string) {
	t.Helper()
	_ = gfxRun(t, bin, gfoRunOpt{Timeout: gfxHang}, "-h")
}

// gfxQuickRuns judges a "1 second" bound (GFX-010 exit 2, GFX-013, GFX-014):
// it runs pathctl under the hang guard at most three times, applies check
// (the exit, stdout and stderr assertions) to every run, and passes as soon as
// one run finishes within gfxQuick; otherwise it reports the fastest run.
// The retries only absorb process start-up and host scheduling noise. A run
// that blocks is killed by the hang guard, fails check and is reported at
// once without further attempts.
func gfxQuickRuns(t *testing.T, bin, clause string, check func(gfoResult), args ...string) {
	t.Helper()
	const attempts = 3
	var best time.Duration
	for i := 0; i < attempts; i++ {
		res := gfxRun(t, bin, gfoRunOpt{Timeout: gfxHang}, args...)
		check(res)
		if res.Killed {
			t.Errorf("%s: killed by the hang guard after %v, want <= %v", clause, res.Elapsed, gfxQuick)
			return
		}
		if i == 0 || res.Elapsed < best {
			best = res.Elapsed
		}
		if best <= gfxQuick {
			return
		}
	}
	t.Errorf("%s: fastest of %d runs took %v, want <= %v", clause, attempts, best, gfxQuick)
}

// gfxOK asserts exit 0 with an empty stderr and returns stdout (GFX-011, GFX-012).
func gfxOK(t *testing.T, res gfoResult, clause string) []byte {
	t.Helper()
	if res.Killed {
		t.Fatalf("%s: pathctl did not finish (killed after %v)", clause, res.Elapsed)
	}
	if res.Exit != 0 {
		t.Fatalf("%s: GFX-011: exit %d, want 0; stderr=%q", clause, res.Exit, gfoTail(res.Stderr))
	}
	if len(res.Stderr) != 0 {
		t.Errorf("%s: GFX-012: exit 0 wrote stderr %q, want 0 bytes", clause, gfoTail(res.Stderr))
	}
	return res.Stdout
}

// ---------------------------------------------------------------------------
// Fleet file (GFX §3).
// ---------------------------------------------------------------------------

type gfxCovReq struct {
	Name, PathKind string
	Required       bool
}

type gfxDevice struct {
	Name, UID         string
	NodeName, NodeUID string
	Desired           string
	RequestID         string
	Generation        int64
	IntentAt          string
	Vendor            string
	UUID, Serial      string // "" means the key is absent
	Source            string
	EvidenceID        string
}

type gfxFleet struct {
	ClusterID           string
	Name, UID           string
	Revision            string
	Freshness, ReadyFor int64
	Coverage            []gfxCovReq
	Devices             []gfxDevice
}

func gfxDefaultCoverage() []gfxCovReq {
	return []gfxCovReq{
		{"pcie-parent", "gpu-pcie-parent", true},
		{"pcie-root", "gpu-pcie-root", true},
		{"pcie-width", "gpu-pcie-link-width-normal", true},
		{"nic-lldp", "nic-lldp-remote", false},
	}
}

func gfxDefaultDevice() gfxDevice {
	return gfxDevice{
		Name: gfxDevName, UID: gfxDevUID, NodeName: gfoNodeName, NodeUID: gfoNodeUID,
		Desired: "InService", RequestID: "enroll-1", Generation: 1, IntentAt: gfxIntentAt,
		Vendor: "NVIDIA", UUID: gfxUUID, Source: gfxClaimSource, EvidenceID: gfxClaimEvidence,
	}
}

// gfxDefaultFleet is the normative fleet file of GFX §3.
func gfxDefaultFleet() gfxFleet {
	return gfxFleet{
		ClusterID: gfoCluster, Name: gfxFleetName, UID: gfxFleetUID, Revision: gfxPolicyRev,
		Freshness: 60, ReadyFor: 30, Coverage: gfxDefaultCoverage(), Devices: []gfxDevice{gfxDefaultDevice()},
	}
}

// JSON renders the fleet file compactly with the key order of the GFX §3
// example. Tests inject invalid content by editing this rendering.
func (f gfxFleet) JSON() string {
	var b strings.Builder
	b.WriteString(`{"schemaVersion":` + gfoQ(gfxSchemaFleet))
	b.WriteString(`,"clusterID":` + gfoQ(f.ClusterID))
	b.WriteString(`,"fleet":{"name":` + gfoQ(f.Name) + `,"uid":` + gfoQ(f.UID) + `}`)
	b.WriteString(`,"policy":{"revision":` + gfoQ(f.Revision))
	b.WriteString(`,"freshnessSeconds":` + strconv.FormatInt(f.Freshness, 10))
	b.WriteString(`,"readyForSeconds":` + strconv.FormatInt(f.ReadyFor, 10))
	b.WriteString(`,"requiredCoverage":[`)
	for i, c := range f.Coverage {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"name":` + gfoQ(c.Name) + `,"pathKind":` + gfoQ(c.PathKind) + `,"required":` + strconv.FormatBool(c.Required) + `}`)
	}
	b.WriteString(`]},"devices":[`)
	for i, d := range f.Devices {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(d.JSON())
	}
	b.WriteString(`]}`)
	return b.String()
}

func (d gfxDevice) JSON() string {
	var b strings.Builder
	b.WriteString(`{"name":` + gfoQ(d.Name) + `,"uid":` + gfoQ(d.UID))
	b.WriteString(`,"nodeRef":{"name":` + gfoQ(d.NodeName) + `,"uid":` + gfoQ(d.NodeUID) + `}`)
	b.WriteString(`,"desiredState":` + gfoQ(d.Desired) + `,"requestID":` + gfoQ(d.RequestID))
	b.WriteString(`,"metadataGeneration":` + strconv.FormatInt(d.Generation, 10))
	b.WriteString(`,"intentObservedAt":` + gfoQ(d.IntentAt))
	b.WriteString(`,"inventoryClaim":{"vendor":` + gfoQ(d.Vendor))
	if d.UUID != "" {
		b.WriteString(`,"uuid":` + gfoQ(d.UUID))
	}
	if d.Serial != "" {
		b.WriteString(`,"serial":` + gfoQ(d.Serial))
	}
	b.WriteString(`,"source":` + gfoQ(d.Source) + `,"evidenceID":` + gfoQ(d.EvidenceID) + `}}`)
	return b.String()
}

// gfxReplace replaces exactly one occurrence of old in s and fails the test
// when old does not occur exactly once.
func gfxReplace(t *testing.T, s, old, repl string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("test bug: %q occurs %d times, want exactly 1", old, n)
	}
	return strings.Replace(s, old, repl, 1)
}

// ---------------------------------------------------------------------------
// Fixture trees (GFX-102 BASE and variants, GFO fixture layout).
// ---------------------------------------------------------------------------

// gfxPCI describes a function directory with the BASE attribute files; an
// empty width value omits that file.
func gfxPCI(path []string, class, vendor, cur, max string) gfoDev {
	attrs := map[string]string{"class": class + "\n", "vendor": vendor + "\n"}
	if cur != "" {
		attrs["current_link_width"] = cur + "\n"
	}
	if max != "" {
		attrs["max_link_width"] = max + "\n"
	}
	return gfoDev{BDF: path[len(path)-1], Path: path, Attrs: attrs}
}

// gfxBaseFrame adds the BASE topology of GFX-102 to frame i.
func gfxBaseFrame(f *gfoFx, i int) {
	f.t.Helper()
	f.Dev(i, gfxPCI(gfoPathRPA, "0x060400", "0x8086", "16", "16"))
	f.Dev(i, gfxPCI(gfoPathSWU, "0x060400", "0x10b5", "16", "16"))
	f.Dev(i, gfxPCI(gfoPathSWD, "0x060400", "0x10b5", "16", "16"))
	f.Dev(i, gfxPCI(gfoPathGPU, "0x030200", "0x10de", "16", "16"))
	f.Dev(i, gfxPCI(gfoPathRPB, "0x060400", "0x8086", "16", "16"))
	f.Dev(i, gfxPCI(gfoPathNIC, "0x020000", "0x15b3", "16", "16"))
	f.Write(gfoCSV(i), gfxUUID+", 00000000:03:00.0\n")
}

// gfxBaseFixture returns a fixture with `frames` BASE frames at 0/15/30...
// seconds and the GPU operator baseline of the BASE manifest.
func gfxBaseFixture(t *testing.T, frames int) *gfoFx {
	t.Helper()
	f := gfoNewFx(t, frames)
	f.M.Baselines = []gfoBL{{gfoGPU, gfoSWD, "16"}}
	for i := 0; i < frames; i++ {
		gfxBaseFrame(f, i)
	}
	return f
}

func gfxAttr(frame int, path []string, attr string) string {
	return gfoDevDir(frame, path) + "/" + attr
}

// Normative scenario fixtures (GFX-102).

func gfxSBasic(t *testing.T) *gfoFx { t.Helper(); return gfxBaseFixture(t, 1) }

func gfxSReady(t *testing.T) *gfoFx {
	t.Helper()
	f := gfxBaseFixture(t, 3)
	f.M.Profile = gfxProfileWidth
	return f
}

func gfxSWidth(t *testing.T) *gfoFx {
	t.Helper()
	f := gfxSReady(t)
	for i := 0; i < 3; i++ {
		f.Write(gfxAttr(i, gfoPathGPU, "current_link_width"), "8\n")
	}
	return f
}

// gfxAddFrame appends a BASE frame at the given manifest time.
func gfxAddFrame(f *gfoFx, at string) int {
	f.t.Helper()
	i := len(f.M.Frames)
	f.M.Frames = append(f.M.Frames, at)
	f.Mkdir(gfoSys(i))
	gfxBaseFrame(f, i)
	return i
}

// gfxAgentArtifact runs path-agent on the fixture and returns the decoded
// artifact. Any failure is a fixture setup failure, not a pathctl finding.
func gfxAgentArtifact(t *testing.T, agent string, f *gfoFx) *gfoArtifact {
	t.Helper()
	res := f.Run(agent)
	if res.Exit != 0 || res.Killed {
		t.Fatalf("fixture setup: path-agent exit %d (killed=%v); stderr=%q", res.Exit, res.Killed, gfoTail(res.Stderr))
	}
	a, err := gfoParseArtifact(res.Stdout)
	if err != nil {
		t.Fatalf("fixture setup: path-agent artifact does not decode: %v", err)
	}
	return a
}

// ---------------------------------------------------------------------------
// Artifact edits (GFO-080..087 recomputation for directly made artifacts).
// ---------------------------------------------------------------------------

// gfxClone deep-copies an artifact through its GFO encoding.
func gfxClone(t *testing.T, a *gfoArtifact) *gfoArtifact {
	t.Helper()
	c, err := gfoParseArtifact(gfoRenderArtifact(a))
	if err != nil {
		t.Fatalf("test bug: artifact clone does not decode: %v", err)
	}
	return c
}

// gfxFrames returns a copy of a holding only its first n frames.
func gfxFrames(t *testing.T, a *gfoArtifact, n int) *gfoArtifact {
	t.Helper()
	c := gfxClone(t, a)
	c.Frames = c.Frames[:n]
	c.Raw = gfoRenderArtifact(c)
	return c
}

// gfxSortPayload restores the GFO-085 order of every payload collection and
// renumbers edgeIndex so each edgeEvidence stays with its edge.
func gfxSortPayload(p *gfoPayload) {
	sort.SliceStable(p.Assets, func(i, j int) bool { return p.Assets[i].Key() < p.Assets[j].Key() })
	type pair struct {
		e  gfoEdge
		ev []gfoEdgeEvidence
	}
	pairs := make([]pair, len(p.Edges))
	for i, e := range p.Edges {
		pairs[i].e = e
	}
	for _, ev := range p.EdgeEvidence {
		if ev.EdgeIndex >= 0 && ev.EdgeIndex < len(pairs) {
			pairs[ev.EdgeIndex].ev = append(pairs[ev.EdgeIndex].ev, ev)
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return gfoCompareEdge(pairs[i].e, pairs[j].e) < 0 })
	p.Edges = p.Edges[:0]
	p.EdgeEvidence = p.EdgeEvidence[:0]
	for i, pr := range pairs {
		p.Edges = append(p.Edges, pr.e)
		for _, ev := range pr.ev {
			ev.EdgeIndex = i
			p.EdgeEvidence = append(p.EdgeEvidence, ev)
		}
	}
	sort.SliceStable(p.Observations, func(i, j int) bool { return gfoCompareObs(p.Observations[i], p.Observations[j]) < 0 })
	sort.SliceStable(p.GPUBindings, func(i, j int) bool {
		if p.GPUBindings[i].BDF != p.GPUBindings[j].BDF {
			return p.GPUBindings[i].BDF < p.GPUBindings[j].BDF
		}
		return p.GPUBindings[i].UUID < p.GPUBindings[j].UUID
	})
}

// gfxSeal recomputes, for every frame, the GFO-084 IDs (when ids is true),
// the GFO-086 payloadDigest and the GFO-087 bundleRevision, and re-renders
// the artifact bytes.
func gfxSeal(t *testing.T, a *gfoArtifact, ids bool) {
	t.Helper()
	for i := range a.Frames {
		fr := &a.Frames[i]
		p := &fr.Payload
		if ids {
			for k := range p.EdgeEvidence {
				ev := &p.EdgeEvidence[k]
				if ev.EdgeIndex >= 0 && ev.EdgeIndex < len(p.Edges) {
					ev.EvidenceID = gfoEdgeEvidenceID(fr.Session, fr.Sequence, p.Edges[ev.EdgeIndex])
				}
			}
			for k := range p.Observations {
				o := &p.Observations[k]
				o.ID = gfoObservationID(fr.Session, fr.Sequence, o.Subject.Key(), o.Signal)
			}
			for k := range p.GPUBindings {
				b := &p.GPUBindings[k]
				b.EvidenceID = gfoBindingID(fr.Session, fr.Sequence, b.UUID, b.BDF)
			}
		}
		d, err := gfoPayloadDigest(*p)
		if err != nil {
			t.Fatalf("test bug: frame %d digest: %v", i, err)
		}
		fr.PayloadDigest = d
		fr.BundleRevision = fr.Session + ":" + fr.Sequence + ":" + d
	}
	a.Raw = gfoRenderArtifact(a)
}

// gfxAddEdge appends an observed edge with its parent-source edgeEvidence to
// frame i, restores the payload order and reseals the artifact.
func gfxAddEdge(t *testing.T, a *gfoArtifact, i int, from, to string) {
	t.Helper()
	fr := &a.Frames[i]
	exp := gfoAddSeconds(t, fr.ObservedAt, 300)
	if len(fr.Payload.EdgeEvidence) > 0 {
		exp = fr.Payload.EdgeEvidence[0].ExpiresAt
	}
	e := gfoEdge{FromKey: from, Relation: "LOCATED_IN", ToKey: to, Origin: "Observed"}
	fr.Payload.Edges = append(fr.Payload.Edges, e)
	fr.Payload.EdgeEvidence = append(fr.Payload.EdgeEvidence, gfoEdgeEvidence{
		EdgeIndex: len(fr.Payload.Edges) - 1, Kind: "Observed", SourceType: "agent", SourceName: gfoParentSrc,
		ObservedAt: fr.ObservedAt, ExpiresAt: exp,
	})
	gfxSortPayload(&fr.Payload)
	gfxSeal(t, a, true)
}

// gfxRemoveEdge removes the edge from -> to (and its edgeEvidence) from frame
// i, restores the payload order and reseals the artifact.
func gfxRemoveEdge(t *testing.T, a *gfoArtifact, i int, from, to string) {
	t.Helper()
	p := &a.Frames[i].Payload
	idx := -1
	for k, e := range p.Edges {
		if e.FromKey == from && e.ToKey == to {
			idx = k
		}
	}
	if idx < 0 {
		t.Fatalf("test bug: frame %d has no edge %s -> %s", i, from, to)
	}
	edges := append([]gfoEdge{}, p.Edges[:idx]...)
	edges = append(edges, p.Edges[idx+1:]...)
	var evs []gfoEdgeEvidence
	for _, ev := range p.EdgeEvidence {
		switch {
		case ev.EdgeIndex == idx:
			continue
		case ev.EdgeIndex > idx:
			ev.EdgeIndex--
		}
		evs = append(evs, ev)
	}
	p.Edges = edges
	p.EdgeEvidence = append([]gfoEdgeEvidence{}, evs...)
	gfxSortPayload(p)
	gfxSeal(t, a, true)
}

// gfxObsIndex returns the index of the observation of subjectKey/signal in
// frame i or fails the test.
func gfxObsIndex(t *testing.T, a *gfoArtifact, i int, subjectKey, signal string) int {
	t.Helper()
	for k, o := range a.Frames[i].Payload.Observations {
		if o.Subject.Key() == subjectKey && o.Signal == signal {
			return k
		}
	}
	t.Fatalf("test bug: frame %d has no %s observation of %s", i, signal, subjectKey)
	return -1
}

func gfxSetDim(o *gfoObs, k, v string) {
	for i := range o.Dims {
		if o.Dims[i].K == k {
			o.Dims[i].V = v
			return
		}
	}
	o.Dims = append(o.Dims, gfoKV{k, v})
	sort.Slice(o.Dims, func(i, j int) bool { return o.Dims[i].K < o.Dims[j].K })
}

// ---------------------------------------------------------------------------
// Input files and explain runs.
// ---------------------------------------------------------------------------

type gfxInputs struct{ Dir, Artifact, Fleet string }

func gfxWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// gfxWriteInputs writes the artifact bytes and the fleet file into a fresh
// t.TempDir() (GFX-104).
func gfxWriteInputs(t *testing.T, artifact []byte, fleet string) gfxInputs {
	t.Helper()
	dir := t.TempDir()
	in := gfxInputs{Dir: dir, Artifact: filepath.Join(dir, "artifact.json"), Fleet: filepath.Join(dir, "fleet.json")}
	gfxWriteFile(t, in.Artifact, artifact)
	gfxWriteFile(t, in.Fleet, []byte(fleet))
	return in
}

func gfxArgs(kind, name string, in gfxInputs, output string) []string {
	args := []string{"explain", kind, name, "--artifact", in.Artifact, "--fleet", in.Fleet}
	if output != "" {
		args = append(args, "--output", output)
	}
	return args
}

// gfxCase bundles one artifact and one fleet file with the built binaries.
type gfxCase struct {
	t      *testing.T
	Bins   gfxBins
	Art    *gfoArtifact
	Fleet  gfxFleet
	In     gfxInputs
	replay *gfxReplay
	rerr   error
	done   bool
}

func gfxNewCase(t *testing.T, bins gfxBins, a *gfoArtifact, fl gfxFleet) *gfxCase {
	t.Helper()
	raw := a.Raw
	if len(raw) == 0 {
		raw = gfoRenderArtifact(a)
	}
	return &gfxCase{t: t, Bins: bins, Art: a, Fleet: fl, In: gfxWriteInputs(t, raw, fl.JSON())}
}

// Oracle returns the independent GFX-101 replay of this case (computed once).
func (c *gfxCase) Oracle() (*gfxReplay, error) {
	if !c.done {
		c.replay, c.rerr = gfxReplayOf(c.Art, c.Fleet)
		c.done = true
	}
	return c.replay, c.rerr
}

// Run runs explain with the given output flag ("" keeps the default).
func (c *gfxCase) Run(kind, name, output string) gfoResult {
	c.t.Helper()
	return gfxRun(c.t, c.Bins.Pathctl, gfoRunOpt{}, gfxArgs(kind, name, c.In, output)...)
}

// Check runs `--output json` and `--output text` (and the default output),
// applies every structural rule of §6/§7 to the JSON output, requires the
// text output to equal its reconstruction from the JSON output (GFX-070) and
// compares the JSON output with the independent oracle (GFX-101). It returns
// the decoded JSON explanation for scenario-specific assertions, with the
// text output of the same request in Text.
func (c *gfxCase) Check(kind, name, clause string) *gfxExpl {
	t := c.t
	t.Helper()
	raw := gfxOK(t, c.Run(kind, name, "json"), clause+" --output json")
	e := gfxDecodeChecked(t, raw, clause)
	text := gfxOK(t, c.Run(kind, name, "text"), clause+" --output text")
	e.Text = text
	for _, m := range gfxTextRules(text) {
		t.Errorf("%s: %s", clause, m)
	}
	if want := gfxRenderText(e); string(text) != want {
		i := gfoDiffAt(text, []byte(want))
		t.Errorf("%s: GFX-070..077: text output differs from its reconstruction from the JSON output at byte %d\n got: %q\nwant: %q",
			clause, i, gfoAround(text, i), gfoAround([]byte(want), i))
	}
	def := gfxOK(t, c.Run(kind, name, ""), clause+" default output")
	if !bytes.Equal(def, text) {
		t.Errorf("%s: GFX-010: default output differs from --output text", clause)
	}
	gfxCompareOracle(t, c, kind, name, raw, clause)
	return e
}

func gfxJoin(items []string) string { return "[" + strings.Join(items, ", ") + "]" }

// gfxSpaced inserts JSON whitespace (space, tab, CR, LF) around structural
// characters outside strings without changing the value.
func gfxSpaced(s string) string {
	var b strings.Builder
	in, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if in {
			b.WriteByte(c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				in = false
			}
			continue
		}
		switch c {
		case '"':
			in = true
			b.WriteByte(c)
		case ',':
			b.WriteString(" ,\n\t")
		case ':':
			b.WriteString(" :\r\n ")
		case '{', '[':
			b.WriteByte(c)
			b.WriteString("\n  ")
		case '}', ']':
			b.WriteString("\t\n")
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// gfxEnv returns a copy of the test process environment.
func gfxEnv() []string { return append([]string{}, os.Environ()...) }

// gfxWant compares printable values.
func gfxWant(t *testing.T, clause, what string, got, want any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s: %s = %v, want %v", clause, what, got, want)
	}
}

// gfxWantCov asserts the state and reason of one coverage element.
func gfxWantCov(t *testing.T, clause string, e *gfxExpl, name, state, reason string) {
	t.Helper()
	c, ok := e.Cov(name)
	if !ok {
		t.Errorf("%s: coverage %q missing (have %d elements)", clause, name, len(e.Coverage))
		return
	}
	if c.State != state || c.Reason != reason {
		t.Errorf("%s: coverage %s = %s/%s, want %s/%s", clause, name, c.State, c.Reason, state, reason)
	}
}

// gfxWantLims asserts that every entry of present is in the limitations and
// no entry of absent is. Entries are "code" or "code subject"; subject "*"
// matches any subject.
func gfxWantLims(t *testing.T, clause string, e *gfxExpl, present, absent []string) {
	t.Helper()
	split := func(s string) (string, string) {
		if i := strings.IndexByte(s, ' '); i >= 0 {
			return s[:i], s[i+1:]
		}
		return s, ""
	}
	for _, p := range present {
		code, subject := split(p)
		if !e.HasLim(code, subject) {
			t.Errorf("%s: limitation %q missing from %v", clause, p, e.Lims())
		}
	}
	for _, p := range absent {
		code, subject := split(p)
		if subject == "" {
			subject = "*"
			if e.HasLim(code, "") {
				t.Errorf("%s: unexpected limitation %q in %v", clause, p, e.Lims())
				continue
			}
		}
		if e.HasLim(code, subject) {
			t.Errorf("%s: unexpected limitation %q in %v", clause, p, e.Lims())
		}
	}
}

// gfxWantSegs asserts the exact ordered from -> to list of pathSegments.
func gfxWantSegs(t *testing.T, clause string, e *gfxExpl, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := e.SegStrings(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s: pathSegments %v, want %v", clause, got, want)
	}
}

func gfxItoa(n int) string { return strconv.Itoa(n) }
