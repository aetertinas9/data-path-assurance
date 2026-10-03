package liveingest_test

// GLI-078 offline <-> live parity. For every scenario of GFX-102 that GLI-078
// names, the test builds the fixture in a temporary directory, lets the
// path-agent binary (--fixture-root) write the offline artifact, maps the JSON
// frames to wire frames (GLI-062, GLI-123), feeds them to an in-process ingest
// server in order and, after every accepted frame, asks the live assessor
// (app.NewLiveAssessor(server.BundleSource())) for the node at the frame time.
// The expectation is the independent GFX-048/049 replay (fleet.EvaluateDevice
// chain and fleet.AggregateNode) over the same frames; the nic-lldp-remote
// coverage item, which S1 renders by trust mode, is left out of that comparison
// and compared with an S1 call that uses the live trust profile. The CP of every
// unedited artifact frame must also equal the digest the agent wrote
// (GLI-061, GLI-062).
//
// Top-level identifiers of this file use the prefix gliPar.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/ingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// gliParSessions is a SessionStore that always issues one fixed session (the
// session of the offline artifact), which GLI-031 allows when the stored value
// and after are smaller.
type gliParSessions struct{ session int64 }

var _ liveingest.SessionStore = gliParSessions{}

func (s gliParSessions) AllocateSession(ctx context.Context, node fleet.NodeRef, after int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return s.session, nil
}

// ---------------------------------------------------------------------------
// Build and fixture.
// ---------------------------------------------------------------------------

func gliParRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("no go.mod above the test directory")
	return ""
}

func gliParBuildAgent(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "path-agent")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/path-agent")
	cmd.Dir = gliParRepoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/path-agent: %v\n%s", err, out)
	}
	return bin
}

const (
	gliParHB = "pci0000:00"
	gliParT0 = "2026-09-24T00:00:00Z"
)

type gliParSrc struct{ Cap, Type, Name string }

type gliParDev struct {
	BDF   string
	Path  []string
	Attrs map[string]string
}

// gliParFx is a GFO fixture tree under a temporary directory.
type gliParFx struct {
	t         *testing.T
	Root      string
	Frames    []string
	Profile   string
	Sources   []gliParSrc
	Baselines bool
}

func gliParTime(i int) string {
	t0, _ := time.Parse(time.RFC3339, gliParT0)
	return t0.Add(time.Duration(i) * 15 * time.Second).Format(time.RFC3339Nano)
}

func gliParNewFx(t *testing.T, frames int) *gliParFx {
	t.Helper()
	f := &gliParFx{
		t: t, Root: filepath.Join(t.TempDir(), "fixture"), Profile: "offline:lab-a-basic", Baselines: true,
		Sources: []gliParSrc{
			{"SysfsPhysicalParent", "agent", gliFxParentSrc},
			{"SysfsPCIeWidth", "agent", gliFxWidthSrc},
			{"OperatorBaseline", "agent", gliFxWidthSrc},
			{"NVIDIAUUIDBinding", "agent", gliFxNVSrc},
		},
	}
	for i := 0; i < frames; i++ {
		f.Frames = append(f.Frames, gliParTime(i))
		f.mkdir(f.sys(i))
	}
	return f
}

func (f *gliParFx) sys(i int) string { return fmt.Sprintf("frames/%d/sys", i) }

func (f *gliParFx) p(rel string) string { return filepath.Join(f.Root, filepath.FromSlash(rel)) }

func (f *gliParFx) mkdir(rel string) {
	f.t.Helper()
	if err := os.MkdirAll(f.p(rel), 0o755); err != nil {
		f.t.Fatalf("mkdir %s: %v", rel, err)
	}
}

func (f *gliParFx) write(rel, data string) {
	f.t.Helper()
	f.mkdir(filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel))))
	if err := os.WriteFile(f.p(rel), []byte(data), 0o644); err != nil {
		f.t.Fatalf("write %s: %v", rel, err)
	}
}

func (f *gliParFx) remove(rel string) {
	f.t.Helper()
	if err := os.RemoveAll(f.p(rel)); err != nil {
		f.t.Fatalf("remove %s: %v", rel, err)
	}
}

// dev writes one PCI function directory with its attributes and the
// bus/pci/devices entry symlink with a relative target.
func (f *gliParFx) dev(frame int, d gliParDev) {
	f.t.Helper()
	dir := f.sys(frame) + "/devices/" + strings.Join(d.Path, "/")
	f.mkdir(dir)
	names := make([]string, 0, len(d.Attrs))
	for k := range d.Attrs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		f.write(dir+"/"+k, d.Attrs[k])
	}
	if d.BDF != "" {
		link := f.sys(frame) + "/bus/pci/devices/" + d.BDF
		f.mkdir(filepath.ToSlash(filepath.Dir(link)))
		if err := os.Symlink("../../../devices/"+strings.Join(d.Path, "/"), f.p(link)); err != nil {
			f.t.Fatalf("symlink %s: %v", link, err)
		}
	}
}

