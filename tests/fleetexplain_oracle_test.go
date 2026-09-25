package tests_test

// Independent oracle (GFX-101): replays an artifact and a fleet file through
// the ratified public APIs exactly as §5 prescribes and assembles the
// expected §6 explanation with the §8 limitations. Every step cites its
// clause. The oracle never looks at pathctl output.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// ---------------------------------------------------------------------------
// M (GFO-090) for every value kind.
// ---------------------------------------------------------------------------

var gfxAllKinds = []model.AssetKind{
	model.KindSite, model.KindRow, model.KindRack, model.KindKubernetesNode, model.KindPCIeRootPort,
	model.KindPCIeSwitch, model.KindPCIeFunction, model.KindNICPort, model.KindVF, model.KindEthernetSwitch,
	model.KindSwitchPort, model.KindTransceiver, model.KindPhysicalLink, model.KindPod, model.KindContainer,
}

func gfxRatifyAsset(a gfoAsset) (model.AssetRef, error) {
	for _, k := range gfxAllKinds {
		if k.String() == a.Kind {
			id, err := identity.ParseCanonical(a.Canonical)
			if err != nil {
				return model.AssetRef{}, fmt.Errorf("identity.ParseCanonical(%q): %w", a.Canonical, err)
			}
			return model.NewAssetRef(k, id)
		}
	}
	return model.AssetRef{}, fmt.Errorf("unknown asset kind %q", a.Kind)
}

func gfxValueOf(o gfoObs) (model.Value, error) {
	raw := strings.Trim(o.ValueJSON, `"`)
	switch o.ValueKind {
	case "int":
		n, err := strconv.ParseInt(raw, 10, 64)
		return model.NewIntValue(n), err
	case "float":
		f, err := strconv.ParseFloat(raw, 64)
		return model.NewFloatValue(f), err
	case "bool":
		return model.NewBoolValue(o.ValueJSON == "true"), nil
	case "string":
		var s string
		err := json.Unmarshal([]byte(o.ValueJSON), &s)
		return model.NewStringValue(s), err
	}
	return model.Value{}, fmt.Errorf("value kind %q", o.ValueKind)
}

func gfxQuality(s string) model.EvidenceQuality {
	for _, q := range []model.EvidenceQuality{model.QualityUnknown, model.QualityGood, model.QualityDegraded} {
		if q.String() == s {
			return q
		}
	}
	return model.QualityUnknown
}

// gfxApplyM applies GFO-090 to frame i (binding vendor V = NVIDIA, §0).
func gfxApplyM(a *gfoArtifact, i int) (gfoRatified, error) {
	var r gfoRatified
	fr := a.Frames[i]
	session, err1 := strconv.ParseInt(fr.Session, 10, 64)
	seq, err2 := strconv.ParseUint(fr.Sequence, 10, 64)
	obsAt, err3 := gfoTime(fr.ObservedAt)
	trustSession, err4 := strconv.ParseInt(a.Trust.Session, 10, 64)
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return r, err
	}
	completeness := fleet.CompletenessComplete
	if fr.Completeness == "PARTIAL" {
		completeness = fleet.CompletenessPartial
	}
	r.Envelope = fleet.SnapshotEnvelope{
		NodeUID: fr.NodeUID, BootID: fr.BootID, PayloadDigest: fr.PayloadDigest, BundleRevision: fr.BundleRevision,
		Session: session, Sequence: seq, Completeness: completeness, ObservedAt: obsAt,
	}
	r.Profile = fleet.CollectorTrustProfile{ID: a.Trust.ProfileID, Mode: fleet.TrustModeOffline, ClusterID: a.ClusterID, NodeUID: a.NodeUID, Session: trustSession}
	for _, s := range a.Trust.Sources {
		c, ok := gfoCapabilityOf(s.Capability)
		if !ok {
			return r, fmt.Errorf("capability %q", s.Capability)
		}
		r.Profile.Sources = append(r.Profile.Sources, fleet.TrustedSource{Capability: c, Source: model.SourceRef{Type: s.Type, Name: s.Name}})
	}
	r.ByKey = map[string]model.AssetRef{}
	for _, as := range fr.Payload.Assets {
		ref, err := gfxRatifyAsset(as)
		if err != nil {
			return r, err
		}
		r.Assets = append(r.Assets, ref)
		r.ByKey[as.Key()] = ref
		if ref.Kind == model.KindKubernetesNode {
			r.Node = ref
		}
	}
	for _, e := range fr.Payload.Edges {
		from, ok1 := r.ByKey[e.FromKey]
		to, ok2 := r.ByKey[e.ToKey]
		if !ok1 || !ok2 {
			return r, fmt.Errorf("edge %s -> %s endpoint not in assets", e.FromKey, e.ToKey)
		}
		edge, err := graph.NewEdge(from, to, model.RelLocatedIn, model.OriginObserved)
		if err != nil {
			return r, err
		}
		r.Edges = append(r.Edges, edge)
	}
	for _, ev := range fr.Payload.EdgeEvidence {
		if ev.EdgeIndex < 0 || ev.EdgeIndex >= len(r.Edges) {
			return r, fmt.Errorf("edgeIndex %d", ev.EdgeIndex)
		}
		o, err1 := gfoTime(ev.ObservedAt)
		x, err2 := gfoTime(ev.ExpiresAt)
		if err := errors.Join(err1, err2); err != nil {
			return r, err
		}
		r.Provenance = append(r.Provenance, fleet.EdgeProvenance{
			Edge: r.Edges[ev.EdgeIndex], EvidenceID: ev.EvidenceID, BundleRevision: fr.BundleRevision,
			CollectorProfileID: a.Trust.ProfileID, Kind: fleet.EdgeEvidenceObserved,
			Source: model.SourceRef{Type: ev.SourceType, Name: ev.SourceName}, ObservedAt: o, ExpiresAt: x,
		})
	}
	for _, o := range fr.Payload.Observations {
		subject, err := gfxRatifyAsset(o.Subject)
		if err != nil {
			return r, err
		}
		v, err := gfxValueOf(o)
		if err != nil {
			return r, err
		}
		oa, err1 := gfoTime(o.ObservedAt)
		ra, err2 := gfoTime(o.ReceivedAt)
		xa, err3 := gfoTime(o.ExpiresAt)
		oseq, err4 := strconv.ParseUint(o.Sequence, 10, 64)
		if err := errors.Join(err1, err2, err3, err4); err != nil {
			return r, err
		}
		dims := map[string]string{}
		for _, kv := range o.Dims {
			dims[kv.K] = kv.V
		}
		obs, err := model.NewObservation(model.Observation{
			ID: o.ID, Source: model.SourceRef{Type: o.SourceType, Name: o.SourceName}, Subject: subject,
			Signal: model.SignalRef(o.Signal), Value: v, Unit: o.Unit, Dimensions: dims,
			ObservedAt: oa, ReceivedAt: ra, ExpiresAt: xa, Sequence: oseq, Quality: gfxQuality(o.Quality), RawDigest: o.RawDigest,
		})
		if err != nil {
			return r, fmt.Errorf("model.NewObservation(%s): %w", o.ID, err)
		}
		r.Observations = append(r.Observations, obs)
	}
	for _, b := range fr.Payload.GPUBindings {
		// Function is asset("PCIeFunction/pci-bdf:"+bdf), a key conversion;
		// GFX-035 requires payload assets only for edge endpoints.
		fn, err := gfxRatifyAsset(gfoAsset{Kind: "PCIeFunction", Canonical: "pci-bdf:" + b.BDF})
		if err != nil {
			return r, fmt.Errorf("binding BDF %s: %w", b.BDF, err)
		}
		o, err1 := gfoTime(b.ObservedAt)
		x, err2 := gfoTime(b.ExpiresAt)
		if err := errors.Join(err1, err2); err != nil {
			return r, err
		}
		r.Bindings = append(r.Bindings, fleet.ObservedBinding{
			Node: fleet.NodeRef{ClusterID: a.ClusterID, Name: a.NodeName, UID: a.NodeUID}, BootID: a.BootID, BDF: b.BDF,
			Function: fn,
			Claim:    fleet.InventoryClaim{Vendor: "NVIDIA", UUID: b.UUID, Serial: "", Source: b.SourceName, EvidenceID: b.EvidenceID},
			Source:   model.SourceRef{Type: b.SourceType, Name: b.SourceName}, EvidenceID: b.EvidenceID,
			BundleRevision: fr.BundleRevision, CollectorProfileID: a.Trust.ProfileID, ObservedAt: o, ExpiresAt: x,
		})
	}
	return r, nil
}

