package liveingest_test

// Independent oracle of the frame and reducer tests. It maps decoded wire frames to ratified
// values with the GFO-090 transformation M, rebuilds the evidence window, the
// topology snapshot, TopologyDigest and BaselineDigest exactly as GFX-042..046
// and GLI-072 prescribe, and renders ratified values into canonical strings so
// that a live bundle can be compared with the expected one without relying on
// opaque types or time zones. It never calls the liveingest domain core; every
// digest here is computed by the independent CP of gli_frame_test.go.
//
// Top-level identifiers of this file use the prefix gliOr.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// gliOrUnmatched is the fixed stamp of an element whose source is not trusted
// for its capability (GLI-073).
const gliOrUnmatched = "unmatched-source"

var gliOrKinds = []model.AssetKind{
	model.KindSite, model.KindRow, model.KindRack, model.KindKubernetesNode, model.KindPCIeRootPort,
	model.KindPCIeSwitch, model.KindPCIeFunction, model.KindNICPort, model.KindVF, model.KindEthernetSwitch,
	model.KindSwitchPort, model.KindTransceiver, model.KindPhysicalLink, model.KindPod, model.KindContainer,
}

func gliOrAsset(kind, canonical string, aliases []*ingestpb.Alias) (model.AssetRef, error) {
	for _, k := range gliOrKinds {
		if k.String() != kind {
			continue
		}
		id, err := identity.ParseCanonical(canonical)
		if err != nil {
			return model.AssetRef{}, fmt.Errorf("identity.ParseCanonical(%q): %w", canonical, err)
		}
		var al []model.TypedID
		for _, a := range aliases {
			t, err := identity.ParseCanonical(a.GetNamespace() + ":" + a.GetValue())
			if err != nil {
				return model.AssetRef{}, fmt.Errorf("alias %q:%q: %w", a.GetNamespace(), a.GetValue(), err)
			}
			al = append(al, t)
		}
		return model.NewAssetRef(k, id, al...)
	}
	return model.AssetRef{}, fmt.Errorf("unknown asset kind %q", kind)
}

func gliOrValue(v *ingestpb.Value) (model.Value, error) {
	switch x := v.GetV().(type) {
	case *ingestpb.Value_IntValue:
		return model.NewIntValue(x.IntValue), nil
	case *ingestpb.Value_FloatValue:
		return model.NewFloatValue(x.FloatValue), nil
	case *ingestpb.Value_BoolValue:
		return model.NewBoolValue(x.BoolValue), nil
	case *ingestpb.Value_StringValue:
		return model.NewStringValue(x.StringValue), nil
	}
	return model.Value{}, errors.New("value has no alternative")
}

func gliOrQuality(s string) model.EvidenceQuality {
	for _, q := range []model.EvidenceQuality{model.QualityUnknown, model.QualityGood, model.QualityDegraded} {
		if q.String() == s {
			return q
		}
	}
	return model.QualityUnknown
}

// gliOrCtx carries the per-node inputs of M.
type gliOrCtx struct {
	Cluster, NodeName string
	// StampEdge and StampBinding return the CollectorProfileID stamp of an
	// edgeEvidence or gpuBinding source.
	StampEdge, StampBinding func(src model.SourceRef) string
}

// gliOrConstStamp stamps every element with id (the offline profile of GFX).
func gliOrConstStamp(id string) func(model.SourceRef) string {
	return func(model.SourceRef) string { return id }
}

// gliOrLiveStamps implements GLI-073: an element is stamped with the profile ID
// only when its source equals a profile source of the matching capability.
func gliOrLiveStamps(profileID string, sources []fleet.TrustedSource) (func(model.SourceRef) string, func(model.SourceRef) string) {
	match := func(c fleet.TrustCapability) func(model.SourceRef) string {
		return func(src model.SourceRef) string {
			for _, s := range sources {
				if s.Capability == c && s.Source == src {
					return profileID
				}
			}
			return gliOrUnmatched
		}
	}
	return match(fleet.TrustSysfsPhysicalParent), match(fleet.TrustNVIDIAUUIDBinding)
}