func gliParPCI(path []string, class, vendor, cur, max string) gliParDev {
	attrs := map[string]string{"class": class + "\n", "vendor": vendor + "\n"}
	if cur != "" {
		attrs["current_link_width"] = cur + "\n"
	}
	if max != "" {
		attrs["max_link_width"] = max + "\n"
	}
	return gliParDev{BDF: path[len(path)-1], Path: path, Attrs: attrs}
}

var (
	gliParPathRPA = []string{gliParHB, gliFxRPABDF}
	gliParPathSWU = []string{gliParHB, gliFxRPABDF, gliFxSWUBDF}
	gliParPathSWD = []string{gliParHB, gliFxRPABDF, gliFxSWUBDF, gliFxSWDBDF}
	gliParPathGPU = []string{gliParHB, gliFxRPABDF, gliFxSWUBDF, gliFxSWDBDF, gliFxGPUBDF}
	gliParPathRPB = []string{gliParHB, gliFxRPBBDF}
	gliParPathNIC = []string{gliParHB, gliFxRPBBDF, gliFxNICBDF}
)

// base adds the BASE topology of GFX-102 to frame i.
func (f *gliParFx) base(i int) {
	f.t.Helper()
	f.dev(i, gliParPCI(gliParPathRPA, "0x060400", "0x8086", "16", "16"))
	f.dev(i, gliParPCI(gliParPathSWU, "0x060400", "0x10b5", "16", "16"))
	f.dev(i, gliParPCI(gliParPathSWD, "0x060400", "0x10b5", "16", "16"))
	f.dev(i, gliParPCI(gliParPathGPU, "0x030200", "0x10de", "16", "16"))
	f.dev(i, gliParPCI(gliParPathRPB, "0x060400", "0x8086", "16", "16"))
	f.dev(i, gliParPCI(gliParPathNIC, "0x020000", "0x15b3", "16", "16"))
	f.write(fmt.Sprintf("frames/%d/nvidia-smi.csv", i), gliFxUUID+", 00000000:03:00.0\n")
}

func gliParBaseFx(t *testing.T, frames int) *gliParFx {
	t.Helper()
	f := gliParNewFx(t, frames)
	for i := 0; i < frames; i++ {
		f.base(i)
	}
	return f
}

func (f *gliParFx) addFrame(at string) int {
	f.t.Helper()
	i := len(f.Frames)
	f.Frames = append(f.Frames, at)
	f.mkdir(f.sys(i))
	f.base(i)
	return i
}

func (f *gliParFx) attr(frame int, path []string, name string) string {
	return f.sys(frame) + "/devices/" + strings.Join(path, "/") + "/" + name
}

func (f *gliParFx) dropSources(caps ...string) {
	var keep []gliParSrc
	for _, s := range f.Sources {
		drop := false
		for _, c := range caps {
			drop = drop || s.Cap == c
		}
		if !drop {
			keep = append(keep, s)
		}
	}
	f.Sources = keep
}

type gliParManifestSrc struct {
	Capability string `json:"capability"`
	SourceType string `json:"sourceType"`
	SourceName string `json:"sourceName"`
}

type gliParManifest struct {
	Schema  string `json:"schemaVersion"`
	Cluster string `json:"clusterID"`
	Node    struct {
		Name string `json:"name"`
		UID  string `json:"uid"`
	} `json:"node"`
	BootID string `json:"bootID"`
	Trust  struct {
		ProfileID string              `json:"profileID"`
		Session   int                 `json:"session"`
		Sources   []gliParManifestSrc `json:"sources"`
	} `json:"trust"`
	TTL       int                          `json:"evidenceTTLSeconds"`
	Frames    []map[string]string          `json:"frames"`
	Baselines []map[string]json.RawMessage `json:"operatorBaselines,omitempty"`
}

func (f *gliParFx) manifest() []byte {
	f.t.Helper()
	var m gliParManifest
	m.Schema, m.Cluster, m.BootID, m.TTL = "dpa.offline-fixture/v1", gliFxCluster, gliFxBootID, 300
	m.Node.Name, m.Node.UID = gliFxNodeName, gliFxNodeUID
	m.Trust.ProfileID, m.Trust.Session = f.Profile, 7
	for _, s := range f.Sources {
		m.Trust.Sources = append(m.Trust.Sources, gliParManifestSrc{s.Cap, s.Type, s.Name})
	}
	for _, at := range f.Frames {
		m.Frames = append(m.Frames, map[string]string{"observedAt": at})
	}
	if f.Baselines {
		m.Baselines = []map[string]json.RawMessage{{
			"function": json.RawMessage(strconv.Quote(gliFxGPUBDF)), "peer": json.RawMessage(strconv.Quote(gliFxSWDBDF)),
			"expectedWidth": json.RawMessage("16"),
		}}
	}
	b, err := json.Marshal(m)
	if err != nil {
		f.t.Fatalf("marshal manifest: %v", err)
	}
	return b
}