// ---------------------------------------------------------------------------
// GFX-044 / GFX-045 digests.
// ---------------------------------------------------------------------------

func gfxEncStr(b []byte, s string) []byte {
	b = gfoAppendU32(b, uint32(len(s)))
	return append(b, s...)
}

// gfxEncList sorts the encoded elements by their bytes and prefixes the count;
// dedup merges byte-equal elements (GFX-045) and otherwise keeps them (GFX-044).
func gfxEncList(elems [][]byte, dedup bool) []byte {
	s := append([][]byte{}, elems...)
	sort.Slice(s, func(i, j int) bool { return bytes.Compare(s[i], s[j]) < 0 })
	if dedup {
		var u [][]byte
		for _, e := range s {
			if len(u) == 0 || !bytes.Equal(u[len(u)-1], e) {
				u = append(u, e)
			}
		}
		s = u
	}
	out := gfoAppendU32(nil, uint32(len(s)))
	for _, e := range s {
		out = append(out, e...)
	}
	return out
}

func gfxEdgeKey(e graph.Edge) string {
	return e.From.Key() + "\x00" + e.Relation.String() + "\x00" + e.To.Key() + "\x00" + e.Origin.String()
}

func gfxTopologyDigest(p model.PartitionKey, r gfoRatified) string {
	var assets, edges [][]byte
	for _, a := range r.Assets {
		assets = append(assets, gfxEncStr(nil, a.Key()))
	}
	// Provenance entries per edge identity, in provenance order.
	provsOf := map[string][][]byte{}
	for _, pv := range r.Provenance {
		x := gfxEncStr(nil, pv.Kind.String())
		x = gfxEncStr(x, pv.Source.Type)
		x = gfxEncStr(x, pv.Source.Name)
		k := gfxEdgeKey(pv.Edge)
		provsOf[k] = append(provsOf[k], x)
	}
	for _, e := range r.Edges {
		entry := gfxEncStr(nil, e.From.Key())
		entry = gfxEncStr(entry, e.Relation.String())
		entry = gfxEncStr(entry, e.To.Key())
		entry = gfxEncStr(entry, e.Origin.String())
		entry = append(entry, gfxEncList(provsOf[gfxEdgeKey(e)], false)...)
		edges = append(edges, entry)
	}
	ct := gfxEncStr(nil, "dpa.TopologyDigest.v1")
	ct = gfxEncStr(ct, p.String())
	ct = append(ct, gfxEncList(assets, false)...)
	ct = append(ct, gfxEncList(edges, false)...)
	sum := sha256.Sum256(ct)
	return hex.EncodeToString(sum[:])
}

func gfxBaselineValue(v model.Value) []byte {
	if n, ok := v.Int(); ok {
		return gfoAppendU64(gfxEncStr(nil, "I"), uint64(n))
	}
	if f, ok := v.Float(); ok {
		if !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) && f >= -9223372036854775808.0 && f < 9223372036854775808.0 {
			return gfoAppendU64(gfxEncStr(nil, "I"), uint64(int64(f)))
		}
		return gfoAppendU64(gfxEncStr(nil, "Float"), math.Float64bits(f))
	}
	if b, ok := v.Bool(); ok {
		out := gfxEncStr(nil, "Bool")
		if b {
			return append(out, 1)
		}
		return append(out, 0)
	}
	s, _ := v.Str()
	return gfxEncStr(gfxEncStr(nil, "String"), s)
}

