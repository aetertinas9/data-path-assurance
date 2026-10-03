package liveingest

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// FrameContext is what checking a frame needs to know besides the frame: who
// the stream belongs to and which collector trust profile stamps its values.
// The caller has already matched the envelope identity of the frame (node UID,
// boot ID and session) against the stream; CheckFrame repeats that match as a
// safety net.
type FrameContext struct {
	// ClusterID is the cluster the controller serves.
	ClusterID string
	// NodeName and NodeUID name the Node of the stream; NodeUID is the UID of
	// the client certificate and of the hello.
	NodeName, NodeUID string
	// BootID is the boot ID of the hello.
	BootID string
	// Session is the session of the stream.
	Session int64
	// ProfileID is the collector profile ID stamped on the values whose source
	// Sources lists, and Sources are the trusted sources of the profile.
	ProfileID string
	Sources   []fleet.TrustedSource
}

func (fc FrameContext) validate() error {
	switch {
	case !ValidClusterID(fc.ClusterID):
		return fmt.Errorf("%w: cluster ID", ErrInvalidContext)
	case !ValidNodeName(fc.NodeName):
		return fmt.Errorf("%w: node name", ErrInvalidContext)
	case !ValidNodeUID(fc.NodeUID):
		return fmt.Errorf("%w: node UID", ErrInvalidContext)
	case !ValidBootID(fc.BootID):
		return fmt.Errorf("%w: boot ID", ErrInvalidContext)
	case fc.Session < 1:
		return fmt.Errorf("%w: session", ErrInvalidContext)
	case !isIdentifier(fc.ProfileID, 1, maxIDBytes):
		return fmt.Errorf("%w: profile ID", ErrInvalidContext)
	}
	return nil
}

// Checked is a frame that passed CheckFrame, converted to ratified domain
// values. Every slice is freshly allocated and owned by the caller.
type Checked struct {
	// Envelope is the admission envelope of the frame. Its PayloadDigest is the
	// recomputed digest in lowercase hex and its BundleRevision the canonical
	// revision (both equal what the frame carried).
	Envelope fleet.SnapshotEnvelope
	// Partition is the graph partition of the node and NodeAsset the
	// KubernetesNode asset that anchors it.
	Partition model.PartitionKey
	NodeAsset model.AssetRef
	// Assets, Edges, Provenance, Observations and Bindings are the payload
	// values in wire order. Provenance and Bindings carry the collector
	// profile ID of the stamp rule (StampProfile).
	Assets       []model.AssetRef
	Edges        []graph.Edge
	Provenance   []fleet.EdgeProvenance
	Observations []model.Observation
	Bindings     []fleet.ObservedBinding
	// Allocation is a copy of the allocation batch as the wire carried it, nil
	// when the frame has none.
	Allocation *AllocationBatch
	// CanonicalLen is the length of the canonical encoding of the payload.
	CanonicalLen int
}

// CheckEnvelope checks the structure of the frame envelope: no unknown fields
// outside the payload, a valid completeness, a 32-byte payload digest, a
// bundle revision of at most MaxBundleRevisionBytes and a valid observation
// time. A violation is an error that matches ErrInvalidFrame.
func CheckEnvelope(f *Frame) error {
	switch {
	case f == nil:
		return invalid("envelope")
	case f.UnknownFields:
		return invalid("envelope_unknown_field")
	case f.Completeness != CompletenessComplete && f.Completeness != CompletenessPartial:
		return invalid("envelope_completeness")
	case len(f.PayloadDigest) != sha256Size:
		return invalid("envelope_digest")
	case len(f.BundleRevision) > MaxBundleRevisionBytes:
		return invalid("envelope_revision")
	case !f.ObservedAt.Valid():
		return invalid("envelope_time")
	}
	return nil
}

const sha256Size = 32