// run writes the manifest and returns the artifact path-agent prints.
func (f *gliParFx) run(bin string) []byte {
	f.t.Helper()
	f.write("manifest.json", string(f.manifest()))
	cmd := exec.Command(bin, "--fixture-root", f.Root)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		f.t.Fatalf("fixture setup: path-agent --fixture-root failed: %v; stderr=%q", err, stderr.String())
	}
	return stdout.Bytes()
}

// ---------------------------------------------------------------------------
// Artifact JSON and its mapping to wire frames (GLI-062).
// ---------------------------------------------------------------------------

type gliParAlias struct {
	Namespace string `json:"namespace"`
	Value     string `json:"value"`
}

type gliParAsset struct {
	Kind      string        `json:"kind"`
	Canonical string        `json:"canonical"`
	Aliases   []gliParAlias `json:"aliases"`
}

type gliParArt struct {
	ClusterID string `json:"clusterID"`
	Node      struct {
		Name string `json:"name"`
		UID  string `json:"uid"`
	} `json:"node"`
	BootID string `json:"bootID"`
	Trust  struct {
		ProfileID string `json:"profileID"`
		Session   string `json:"session"`
		Sources   []struct {
			Capability string `json:"capability"`
			Source     struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"source"`
		} `json:"sources"`
	} `json:"trust"`
	Frames []struct {
		NodeUID        string `json:"nodeUID"`
		BootID         string `json:"bootID"`
		Session        string `json:"session"`
		Sequence       string `json:"sequence"`
		Completeness   string `json:"completeness"`
		ObservedAt     string `json:"observedAt"`
		PayloadDigest  string `json:"payloadDigest"`
		BundleRevision string `json:"bundleRevision"`
		Payload        struct {
			Assets []gliParAsset `json:"assets"`
			Edges  []struct {
				FromKey  string `json:"fromKey"`
				Relation string `json:"relation"`
				ToKey    string `json:"toKey"`
				Origin   string `json:"origin"`
			} `json:"edges"`
			EdgeEvidence []struct {
				EdgeIndex  uint32 `json:"edgeIndex"`
				Kind       string `json:"kind"`
				SourceType string `json:"sourceType"`
				SourceName string `json:"sourceName"`
				ObservedAt string `json:"observedAt"`
				ExpiresAt  string `json:"expiresAt"`
				EvidenceID string `json:"evidenceID"`
			} `json:"edgeEvidence"`
			Observations []struct {
				ID     string `json:"id"`
				Source struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"source"`
				Subject    gliParAsset       `json:"subject"`
				Signal     string            `json:"signal"`
				Value      map[string]any    `json:"value"`
				Unit       string            `json:"unit"`
				Dimensions map[string]string `json:"dimensions"`
				ObservedAt string            `json:"observedAt"`
				ReceivedAt string            `json:"receivedAt"`
				ExpiresAt  string            `json:"expiresAt"`
				Sequence   string            `json:"sequence"`
				Quality    string            `json:"quality"`
				RawDigest  string            `json:"rawDigest"`
			} `json:"observations"`
			GPUBindings []struct {
				UUID       string `json:"uuid"`
				Serial     string `json:"serial"`
				BDF        string `json:"bdf"`
				SourceType string `json:"sourceType"`
				SourceName string `json:"sourceName"`
				EvidenceID string `json:"evidenceID"`
				ObservedAt string `json:"observedAt"`
				ExpiresAt  string `json:"expiresAt"`
			} `json:"gpuBindings"`
		} `json:"payload"`
	} `json:"frames"`
}

func gliParTS(t *testing.T, s string) *timestamppb.Timestamp {
	t.Helper()
	if s == "" {
		return nil
	}
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("artifact time %q: %v", s, err)
	}
	return timestamppb.New(v.UTC())
}

func gliParAliases(in []gliParAlias) []*ingestpb.Alias {
	var out []*ingestpb.Alias
	for _, a := range in {
		out = append(out, &ingestpb.Alias{Namespace: a.Namespace, Value: a.Value})
	}
	return out
}