func gfxBaselineDigest(p model.PartitionKey, w evidence.Window) (string, error) {
	sig := model.SignalRef("pcie.link.width.expected")
	var entries [][]byte
	for _, s := range w.Subjects() {
		sigs, err := w.Signals(s)
		if err != nil {
			return "", err
		}
		has := false
		for _, x := range sigs {
			if x == sig {
				has = true
			}
		}
		if !has {
			continue
		}
		series, err := w.SeriesFor(s, sig)
		if err != nil {
			return "", err
		}
		var latest []model.Observation
		for _, se := range series {
			if o, ok := se.Latest(); ok {
				latest = append(latest, o)
			}
		}
		var maxT time.Time
		for _, o := range latest {
			if o.ObservedAt.After(maxT) {
				maxT = o.ObservedAt
			}
		}
		for _, o := range latest {
			if !o.ObservedAt.Equal(maxT) {
				continue
			}
			e := gfxEncStr(nil, s.Key())
			e = append(e, gfxBaselineValue(o.Value)...)
			keys := make([]string, 0, len(o.Dimensions))
			for k := range o.Dimensions {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			e = gfoAppendU32(e, uint32(len(keys)))
			for _, k := range keys {
				e = gfxEncStr(e, k)
				e = gfxEncStr(e, o.Dimensions[k])
			}
			entries = append(entries, e)
		}
	}
	cb := gfxEncStr(nil, "dpa.BaselineDigest.v1")
	cb = gfxEncStr(cb, p.String())
	cb = append(cb, gfxEncList(entries, true)...)
	sum := sha256.Sum256(cb)
	return hex.EncodeToString(sum[:]), nil
}

// ---------------------------------------------------------------------------
// §5 replay.
// ---------------------------------------------------------------------------

type gfxFrameEval struct {
	R          gfoRatified
	T          time.Time
	W          evidence.Window
	TS         *graph.Snapshot
	TopoDigest string
	BaseDigest string
	Findings   []model.Finding
}

type gfxReplay struct {
	Art     *gfoArtifact
	Fleet   gfxFleet
	Policy  fleet.Policy
	Profile fleet.CollectorTrustProfile
	Node    model.AssetRef
	P       model.PartitionKey
	Frames  []gfxFrameEval
	Cursors []fleet.SnapshotCursor // cursors of A (GFX-040)
	Order0  fleet.SnapshotOrder    // order of frame 0 when A is empty
	R       int
	E       time.Time
	Intents []fleet.Intent             // fleet file order
	Dec     []fleet.DeviceDecision     // dec(d), fleet file order (GFX-048)
	DecAt   [][]fleet.DeviceDecision   // dec_k(d) per device and frame (A non-empty)
	ND      fleet.NodeDecision         // GFX-049
	Bundles [][]fleet.AssessmentBundle // B_k(d) per device and frame
}

var gfxDesired = map[string]fleet.DesiredState{
	"InService": fleet.DesiredInService, "Maintenance": fleet.DesiredMaintenance, "Retired": fleet.DesiredRetired,
}

func gfxParseInputTime(s string) (time.Time, error) {
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return v.UTC(), nil
}

// gfxReplayOf runs §5 on a valid artifact and fleet file.
func gfxReplayOf(a *gfoArtifact, fl gfxFleet) (*gfxReplay, error) {
	rp := &gfxReplay{Art: a, Fleet: fl}
	if len(a.Frames) == 0 {
		return nil, fmt.Errorf("oracle: artifact has no frames")
	}
	// GFX-022 Policy and Intents.
	var reqs []fleet.CoverageRequirement
	for _, c := range fl.Coverage {
		reqs = append(reqs, fleet.CoverageRequirement{Name: c.Name, PathKind: c.PathKind, Required: c.Required})
	}
	pol, err := fleet.NewPolicy(fleet.Policy{Revision: fl.Revision, RequiredCoverage: reqs,
		Freshness: time.Duration(fl.Freshness) * time.Second, ReadyFor: time.Duration(fl.ReadyFor) * time.Second})
	if err != nil {
		return nil, fmt.Errorf("oracle: GFX-022 fleet.NewPolicy: %w", err)
	}
	rp.Policy = pol
	for _, d := range fl.Devices {
		at, err := gfxParseInputTime(d.IntentAt)
		if err != nil {
			return nil, fmt.Errorf("oracle: intentObservedAt %q: %w", d.IntentAt, err)
		}
		in := fleet.Intent{
			Device: fleet.DeviceRef{Name: d.Name, UID: d.UID}, Node: fleet.NodeRef{ClusterID: fl.ClusterID, Name: d.NodeName, UID: d.NodeUID},
			Desired: gfxDesired[d.Desired], RequestID: d.RequestID, MetadataGeneration: d.Generation, ObservedAt: at,
			Claim: fleet.InventoryClaim{Vendor: d.Vendor, UUID: d.UUID, Serial: d.Serial, Source: d.Source, EvidenceID: d.EvidenceID},
		}
		if err := in.Validate(); err != nil {
			return nil, fmt.Errorf("oracle: GFX-022 Intent(%s).Validate: %w", d.Name, err)
		}
		rp.Intents = append(rp.Intents, in)
	}
	// GFX-042 cumulative window, GFX-043 topology, GFX-044/045 digests, GFX-046 findings.
	cfg := evidence.Config{MaxAge: pol.Freshness, Horizon: 86400 * time.Second, MaxSamples: 16}
	w, err := evidence.NewWindow(cfg)
	if err != nil {
		return nil, fmt.Errorf("oracle: GFX-042 evidence.NewWindow: %w", err)
	}
	for k := range a.Frames {
		r, err := gfxApplyM(a, k)
		if err != nil {
			return nil, fmt.Errorf("oracle: GFO-090 M(frame %d): %w", k, err)
		}
		if k == 0 {
			rp.Profile = r.Profile
			rp.Node = r.Node
			if rp.P, err = graph.PartitionFor(r.Node); err != nil {
				return nil, fmt.Errorf("oracle: graph.PartitionFor: %w", err)
			}
		}
		for _, o := range r.Observations {
			if w, err = w.Add(o); err != nil {
				return nil, fmt.Errorf("oracle: GFX-042 frame %d Window.Add(%s): %w", k, o.ID, err)
			}
		}
		st, err := graph.NewState(rp.P, cfg)
		if err != nil {
			return nil, fmt.Errorf("oracle: GFX-043 graph.NewState: %w", err)
		}
		rs, err := graph.NewResync(rp.P, r.Envelope.Sequence, r.Assets, r.Edges)
		if err != nil {
			return nil, fmt.Errorf("oracle: GFX-043 graph.NewResync(frame %d): %w", k, err)
		}
		if st, _, err = st.Apply(rs); err != nil {
			return nil, fmt.Errorf("oracle: GFX-043 State.Apply(frame %d): %w", k, err)
		}
		bd, err := gfxBaselineDigest(rp.P, w)
		if err != nil {
			return nil, fmt.Errorf("oracle: GFX-045 frame %d: %w", k, err)
		}
		rp.Frames = append(rp.Frames, gfxFrameEval{
			R: r, T: r.Envelope.ObservedAt, W: w, TS: st.Snapshot(),
			TopoDigest: gfxTopologyDigest(rp.P, r), BaseDigest: bd,
			Findings: pcie.EvaluateLinkWidth(w, r.Envelope.ObservedAt),
		})
	}
	// GFX-040 admission chain.
	var prev *fleet.SnapshotCursor
	for k := range rp.Frames {
		c, order, err := fleet.AdmitSnapshot(rp.Profile.Session, prev, rp.Frames[k].R.Envelope)
		if err != nil {
			return nil, fmt.Errorf("oracle: GFX-040 AdmitSnapshot(frame %d) error: %w", k, err)
		}
		if order != fleet.SnapshotAccepted {
			if k == 0 {
				rp.Order0 = order
			}
			break
		}
		cc := c
		rp.Cursors = append(rp.Cursors, cc)
		prev = &cc
	}
	n := len(rp.Frames)
	switch len(rp.Cursors) {
	case 0:
		rp.R = 0
	case n:
		rp.R = n - 1
	default:
		return nil, fmt.Errorf("oracle: GFX-048 0 < |A| = %d < N = %d", len(rp.Cursors), n)
	}
	rp.E = rp.Frames[rp.R].T
	// GFX-047 bundles, GFX-048 device chain.
	for i := range rp.Intents {
		var bundles []fleet.AssessmentBundle
		for k := range rp.Frames {
			bundles = append(bundles, rp.bundle(k, i))
		}
		rp.Bundles = append(rp.Bundles, bundles)
		if len(rp.Cursors) == 0 {
			d, err := fleet.EvaluateDevice(bundles[0], nil, rp.Frames[0].T)
			if err != nil {
				return nil, fmt.Errorf("oracle: GFX-048 EvaluateDevice(A empty, %s): %w", rp.Intents[i].Device.Name, err)
			}
			rp.Dec = append(rp.Dec, d)
			rp.DecAt = append(rp.DecAt, []fleet.DeviceDecision{d})
			continue
		}
		var prevD *fleet.DeviceDecision
		var chain []fleet.DeviceDecision
		for k := range rp.Frames {
			d, err := fleet.EvaluateDevice(bundles[k], prevD, rp.Frames[k].T)
			if err != nil {
				return nil, fmt.Errorf("oracle: GFX-048 EvaluateDevice(frame %d, %s): %w", k, rp.Intents[i].Device.Name, err)
			}
			chain = append(chain, d)
			dc := d
			prevD = &dc
		}
		rp.Dec = append(rp.Dec, chain[rp.R])
		rp.DecAt = append(rp.DecAt, chain)
	}
	// GFX-049 node aggregate.
	idx := rp.byUID()
	var devs []fleet.DeviceAggregate
	for _, i := range idx {
		devs = append(devs, fleet.DeviceAggregate{DeviceUID: fl.Devices[i].UID, NodeUID: a.NodeUID, Desired: rp.Intents[i].Desired,
			MetadataGeneration: fl.Devices[i].Generation, Decision: rp.Dec[i]})
	}
	sel := fleet.SelectionComplete
	if len(devs) == 0 {
		sel = fleet.SelectionNoDevices
	}
	nd, err := fleet.AggregateNode(fleet.AggregateNodeInput{Node: fleet.NodeRef{ClusterID: fl.ClusterID, Name: a.NodeName, UID: a.NodeUID},
		FleetUID: fl.UID, Selection: sel, Devices: devs}, nil, rp.E)
	if err != nil {
		return nil, fmt.Errorf("oracle: GFX-049 AggregateNode: %w", err)
	}
	rp.ND = nd
	return rp, nil
}

// bundle assembles B_k(d) (GFX-047).
func (rp *gfxReplay) bundle(k, i int) fleet.AssessmentBundle {
	fe := rp.Frames[k]
	env := fe.R.Envelope
	var cur fleet.SnapshotCursor
	if k < len(rp.Cursors) {
		cur = rp.Cursors[k]
	} else {
		cur = fleet.SnapshotCursor{NodeUID: env.NodeUID, BootID: env.BootID, PayloadDigest: env.PayloadDigest, BundleRevision: env.BundleRevision,
			Session: env.Session, Sequence: env.Sequence, Completeness: env.Completeness, ObservedAt: env.ObservedAt, Baseline: false}
	}
	return fleet.AssessmentBundle{
		Policy: rp.Policy, Intent: rp.Intents[i], Snapshot: env, Admitted: cur,
		GraphRevision: env.BundleRevision, WindowRevision: env.BundleRevision,
		TopologyDigest: fe.TopoDigest, BaselineDigest: fe.BaseDigest,
		Topology: fe.TS, Window: fe.W,
		Provenance: fe.R.Provenance, Bindings: fe.R.Bindings,
		Findings: fe.Findings, FindingsEvaluatedAt: fe.T, FindingsGraphRevision: env.BundleRevision,
		Allocation: nil, Fence: nil, CollectorTrust: rp.Profile, FenceTrust: nil,
	}
}

// byUID returns device indices in UID byte order.
func (rp *gfxReplay) byUID() []int {
	idx := make([]int, len(rp.Fleet.Devices))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(x, y int) bool { return rp.Fleet.Devices[idx[x]].UID < rp.Fleet.Devices[idx[y]].UID })
	return idx
}