// gliOrFrame is one frame after M.
type gliOrFrame struct {
	Env    fleet.SnapshotEnvelope
	Node   model.AssetRef
	P      model.PartitionKey
	Assets []model.AssetRef
	ByKey  map[string]model.AssetRef
	Edges  []graph.Edge
	Prov   []fleet.EdgeProvenance
	Obs    []model.Observation
	Binds  []fleet.ObservedBinding
}

// gliOrApplyM applies GFO-090 to a wire frame (binding vendor V = NVIDIA). The
// digest and revision are the independent CP values, not the frame's fields.
func gliOrApplyM(f *ingestpb.SnapshotFrame, c gliOrCtx) (gliOrFrame, error) {
	var r gliOrFrame
	pl := f.GetPayload()
	d := gliDigest(pl)
	dh := hex.EncodeToString(d[:])
	rev := fmt.Sprintf("%d:%d:%s", f.GetSession(), f.GetSequence(), dh)
	comp := fleet.CompletenessComplete
	if f.GetCompleteness() == ingestpb.Completeness_PARTIAL {
		comp = fleet.CompletenessPartial
	}
	r.Env = fleet.SnapshotEnvelope{
		NodeUID: f.GetNodeUid(), BootID: f.GetBootId(), PayloadDigest: dh, BundleRevision: rev,
		Session: f.GetSession(), Sequence: f.GetSequence(), Completeness: comp, ObservedAt: f.GetObservedAt().AsTime(),
	}
	r.ByKey = map[string]model.AssetRef{}
	for _, a := range pl.GetAssets() {
		ref, err := gliOrAsset(a.GetKind(), a.GetCanonical(), a.GetAliases())
		if err != nil {
			return r, err
		}
		r.Assets = append(r.Assets, ref)
		r.ByKey[gliFxAssetKey(a)] = ref
		if ref.Kind == model.KindKubernetesNode {
			r.Node = ref
		}
	}
	for _, e := range pl.GetEdges() {
		from, ok1 := r.ByKey[e.GetFromKey()]
		to, ok2 := r.ByKey[e.GetToKey()]
		if !ok1 || !ok2 {
			return r, fmt.Errorf("edge %s -> %s endpoint not in assets", e.GetFromKey(), e.GetToKey())
		}
		edge, err := graph.NewEdge(from, to, model.RelLocatedIn, model.OriginObserved)
		if err != nil {
			return r, err
		}
		r.Edges = append(r.Edges, edge)
	}
	for _, ev := range pl.GetEdgeEvidence() {
		if int(ev.GetEdgeIndex()) >= len(r.Edges) {
			return r, fmt.Errorf("edge_index %d out of range", ev.GetEdgeIndex())
		}
		src := model.SourceRef{Type: ev.GetSourceType(), Name: ev.GetSourceName()}
		r.Prov = append(r.Prov, fleet.EdgeProvenance{
			Edge: r.Edges[ev.GetEdgeIndex()], EvidenceID: ev.GetEvidenceId(), BundleRevision: rev,
			CollectorProfileID: c.StampEdge(src), Kind: fleet.EdgeEvidenceObserved, Source: src,
			ObservedAt: ev.GetObservedAt().AsTime(), ExpiresAt: ev.GetExpiresAt().AsTime(),
		})
	}
	for _, o := range pl.GetObservations() {
		subj, err := gliOrAsset(o.GetSubject().GetKind(), o.GetSubject().GetCanonical(), o.GetSubject().GetAliases())
		if err != nil {
			return r, err
		}
		v, err := gliOrValue(o.GetValue())
		if err != nil {
			return r, err
		}
		dims := map[string]string{}
		for _, dm := range o.GetDimensions() {
			dims[dm.GetKey()] = dm.GetValue()
		}
		var exp time.Time
		if x := o.GetExpiresAt(); x != nil {
			exp = x.AsTime()
		}
		obs, err := model.NewObservation(model.Observation{
			ID: o.GetId(), Source: model.SourceRef{Type: o.GetSource().GetType(), Name: o.GetSource().GetName()},
			Subject: subj, Signal: model.SignalRef(o.GetSignal()), Value: v, Unit: o.GetUnit(), Dimensions: dims,
			ObservedAt: o.GetObservedAt().AsTime(), ReceivedAt: o.GetReceivedAt().AsTime(), ExpiresAt: exp,
			Sequence: o.GetSequence(), Quality: gliOrQuality(o.GetQuality()), RawDigest: o.GetRawDigest(),
		})
		if err != nil {
			return r, fmt.Errorf("model.NewObservation(%s): %w", o.GetId(), err)
		}
		r.Obs = append(r.Obs, obs)
	}
	for _, b := range pl.GetGpuBindings() {
		fn, err := gliOrAsset("PCIeFunction", "pci-bdf:"+b.GetBdf(), nil)
		if err != nil {
			return r, fmt.Errorf("binding BDF %s: %w", b.GetBdf(), err)
		}
		src := model.SourceRef{Type: b.GetSourceType(), Name: b.GetSourceName()}
		r.Binds = append(r.Binds, fleet.ObservedBinding{
			Node: fleet.NodeRef{ClusterID: c.Cluster, Name: c.NodeName, UID: f.GetNodeUid()}, BootID: f.GetBootId(),
			BDF: b.GetBdf(), Function: fn,
			Claim:  fleet.InventoryClaim{Vendor: "NVIDIA", UUID: b.GetUuid(), Serial: "", Source: b.GetSourceName(), EvidenceID: b.GetEvidenceId()},
			Source: src, EvidenceID: b.GetEvidenceId(), BundleRevision: rev, CollectorProfileID: c.StampBinding(src),
			ObservedAt: b.GetObservedAt().AsTime(), ExpiresAt: b.GetExpiresAt().AsTime(),
		})
	}
	if r.Node.Kind != model.KindKubernetesNode {
		return r, errors.New("frame has no KubernetesNode asset")
	}
	p, err := graph.PartitionFor(r.Node)
	if err != nil {
		return r, fmt.Errorf("graph.PartitionFor: %w", err)
	}
	r.P = p
	return r, nil
}