func gliParValue(t *testing.T, m map[string]any) *ingestpb.Value {
	t.Helper()
	if len(m) != 1 {
		t.Fatalf("artifact value %v does not have exactly one alternative", m)
	}
	for k, v := range m {
		switch k {
		case "int":
			n, err := strconv.ParseInt(v.(string), 10, 64)
			if err != nil {
				t.Fatalf("artifact int %v: %v", v, err)
			}
			return gliFxInt(n)
		case "float":
			f, err := strconv.ParseFloat(v.(string), 64)
			if err != nil {
				t.Fatalf("artifact float %v: %v", v, err)
			}
			return gliFxFloat(f)
		case "bool":
			return gliFxBool(v.(bool))
		case "string":
			return gliFxStrVal(v.(string))
		}
	}
	t.Fatalf("artifact value %v has an unknown alternative", m)
	return nil
}

// gliParFrames maps the artifact frames to wire frames (GLI-062). Diagnostics
// are not part of the wire frame.
func gliParFrames(t *testing.T, raw []byte) (*gliParArt, []*ingestpb.SnapshotFrame) {
	t.Helper()
	var a gliParArt
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("artifact JSON: %v", err)
	}
	var out []*ingestpb.SnapshotFrame
	for k, fr := range a.Frames {
		session, err1 := strconv.ParseInt(fr.Session, 10, 64)
		seq, err2 := strconv.ParseUint(fr.Sequence, 10, 64)
		digest, err3 := hex.DecodeString(fr.PayloadDigest)
		if err1 != nil || err2 != nil || err3 != nil {
			t.Fatalf("artifact frame %d: session/sequence/digest: %v %v %v", k, err1, err2, err3)
		}
		comp := ingestpb.Completeness_COMPLETE
		if fr.Completeness == "PARTIAL" {
			comp = ingestpb.Completeness_PARTIAL
		}
		p := &ingestpb.HostSnapshotV1{}
		for _, as := range fr.Payload.Assets {
			p.Assets = append(p.Assets, &ingestpb.Asset{Kind: as.Kind, Canonical: as.Canonical, Aliases: gliParAliases(as.Aliases)})
		}
		for _, e := range fr.Payload.Edges {
			p.Edges = append(p.Edges, &ingestpb.Edge{FromKey: e.FromKey, Relation: e.Relation, ToKey: e.ToKey, Origin: e.Origin})
		}
		for _, ev := range fr.Payload.EdgeEvidence {
			p.EdgeEvidence = append(p.EdgeEvidence, &ingestpb.EdgeEvidence{
				EdgeIndex: ev.EdgeIndex, Kind: ev.Kind, SourceType: ev.SourceType, SourceName: ev.SourceName,
				ObservedAt: gliParTS(t, ev.ObservedAt), ExpiresAt: gliParTS(t, ev.ExpiresAt), EvidenceId: ev.EvidenceID,
			})
		}
		for _, o := range fr.Payload.Observations {
			oseq, err := strconv.ParseUint(o.Sequence, 10, 64)
			if err != nil {
				t.Fatalf("artifact observation sequence %q: %v", o.Sequence, err)
			}
			keys := make([]string, 0, len(o.Dimensions))
			for key := range o.Dimensions {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			var dims []*ingestpb.Dimension
			for _, key := range keys {
				dims = append(dims, &ingestpb.Dimension{Key: key, Value: o.Dimensions[key]})
			}
			p.Observations = append(p.Observations, &ingestpb.Observation{
				Id: o.ID, Source: &ingestpb.SourceRef{Type: o.Source.Type, Name: o.Source.Name},
				Subject: &ingestpb.Asset{Kind: o.Subject.Kind, Canonical: o.Subject.Canonical, Aliases: gliParAliases(o.Subject.Aliases)},
				Signal:  o.Signal, Value: gliParValue(t, o.Value), Unit: o.Unit, Dimensions: dims,
				ObservedAt: gliParTS(t, o.ObservedAt), ReceivedAt: gliParTS(t, o.ReceivedAt), ExpiresAt: gliParTS(t, o.ExpiresAt),
				Sequence: oseq, Quality: o.Quality, RawDigest: o.RawDigest,
			})
		}
		for _, b := range fr.Payload.GPUBindings {
			p.GpuBindings = append(p.GpuBindings, &ingestpb.GPUBinding{
				Uuid: b.UUID, Serial: b.Serial, Bdf: b.BDF, SourceType: b.SourceType, SourceName: b.SourceName,
				EvidenceId: b.EvidenceID, ObservedAt: gliParTS(t, b.ObservedAt), ExpiresAt: gliParTS(t, b.ExpiresAt),
			})
		}
		out = append(out, &ingestpb.SnapshotFrame{
			NodeUid: fr.NodeUID, BootId: fr.BootID, Session: session, Sequence: seq, Completeness: comp,
			ObservedAt: gliParTS(t, fr.ObservedAt), Payload: p, PayloadDigest: digest, BundleRevision: fr.BundleRevision,
		})
	}
	return &a, out
}

