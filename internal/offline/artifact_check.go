package offline

import (
	"strconv"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// nvidiaVendor is the vendor every offline GPU binding claims.
const nvidiaVendor = "NVIDIA"

// checkedPayload is a frame payload with its parsed timestamps. All payload
// observedAt and receivedAt values equal the frame observedAt.
type checkedPayload struct {
	raw                 *rawPayload
	observedAt          time.Time
	edgeEvidenceExpires []time.Time
	observationExpires  []time.Time
	bindingExpires      []time.Time
}

// artifactContent is a validated artifact converted to ratified values.
type artifactContent struct {
	clusterID string
	node      app.NodeIdentity
	trust     fleet.CollectorTrustProfile
	frames    []app.Frame
}

var (
	assetKindByName = func() map[string]model.AssetKind {
		m := map[string]model.AssetKind{}
		for k := model.AssetKind(1); k.IsValid(); k++ {
			m[k.String()] = k
		}
		return m
	}()
	qualityByName = func() map[string]model.EvidenceQuality {
		m := map[string]model.EvidenceQuality{}
		for q := model.EvidenceQuality(0); q.IsValid(); q++ {
			m[q.String()] = q
		}
		return m
	}()
	trustCapabilityByName = map[string]fleet.TrustCapability{
		"SysfsPhysicalParent": fleet.TrustSysfsPhysicalParent,
		"SysfsPCIeWidth":      fleet.TrustSysfsPCIeWidth,
		"NVIDIAUUIDBinding":   fleet.TrustNVIDIAUUIDBinding,
		"OperatorBaseline":    fleet.TrustOperatorBaseline,
	}
)

func artifactFail(where, problem string) error {
	return inputError(ErrArtifactInvalid, where+": "+problem)
}

// checkArtifact applies the cross-field rules, recomputes IDs, digests and
// revisions, and converts every frame to ratified values.
func checkArtifact(a *rawArtifact) (artifactContent, error) {
	var out artifactContent
	out.clusterID = a.clusterID
	out.node = app.NodeIdentity{Name: a.nodeName, UID: a.nodeUID}

	for i := 1; i < len(a.trust.sources); i++ {
		p, c := a.trust.sources[i-1], a.trust.sources[i]
		if compareStrings3(p.capability, p.sourceType, p.sourceName, c.capability, c.sourceType, c.sourceName) >= 0 {
			return out, artifactFail("trust.sources["+strconv.Itoa(i)+"]", "sources are not strictly ordered")
		}
	}
	trust := fleet.CollectorTrustProfile{
		ID:        a.trust.profileID,
		Mode:      fleet.TrustModeOffline,
		ClusterID: a.clusterID,
		NodeUID:   a.nodeUID,
		Session:   int64(a.trust.session),
	}
	for _, s := range a.trust.sources {
		trust.Sources = append(trust.Sources, fleet.TrustedSource{
			Capability: trustCapabilityByName[s.capability],
			Source:     model.SourceRef{Type: s.sourceType, Name: s.sourceName},
		})
	}
	if err := trust.Validate(); err != nil {
		return out, artifactFail("trust", "collector trust profile is rejected by domain validation")
	}
	out.trust = trust

	nodeAsset := model.AssetRef{Kind: model.KindKubernetesNode, Canonical: model.NamespaceKubernetesNodeUID + ":" + a.nodeUID}
	partition, err := graph.PartitionFor(nodeAsset)
	if err != nil {
		return out, artifactFail("node.uid", "node asset anchors no partition")
	}

	var previous time.Time
	for k := range a.frames {
		f, t, err := checkFrame(a, k, previous, partition, nodeAsset.Key())
		if err != nil {
			return out, err
		}
		previous = t
		out.frames = append(out.frames, f)
	}
	return out, nil
}

func compareStrings3(a1, a2, a3, b1, b2, b3 string) int {
	if c := strings.Compare(a1, b1); c != 0 {
		return c
	}
	if c := strings.Compare(a2, b2); c != 0 {
		return c
	}
	return strings.Compare(a3, b3)
}

func checkFrame(a *rawArtifact, k int, previous time.Time, partition model.PartitionKey, nodeKey string) (app.Frame, time.Time, error) {
	f := &a.frames[k]
	where := "frames[" + strconv.Itoa(k) + "]"
	fail := func(field, problem string) error { return artifactFail(where+field, problem) }

	if f.nodeUID != a.nodeUID {
		return app.Frame{}, time.Time{}, fail(".nodeUID", "does not match node.uid")
	}
	if f.bootID != a.bootID {
		return app.Frame{}, time.Time{}, fail(".bootID", "does not match bootID")
	}
	if f.session != a.trust.session {
		return app.Frame{}, time.Time{}, fail(".session", "does not match trust.session")
	}
	if f.sequence != uint64(k) {
		return app.Frame{}, time.Time{}, fail(".sequence", "does not match the frame index")
	}
	observedAt, ok := parseTNotation(f.observedAt)
	if !ok {
		return app.Frame{}, time.Time{}, fail(".observedAt", "is not a valid timestamp in canonical notation")
	}
	if k > 0 && !observedAt.After(previous) {
		return app.Frame{}, time.Time{}, fail(".observedAt", "is not later than the previous frame")
	}
	if !hasEvaluationMargin(observedAt) {
		return app.Frame{}, time.Time{}, fail(".observedAt", "leaves no evaluation margin inside the timestamp range")
	}
	for i := 1; i < len(f.diagnostics); i++ {
		p, c := f.diagnostics[i-1], f.diagnostics[i]
		if p.code > c.code || (p.code == c.code && p.subject >= c.subject) {
			return app.Frame{}, time.Time{}, fail(".diagnostics["+strconv.Itoa(i)+"]", "diagnostics are not strictly ordered")
		}
	}
	if f.diagnosticsTruncated != (f.diagnosticsTotal > maxDiagnostics) {
		return app.Frame{}, time.Time{}, fail(".diagnosticsTruncated", "does not match diagnosticsTotal")
	}
	if f.diagnosticsTruncated {
		if len(f.diagnostics) != maxDiagnostics {
			return app.Frame{}, time.Time{}, fail(".diagnostics", "a truncated list must hold exactly 256 entries")
		}
	} else if f.diagnosticsTotal != uint64(len(f.diagnostics)) {
		return app.Frame{}, time.Time{}, fail(".diagnosticsTotal", "does not match the number of diagnostics")
	}

	cp, err := checkPayload(f, where+".payload", observedAt, nodeKey)
	if err != nil {
		return app.Frame{}, time.Time{}, err
	}
	digest, length := payloadCanonical(cp)
	if length > maxCanonicalBytes {
		return app.Frame{}, time.Time{}, fail(".payload", "canonical encoding exceeds its bound")
	}
	if digest != f.payloadDigest {
		return app.Frame{}, time.Time{}, fail(".payloadDigest", "does not match the recomputed digest")
	}
	revision := strconv.FormatUint(f.session, 10) + ":" + strconv.FormatUint(f.sequence, 10) + ":" + f.payloadDigest
	if f.bundleRevision != revision {
		return app.Frame{}, time.Time{}, fail(".bundleRevision", "does not match session, sequence and digest")
	}

	frame, err := convertFrame(a, f, cp, where, partition)
	if err != nil {
		return app.Frame{}, time.Time{}, err
	}
	return frame, observedAt, nil
}

// checkPayload checks payload order, timestamps and IDs and parses the
// expiry timestamps.
func checkPayload(f *rawFrame, where string, observedAt time.Time, nodeKey string) (*checkedPayload, error) {
	p := &f.payload
	cp := &checkedPayload{raw: p, observedAt: observedAt}
	fail := func(field string, i int, problem string) error {
		return artifactFail(where+"."+field+"["+strconv.Itoa(i)+"]", problem)
	}
	expiry := func(s string) (time.Time, bool) {
		t, ok := parseTNotation(s)
		return t, ok && t.After(observedAt)
	}

	nodeAssets := 0
	for i, as := range p.assets {
		if _, ok := assetKindByName[as.kind]; !ok {
			return nil, fail("assets", i, "kind is not an asset kind")
		}
		if i > 0 && p.assets[i-1].key() >= as.key() {
			return nil, fail("assets", i, "assets are not strictly ordered")
		}
		if as.kind == model.KindKubernetesNode.String() {
			nodeAssets++
			if as.key() != nodeKey {
				return nil, fail("assets", i, "node asset does not name node.uid")
			}
		}
	}
	if nodeAssets != 1 {
		return nil, artifactFail(where+".assets", "must hold exactly one node asset")
	}

	for i, e := range p.edges {
		if i > 0 {
			q := p.edges[i-1]
			c := compareStrings3(q.fromKey, q.toKey, q.relation, e.fromKey, e.toKey, e.relation)
			if c > 0 || (c == 0 && q.origin >= e.origin) {
				return nil, fail("edges", i, "edges are not strictly ordered")
			}
		}
	}

	if len(p.edgeEvidence) != len(p.edges) {
		return nil, artifactFail(where+".edgeEvidence", "count does not match edges")
	}
	cp.edgeEvidenceExpires = make([]time.Time, len(p.edgeEvidence))
	for i, e := range p.edgeEvidence {
		if e.edgeIndex != uint64(i) {
			return nil, fail("edgeEvidence", i, "edgeIndex does not match the position")
		}
		if e.observedAt != f.observedAt {
			return nil, fail("edgeEvidence", i, "observedAt does not match the frame")
		}
		t, ok := expiry(e.expiresAt)
		if !ok {
			return nil, fail("edgeEvidence", i, "expiresAt is invalid or not after observedAt")
		}
		cp.edgeEvidenceExpires[i] = t
		edge := p.edges[i]
		want := deterministicID("pe", f.session, f.sequence, "dpa.edge-evidence.v1", edge.fromKey, edge.relation, edge.toKey, edge.origin)
		if e.evidenceID != want {
			return nil, fail("edgeEvidence", i, "evidenceID does not match the recomputed ID")
		}
	}

	cp.observationExpires = make([]time.Time, len(p.observations))
	ids := make(map[string]struct{}, len(p.observations))
	for i := range p.observations {
		o := &p.observations[i]
		if _, ok := assetKindByName[o.subject.kind]; !ok {
			return nil, fail("observations", i, "subject kind is not an asset kind")
		}
		if o.observedAt != f.observedAt || o.receivedAt != f.observedAt {
			return nil, fail("observations", i, "observedAt or receivedAt does not match the frame")
		}
		t, ok := expiry(o.expiresAt)
		if !ok {
			return nil, fail("observations", i, "expiresAt is invalid or not after observedAt")
		}
		cp.observationExpires[i] = t
		if o.sequence != f.sequence {
			return nil, fail("observations", i, "sequence does not match the frame")
		}
		if i > 0 && compareObservations(&p.observations[i-1], o) >= 0 {
			return nil, fail("observations", i, "observations are not strictly ordered")
		}
		want := deterministicID("ob", f.session, f.sequence, "dpa.observation.v1", o.subject.key(), o.signal)
		if o.id != want {
			return nil, fail("observations", i, "id does not match the recomputed ID")
		}
		if _, dup := ids[o.id]; dup {
			return nil, fail("observations", i, "id is not unique in the frame")
		}
		ids[o.id] = struct{}{}
	}

	cp.bindingExpires = make([]time.Time, len(p.gpuBindings))
	for i, b := range p.gpuBindings {
		if i > 0 {
			q := p.gpuBindings[i-1]
			if q.bdf > b.bdf || (q.bdf == b.bdf && q.uuid >= b.uuid) {
				return nil, fail("gpuBindings", i, "bindings are not strictly ordered")
			}
		}
		if b.observedAt != f.observedAt {
			return nil, fail("gpuBindings", i, "observedAt does not match the frame")
		}
		t, ok := expiry(b.expiresAt)
		if !ok {
			return nil, fail("gpuBindings", i, "expiresAt is invalid or not after observedAt")
		}
		cp.bindingExpires[i] = t
		want := deterministicID("nb", f.session, f.sequence, "dpa.gpu-binding.v1", b.uuid, b.bdf)
		if b.evidenceID != want {
			return nil, fail("gpuBindings", i, "evidenceID does not match the recomputed ID")
		}
	}
	return cp, nil
}

// compareObservations orders observations by subject key, signal, source
// type, source name, dimensions, observedAt and ID. Within one frame every
// observedAt is equal, so the instant never decides.
func compareObservations(a, b *rawObservation) int {
	if c := compareStrings3(a.subject.key(), a.signal, a.sourceType, b.subject.key(), b.signal, b.sourceType); c != 0 {
		return c
	}
	if c := strings.Compare(a.sourceName, b.sourceName); c != 0 {
		return c
	}
	if c := compareDimensionSets(a.dimensions, b.dimensions); c != 0 {
		return c
	}
	return strings.Compare(a.id, b.id)
}

// assetOf converts a raw asset with the offline conversion rule: kind and
// canonical identity only.
func assetOf(a rawAsset) (model.AssetRef, bool) {
	kind, ok := assetKindByName[a.kind]
	if !ok {
		return model.AssetRef{}, false
	}
	id, err := identity.ParseCanonical(a.canonical)
	if err != nil {
		return model.AssetRef{}, false
	}
	ref, err := model.NewAssetRef(kind, id)
	if err != nil || ref.Validate() != nil {
		return model.AssetRef{}, false
	}
	return ref, true
}

func valueOf(v rawValue) model.Value {
	switch v.kind {
	case "int":
		return model.NewIntValue(v.integer)
	case "float":
		return model.NewFloatValue(v.float)
	case "bool":
		return model.NewBoolValue(v.boolean)
	default:
		return model.NewStringValue(v.str)
	}
}

// convertFrame applies the offline conversion to ratified values and checks
// that each value validates and that the frame topology forms a resync.
func convertFrame(a *rawArtifact, f *rawFrame, cp *checkedPayload, where string, partition model.PartitionKey) (app.Frame, error) {
	p := &f.payload
	fail := func(field string, i int, problem string) error {
		return artifactFail(where+".payload."+field+"["+strconv.Itoa(i)+"]", problem)
	}
	completeness := fleet.CompletenessComplete
	if f.completeness == "PARTIAL" {
		completeness = fleet.CompletenessPartial
	}
	env := fleet.SnapshotEnvelope{
		NodeUID:        f.nodeUID,
		BootID:         f.bootID,
		PayloadDigest:  f.payloadDigest,
		BundleRevision: f.bundleRevision,
		Session:        int64(f.session),
		Sequence:       f.sequence,
		Completeness:   completeness,
		ObservedAt:     cp.observedAt,
	}
	if err := env.Validate(); err != nil {
		return app.Frame{}, artifactFail(where, "snapshot envelope is rejected by domain validation")
	}
	out := app.Frame{
		Envelope:             env,
		Assets:               make([]model.AssetRef, 0, len(p.assets)),
		Edges:                make([]graph.Edge, 0, len(p.edges)),
		Provenance:           make([]fleet.EdgeProvenance, 0, len(p.edgeEvidence)),
		Observations:         make([]model.Observation, 0, len(p.observations)),
		Bindings:             make([]fleet.ObservedBinding, 0, len(p.gpuBindings)),
		Diagnostics:          make([]app.Diagnostic, 0, len(f.diagnostics)),
		DiagnosticsTotal:     f.diagnosticsTotal,
		DiagnosticsTruncated: f.diagnosticsTruncated,
	}
	byKey := make(map[string]model.AssetRef, len(p.assets))
	for i, as := range p.assets {
		ref, ok := assetOf(as)
		if !ok {
			return app.Frame{}, fail("assets", i, "asset is rejected by domain validation")
		}
		byKey[ref.Key()] = ref
		out.Assets = append(out.Assets, ref)
	}
	for i, e := range p.edges {
		from, okFrom := byKey[e.fromKey]
		to, okTo := byKey[e.toKey]
		if !okFrom || !okTo {
			return app.Frame{}, fail("edges", i, "endpoint is not a payload asset")
		}
		edge, err := graph.NewEdge(from, to, model.RelLocatedIn, model.OriginObserved)
		if err != nil {
			return app.Frame{}, fail("edges", i, "edge is rejected by domain validation")
		}
		out.Edges = append(out.Edges, edge)
	}
	for i, e := range p.edgeEvidence {
		prov := fleet.EdgeProvenance{
			Edge:               out.Edges[e.edgeIndex],
			EvidenceID:         e.evidenceID,
			BundleRevision:     f.bundleRevision,
			CollectorProfileID: a.trust.profileID,
			Kind:               fleet.EdgeEvidenceObserved,
			Source:             model.SourceRef{Type: e.sourceType, Name: e.sourceName},
			ObservedAt:         cp.observedAt,
			ExpiresAt:          cp.edgeEvidenceExpires[i],
		}
		if err := prov.Validate(); err != nil {
			return app.Frame{}, fail("edgeEvidence", i, "provenance is rejected by domain validation")
		}
		out.Provenance = append(out.Provenance, prov)
	}
	for i, o := range p.observations {
		subject, ok := assetOf(o.subject)
		if !ok {
			return app.Frame{}, fail("observations", i, "subject is rejected by domain validation")
		}
		dims := make(map[string]string, len(o.dimensions))
		for _, d := range o.dimensions {
			dims[d.key] = d.value
		}
		obs, err := model.NewObservation(model.Observation{
			ID:         o.id,
			Source:     model.SourceRef{Type: o.sourceType, Name: o.sourceName},
			Subject:    subject,
			Signal:     model.SignalRef(o.signal),
			Value:      valueOf(o.value),
			Unit:       o.unit,
			Dimensions: dims,
			ObservedAt: cp.observedAt,
			ReceivedAt: cp.observedAt,
			ExpiresAt:  cp.observationExpires[i],
			Sequence:   o.sequence,
			Quality:    qualityByName[o.quality],
			RawDigest:  o.rawDigest,
		})
		if err != nil {
			return app.Frame{}, fail("observations", i, "observation is rejected by domain validation")
		}
		out.Observations = append(out.Observations, obs)
	}
	for i, b := range p.gpuBindings {
		function, err := model.NewAssetRef(model.KindPCIeFunction, model.TypedID{Namespace: model.NamespacePCIBDF, Value: b.bdf})
		if err != nil {
			return app.Frame{}, fail("gpuBindings", i, "function is rejected by domain validation")
		}
		binding := fleet.ObservedBinding{
			Node:     fleet.NodeRef{ClusterID: a.clusterID, Name: a.nodeName, UID: a.nodeUID},
			BootID:   a.bootID,
			BDF:      b.bdf,
			Function: function,
			Claim: fleet.InventoryClaim{
				Vendor:     nvidiaVendor,
				UUID:       b.uuid,
				Serial:     "",
				Source:     b.sourceName,
				EvidenceID: b.evidenceID,
			},
			Source:             model.SourceRef{Type: b.sourceType, Name: b.sourceName},
			EvidenceID:         b.evidenceID,
			BundleRevision:     f.bundleRevision,
			CollectorProfileID: a.trust.profileID,
			ObservedAt:         cp.observedAt,
			ExpiresAt:          cp.bindingExpires[i],
		}
		if err := binding.Validate(); err != nil {
			return app.Frame{}, fail("gpuBindings", i, "binding is rejected by domain validation")
		}
		out.Bindings = append(out.Bindings, binding)
	}
	if _, err := graph.NewResync(partition, f.sequence, out.Assets, out.Edges); err != nil {
		return app.Frame{}, artifactFail(where+".payload", "topology does not form a valid resync")
	}
	for _, d := range f.diagnostics {
		out.Diagnostics = append(out.Diagnostics, app.Diagnostic{Code: d.code, Subject: d.subject})
	}
	return out, nil
}