// ---------------------------------------------------------------------------
// GFX-044 TopologyDigest and GFX-045 BaselineDigest.
// ---------------------------------------------------------------------------

// gliOrEncList sorts the encoded elements by their bytes and prefixes the
// count; dedup merges byte-equal elements (GFX-045) and otherwise keeps them
// (GFX-044).
func gliOrEncList(elems [][]byte, dedup bool) []byte {
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
	out := gliFxAppendU32(nil, uint32(len(s)))
	for _, e := range s {
		out = append(out, e...)
	}
	return out
}

func gliOrEdgeKey(e graph.Edge) string {
	return e.From.Key() + "\x00" + e.Relation.String() + "\x00" + e.To.Key() + "\x00" + e.Origin.String()
}

func gliOrTopologyDigest(fr gliOrFrame) string {
	var assets, edges [][]byte
	for _, a := range fr.Assets {
		assets = append(assets, gliFxAppendStr(nil, a.Key()))
	}
	provsOf := map[string][][]byte{}
	for _, pv := range fr.Prov {
		x := gliFxAppendStr(nil, pv.Kind.String())
		x = gliFxAppendStr(x, pv.Source.Type)
		x = gliFxAppendStr(x, pv.Source.Name)
		k := gliOrEdgeKey(pv.Edge)
		provsOf[k] = append(provsOf[k], x)
	}
	for _, e := range fr.Edges {
		entry := gliFxAppendStr(nil, e.From.Key())
		entry = gliFxAppendStr(entry, e.Relation.String())
		entry = gliFxAppendStr(entry, e.To.Key())
		entry = gliFxAppendStr(entry, e.Origin.String())
		entry = append(entry, gliOrEncList(provsOf[gliOrEdgeKey(e)], false)...)
		edges = append(edges, entry)
	}
	ct := gliFxAppendStr(nil, "dpa.TopologyDigest.v1")
	ct = gliFxAppendStr(ct, fr.P.String())
	ct = append(ct, gliOrEncList(assets, false)...)
	ct = append(ct, gliOrEncList(edges, false)...)
	sum := sha256.Sum256(ct)
	return hex.EncodeToString(sum[:])
}