func gliParCapability(t *testing.T, name string) fleet.TrustCapability {
	t.Helper()
	for _, c := range []fleet.TrustCapability{
		fleet.TrustNVIDIAUUIDBinding, fleet.TrustSysfsPhysicalParent, fleet.TrustSysfsPCIeWidth,
		fleet.TrustPodResourcesUUIDAllocation, fleet.TrustOperatorBaseline, fleet.TrustExternalFence,
	} {
		if c.String() == name {
			return c
		}
	}
	t.Fatalf("unknown trust capability %q", name)
	return 0
}

// ---------------------------------------------------------------------------
// Scenarios (GFX-102).
// ---------------------------------------------------------------------------

type gliParFleet struct {
	Desired  string
	IntentAt string
}

type gliParScenario struct {
	name  string
	build func(t *testing.T) *gliParFx
	edit  func(t *testing.T, frames []*ingestpb.SnapshotFrame)
	fleet func(f *gliParFleet)
	// want is "phase/qualification/reason" of the last decision with * as a
	// wildcard: the GFX-102 table value, used to show that the oracle is not
	// vacuous.
	want string
}

func gliParReady(t *testing.T) *gliParFx {
	f := gliParBaseFx(t, 3)
	f.Profile = "offline:lab-a-width-replay"
	return f
}

func gliParGPUKey() string { return gliFxFnKey(gliFxGPUBDF) }

// gliParEditObs applies mod to the GPU observation of the signal in frame k and reseals.
func gliParEditObs(t *testing.T, frames []*ingestpb.SnapshotFrame, k int, signal string, mod func(o *ingestpb.Observation)) {
	t.Helper()
	o := gliFxFindObs(frames[k].GetPayload(), gliParGPUKey(), signal)
	if o == nil {
		t.Fatalf("fixture setup: frame %d has no %s observation of the GPU", k, signal)
	}
	mod(o)
	gliReseal(t, frames[k])
}