func (rp *gfxReplay) devIndex(name string) int {
	for i, d := range rp.Fleet.Devices {
		if d.Name == name {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// §6 / §8 expected explanation.
// ---------------------------------------------------------------------------

func gfxT(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

type gfxLim struct {
	Code, Subject string
	Has           bool
}

type gfxSegX struct {
	Depth int
	E     graph.Edge
	P     fleet.EdgeProvenance
}

func (rp *gfxReplay) trusted(cap fleet.TrustCapability, src model.SourceRef) bool {
	for _, s := range rp.Profile.Sources {
		if s.Capability == cap && s.Source == src {
			return true
		}
	}
	return false
}

// gfxExpected returns the expected JSON explanation tree for the request.
func gfxExpected(rp *gfxReplay, kind, name string) (*gfoJ, error) {
	a := rp.Art
	gpu := kind == "gpu"
	var out []int
	if gpu {
		i := rp.devIndex(name)
		if i < 0 {
			return nil, fmt.Errorf("oracle: device %q not in the fleet file", name)
		}
		out = []int{i}
	} else {
		out = rp.byUID()
	}
	admitted := len(rp.Cursors) > 0
	fe := rp.Frames[rp.R]
	artR := a.Frames[rp.R]
	E := rp.E
	nodeKey := rp.Node.Key()

	// Edges of R with their provenance and the trusted-hop predicate (GFX-063).
	type edgeX struct {
		E       graph.Edge
		P       fleet.EdgeProvenance
		HasP    bool
		Trusted bool
	}
	// First provenance of each edge identity, in provenance order.
	firstProv := map[string]int{}
	for k, p := range fe.R.Provenance {
		if _, ok := firstProv[gfxEdgeKey(p.Edge)]; !ok {
			firstProv[gfxEdgeKey(p.Edge)] = k
		}
	}
	var edges []edgeX
	for _, e := range fe.R.Edges {
		x := edgeX{E: e}
		if k, ok := firstProv[gfxEdgeKey(e)]; ok {
			p := fe.R.Provenance[k]
			x.P, x.HasP = p, true
			deadline := p.ExpiresAt
			if alt := p.ObservedAt.Add(rp.Policy.Freshness); alt.Before(deadline) {
				deadline = alt
			}
			if p.Kind == fleet.EdgeEvidenceObserved && rp.trusted(fleet.TrustSysfsPhysicalParent, p.Source) &&
				!E.Before(p.ObservedAt) && E.Before(deadline) {
				x.Trusted = true
			}
		}
		edges = append(edges, x)
	}
	from := map[string][]int{}
	for k, x := range edges {
		from[x.E.From.Key()] = append(from[x.E.From.Key()], k)
	}
	bfs := func(start string, onlyTrusted bool) (map[string]int, map[int]int) {
		dist := map[string]int{start: 0}
		reached := map[int]int{}
		queue := []string{start}
		for len(queue) > 0 {
			u := queue[0]
			queue = queue[1:]
			for _, k := range from[u] {
				if onlyTrusted && !edges[k].Trusted {
					continue
				}
				if _, ok := reached[k]; !ok {
					reached[k] = dist[u]
				}
				to := edges[k].E.To.Key()
				if _, ok := dist[to]; !ok {
					dist[to] = dist[u] + 1
					queue = append(queue, to)
				}
			}
		}
		return dist, reached
	}

	var lims []gfxLim
	add := func(code, subject string, has bool) { lims = append(lims, gfxLim{code, subject, has}) }

	// Bound output devices and F(d).
	fnOf := map[int]model.AssetRef{}
	for _, i := range out {
		if rp.Dec[i].BindingState == fleet.BindingBound {
			fnOf[i] = rp.Dec[i].Binding.Function
		}
	}

	// pathSegments (GFX-063) and the per-device path limitations (GFX-063, 082..084).
	depth := map[int]int{}
	if admitted {
		for _, i := range out {
			fn, ok := fnOf[i]
			if !ok {
				continue
			}
			_, reached := bfs(fn.Key(), true)
			for k, d := range reached {
				if old, ok := depth[k]; !ok || d < old {
					depth[k] = d
				}
			}
		}
	}
	for _, i := range out {
		fn, ok := fnOf[i]
		if !ok {
			continue
		}
		fk := fn.Key()
		dist, reachedAll := bfs(fk, false)
		for k := range reachedAll {
			if !edges[k].Trusted {
				add("path_untrusted", fk, true)
				break
			}
		}
		outF := from[fk]
		if len(outF) == 1 && edges[outF[0]].E.To.Key() == nodeKey {
			add("root_port_absent", fk, true)
		}
		contra := false
		for asset := range dist {
			if len(from[asset]) >= 2 {
				contra = true
			}
		}
		if !contra {
			seen := map[string]bool{fk: true}
			cur := fk
			for len(from[cur]) == 1 {
				cur = edges[from[cur][0]].E.To.Key()
				if seen[cur] {
					contra = true
					break
				}
				seen[cur] = true
			}
		}
		if contra {
			add("path_contradiction", fk, true)
		}
		hasCurrent := false
		for _, o := range artR.Payload.Observations {
			if o.Signal == "pcie.link.width.current" && o.Subject.Key() == fk {
				hasCurrent = true
			}
		}
		if !hasCurrent {
			add("width_pair_absent", fk, true)
		}
	}
	var segs []gfxSegX
	for k, d := range depth {
		segs = append(segs, gfxSegX{Depth: d, E: edges[k].E, P: edges[k].P})
	}
	sort.Slice(segs, func(x, y int) bool {
		p, q := segs[x], segs[y]
		if p.Depth != q.Depth {
			return p.Depth < q.Depth
		}
		for _, c := range [][2]string{{p.E.From.Key(), q.E.From.Key()}, {p.E.To.Key(), q.E.To.Key()},
			{p.E.Relation.String(), q.E.Relation.String()}, {p.E.Origin.String(), q.E.Origin.String()}} {
			if c[0] != c[1] {
				return c[0] < c[1]
			}
		}
		return false
	})
	truncated := false
	totalSegs := len(segs)
	if len(segs) > 512 {
		segs, truncated = segs[:512], true
	}
	segArr := gfxArr()
	for _, s := range segs {
		segArr.elems = append(segArr.elems, gfxObj(
			gfxKV{"from", gfxS(s.E.From.Key())}, gfxKV{"relation", gfxS(s.E.Relation.String())}, gfxKV{"to", gfxS(s.E.To.Key())},
			gfxKV{"origin", gfxS(s.E.Origin.String())}, gfxKV{"kind", gfxS(s.P.Kind.String())},
			gfxKV{"source", gfxObj(gfxKV{"type", gfxS(s.P.Source.Type)}, gfxKV{"name", gfxS(s.P.Source.Name)})},
			gfxKV{"evidenceID", gfxS(s.P.EvidenceID)}, gfxKV{"observedAt", gfxS(gfxT(s.P.ObservedAt))}, gfxKV{"expiresAt", gfxS(gfxT(s.P.ExpiresAt))}))
	}

	// activeFindings (GFX-064).
	selectedIDs := map[string]bool{}
	if admitted {
		for _, i := range out {
			for _, id := range rp.Dec[i].FindingIDs {
				selectedIDs[id] = true
			}
		}
	}
	var selected []model.Finding
	for _, f := range fe.Findings {
		if selectedIDs[f.ID] {
			selected = append(selected, f)
			continue
		}
		if gpu {
			if fn, ok := fnOf[out[0]]; ok {
				for _, s := range f.Scope {
					if s.Key() == fn.Key() {
						add("finding_not_applied", f.ID, true)
						break
					}
				}
			}
		} else {
			add("finding_not_applied", f.ID, true)
		}
	}
	sort.Slice(selected, func(x, y int) bool { return selected[x].ID < selected[y].ID })
	obsByID := map[string]model.Observation{}
	for _, s := range fe.W.Subjects() {
		sigs, _ := fe.W.Signals(s)
		for _, sig := range sigs {
			series, _ := fe.W.SeriesFor(s, sig)
			for _, se := range series {
				for _, o := range se.Observations() {
					obsByID[o.ID] = o
				}
			}
		}
	}
	for _, f := range selected {
		if f.Type != model.FindingPCIeLinkWidthDegraded {
			continue
		}
		for _, ev := range f.Evidence {
			o, ok := obsByID[ev.ObservationID]
			if !ok {
				continue
			}
			if !rp.trusted(fleet.TrustSysfsPCIeWidth, o.Source) ||
				(o.Dimensions["pcie.expected.provenance"] == "operator_verified_wiring" && !rp.trusted(fleet.TrustOperatorBaseline, o.Source)) {
				add("finding_source_untrusted", f.ID, true)
				break
			}
		}
	}
	totalFindings := len(selected)
	if len(selected) > 64 {
		selected, truncated = selected[:64], true
	}
	evidenceRefs := 0
	findArr := gfxArr()
	for _, f := range selected {
		evidenceRefs += len(f.Evidence)
		evs := f.Evidence
		if len(evs) > 256 {
			evs, truncated = evs[:256], true
		}
		evArr := gfxArr()
		for _, ev := range evs {
			evArr.elems = append(evArr.elems, gfxObj(gfxKV{"observationID", gfxS(ev.ObservationID)}, gfxKV{"summary", gfxS(ev.Summary)}))
		}
		scope := []string{}
		for _, s := range f.Scope {
			scope = append(scope, s.Key())
		}
		missing := []string{}
		for _, m := range f.MissingInputs {
			missing = append(missing, string(m))
		}
		aff := gfxArr()
		for _, x := range f.Affected {
			aff.elems = append(aff.elems, gfxObj(gfxKV{"asset", gfxS(x.Asset.Key())}, gfxKV{"accuracy", gfxS(x.Accuracy.String())}))
		}
		findArr.elems = append(findArr.elems, gfxObj(
			gfxKV{"id", gfxS(f.ID)}, gfxKV{"type", gfxS(f.Type.String())}, gfxKV{"severity", gfxS(f.Severity.String())},
			gfxKV{"state", gfxS(f.State.String())}, gfxKV{"confidence", gfxS(f.Confidence.String())}, gfxKV{"scope", gfxStrArr(scope)},
			gfxKV{"evidence", evArr}, gfxKV{"missingInputs", gfxStrArr(missing)}, gfxKV{"affected", aff},
			gfxKV{"firstSeen", gfxS(gfxT(f.FirstSeen))}, gfxKV{"lastSeen", gfxS(gfxT(f.LastSeen))},
			gfxKV{"explanation", gfxS(f.Explanation)}, gfxKV{"suggestedStep", gfxS(f.SuggestedStep)}))
	}

	// coverage (GFX-065).
	reqs := append([]gfxCovReq{}, rp.Fleet.Coverage...)
	sort.SliceStable(reqs, func(x, y int) bool { return reqs[x].Name < reqs[y].Name })
	policyNames := map[string]bool{}
	for _, c := range reqs {
		policyNames[c.Name] = true
	}
	type covX struct {
		obj  *gfoJ
		refs int
	}
	var covs []covX
	for _, i := range out {
		dec := rp.Dec[i]
		for _, a := range dec.Coverage {
			if !policyNames[a.Name] {
				return nil, fmt.Errorf("oracle: GFX-065 dec(%s).Coverage has name %q outside the policy (exit 1 [defensive])", rp.Fleet.Devices[i].Name, a.Name)
			}
		}
		for _, c := range reqs {
			var kvs []gfxKV
			if !gpu {
				kvs = append(kvs, gfxKV{"device", gfxS(rp.Fleet.Devices[i].Name)})
			}
			var found *fleet.CoverageAssessment
			for k := range dec.Coverage {
				if dec.Coverage[k].Name == c.Name {
					found = &dec.Coverage[k]
					break
				}
			}
			state, reason, pathKind := "Unknown", dec.Reason, c.PathKind
			refs := []string{}
			var times []gfxKV
			if found != nil {
				state, reason, pathKind = found.State.String(), found.Reason, found.PathKind
				refs = gfxSorted(found.EvidenceIDs)
				var u []string
				for _, r := range refs {
					if len(u) == 0 || u[len(u)-1] != r {
						u = append(u, r)
					}
				}
				refs = append([]string{}, u...)
				for _, tv := range []struct {
					k string
					v time.Time
				}{{"observedAt", found.ObservedAt}, {"latestObservedAt", found.LatestObservedAt}, {"expiresAt", found.ExpiresAt}} {
					if !tv.v.IsZero() {
						times = append(times, gfxKV{tv.k, gfxS(gfxT(tv.v))})
					}
				}
			}
			if !gpu && (!c.Required || state == "Normal") {
				continue
			}
			n := len(refs)
			if len(refs) > 256 {
				refs, truncated = refs[:256], true
			}
			kvs = append(kvs, gfxKV{"name", gfxS(c.Name)}, gfxKV{"pathKind", gfxS(pathKind)}, gfxKV{"required", gfxBool(c.Required)},
				gfxKV{"state", gfxS(state)}, gfxKV{"reason", gfxS(reason)}, gfxKV{"evidenceRefs", gfxStrArr(refs)})
			kvs = append(kvs, times...)
			covs = append(covs, covX{gfxObj(kvs...), n})
		}
	}
	totalCov := len(covs)
	if len(covs) > 32 {
		covs, truncated = covs[:32], true
	}
	covArr := gfxArr()
	for _, c := range covs {
		covArr.elems = append(covArr.elems, c.obj)
		evidenceRefs += c.refs
	}

	// Limitations (GFX-080..085).
	if len(out) > 0 {
		add("allocation_unavailable", "", false)
	}
	for _, d := range artR.Diagnostics {
		add("collector:"+d.Code, d.Subject, true)
	}
	if len(artR.Diagnostics) > 0 {
		add("diagnostics_outside_digest", "", false)
	}
	if artR.DiagnosticsTruncated {
		add("diagnostics_truncated", strconv.Itoa(artR.DiagnosticsTotal), true)
	}
	for _, i := range out {
		if rp.Intents[i].Desired != fleet.DesiredInService {
			add("fence_unavailable", "", false)
		}
	}
	for k := 0; k <= rp.R; k++ {
		if a.Frames[k].Completeness == "PARTIAL" {
			add("frame_partial", strconv.Itoa(k), true)
		}
	}
	if !admitted {
		add("no_admitted_frame", rp.Order0.String(), true)
	}
	if !gpu && len(rp.Fleet.Devices) == 0 {
		add("no_devices", "", false)
	}
	for _, i := range out {
		if rp.Fleet.Devices[i].NodeName != a.NodeName {
			add("node_name_differs", rp.Fleet.Devices[i].Name, true)
		}
	}
	for _, d := range artR.Diagnostics {
		last := d.Subject
		if j := strings.LastIndexByte(last, '/'); j >= 0 {
			last = last[j+1:]
		}
		if gfxIn(d.Code, "sysfs_list_failed", "sysfs_link_invalid", "sysfs_link_escape") ||
			(strings.HasPrefix(d.Code, "sysfs_attr_") && gfxIn(last, "class", "vendor", "physfn")) {
			add("nvidia_classification_unknown", "", false)
		}
	}
	if rp.Profile.Mode == fleet.TrustModeOffline {
		add("offline", "", false)
		add("offline_trust_not_live", rp.Profile.ID, true)
	}
	add("traffic_path_unverified", "", false)
	sort.SliceStable(lims, func(x, y int) bool {
		if lims[x].Code != lims[y].Code {
			return lims[x].Code < lims[y].Code
		}
		return lims[x].Subject < lims[y].Subject
	})
	var uniq []gfxLim
	for _, l := range lims {
		if len(uniq) > 0 && uniq[len(uniq)-1] == l {
			continue
		}
		uniq = append(uniq, l)
	}
	totalLims := len(uniq)
	if len(uniq) > 2048 {
		uniq, truncated = uniq[:2048], true
	}
	limArr := gfxArr()
	for _, l := range uniq {
		kvs := []gfxKV{{"code", gfxS(l.Code)}}
		if l.Has {
			kvs = append(kvs, gfxKV{"subject", gfxS(l.Subject)})
		}
		limArr.elems = append(limArr.elems, gfxObj(kvs...))
	}

	// deviceSummaries (GFX-067).
	devArr := gfxArr()
	totalDevs := 0
	if !gpu {
		for _, i := range rp.byUID() {
			d, dec := rp.Fleet.Devices[i], rp.Dec[i]
			devArr.elems = append(devArr.elems, gfxObj(gfxKV{"name", gfxS(d.Name)}, gfxKV{"uid", gfxS(d.UID)},
				gfxKV{"desiredState", gfxS(rp.Intents[i].Desired.String())}, gfxKV{"observedGeneration", gfxS(strconv.FormatInt(d.Generation, 10))},
				gfxKV{"qualification", gfxS(dec.Qualification.String())}, gfxKV{"lifecyclePhase", gfxS(dec.Phase.String())},
				gfxKV{"reason", gfxS(dec.Reason)}))
		}
		totalDevs = len(devArr.elems)
		if len(devArr.elems) > 256 {
			devArr.elems, truncated = devArr.elems[:256], true
		}
	}

	totals := gfxObj(gfxKV{"pathSegments", gfxN(totalSegs)}, gfxKV{"activeFindings", gfxN(totalFindings)}, gfxKV{"coverage", gfxN(totalCov)},
		gfxKV{"affectedWorkloads", gfxN(0)}, gfxKV{"deviceSummaries", gfxN(totalDevs)}, gfxKV{"evidenceRefs", gfxN(evidenceRefs)},
		gfxKV{"limitations", gfxN(totalLims)})

	nd := rp.ND
	var kvs []gfxKV
	kvs = append(kvs, gfxKV{"apiVersion", gfxS("v1alpha1")})
	if gpu {
		i := out[0]
		dec := rp.Dec[i]
		dev := rp.Fleet.Devices[i]
		var ident *gfoJ
		if dec.BindingState == fleet.BindingBound {
			b := dec.Binding
			ikv := []gfxKV{{"state", gfxS(dec.BindingState.String())}, {"reason", gfxS("Ready")}, {"vendor", gfxS(b.Claim.Vendor)}, {"uuid", gfxS(b.Claim.UUID)}}
			if b.Claim.Serial != "" {
				ikv = append(ikv, gfxKV{"serial", gfxS(b.Claim.Serial)})
			}
			ikv = append(ikv, gfxKV{"nodeUID", gfxS(b.Node.UID)}, gfxKV{"bootID", gfxS(b.BootID)}, gfxKV{"bdf", gfxS(b.BDF)},
				gfxKV{"functionKey", gfxS(b.Function.Key())},
				gfxKV{"source", gfxObj(gfxKV{"type", gfxS(b.Source.Type)}, gfxKV{"name", gfxS(b.Source.Name)})},
				gfxKV{"evidenceID", gfxS(b.EvidenceID)}, gfxKV{"observedAt", gfxS(gfxT(b.ObservedAt))}, gfxKV{"expiresAt", gfxS(gfxT(b.ExpiresAt))})
			ident = gfxObj(ikv...)
		} else {
			ident = gfxObj(gfxKV{"state", gfxS(dec.BindingState.String())}, gfxKV{"reason", gfxS(dec.Reason)})
		}
		kvs = append(kvs, gfxKV{"kind", gfxS("GPUExplanation")}, gfxKV{"targetName", gfxS(dev.Name)}, gfxKV{"targetUID", gfxS(dev.UID)},
			gfxKV{"nodeName", gfxS(a.NodeName)}, gfxKV{"nodeUID", gfxS(a.NodeUID)}, gfxKV{"identity", ident})
		if admitted {
			kvs = append(kvs, gfxKV{"graphRevision", gfxS(artR.BundleRevision)})
		}
		kvs = append(kvs, gfxKV{"phase", gfxS(dec.Phase.String())}, gfxKV{"qualification", gfxS(dec.Qualification.String())},
			gfxKV{"nodeEligibility", gfxS(nd.Eligibility.String())}, gfxKV{"pathSegments", segArr}, gfxKV{"activeFindings", findArr},
			gfxKV{"coverage", covArr},
			gfxKV{"allocation", gfxObj(gfxKV{"state", gfxS(dec.Allocation.String())}, gfxKV{"reason", gfxS("AllocationUnknown")},
				gfxKV{"evidenceRefs", gfxArr()}, gfxKV{"affectedWorkloads", gfxArr()})})
	} else {
		kvs = append(kvs, gfxKV{"kind", gfxS("NodeExplanation")}, gfxKV{"targetName", gfxS(a.NodeName)}, gfxKV{"targetUID", gfxS(a.NodeUID)},
			gfxKV{"nodeName", gfxS(a.NodeName)}, gfxKV{"nodeUID", gfxS(a.NodeUID)})
		if admitted {
			kvs = append(kvs, gfxKV{"graphRevision", gfxS(artR.BundleRevision)})
		}
		kvs = append(kvs, gfxKV{"qualification", gfxS(nd.Qualification.String())}, gfxKV{"nodeEligibility", gfxS(nd.Eligibility.String())},
			gfxKV{"pathSegments", segArr}, gfxKV{"activeFindings", findArr}, gfxKV{"coverage", covArr})
	}
	kvs = append(kvs, gfxKV{"affectedWorkloads", gfxArr()}, gfxKV{"observedAt", gfxS(gfxT(fe.T))}, gfxKV{"evaluatedAt", gfxS(gfxT(E))},
		gfxKV{"truncated", gfxBool(truncated)}, gfxKV{"totalCounts", totals}, gfxKV{"limitations", limArr},
		gfxKV{"nodeAssessmentRevision", gfxS(nd.AssessmentRevision)}, gfxKV{"deviceSummaries", devArr})
	if gpu {
		kvs = append(kvs, gfxKV{"reason", gfxS(rp.Dec[out[0]].Reason)})
	} else {
		kvs = append(kvs, gfxKV{"eligibilityReason", gfxS(nd.Reason)})
	}
	return gfxObj(kvs...), nil
}

// gfxCompareOracle compares a JSON output with the oracle (GFX-101). An
// oracle failure is reported as such so it is never mistaken for a pathctl
// finding.
func gfxCompareOracle(t *testing.T, c *gfxCase, kind, name string, raw []byte, clause string) {
	t.Helper()
	rp, err := c.Oracle()
	if err != nil {
		t.Errorf("%s: GFX-101 oracle could not replay the inputs: %v", clause, err)
		return
	}
	want, err := gfxExpected(rp, kind, name)
	if err != nil {
		t.Errorf("%s: GFX-101 oracle: %v", clause, err)
		return
	}
	wantRaw := gfxEncode(want) + "\n"
	if string(raw) == wantRaw {
		return
	}
	got, err := gfoParseJSON(bytes.TrimSuffix(raw, []byte("\n")))
	if err != nil {
		t.Errorf("%s: GFX-101: output differs from the oracle and does not decode: %v", clause, err)
		return
	}
	if d := gfxDiffJ(got, want, "$"); d != "" {
		t.Errorf("%s: GFX-101 (independent ratified-API oracle): %s", clause, d)
		return
	}
	i := gfoDiffAt(raw, []byte(wantRaw))
	t.Errorf("%s: GFX-069/GFX-101: output bytes differ from the oracle encoding at byte %d: got %q want %q", clause, i, gfoAround(raw, i), gfoAround([]byte(wantRaw), i))
}