func gliOrBaselineValue(v model.Value) []byte {
	if n, ok := v.Int(); ok {
		return gliFxAppendU64(gliFxAppendStr(nil, "I"), uint64(n))
	}
	if f, ok := v.Float(); ok {
		if !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) && f >= -9223372036854775808.0 && f < 9223372036854775808.0 {
			return gliFxAppendU64(gliFxAppendStr(nil, "I"), uint64(int64(f)))
		}
		return gliFxAppendU64(gliFxAppendStr(nil, "Float"), math.Float64bits(f))
	}
	if b, ok := v.Bool(); ok {
		out := gliFxAppendStr(nil, "Bool")
		if b {
			return append(out, 1)
		}
		return append(out, 0)
	}
	s, _ := v.Str()
	return gliFxAppendStr(gliFxAppendStr(nil, "String"), s)
}

func gliOrBaselineDigest(p model.PartitionKey, w evidence.Window) (string, error) {
	sig := model.SignalRef(gliFxSigExpect)
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
			e := gliFxAppendStr(nil, s.Key())
			e = append(e, gliOrBaselineValue(o.Value)...)
			keys := make([]string, 0, len(o.Dimensions))
			for k := range o.Dimensions {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			e = gliFxAppendU32(e, uint32(len(keys)))
			for _, k := range keys {
				e = gliFxAppendStr(e, k)
				e = gliFxAppendStr(e, o.Dimensions[k])
			}
			entries = append(entries, e)
		}
	}
	cb := gliFxAppendStr(nil, "dpa.BaselineDigest.v1")
	cb = gliFxAppendStr(cb, p.String())
	cb = append(cb, gliOrEncList(entries, true)...)
	sum := sha256.Sum256(cb)
	return hex.EncodeToString(sum[:]), nil
}

// ---------------------------------------------------------------------------
// Window, snapshot and the live ring (GLI-072).
// ---------------------------------------------------------------------------

func gliOrConfig(freshness time.Duration) evidence.Config {
	return evidence.Config{MaxAge: freshness, Horizon: 86400 * time.Second, MaxSamples: 16}
}

// gliOrWindow adds the observations of frames, in frame order and payload
// order, to a fresh window with MaxAge = freshness (GFX-042, GLI-072 (a)).
func gliOrWindow(frames []gliOrFrame, freshness time.Duration) (evidence.Window, error) {
	w, err := evidence.NewWindow(gliOrConfig(freshness))
	if err != nil {
		return w, fmt.Errorf("evidence.NewWindow: %w", err)
	}
	for k, fr := range frames {
		for _, o := range fr.Obs {
			if w, err = w.Add(o); err != nil {
				return w, fmt.Errorf("frame %d Window.Add(%s): %w", k, o.ID, err)
			}
		}
	}
	return w, nil
}

// gliOrSnapshot is the topology snapshot of one frame's payload alone (GFX-043).
func gliOrSnapshot(fr gliOrFrame, freshness time.Duration) (*graph.Snapshot, error) {
	st, err := graph.NewState(fr.P, gliOrConfig(freshness))
	if err != nil {
		return nil, fmt.Errorf("graph.NewState: %w", err)
	}
	rs, err := graph.NewResync(fr.P, fr.Env.Sequence, fr.Assets, fr.Edges)
	if err != nil {
		return nil, fmt.Errorf("graph.NewResync: %w", err)
	}
	if st, _, err = st.Apply(rs); err != nil {
		return nil, fmt.Errorf("State.Apply: %w", err)
	}
	return st.Snapshot(), nil
}

// gliOrRingObsMax is the ring observation total of GLI-072 (8192).
const gliOrRingObsMax = 8192

// gliOrNICPathKind is the path kind of the coverage item that S1 renders by
// CollectorTrust.Mode (live Unknown, offline Unsupported); GLI-078 comparisons
// leave it out and compare it with a live-profile S1 call instead.
const gliOrNICPathKind = "nic-lldp-remote"

// gliOrRing keeps the last 16 accepted frames and drops the oldest frames while
// the observation total exceeds gliOrRingObsMax; the last frame is never dropped.
func gliOrRing(frames []gliOrFrame) []gliOrFrame {
	ring := frames
	if len(ring) > 16 {
		ring = ring[len(ring)-16:]
	}
	total := 0
	for _, f := range ring {
		total += len(f.Obs)
	}
	for len(ring) > 1 && total > gliOrRingObsMax {
		total -= len(ring[0].Obs)
		ring = ring[1:]
	}
	return ring
}