func gliParScenarios() []gliParScenario {
	basic := func(t *testing.T) *gliParFx { return gliParBaseFx(t, 1) }
	width := func(t *testing.T) *gliParFx {
		f := gliParReady(t)
		for i := 0; i < 3; i++ {
			f.write(f.attr(i, gliParPathGPU, "current_link_width"), "8\n")
		}
		return f
	}
	return []gliParScenario{
		{"S-BASIC", basic, nil, nil, "Validating/Unknown/Validating"},
		{"S-WIDTH", width, nil, nil, "Degraded/Disqualified/Degraded"},
		{"S-READY", gliParReady, nil, nil, "Ready/Qualified/Ready"},
		{"S-LASTPARTIAL", func(t *testing.T) *gliParFx {
			f := gliParReady(t)
			f.remove(f.attr(2, gliParPathGPU, "class"))
			return f
		}, nil, nil, "Pending/Unknown/Validating"},
		{"S-STALE", func(t *testing.T) *gliParFx {
			f := gliParReady(t)
			f.Frames = []string{"2026-09-24T00:00:00Z", "2026-09-24T00:00:15Z", "2026-09-24T00:01:16Z"}
			f.remove(f.attr(2, gliParPathGPU, "current_link_width"))
			return f
		}, nil, nil, "Pending/Unknown/Validating"},
		{"S-CARRY", func(t *testing.T) *gliParFx {
			f := gliParReady(t)
			f.remove(f.attr(2, gliParPathGPU, "current_link_width"))
			return f
		}, nil, nil, "Validating/*/*"},
		{"S-CARRY4", func(t *testing.T) *gliParFx {
			f := gliParReady(t)
			i := f.addFrame("2026-09-24T00:00:45Z")
			f.remove(f.attr(i, gliParPathGPU, "current_link_width"))
			return f
		}, nil, nil, "Ready/Qualified/*"},
		{"S-NICBASE", func(t *testing.T) *gliParFx {
			f := gliParReady(t)
			for i := 1; i <= 2; i++ {
				f.write(f.attr(i, gliParPathNIC, "current_link_width"), "8\n")
				f.write(f.attr(i, gliParPathNIC, "max_link_width"), "8\n")
			}
			return f
		}, nil, nil, "Validating/Unknown/*"},
		{"S-REVERT", func(t *testing.T) *gliParFx {
			f := gliParReady(t)
			f.addFrame("2026-09-24T00:00:45Z")
			return f
		}, func(t *testing.T, frames []*ingestpb.SnapshotFrame) {
			for _, sig := range []string{gliFxSigCurrent, gliFxSigExpect} {
				gliParEditObs(t, frames, 1, sig, func(o *ingestpb.Observation) {
					for _, d := range o.Dimensions {
						if d.Key == "pcie.expected.provenance" {
							d.Value = gliFxProvAdj
						}
					}
				})
			}
		}, nil, "Validating/*/*"},
		{"S-ROOTLESS", func(t *testing.T) *gliParFx {
			f := gliParNewFx(t, 1)
			f.dev(0, gliParPCI(gliParPathRPA, "0x060400", "0x8086", "16", "16"))
			f.dev(0, gliParPCI(gliParPathSWU, "0x060400", "0x10b5", "16", "16"))
			f.dev(0, gliParPCI(gliParPathSWD, "0x060400", "0x10b5", "16", "16"))
			f.dev(0, gliParPCI([]string{gliParHB, gliFxGPUBDF}, "0x030200", "0x10de", "16", "16"))
			f.dev(0, gliParPCI(gliParPathRPB, "0x060400", "0x8086", "16", "16"))
			f.dev(0, gliParPCI(gliParPathNIC, "0x020000", "0x15b3", "16", "16"))
			f.write("frames/0/nvidia-smi.csv", gliFxUUID+", 00000000:03:00.0\n")
			return f
		}, nil, nil, "Pending/Unknown/*"},
		{"S-CONTRA", func(t *testing.T) *gliParFx {
			f := gliParBaseFx(t, 1)
			f.dev(0, gliParDev{BDF: "0000:05:00.0", Path: []string{gliParHB, gliFxRPBBDF, gliFxSWDBDF, "0000:05:00.0"}, Attrs: map[string]string{"class": "0x020000\n"}})
			return f
		}, nil, nil, "Pending/Unknown/*"},
		{"S-CONTRA-F", basic, func(t *testing.T, frames []*ingestpb.SnapshotFrame) {
			f := frames[0]
			gliFxAddEdge(f.Payload, f.Session, f.Sequence, f.ObservedAt.AsTime(), gliFxFnKey(gliFxGPUBDF), gliFxSWKey(gliFxSWUBDF))
			gliReseal(t, f)
		}, nil, ""},
		{"S-IDCONFLICT", basic, func(t *testing.T, frames []*ingestpb.SnapshotFrame) {
			f := frames[0]
			b := f.Payload.GpuBindings[0]
			f.Payload.GpuBindings = append(f.Payload.GpuBindings, &ingestpb.GPUBinding{
				Uuid: b.Uuid, Bdf: gliFxNICBDF, SourceType: b.SourceType, SourceName: b.SourceName,
				EvidenceId: gliFxBindID(f.Session, f.Sequence, b.Uuid, gliFxNICBDF),
				ObservedAt: timestamppb.New(b.ObservedAt.AsTime()), ExpiresAt: timestamppb.New(b.ExpiresAt.AsTime()),
			})
			gliFxSortPayload(f.Payload)
			gliReseal(t, f)
		}, nil, "*/Unknown/IdentityConflict"},
		{"S-FLOATBASE", gliParReady, func(t *testing.T, frames []*ingestpb.SnapshotFrame) {
			gliParEditObs(t, frames, 1, gliFxSigExpect, func(o *ingestpb.Observation) { o.Value = gliFxFloat(16) })
		}, nil, "Ready/Qualified/*"},
		{"S-INTENT-Maintenance", basic, nil, func(f *gliParFleet) { f.Desired = "Maintenance" }, "MaintenancePending/*/AllocationUnknown"},
		{"S-INTENT-Retired", basic, nil, func(f *gliParFleet) { f.Desired = "Retired" }, "Retiring/*/AllocationUnknown"},
		{"S-LATEINTENT", basic, nil, func(f *gliParFleet) { f.IntentAt = "2026-09-24T00:00:01Z" }, "*/Unknown/*"},
		{"S-UNTRUSTED", func(t *testing.T) *gliParFx {
			f := gliParBaseFx(t, 1)
			f.dropSources("NVIDIAUUIDBinding")
			return f
		}, nil, nil, "*/Unknown/*"},
		{"S-UNTRUSTED-PARENT", func(t *testing.T) *gliParFx {
			f := gliParBaseFx(t, 1)
			f.dropSources("SysfsPhysicalParent")
			return f
		}, nil, nil, "Pending/Unknown/*"},
		{"S-UNTRUSTED-WIDTH", func(t *testing.T) *gliParFx {
			f := width(t)
			f.dropSources("SysfsPCIeWidth", "OperatorBaseline")
			return f
		}, nil, nil, "Pending/Unknown/Validating"},
		{"S-UNTRUSTED-WIDTHONLY", func(t *testing.T) *gliParFx {
			f := width(t)
			f.dropSources("SysfsPCIeWidth")
			return f
		}, nil, nil, "Pending/Unknown/Validating"},
		{"S-UNTRUSTED-BASELINE", func(t *testing.T) *gliParFx {
			f := width(t)
			f.dropSources("OperatorBaseline")
			return f
		}, nil, nil, "Pending/Unknown/Validating"},
	}
}