// CheckFrame applies every rule that a frame has on its own: the envelope
// structure (CheckEnvelope), the payload rules (counts, character sets, value
// grammar, ordering and uniqueness, references, deterministic IDs, the node
// asset, the conversion to ratified values and the allocation batch), and the
// match of the payload digest and the bundle revision against the values
// recomputed from the payload. Nothing is truncated and nothing is repaired:
// the first violation rejects the frame as a whole with an error that matches
// ErrInvalidFrame and whose Class names the violated rule. A FrameContext the
// caller cannot use yields an error that matches ErrInvalidContext instead.
//
// The rules that relate a frame to the previous accepted one are CheckRelative.
func CheckFrame(fc FrameContext, f *Frame) (*Checked, error) {
	if err := fc.validate(); err != nil {
		return nil, err
	}
	if err := CheckEnvelope(f); err != nil {
		return nil, err
	}
	if f.NodeUID != fc.NodeUID || f.BootID != fc.BootID || f.Session != fc.Session {
		return nil, invalid("identity")
	}
	p := f.Payload
	if p == nil {
		p = &Payload{}
	}
	if err := checkShape(fc, f, p); err != nil {
		return nil, err
	}
	sum, length, err := Digest(p)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(sum[:], f.PayloadDigest) {
		return nil, invalid("digest")
	}
	revision := Revision(f.Session, f.Sequence, sum)
	if f.BundleRevision != revision {
		return nil, invalid("revision")
	}
	c, err := convert(fc, f, p, hex.EncodeToString(sum[:]), revision)
	if err != nil {
		return nil, err
	}
	c.CanonicalLen = length
	return c, nil
}

// CheckRelative applies the rules that relate an accepted frame to its
// session: the observation time of the frame leaves room of 86400 seconds on
// both sides inside the valid timestamp range, every timestamp of the payload
// elements equals the observation time of the frame and every expiry is later,
// every observation carries the sequence of the frame, and, when previous is
// the observation time of the previous accepted frame of the session
// (hasPrevious), the frame is strictly later than it. A violation is an error
// that matches ErrInvalidFrame.
func CheckRelative(c *Checked, previous time.Time, hasPrevious bool) error {
	if c == nil {
		return fmt.Errorf("%w: no checked frame", ErrInvalidContext)
	}
	at := c.Envelope.ObservedAt
	if !hasEvaluationMargin(at) {
		return invalid("frame_time_margin")
	}
	for i := range c.Provenance {
		p := &c.Provenance[i]
		if !p.ObservedAt.Equal(at) || !p.ExpiresAt.After(at) {
			return invalid("frame_time")
		}
	}
	for i := range c.Bindings {
		b := &c.Bindings[i]
		if !b.ObservedAt.Equal(at) || !b.ExpiresAt.After(at) {
			return invalid("frame_time")
		}
	}
	for i := range c.Observations {
		o := &c.Observations[i]
		if !o.ObservedAt.Equal(at) || !o.ReceivedAt.Equal(at) {
			return invalid("frame_time")
		}
		if !o.ExpiresAt.IsZero() && !o.ExpiresAt.After(at) {
			return invalid("frame_time")
		}
		if o.Sequence != c.Envelope.Sequence {
			return invalid("observation_sequence")
		}
	}
	if hasPrevious && !at.After(previous) {
		return invalid("frame_time_order")
	}
	return nil
}

// hasEvaluationMargin reports whether t - 86400s and t + 86400s both stay in
// the valid timestamp range, 0001-01-01T00:00:00.000000001Z through
// 9999-12-31T23:59:59.999999999Z.
func hasEvaluationMargin(t time.Time) bool {
	const margin = 86400 * time.Second
	lo := time.Date(1, time.January, 1, 0, 0, 0, 1, time.UTC)
	hi := time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC)
	return !t.Add(-margin).Before(lo) && !t.Add(margin).After(hi)
}

// assetKindOf returns the asset kind with the given name.
func assetKindOf(name string) (model.AssetKind, bool) {
	for k := model.AssetKind(1); k.IsValid(); k++ {
		if k.String() == name {
			return k, true
		}
	}
	return 0, false
}