// gliOrExpect is what a NodeBundles call after the given accepted frames must
// carry (GLI-072, GLI-073, GLI-075).
type gliOrExpect struct {
	Last     gliOrFrame
	Ring     []gliOrFrame
	Window   evidence.Window
	Topology string
	Baseline string
	Findings []model.Finding
}

func gliOrExpectFor(frames []gliOrFrame, freshness time.Duration, now time.Time) (gliOrExpect, error) {
	var x gliOrExpect
	if len(frames) == 0 {
		return x, errors.New("no frames")
	}
	x.Last = frames[len(frames)-1]
	x.Ring = gliOrRing(frames)
	w, err := gliOrWindow(x.Ring, freshness)
	if err != nil {
		return x, err
	}
	x.Window = w
	x.Topology = gliOrTopologyDigest(x.Last)
	if x.Baseline, err = gliOrBaselineDigest(x.Last.P, w); err != nil {
		return x, err
	}
	x.Findings = pcie.EvaluateLinkWidth(w, now)
	return x, nil
}

// ---------------------------------------------------------------------------
// Canonical strings of ratified values.
// ---------------------------------------------------------------------------

func gliOrT(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func gliOrValueString(v model.Value) string {
	if n, ok := v.Int(); ok {
		return "int:" + strconv.FormatInt(n, 10)
	}
	if f, ok := v.Float(); ok {
		return "float:" + strconv.FormatFloat(f, 'g', -1, 64)
	}
	if b, ok := v.Bool(); ok {
		return "bool:" + strconv.FormatBool(b)
	}
	s, _ := v.Str()
	return "string:" + s
}

// gliOrWindowSig renders every retained observation of a window.
func gliOrWindowSig(w evidence.Window) string {
	var b strings.Builder
	for _, s := range w.Subjects() {
		sigs, err := w.Signals(s)
		if err != nil {
			fmt.Fprintf(&b, "%s signals error: %v\n", s.Key(), err)
			continue
		}
		for _, sig := range sigs {
			series, err := w.SeriesFor(s, sig)
			if err != nil {
				fmt.Fprintf(&b, "%s %s series error: %v\n", s.Key(), sig, err)
				continue
			}
			for _, se := range series {
				for _, o := range se.Observations() {
					keys := make([]string, 0, len(o.Dimensions))
					for k := range o.Dimensions {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					var dims []string
					for _, k := range keys {
						dims = append(dims, k+"="+o.Dimensions[k])
					}
					fmt.Fprintf(&b, "%s|%s|%s/%s|%s|%s|%s|%s|%s|%d|%s|%s\n", s.Key(), sig, o.Source.Type, o.Source.Name, o.ID,
						gliOrValueString(o.Value), o.Unit, strings.Join(dims, ","), gliOrT(o.ObservedAt), o.Sequence, o.Quality.String(), gliOrT(o.ExpiresAt))
				}
			}
		}
	}
	return b.String()
}

// gliOrSubjects lists the subject keys of a window.
func gliOrSubjects(w evidence.Window) []string {
	var out []string
	for _, s := range w.Subjects() {
		out = append(out, s.Key())
	}
	return out
}

func gliOrProvString(p fleet.EdgeProvenance) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%s/%s|%s|%s", p.Edge.From.Key(), p.Edge.Relation.String(), p.Edge.To.Key(), p.Edge.Origin.String(),
		p.EvidenceID, p.BundleRevision, p.CollectorProfileID, p.Kind.String(), p.Source.Type, p.Source.Name, gliOrT(p.ObservedAt), gliOrT(p.ExpiresAt))
}

func gliOrBindString(b fleet.ObservedBinding) string {
	return fmt.Sprintf("%s/%s/%s|%s|%s|%s|%s/%s/%s/%s/%s|%s/%s|%s|%s|%s|%s|%s", b.Node.ClusterID, b.Node.Name, b.Node.UID, b.BootID, b.BDF, b.Function.Key(),
		b.Claim.Vendor, b.Claim.UUID, b.Claim.Serial, b.Claim.Source, b.Claim.EvidenceID, b.Source.Type, b.Source.Name, b.EvidenceID,
		b.BundleRevision, b.CollectorProfileID, gliOrT(b.ObservedAt), gliOrT(b.ExpiresAt))
}