func gliParWant(t *testing.T, got fleet.DeviceDecision, want, clause string) {
	t.Helper()
	if want == "" {
		return
	}
	g := []string{got.Phase.String(), got.Qualification.String(), got.Reason}
	for i, w := range strings.Split(want, "/") {
		if w != "*" && w != g[i] {
			t.Errorf("%s: oracle decision %s, want the GFX-102 row %s (the oracle itself disagrees with the table)", clause, strings.Join(g, "/"), want)
			return
		}
	}
}

// GLI-078: offline <-> live parity for the 21 scenarios of GFX-102 that the
// clause names. One go build of path-agent serves all subtests.
func TestGLI078_OfflineLiveParity(t *testing.T) {
	bin := gliParBuildAgent(t)
	for _, sc := range gliParScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			fx := sc.build(t)
			art, frames := gliParFrames(t, fx.run(bin))
			// GLI-061/062: the independent CP of every artifact frame is the digest of the agent.
			for k, f := range frames {
				d := gliDigest(f.Payload)
				if got := gliFxHex(f.PayloadDigest); got != gliFxHex(d[:]) {
					t.Fatalf("GLI-061: frame %d: the agent's payloadDigest %s differs from the independent CP digest %s", k, got, gliFxHex(d[:]))
				}
				if want := gliFxRevision(f.Session, f.Sequence, f.Payload); f.BundleRevision != want {
					t.Fatalf("GLI-061: frame %d: bundleRevision %q, independent %q", k, f.BundleRevision, want)
				}
			}
			if sc.edit != nil {
				sc.edit(t, frames)
			}
			session, err := strconv.ParseInt(art.Trust.Session, 10, 64)
			if err != nil {
				t.Fatalf("trust.session: %v", err)
			}
			var srcs []fleet.TrustedSource
			for _, s := range art.Trust.Sources {
				srcs = append(srcs, fleet.TrustedSource{Capability: gliParCapability(t, s.Capability), Source: model.SourceRef{Type: s.Source.Type, Name: s.Source.Name}})
			}
			fl := gliParFleet{Desired: "InService", IntentAt: gliParT0}
			if sc.fleet != nil {
				sc.fleet(&fl)
			}
			pol := gliFxPolicy(t, 60*time.Second)
			in := gliFxDefaultIntent(t)
			in.ObservedAt = gliParTS(t, fl.IntentAt).AsTime()
			switch fl.Desired {
			case "Maintenance":
				in.Desired = fleet.DesiredMaintenance
			case "Retired":
				in.Desired = fleet.DesiredRetired
			}
			if err := in.Validate(); err != nil {
				t.Fatalf("Intent.Validate: %v", err)
			}
			rep, err := gliOrReplay(gliOrReplayIn{
				Frames: frames, Policy: pol, Intents: []fleet.Intent{in}, Cluster: art.ClusterID, NodeName: art.Node.Name, FleetUID: gliFxFleetUID,
				Profile: fleet.CollectorTrustProfile{
					ID: art.Trust.ProfileID, Mode: fleet.TrustModeOffline, ClusterID: art.ClusterID, NodeUID: art.Node.UID, Session: session, Sources: srcs,
				},
			})
			if err != nil {
				t.Fatalf("oracle replay: %v", err)
			}
			gliParWant(t, rep.DecAt[0][len(frames)-1], sc.want, sc.name)
			// S1 renders the nic-lldp-remote coverage item by CollectorTrust.Mode, so
			// that item is compared with an independent S1 call that uses the live trust profile.
			repLive, err := gliOrReplay(gliOrReplayIn{
				Frames: frames, Policy: pol, Intents: []fleet.Intent{in}, Cluster: art.ClusterID, NodeName: art.Node.Name, FleetUID: gliFxFleetUID,
				Profile: fleet.CollectorTrustProfile{
					ID: "live:parity", Mode: fleet.TrustModeLive, ClusterID: art.ClusterID, NodeUID: art.Node.UID, Session: session, Sources: srcs,
				},
				LiveStamps: true,
			})
			if err != nil {
				t.Fatalf("live-profile oracle replay: %v", err)
			}

			env := gliFxStart(t, func(c *ingest.Config) {
				c.ClusterID = art.ClusterID
				c.CollectorProfileID = "live:parity"
				c.TrustedSources = srcs
				c.Sessions = gliParSessions{session: session}
			})
			c := env.Connect(art.Node.Name, art.Node.UID, art.BootID)
			if c.Session != session {
				t.Fatalf("hello session %d, want the artifact session %d from the injected store", c.Session, session)
			}
			if c.Hello.GetCollectorProfileId() != "live:parity" {
				t.Fatalf("hello profile %q, want the configured live:parity (never the artifact's offline profile)", c.Hello.GetCollectorProfileId())
			}
			assessor := app.NewLiveAssessor(env.S.Server.BundleSource())
			var last app.NodeAssessment
			for k, f := range frames {
				if got := c.Classified(f, fmt.Sprintf("frame %d", k)); got != ingestpb.AckCode_ACCEPTED {
					t.Fatalf("frame %d: oracle %s, want ACCEPTED", k, got)
				}
				at := f.ObservedAt.AsTime()
				res, err := assessor.AssessNode(context.Background(), app.NodeAssessmentRequest{
					Node: fleet.NodeRef{ClusterID: art.ClusterID, Name: art.Node.Name, UID: art.Node.UID}, FleetUID: gliFxFleetUID,
					Policy: pol, Selection: fleet.SelectionComplete, Intents: []fleet.Intent{in}, Now: at,
				})
				if err != nil {
					t.Fatalf("frame %d: AssessNode: %v (a live evaluation failure would wrap fleet.ErrInvalidInput)", k, err)
				}
				if len(res.Devices) != 1 || res.Node == nil || res.Observation == nil {
					t.Fatalf("frame %d: AssessNode returned %d device decisions, node %v, observation %v", k, len(res.Devices), res.Node, res.Observation)
				}
				if g, w := gliOrDecisionStringNoNIC(res.Devices[0].Decision), gliOrDecisionStringNoNIC(rep.DecAt[0][k]); g != w {
					t.Errorf("GLI-078 %s frame %d device decision (nic-lldp-remote coverage left out)\n live   %s\n oracle %s", sc.name, k, g, w)
				}
				if g, w := gliOrNICCoverage(res.Devices[0].Decision), gliOrNICCoverage(repLive.DecAt[0][k]); g != w {
					t.Errorf("GLI-078 %s frame %d: nic-lldp-remote coverage differs from the S1 call with the live trust profile\n live   %s\n oracle %s", sc.name, k, g, w)
				}
				wantComp := fleet.CompletenessComplete
				if f.Completeness == ingestpb.Completeness_PARTIAL {
					wantComp = fleet.CompletenessPartial
				}
				if res.Observation.GraphRevision != f.BundleRevision || res.Observation.Completeness != wantComp {
					t.Errorf("GKA-032 frame %d: observation %q/%s, want revision %q and completeness %s",
						k, res.Observation.GraphRevision, res.Observation.Completeness, f.BundleRevision, wantComp)
				}
				last = res
			}
			if g, w := gliOrNodeString(*last.Node), gliOrNodeString(rep.Node); g != w {
				t.Errorf("GLI-078 %s node decision after the last frame (a mismatch here points at the node-memory rule of the assessor versus GFX-049 with a nil previous)\n live   %s\n oracle %s", sc.name, g, w)
			}
		})
	}
	t.Run("S-PARTIAL0 is WRONG_SESSION with no decision", func(t *testing.T) {
		fx := gliParReady(t)
		fx.write("frames/0/sys/bus/pci/devices/.gitkeep", "")
		_, frames := gliParFrames(t, fx.run(bin))
		if frames[0].Completeness != ingestpb.Completeness_PARTIAL {
			t.Fatalf("fixture setup: frame 0 is %s, want PARTIAL", frames[0].Completeness)
		}
		env := gliFxStart(t, func(c *ingest.Config) { c.Sessions = gliParSessions{session: 7} })
		c := env.ConnectDefault()
		c.Reject(frames[0], ingestpb.AckCode_WRONG_SESSION, codes.FailedPrecondition, "GLI-078 S-PARTIAL0")
		env.WantNoObservation("S-PARTIAL0")
		res, err := app.NewLiveAssessor(env.S.Server.BundleSource()).AssessNode(context.Background(), app.NodeAssessmentRequest{
			Node: fleet.NodeRef{ClusterID: gliFxCluster, Name: gliFxNodeName, UID: gliFxNodeUID}, FleetUID: gliFxFleetUID,
			Policy: gliFxPolicy(t, 60*time.Second), Selection: fleet.SelectionComplete, Intents: []fleet.Intent{gliFxDefaultIntent(t)}, Now: gliFxT0,
		})
		if err != nil || len(res.Devices) != 0 || res.Node != nil || res.Observation != nil {
			t.Errorf("S-PARTIAL0: AssessNode = %d decisions, node %v, observation %v, error %v; want the no-observation assessment", len(res.Devices), res.Node, res.Observation, err)
		}
	})
}