// evidenceQualityOf returns the evidence quality with the given name.
func evidenceQualityOf(name string) (model.EvidenceQuality, bool) {
	for q := model.EvidenceQuality(0); q.IsValid(); q++ {
		if q.String() == name {
			return q, true
		}
	}
	return 0, false
}

// checkAsset checks the grammar of one asset reference.
func checkAsset(a *Asset, class string) error {
	if !isIdentifier(a.Kind, 1, maxIdentBytes) || !isIdentifier(a.Canonical, 1, maxIdentBytes) {
		return invalid(class + "_text")
	}
	if _, ok := assetKindOf(a.Kind); !ok {
		return invalid(class + "_kind")
	}
	if len(a.Aliases) > MaxAliases {
		return invalid("payload_count")
	}
	for i, al := range a.Aliases {
		if !isIdentifier(al.Namespace, 1, maxIdentBytes) || !isIdentifier(al.Value, 1, maxIdentBytes) {
			return invalid(class + "_text")
		}
		if i > 0 {
			prev := a.Aliases[i-1]
			if c := strings.Compare(prev.Namespace, al.Namespace); c > 0 || (c == 0 && prev.Value >= al.Value) {
				return invalid(class + "_order")
			}
		}
	}
	return nil
}

// checkShape applies the payload rules that need nothing but the payload and
// the frame context (counts, character sets, value grammar, order,
// uniqueness, references, IDs, node asset, allocation batch).
func checkShape(fc FrameContext, f *Frame, p *Payload) error {
	if p.UnknownFields {
		return invalid("payload_unknown_field")
	}
	if len(p.Assets) > MaxAssets || len(p.Edges) > MaxEdges || len(p.EdgeEvidence) > MaxEdgeEvidence ||
		len(p.Observations) > MaxObservations || len(p.GPUBindings) > MaxGPUBindings {
		return invalid("payload_count")
	}
	if a := p.Allocation; a != nil && (len(a.Entries) > MaxAllocationEntries || len(a.EvidenceRefs) > MaxAllocationEvidence) {
		return invalid("payload_count")
	}

	nodeKey := model.KindKubernetesNode.String() + "/" + model.NamespaceKubernetesNodeUID + ":" + fc.NodeUID
	nodeAssets := 0
	keys := make(map[string]struct{}, len(p.Assets))
	for i := range p.Assets {
		a := &p.Assets[i]
		if err := checkAsset(a, "assets"); err != nil {
			return err
		}
		key := a.Key()
		if i > 0 && p.Assets[i-1].Key() >= key {
			return invalid("assets_order")
		}
		keys[key] = struct{}{}
		if a.Kind == model.KindKubernetesNode.String() {
			nodeAssets++
			if key != nodeKey {
				return invalid("assets_node")
			}
		}
	}
	if nodeAssets != 1 {
		return invalid("assets_node")
	}

	for i := range p.Edges {
		e := &p.Edges[i]
		if !isIdentifier(e.FromKey, 1, maxIdentBytes) || !isIdentifier(e.ToKey, 1, maxIdentBytes) {
			return invalid("edges_text")
		}
		if e.Relation != model.RelLocatedIn.String() || e.Origin != model.OriginObserved.String() {
			return invalid("edges_value")
		}
		if i > 0 {
			q := &p.Edges[i-1]
			c := strings.Compare(q.FromKey, e.FromKey)
			if c == 0 {
				c = strings.Compare(q.ToKey, e.ToKey)
			}
			if c == 0 {
				c = strings.Compare(q.Relation, e.Relation)
			}
			if c == 0 {
				c = strings.Compare(q.Origin, e.Origin)
			}
			if c >= 0 {
				return invalid("edges_order")
			}
		}
		if _, ok := keys[e.FromKey]; !ok {
			return invalid("edges_reference")
		}
		if _, ok := keys[e.ToKey]; !ok {
			return invalid("edges_reference")
		}
	}

	if len(p.EdgeEvidence) != len(p.Edges) {
		return invalid("edge_evidence_count")
	}
	for i := range p.EdgeEvidence {
		ev := &p.EdgeEvidence[i]
		if uint64(ev.EdgeIndex) != uint64(i) {
			return invalid("edge_evidence_index")
		}
		if ev.Kind != fleet.EdgeEvidenceObserved.String() {
			return invalid("edge_evidence_value")
		}
		if !isIdentifier(ev.SourceType, 1, maxSourceBytes) || !isIdentifier(ev.SourceName, 1, maxSourceBytes) ||
			!isIdentifier(ev.EvidenceID, 1, maxIDBytes) {
			return invalid("edge_evidence_text")
		}
		if !ev.ObservedAt.Valid() || !ev.ExpiresAt.Valid() {
			return invalid("edge_evidence_time")
		}
		e := &p.Edges[i]
		if ev.EvidenceID != EdgeEvidenceID(fc.Session, f.Sequence, e.FromKey, e.Relation, e.ToKey, e.Origin) {
			return invalid("edge_evidence_id")
		}
	}

	if err := checkObservations(fc, f, p); err != nil {
		return err
	}

	for i := range p.GPUBindings {
		b := &p.GPUBindings[i]
		if !isGPUUUID(b.UUID) || !isCanonicalBDF(b.BDF) || b.Serial != "" {
			return invalid("bindings_value")
		}
		if !isIdentifier(b.SourceType, 1, maxSourceBytes) || !isIdentifier(b.SourceName, 1, maxSourceBytes) ||
			!isIdentifier(b.EvidenceID, 1, maxIDBytes) {
			return invalid("bindings_text")
		}
		if !b.ObservedAt.Valid() || !b.ExpiresAt.Valid() {
			return invalid("bindings_time")
		}
		if i > 0 {
			q := &p.GPUBindings[i-1]
			if c := strings.Compare(q.BDF, b.BDF); c > 0 || (c == 0 && q.UUID >= b.UUID) {
				return invalid("bindings_order")
			}
		}
		if b.EvidenceID != BindingEvidenceID(fc.Session, f.Sequence, b.UUID, b.BDF) {
			return invalid("bindings_id")
		}
	}

	if a := p.Allocation; a != nil {
		return checkAllocation(f, a)
	}
	return nil
}