func gliOrProvStrings(ps []fleet.EdgeProvenance) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, gliOrProvString(p))
	}
	return out
}

func gliOrBindStrings(bs []fleet.ObservedBinding) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, gliOrBindString(b))
	}
	return out
}

func gliOrEnvString(e fleet.SnapshotEnvelope) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%d|%s|%s", e.NodeUID, e.BootID, e.PayloadDigest, e.BundleRevision, e.Session, e.Sequence,
		e.Completeness.String(), gliOrT(e.ObservedAt))
}

func gliOrCursorString(c fleet.SnapshotCursor) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%d|%s|%s|%t", c.NodeUID, c.BootID, c.PayloadDigest, c.BundleRevision, c.Session, c.Sequence,
		c.Completeness.String(), gliOrT(c.ObservedAt), c.Baseline)
}

func gliOrTrustString(p fleet.CollectorTrustProfile) string {
	var srcs []string
	for _, s := range p.Sources {
		srcs = append(srcs, s.Capability.String()+":"+s.Source.Type+"/"+s.Source.Name)
	}
	return fmt.Sprintf("%s|%s|%s|%s|%d|%s", p.ID, p.Mode.String(), p.ClusterID, p.NodeUID, p.Session, strings.Join(srcs, ","))
}

func gliOrPolicyString(p fleet.Policy) string {
	var cov []string
	for _, c := range p.RequiredCoverage {
		cov = append(cov, fmt.Sprintf("%s/%s/%t", c.Name, c.PathKind, c.Required))
	}
	return fmt.Sprintf("%s|%s|%s|%s", p.Revision, p.Freshness, p.ReadyFor, strings.Join(cov, ","))
}

func gliOrIntentString(in fleet.Intent) string {
	return fmt.Sprintf("%s/%s|%s/%s/%s|%s|%s|%d|%s|%s/%s/%s/%s/%s", in.Device.Name, in.Device.UID, in.Node.ClusterID, in.Node.Name, in.Node.UID,
		in.Desired.String(), in.RequestID, in.MetadataGeneration, gliOrT(in.ObservedAt),
		in.Claim.Vendor, in.Claim.UUID, in.Claim.Serial, in.Claim.Source, in.Claim.EvidenceID)
}

// gliOrFindingSigs renders findings as sorted ID|type|severity|state|scope strings.
func gliOrFindingSigs(fs []model.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		var scope []string
		for _, s := range f.Scope {
			scope = append(scope, s.Key())
		}
		sort.Strings(scope)
		out = append(out, fmt.Sprintf("%s|%s|%s|%s|%s", f.ID, f.Type.String(), f.Severity.String(), f.State.String(), strings.Join(scope, ",")))
	}
	sort.Strings(out)
	return out
}

// gliOrDecisionStringWith renders the GLI-078 device fields of a decision;
// withNIC false leaves out the coverage items of path kind nic-lldp-remote.
func gliOrDecisionStringWith(d fleet.DeviceDecision, withNIC bool) string {
	var cov []string
	for _, c := range d.Coverage {
		if !withNIC && c.PathKind == gliOrNICPathKind {
			continue
		}
		cov = append(cov, c.Name+"/"+c.State.String()+"/"+c.Reason)
	}
	ids := append([]string(nil), d.FindingIDs...)
	sort.Strings(ids)
	return fmt.Sprintf("phase=%s qualification=%s reason=%q binding=%s allocation=%s graphRevision=%q findings=%v coverage=%v",
		d.Phase.String(), d.Qualification.String(), d.Reason, d.BindingState.String(), d.Allocation.String(), d.GraphRevision, ids, cov)
}

// gliOrDecisionString is the full GLI-078 rendering (for two decisions made
// with the same CollectorTrust.Mode).
func gliOrDecisionString(d fleet.DeviceDecision) string { return gliOrDecisionStringWith(d, true) }