func checkObservations(fc FrameContext, f *Frame, p *Payload) error {
	ids := make(map[string]struct{}, len(p.Observations))
	var prevKey string
	for i := range p.Observations {
		o := &p.Observations[i]
		if !isIdentifier(o.ID, 1, maxIDBytes) ||
			!isIdentifier(o.Source.Type, 1, maxSourceBytes) || !isIdentifier(o.Source.Name, 1, maxSourceBytes) ||
			!isIdentifier(o.Signal, 1, maxIdentBytes) || !isIdentifier(o.Unit, 1, maxIdentBytes) {
			return invalid("observations_text")
		}
		if err := checkAsset(&o.Subject, "observations_subject"); err != nil {
			return err
		}
		switch o.Value.Kind {
		case ValueInt, ValueBool:
		case ValueFloat:
			if math.IsNaN(o.Value.Float) || math.IsInf(o.Value.Float, 0) {
				return invalid("observations_value")
			}
		case ValueString:
			if !isPrintable(o.Value.Str, 0, maxValueString) {
				return invalid("observations_value")
			}
		default:
			return invalid("observations_value")
		}
		if len(o.Dimensions) > MaxDimensions {
			return invalid("payload_count")
		}
		for j, d := range o.Dimensions {
			if !isIdentifier(d.Key, 1, maxIdentBytes) || !isIdentifier(d.Value, 1, maxIdentBytes) {
				return invalid("observations_text")
			}
			if j > 0 && o.Dimensions[j-1].Key >= d.Key {
				return invalid("observations_order")
			}
		}
		if !o.ObservedAt.Valid() || !o.ReceivedAt.Valid() || (o.ExpiresAt.Present && !o.ExpiresAt.Valid()) {
			return invalid("observations_time")
		}
		if _, ok := evidenceQualityOf(o.Quality); !ok {
			return invalid("observations_value")
		}
		if o.RawDigest != "" && !isIdentifier(o.RawDigest, 1, maxOptionalBytes) {
			return invalid("observations_text")
		}
		key := o.Subject.Key()
		if i > 0 && compareObservations(prevKey, key, &p.Observations[i-1], o) >= 0 {
			return invalid("observations_order")
		}
		prevKey = key
		if _, dup := ids[o.ID]; dup {
			return invalid("observations_id")
		}
		ids[o.ID] = struct{}{}
		if o.ID != ObservationID(fc.Session, f.Sequence, key, o.Signal) {
			return invalid("observations_id")
		}
	}
	return nil
}

// compareObservations orders two observations by subject key, signal, source
// type, source name, dimensions, observation time and ID. The dimensions are
// compared as sequences of (key, value) pairs, a proper prefix first.
func compareObservations(aKey, bKey string, a, b *Observation) int {
	if c := strings.Compare(aKey, bKey); c != 0 {
		return c
	}
	if c := strings.Compare(a.Signal, b.Signal); c != 0 {
		return c
	}
	if c := strings.Compare(a.Source.Type, b.Source.Type); c != 0 {
		return c
	}
	if c := strings.Compare(a.Source.Name, b.Source.Name); c != 0 {
		return c
	}
	for i := 0; i < len(a.Dimensions) && i < len(b.Dimensions); i++ {
		if c := strings.Compare(a.Dimensions[i].Key, b.Dimensions[i].Key); c != 0 {
			return c
		}
		if c := strings.Compare(a.Dimensions[i].Value, b.Dimensions[i].Value); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Dimensions) < len(b.Dimensions):
		return -1
	case len(a.Dimensions) > len(b.Dimensions):
		return 1
	}
	if c := a.ObservedAt.compare(b.ObservedAt); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// checkAllocation checks an allocation batch: the identity fields repeat those
// of the enclosing frame, the profile is a known one, the times are valid and
// ordered, and entries and evidence references are strictly increasing.
func checkAllocation(f *Frame, a *AllocationBatch) error {
	if a.NodeUID != f.NodeUID || a.BootID != f.BootID || a.Session != f.Session || a.Sequence != f.Sequence {
		return invalid("allocation_nested")
	}
	if a.Profile != AllocationProfileNVIDIAPodResourcesUUID && a.Profile != AllocationProfileUnsupported {
		return invalid("allocation_profile")
	}
	if !a.ObservedAt.Valid() || !a.ExpiresAt.Valid() || a.ExpiresAt.compare(a.ObservedAt) <= 0 {
		return invalid("allocation_time")
	}
	for i, ref := range a.EvidenceRefs {
		if !isIdentifier(ref, 1, maxIDBytes) {
			return invalid("allocation_text")
		}
		if i > 0 && a.EvidenceRefs[i-1] >= ref {
			return invalid("allocation_order")
		}
	}
	for i := range a.Entries {
		e := &a.Entries[i]
		if !isIdentifier(e.ResourceName, 1, maxIdentBytes) || !isIdentifier(e.DeviceID, 1, maxIdentBytes) ||
			!isIdentifier(e.PodNamespace, 1, maxIdentBytes) || !isIdentifier(e.PodName, 1, maxIdentBytes) ||
			!isIdentifier(e.ContainerName, 1, maxIdentBytes) {
			return invalid("allocation_text")
		}
		if i > 0 && compareEntries(&a.Entries[i-1], e) >= 0 {
			return invalid("allocation_order")
		}
	}
	return nil
}

// compareEntries orders allocation entries field by field in byte order, a
// shorter string before a longer one it prefixes.
func compareEntries(a, b *AllocationEntry) int {
	if c := strings.Compare(a.ResourceName, b.ResourceName); c != 0 {
		return c
	}
	if c := strings.Compare(a.DeviceID, b.DeviceID); c != 0 {
		return c
	}
	if c := strings.Compare(a.PodNamespace, b.PodNamespace); c != 0 {
		return c
	}
	if c := strings.Compare(a.PodName, b.PodName); c != 0 {
		return c
	}
	return strings.Compare(a.ContainerName, b.ContainerName)
}