// gliOrDecisionStringNoNIC is the rendering for offline <-> live comparisons: the
// nic-lldp-remote coverage item is not part of GLI-078.
func gliOrDecisionStringNoNIC(d fleet.DeviceDecision) string {
	return gliOrDecisionStringWith(d, false)
}

// gliOrNICCoverage renders the nic-lldp-remote coverage items of a decision.
func gliOrNICCoverage(d fleet.DeviceDecision) string {
	var out []string
	for _, c := range d.Coverage {
		if c.PathKind == gliOrNICPathKind {
			out = append(out, c.Name+"/"+c.PathKind+"/"+c.State.String()+"/"+c.Reason)
		}
	}
	return fmt.Sprint(out)
}

// gliOrNodeString renders the GLI-078 node fields of a node decision.
func gliOrNodeString(n fleet.NodeDecision) string {
	return fmt.Sprintf("eligibility=%s qualification=%s reason=%q selection=%s deviceCount=%d",
		n.Eligibility.String(), n.Qualification.String(), n.Reason, n.Selection.String(), n.DeviceCount)
}

// ---------------------------------------------------------------------------
// Bundle of the live ring (GLI-072/073/075) and the offline replay of GFX-048.
// ---------------------------------------------------------------------------

// gliOrBundle assembles the bundle a live NodeBundles call must hold for one
// intent after the accepted frames: topology of the last frame only, window of
// the ring, digests, findings at q.Now, GraphRevision = WindowRevision = the
// last bundle_revision, no allocation or fence (GFX-047 with GLI-072).
func gliOrBundle(pol fleet.Policy, in fleet.Intent, frames []gliOrFrame, cursor fleet.SnapshotCursor, trust fleet.CollectorTrustProfile, now time.Time) (fleet.AssessmentBundle, error) {
	x, err := gliOrExpectFor(frames, pol.Freshness, now)
	if err != nil {
		return fleet.AssessmentBundle{}, err
	}
	snap, err := gliOrSnapshot(x.Last, pol.Freshness)
	if err != nil {
		return fleet.AssessmentBundle{}, err
	}
	rev := x.Last.Env.BundleRevision
	return fleet.AssessmentBundle{
		Policy: pol, Intent: in, Snapshot: x.Last.Env, Admitted: cursor,
		GraphRevision: rev, WindowRevision: rev, TopologyDigest: x.Topology, BaselineDigest: x.Baseline,
		Topology: snap, Window: x.Window, Provenance: x.Last.Prov, Bindings: x.Last.Binds,
		Findings: x.Findings, FindingsEvaluatedAt: now, FindingsGraphRevision: rev, CollectorTrust: trust,
	}, nil
}

// gliOrReplayIn is the input of the GFX-048/049 replay over wire frames.
type gliOrReplayIn struct {
	Frames            []*ingestpb.SnapshotFrame
	Policy            fleet.Policy
	Intents           []fleet.Intent // in the order of the AssessNode request
	Profile           fleet.CollectorTrustProfile
	Cluster, NodeName string
	FleetUID          string
	// LiveStamps stamps edges and bindings by source match against the profile
	// sources (GLI-073) instead of with the profile ID; used with a live-mode
	// profile for the independent S1 call for the nic-lldp-remote coverage item.
	LiveStamps bool
}

// gliOrReplayOut holds dec_k(d) per device (request order) and frame, and the
// node decision of the last frame (previous nil, GFX-049).
type gliOrReplayOut struct {
	DecAt   [][]fleet.DeviceDecision
	Node    fleet.NodeDecision
	Cursors []fleet.SnapshotCursor
}

// gliOrReplay runs GFX-040..049 on the wire frames: cumulative window (at most
// 16 frames, so the live ring never evicts), per-frame topology and digests,
// the admission chain, EvaluateDevice with the previous decision and
// AggregateNode at the last frame time. Every element is stamped with the
// profile ID, as the offline artifact is; trust is decided by the profile
// sources (S1) alone.
func gliOrReplay(in gliOrReplayIn) (gliOrReplayOut, error) {
	var out gliOrReplayOut
	if len(in.Frames) == 0 || len(in.Frames) > 16 {
		return out, fmt.Errorf("oracle replay needs 1..16 frames, got %d", len(in.Frames))
	}
	ctx := gliOrCtx{
		Cluster: in.Cluster, NodeName: in.NodeName,
		StampEdge: gliOrConstStamp(in.Profile.ID), StampBinding: gliOrConstStamp(in.Profile.ID),
	}
	if in.LiveStamps {
		ctx.StampEdge, ctx.StampBinding = gliOrLiveStamps(in.Profile.ID, in.Profile.Sources)
	}
	var frames []gliOrFrame
	for k, f := range in.Frames {
		fr, err := gliOrApplyM(f, ctx)
		if err != nil {
			return out, fmt.Errorf("M(frame %d): %w", k, err)
		}
		frames = append(frames, fr)
	}
	var prev *fleet.SnapshotCursor
	for k, fr := range frames {
		c, order, err := fleet.AdmitSnapshot(in.Profile.Session, prev, fr.Env)
		if err != nil || order != fleet.SnapshotAccepted {
			return out, fmt.Errorf("frame %d is not accepted by S1: %s %v", k, order.String(), err)
		}
		cc := c
		out.Cursors = append(out.Cursors, cc)
		prev = &cc
	}
	cfg := gliOrConfig(in.Policy.Freshness)
	w, err := evidence.NewWindow(cfg)
	if err != nil {
		return out, err
	}
	out.DecAt = make([][]fleet.DeviceDecision, len(in.Intents))
	prevDec := make([]*fleet.DeviceDecision, len(in.Intents))
	for k, fr := range frames {
		for _, o := range fr.Obs {
			if w, err = w.Add(o); err != nil {
				return out, fmt.Errorf("frame %d Window.Add(%s): %w", k, o.ID, err)
			}
		}
		snap, err := gliOrSnapshot(fr, in.Policy.Freshness)
		if err != nil {
			return out, err
		}
		bd, err := gliOrBaselineDigest(fr.P, w)
		if err != nil {
			return out, err
		}
		t := fr.Env.ObservedAt
		for i, intent := range in.Intents {
			b := fleet.AssessmentBundle{
				Policy: in.Policy, Intent: intent, Snapshot: fr.Env, Admitted: out.Cursors[k],
				GraphRevision: fr.Env.BundleRevision, WindowRevision: fr.Env.BundleRevision,
				TopologyDigest: gliOrTopologyDigest(fr), BaselineDigest: bd,
				Topology: snap, Window: w, Provenance: fr.Prov, Bindings: fr.Binds,
				Findings: pcie.EvaluateLinkWidth(w, t), FindingsEvaluatedAt: t, FindingsGraphRevision: fr.Env.BundleRevision,
				CollectorTrust: in.Profile,
			}
			d, err := fleet.EvaluateDevice(b, prevDec[i], t)
			if err != nil {
				return out, fmt.Errorf("EvaluateDevice(frame %d, %s): %w", k, intent.Device.Name, err)
			}
			out.DecAt[i] = append(out.DecAt[i], d)
			dc := d
			prevDec[i] = &dc
		}
	}
	var devs []fleet.DeviceAggregate
	for i, intent := range in.Intents {
		devs = append(devs, fleet.DeviceAggregate{
			DeviceUID: intent.Device.UID, NodeUID: intent.Node.UID, Desired: intent.Desired,
			MetadataGeneration: intent.MetadataGeneration, Decision: out.DecAt[i][len(frames)-1],
		})
	}
	sort.SliceStable(devs, func(i, j int) bool { return devs[i].DeviceUID < devs[j].DeviceUID })
	sel := fleet.SelectionComplete
	if len(devs) == 0 {
		sel = fleet.SelectionNoDevices
	}
	last := frames[len(frames)-1]
	nd, err := fleet.AggregateNode(fleet.AggregateNodeInput{
		Node: fleet.NodeRef{ClusterID: in.Cluster, Name: in.NodeName, UID: last.Env.NodeUID}, FleetUID: in.FleetUID,
		Selection: sel, Devices: devs,
	}, nil, last.Env.ObservedAt)
	if err != nil {
		return out, fmt.Errorf("AggregateNode: %w", err)
	}
	out.Node = nd
	return out, nil
}