// assetOf converts a neutral asset to a ratified asset reference.
func assetOf(a *Asset) (model.AssetRef, error) {
	kind, ok := assetKindOf(a.Kind)
	if !ok {
		return model.AssetRef{}, invalid("conversion_asset")
	}
	canonical, err := identity.ParseCanonical(a.Canonical)
	if err != nil {
		return model.AssetRef{}, invalid("conversion_asset")
	}
	aliases := make([]model.TypedID, 0, len(a.Aliases))
	for _, al := range a.Aliases {
		aliases = append(aliases, model.TypedID{Namespace: al.Namespace, Value: al.Value})
	}
	ref, err := model.NewAssetRef(kind, canonical, aliases...)
	if err != nil || ref.Validate() != nil {
		return model.AssetRef{}, invalid("conversion_asset")
	}
	return ref, nil
}

func valueOf(v Value) model.Value {
	switch v.Kind {
	case ValueInt:
		return model.NewIntValue(v.Int)
	case ValueFloat:
		return model.NewFloatValue(v.Float)
	case ValueBool:
		return model.NewBoolValue(v.Bool)
	default:
		return model.NewStringValue(v.Str)
	}
}

// convert maps a payload that passed checkShape to ratified values: the
// envelope, the assets and edges (which must form a resync of the node
// partition), the stamped provenance, the observations and the stamped
// bindings. Every ratified value must validate.
func convert(fc FrameContext, f *Frame, p *Payload, digestHex, revision string) (*Checked, error) {
	completeness := fleet.CompletenessComplete
	if f.Completeness == CompletenessPartial {
		completeness = fleet.CompletenessPartial
	}
	env := fleet.SnapshotEnvelope{
		NodeUID:        f.NodeUID,
		BootID:         f.BootID,
		PayloadDigest:  digestHex,
		BundleRevision: revision,
		Session:        f.Session,
		Sequence:       f.Sequence,
		Completeness:   completeness,
		ObservedAt:     f.ObservedAt.Time(),
	}
	if err := env.Validate(); err != nil {
		return nil, invalid("conversion_envelope")
	}

	nodeAsset := model.AssetRef{Kind: model.KindKubernetesNode, Canonical: model.NamespaceKubernetesNodeUID + ":" + fc.NodeUID}
	partition, err := graph.PartitionFor(nodeAsset)
	if err != nil {
		return nil, invalid("conversion_partition")
	}
	out := &Checked{
		Envelope:     env,
		Partition:    partition,
		NodeAsset:    nodeAsset,
		Assets:       make([]model.AssetRef, 0, len(p.Assets)),
		Edges:        make([]graph.Edge, 0, len(p.Edges)),
		Provenance:   make([]fleet.EdgeProvenance, 0, len(p.EdgeEvidence)),
		Observations: make([]model.Observation, 0, len(p.Observations)),
		Bindings:     make([]fleet.ObservedBinding, 0, len(p.GPUBindings)),
		Allocation:   cloneAllocation(p.Allocation),
	}

	byKey := make(map[string]model.AssetRef, len(p.Assets))
	for i := range p.Assets {
		ref, err := assetOf(&p.Assets[i])
		if err != nil {
			return nil, err
		}
		byKey[ref.Key()] = ref
		out.Assets = append(out.Assets, ref)
	}
	for i := range p.Edges {
		e := &p.Edges[i]
		from, okFrom := byKey[e.FromKey]
		to, okTo := byKey[e.ToKey]
		if !okFrom || !okTo {
			return nil, invalid("conversion_edge")
		}
		edge, err := graph.NewEdge(from, to, model.RelLocatedIn, model.OriginObserved)
		if err != nil {
			return nil, invalid("conversion_edge")
		}
		out.Edges = append(out.Edges, edge)
	}
	for i := range p.EdgeEvidence {
		ev := &p.EdgeEvidence[i]
		source := model.SourceRef{Type: ev.SourceType, Name: ev.SourceName}
		prov := fleet.EdgeProvenance{
			Edge:               out.Edges[i],
			EvidenceID:         ev.EvidenceID,
			BundleRevision:     revision,
			CollectorProfileID: StampProfile(fc.ProfileID, fc.Sources, fleet.TrustSysfsPhysicalParent, source),
			Kind:               fleet.EdgeEvidenceObserved,
			Source:             source,
			ObservedAt:         ev.ObservedAt.Time(),
			ExpiresAt:          ev.ExpiresAt.Time(),
		}
		if err := prov.Validate(); err != nil {
			return nil, invalid("conversion_provenance")
		}
		out.Provenance = append(out.Provenance, prov)
	}
	for i := range p.Observations {
		o := &p.Observations[i]
		subject, err := assetOf(&o.Subject)
		if err != nil {
			return nil, err
		}
		quality, _ := evidenceQualityOf(o.Quality)
		dims := make(map[string]string, len(o.Dimensions))
		for _, d := range o.Dimensions {
			dims[d.Key] = d.Value
		}
		var expires time.Time
		if o.ExpiresAt.Present {
			expires = o.ExpiresAt.Time()
		}
		obs, err := model.NewObservation(model.Observation{
			ID:         o.ID,
			Source:     model.SourceRef{Type: o.Source.Type, Name: o.Source.Name},
			Subject:    subject,
			Signal:     model.SignalRef(o.Signal),
			Value:      valueOf(o.Value),
			Unit:       o.Unit,
			Dimensions: dims,
			ObservedAt: o.ObservedAt.Time(),
			ReceivedAt: o.ReceivedAt.Time(),
			ExpiresAt:  expires,
			Sequence:   o.Sequence,
			Quality:    quality,
			RawDigest:  o.RawDigest,
		})
		if err != nil {
			return nil, invalid("conversion_observation")
		}
		out.Observations = append(out.Observations, obs)
	}
	node := fleet.NodeRef{ClusterID: fc.ClusterID, Name: fc.NodeName, UID: fc.NodeUID}
	for i := range p.GPUBindings {
		b := &p.GPUBindings[i]
		function, err := model.NewAssetRef(model.KindPCIeFunction, model.TypedID{Namespace: model.NamespacePCIBDF, Value: b.BDF})
		if err != nil {
			return nil, invalid("conversion_binding")
		}
		source := model.SourceRef{Type: b.SourceType, Name: b.SourceName}
		binding := fleet.ObservedBinding{
			Node:     node,
			BootID:   f.BootID,
			BDF:      b.BDF,
			Function: function,
			Claim: fleet.InventoryClaim{
				Vendor:     NVIDIAVendor,
				UUID:       b.UUID,
				Serial:     "",
				Source:     b.SourceName,
				EvidenceID: b.EvidenceID,
			},
			Source:             source,
			EvidenceID:         b.EvidenceID,
			BundleRevision:     revision,
			CollectorProfileID: StampProfile(fc.ProfileID, fc.Sources, fleet.TrustNVIDIAUUIDBinding, source),
			ObservedAt:         b.ObservedAt.Time(),
			ExpiresAt:          b.ExpiresAt.Time(),
		}
		if err := binding.Validate(); err != nil {
			return nil, invalid("conversion_binding")
		}
		out.Bindings = append(out.Bindings, binding)
	}
	if _, err := graph.NewResync(partition, f.Sequence, out.Assets, out.Edges); err != nil {
		return nil, invalid("conversion_resync")
	}
	return out, nil
}

// cloneAllocation returns a deep copy of a (nil for nil).
func cloneAllocation(a *AllocationBatch) *AllocationBatch {
	if a == nil {
		return nil
	}
	c := *a
	c.EvidenceRefs = append([]string(nil), a.EvidenceRefs...)
	c.Entries = append([]AllocationEntry(nil), a.Entries...)
	return &c
}
