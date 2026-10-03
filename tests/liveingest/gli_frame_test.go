package liveingest_test

// Shared frame helpers: the independent
// canonical bytes CP of GLI-061, the GFO-084 deterministic IDs and the frame
// builders. Nothing here may use the liveingest domain-core CP, digest or ID
// functions: the digest is part of the oracle, and the two normative vectors
// of GLI-061 (GLI-141) are reproduced by this file.
//
// Top-level identifiers of this file other than the four shared helpers
// (gliPayload, gliFrame, gliDigest, gliReseal) use the prefix gliFx.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

// Values of the GFX-102 BASE topology and the normative examples (GLI-141).
const (
	gliFxCluster   = "lab-a"
	gliFxNodeName  = "gpu-node-1"
	gliFxNodeUID   = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	gliFxBootID    = "3f1c2a9e-8d4b-4e0a-9b1f-2c6d5e7a8b90"
	gliFxProfile   = "live:default"
	gliFxUUID      = "GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d"
	gliFxParentSrc = "path-agent/sysfs-parent"
	gliFxWidthSrc  = "path-agent/sysfs-width"
	gliFxNVSrc     = "path-agent/nvidia-smi"
	gliFxProvAdj   = "adjacent_capability_min"
	gliFxTTL       = 300 * time.Second

	gliFxGPUBDF = "0000:03:00.0"
	gliFxNICBDF = "0000:04:00.0"
	gliFxRPABDF = "0000:00:01.0"
	gliFxRPBBDF = "0000:00:02.0"
	gliFxSWUBDF = "0000:01:00.0"
	gliFxSWDBDF = "0000:02:08.0"

	gliFxSigClass   = "pcie.function.class_code"
	gliFxSigCurrent = "pcie.link.width.current"
	gliFxSigExpect  = "pcie.link.width.expected"
)

// gliFxT0 is 2026-09-30T00:00:00Z (Unix 1790726400, the time of GLI-141).
var gliFxT0 = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

// gliFxIdent remembers the (node UID, boot ID) a payload was built for so that
// gliFrame, whose contract signature has no identity parameters, can fill the
// envelope. Payloads that are not registered (clones, hand-built vectors) fall
// back to the node asset canonical and gliFxBootID.
var gliFxIdent sync.Map // *ingestpb.HostSnapshotV1 -> [2]string{nodeUID, bootID}

// ---------------------------------------------------------------------------
// Keys and GFO-084 IDs.
// ---------------------------------------------------------------------------

func gliFxKey(kind, canonical string) string { return kind + "/" + canonical }

func gliFxFnKey(bdf string) string { return gliFxKey("PCIeFunction", "pci-bdf:"+bdf) }

func gliFxRPKey(bdf string) string { return gliFxKey("PCIeRootPort", "pci-bdf:"+bdf) }

func gliFxSWKey(bdf string) string { return gliFxKey("PCIeSwitch", "pci-bdf:"+bdf) }

func gliFxNodeKey(uid string) string { return gliFxKey("KubernetesNode", "kubernetes-node-uid:"+uid) }

func gliFxAssetKey(a *ingestpb.Asset) string { return gliFxKey(a.GetKind(), a.GetCanonical()) }

// gliFxID is GFO-084: <p>:<session>:<sequence>:<H> where H is the first 16
// bytes (lower-case hex) of SHA-256 over u32be(len)||bytes of every tuple
// element.
func gliFxID(prefix string, session int64, seq uint64, tuple ...string) string {
	var b []byte
	for _, s := range tuple {
		b = gliFxAppendStr(b, s)
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%s:%d:%d:%s", prefix, session, seq, hex.EncodeToString(sum[:16]))
}

func gliFxEdgeID(session int64, seq uint64, from, rel, to, origin string) string {
	return gliFxID("pe", session, seq, "dpa.edge-evidence.v1", from, rel, to, origin)
}

func gliFxObsID(session int64, seq uint64, subjectKey, signal string) string {
	return gliFxID("ob", session, seq, "dpa.observation.v1", subjectKey, signal)
}

func gliFxBindID(session int64, seq uint64, uuid, bdf string) string {
	return gliFxID("nb", session, seq, "dpa.gpu-binding.v1", uuid, bdf)
}

// ---------------------------------------------------------------------------
// Canonical bytes CP (GFO-086 with the allocationBatch extension of GLI-061).
// Every accessor is nil-safe: INVALID tests reseal malformed payloads.
// ---------------------------------------------------------------------------

func gliFxAppendStr(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

func gliFxAppendU32(b []byte, v uint32) []byte { return binary.BigEndian.AppendUint32(b, v) }

func gliFxAppendU64(b []byte, v uint64) []byte { return binary.BigEndian.AppendUint64(b, v) }

func gliFxAppendI64(b []byte, v int64) []byte { return binary.BigEndian.AppendUint64(b, uint64(v)) }

func gliFxAppendTime(b []byte, ts *timestamppb.Timestamp) []byte {
	b = gliFxAppendI64(b, ts.GetSeconds())
	return gliFxAppendU32(b, uint32(ts.GetNanos()))
}

func gliFxAppendOptTime(b []byte, ts *timestamppb.Timestamp) []byte {
	if ts == nil {
		return append(b, 0x00)
	}
	return gliFxAppendTime(append(b, 0x01), ts)
}

func gliFxAppendAsset(b []byte, kind, canonical string, aliases []*ingestpb.Alias) []byte {
	b = gliFxAppendStr(b, kind)
	b = gliFxAppendStr(b, canonical)
	b = gliFxAppendU32(b, uint32(len(aliases)))
	for _, a := range aliases {
		b = gliFxAppendStr(b, a.GetNamespace())
		b = gliFxAppendStr(b, a.GetValue())
	}
	return b
}

func gliFxAppendValue(b []byte, v *ingestpb.Value) []byte {
	switch x := v.GetV().(type) {
	case *ingestpb.Value_IntValue:
		b = gliFxAppendStr(b, "Int")
		return gliFxAppendI64(b, x.IntValue)
	case *ingestpb.Value_FloatValue:
		b = gliFxAppendStr(b, "Float")
		return gliFxAppendU64(b, math.Float64bits(x.FloatValue))
	case *ingestpb.Value_BoolValue:
		b = gliFxAppendStr(b, "Bool")
		if x.BoolValue {
			return append(b, 0x01)
		}
		return append(b, 0x00)
	case *ingestpb.Value_StringValue:
		b = gliFxAppendStr(b, "String")
		return gliFxAppendStr(b, x.StringValue)
	}
	return gliFxAppendStr(b, "")
}

// gliFxProfileLiteral is the CP literal of an allocation profile (GLI-061).
func gliFxProfileLiteral(p ingestpb.AllocationProfile) string {
	switch p {
	case ingestpb.AllocationProfile_NVIDIA_PODRESOURCES_UUID:
		return "NVIDIAPodResourcesUUID"
	case ingestpb.AllocationProfile_UNSUPPORTED:
		return "Unsupported"
	}
	return ""
}

func gliFxCPHeader() []byte { return gliFxAppendStr(nil, "dpa.HostSnapshotV1.canonical.v1") }

func gliFxCPAssets(p *ingestpb.HostSnapshotV1) []byte {
	b := gliFxAppendU32(nil, uint32(len(p.GetAssets())))
	for _, a := range p.GetAssets() {
		b = gliFxAppendAsset(b, a.GetKind(), a.GetCanonical(), a.GetAliases())
	}
	return b
}

func gliFxCPEdges(p *ingestpb.HostSnapshotV1) []byte {
	b := gliFxAppendU32(nil, uint32(len(p.GetEdges())))
	for _, e := range p.GetEdges() {
		b = gliFxAppendStr(b, e.GetFromKey())
		b = gliFxAppendStr(b, e.GetRelation())
		b = gliFxAppendStr(b, e.GetToKey())
		b = gliFxAppendStr(b, e.GetOrigin())
	}
	return b
}

func gliFxCPEdgeEvidence(p *ingestpb.HostSnapshotV1) []byte {
	b := gliFxAppendU32(nil, uint32(len(p.GetEdgeEvidence())))
	for _, ev := range p.GetEdgeEvidence() {
		b = gliFxAppendU32(b, ev.GetEdgeIndex())
		b = gliFxAppendStr(b, ev.GetKind())
		b = gliFxAppendStr(b, ev.GetSourceType())
		b = gliFxAppendStr(b, ev.GetSourceName())
		b = gliFxAppendTime(b, ev.GetObservedAt())
		b = gliFxAppendTime(b, ev.GetExpiresAt())
		b = gliFxAppendStr(b, ev.GetEvidenceId())
	}
	return b
}

func gliFxCPObservations(p *ingestpb.HostSnapshotV1) []byte {
	b := gliFxAppendU32(nil, uint32(len(p.GetObservations())))
	for _, o := range p.GetObservations() {
		b = gliFxAppendStr(b, o.GetId())
		b = gliFxAppendStr(b, o.GetSource().GetType())
		b = gliFxAppendStr(b, o.GetSource().GetName())
		b = gliFxAppendAsset(b, o.GetSubject().GetKind(), o.GetSubject().GetCanonical(), o.GetSubject().GetAliases())
		b = gliFxAppendStr(b, o.GetSignal())
		b = gliFxAppendValue(b, o.GetValue())
		b = gliFxAppendStr(b, o.GetUnit())
		b = gliFxAppendU32(b, uint32(len(o.GetDimensions())))
		for _, d := range o.GetDimensions() {
			b = gliFxAppendStr(b, d.GetKey())
			b = gliFxAppendStr(b, d.GetValue())
		}
		b = gliFxAppendTime(b, o.GetObservedAt())
		b = gliFxAppendTime(b, o.GetReceivedAt())
		b = gliFxAppendOptTime(b, o.GetExpiresAt())
		b = gliFxAppendU64(b, o.GetSequence())
		b = gliFxAppendStr(b, o.GetQuality())
		b = gliFxAppendStr(b, o.GetRawDigest())
	}
	return b
}

func gliFxCPBindings(p *ingestpb.HostSnapshotV1) []byte {
	b := gliFxAppendU32(nil, uint32(len(p.GetGpuBindings())))
	for _, g := range p.GetGpuBindings() {
		b = gliFxAppendStr(b, g.GetUuid())
		b = gliFxAppendStr(b, g.GetSerial())
		b = gliFxAppendStr(b, g.GetBdf())
		b = gliFxAppendStr(b, g.GetSourceType())
		b = gliFxAppendStr(b, g.GetSourceName())
		b = gliFxAppendStr(b, g.GetEvidenceId())
		b = gliFxAppendTime(b, g.GetObservedAt())
		b = gliFxAppendTime(b, g.GetExpiresAt())
	}
	return b
}

// gliFxCPAllocEntry is the CP of one allocation entry (GLI-061).
func gliFxCPAllocEntry(e *ingestpb.AllocationEntry) []byte {
	var b []byte
	b = gliFxAppendStr(b, e.GetResourceName())
	b = gliFxAppendStr(b, e.GetDeviceId())
	b = gliFxAppendStr(b, e.GetPodNamespace())
	b = gliFxAppendStr(b, e.GetPodName())
	return gliFxAppendStr(b, e.GetContainerName())
}

func gliFxCPAlloc(p *ingestpb.HostSnapshotV1) []byte {
	ab := p.GetAllocationBatch()
	if ab == nil {
		return []byte{0x00}
	}
	b := []byte{0x01}
	b = gliFxAppendStr(b, ab.GetNodeUid())
	b = gliFxAppendStr(b, ab.GetBootId())
	b = gliFxAppendI64(b, ab.GetSession())
	b = gliFxAppendU64(b, ab.GetSequence())
	b = gliFxAppendTime(b, ab.GetObservedAt())
	b = gliFxAppendTime(b, ab.GetExpiresAt())
	if ab.GetComplete() {
		b = append(b, 0x01)
	} else {
		b = append(b, 0x00)
	}
	b = gliFxAppendU32(b, uint32(len(ab.GetEvidenceRefs())))
	for _, r := range ab.GetEvidenceRefs() {
		b = gliFxAppendStr(b, r)
	}
	b = gliFxAppendStr(b, gliFxProfileLiteral(ab.GetProfile()))
	b = gliFxAppendU32(b, uint32(len(ab.GetEntries())))
	for _, e := range ab.GetEntries() {
		b = append(b, gliFxCPAllocEntry(e)...)
	}
	return b
}

// gliCanonical is CP: header || assets || edges || edgeEvidence || observations
// || gpuBindings || allocation tail, every list in wire order (GLI-061).
func gliCanonical(p *ingestpb.HostSnapshotV1) []byte {
	var b []byte
	b = append(b, gliFxCPHeader()...)
	b = append(b, gliFxCPAssets(p)...)
	b = append(b, gliFxCPEdges(p)...)
	b = append(b, gliFxCPEdgeEvidence(p)...)
	b = append(b, gliFxCPObservations(p)...)
	b = append(b, gliFxCPBindings(p)...)
	b = append(b, gliFxCPAlloc(p)...)
	return b
}

// gliDigest is the SHA-256 of CP.
func gliDigest(p *ingestpb.HostSnapshotV1) [32]byte { return sha256.Sum256(gliCanonical(p)) }

// gliFxRevision is the canonical bundle_revision of a frame (GLI-061).
func gliFxRevision(session int64, seq uint64, p *ingestpb.HostSnapshotV1) string {
	d := gliDigest(p)
	return fmt.Sprintf("%d:%d:%s", session, seq, hex.EncodeToString(d[:]))
}

// ---------------------------------------------------------------------------
// Payload and frame builders.
// ---------------------------------------------------------------------------

func gliFxTS(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }

func gliFxAsset(kind, canonical string) *ingestpb.Asset {
	return &ingestpb.Asset{Kind: kind, Canonical: canonical}
}

func gliFxInt(n int64) *ingestpb.Value {
	return &ingestpb.Value{V: &ingestpb.Value_IntValue{IntValue: n}}
}

func gliFxFloat(f float64) *ingestpb.Value {
	return &ingestpb.Value{V: &ingestpb.Value_FloatValue{FloatValue: f}}
}

func gliFxBool(v bool) *ingestpb.Value {
	return &ingestpb.Value{V: &ingestpb.Value_BoolValue{BoolValue: v}}
}

func gliFxStrVal(s string) *ingestpb.Value {
	return &ingestpb.Value{V: &ingestpb.Value_StringValue{StringValue: s}}
}

// gliFxDims builds the dimension list of key/value pairs in key order.
func gliFxDims(kv ...string) []*ingestpb.Dimension {
	var out []*ingestpb.Dimension
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, &ingestpb.Dimension{Key: kv[i], Value: kv[i+1]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// gliFxObservation builds one observation of a payload at frame time at.
func gliFxObservation(session int64, seq uint64, at time.Time, kind, canonical, signal, srcName string, v *ingestpb.Value, unit string, dims []*ingestpb.Dimension) *ingestpb.Observation {
	return &ingestpb.Observation{
		Id:         gliFxObsID(session, seq, gliFxKey(kind, canonical), signal),
		Source:     &ingestpb.SourceRef{Type: "agent", Name: srcName},
		Subject:    gliFxAsset(kind, canonical),
		Signal:     signal,
		Value:      v,
		Unit:       unit,
		Dimensions: dims,
		ObservedAt: gliFxTS(at),
		ReceivedAt: gliFxTS(at),
		Sequence:   seq,
		Quality:    "Good",
	}
}

// gliFxEdgeParts builds an observed LOCATED_IN edge and its edgeEvidence
// (edge_index 0; gliFxSortPayload renumbers).
func gliFxEdgeParts(session int64, seq uint64, at time.Time, from, to string) (*ingestpb.Edge, *ingestpb.EdgeEvidence) {
	e := &ingestpb.Edge{FromKey: from, Relation: "LOCATED_IN", ToKey: to, Origin: "Observed"}
	ev := &ingestpb.EdgeEvidence{
		Kind: "Observed", SourceType: "agent", SourceName: gliFxParentSrc,
		ObservedAt: gliFxTS(at), ExpiresAt: gliFxTS(at.Add(gliFxTTL)),
		EvidenceId: gliFxEdgeID(session, seq, from, "LOCATED_IN", to, "Observed"),
	}
	return e, ev
}

func gliFxBinding(session int64, seq uint64, at time.Time, uuid, bdf string) *ingestpb.GPUBinding {
	return &ingestpb.GPUBinding{
		Uuid: uuid, Bdf: bdf, SourceType: "agent", SourceName: gliFxNVSrc,
		EvidenceId: gliFxBindID(session, seq, uuid, bdf),
		ObservedAt: gliFxTS(at), ExpiresAt: gliFxTS(at.Add(gliFxTTL)),
	}
}

func gliFxCmpDims(a, b []*ingestpb.Dimension) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := strings.Compare(a[i].GetKey(), b[i].GetKey()); c != 0 {
			return c
		}
		if c := strings.Compare(a[i].GetValue(), b[i].GetValue()); c != 0 {
			return c
		}
	}
	return len(a) - len(b)
}

func gliFxCmpTS(a, b *timestamppb.Timestamp) int {
	if c := a.GetSeconds() - b.GetSeconds(); c != 0 {
		if c < 0 {
			return -1
		}
		return 1
	}
	return int(a.GetNanos()) - int(b.GetNanos())
}

func gliFxCmpObs(a, b *ingestpb.Observation) int {
	if c := strings.Compare(gliFxAssetKey(a.GetSubject()), gliFxAssetKey(b.GetSubject())); c != 0 {
		return c
	}
	if c := strings.Compare(a.GetSignal(), b.GetSignal()); c != 0 {
		return c
	}
	if c := strings.Compare(a.GetSource().GetType(), b.GetSource().GetType()); c != 0 {
		return c
	}
	if c := strings.Compare(a.GetSource().GetName(), b.GetSource().GetName()); c != 0 {
		return c
	}
	if c := gliFxCmpDims(a.GetDimensions(), b.GetDimensions()); c != 0 {
		return c
	}
	if c := gliFxCmpTS(a.GetObservedAt(), b.GetObservedAt()); c != 0 {
		return c
	}
	return strings.Compare(a.GetId(), b.GetId())
}

func gliFxCmpEdge(a, b *ingestpb.Edge) int {
	if c := strings.Compare(a.GetFromKey(), b.GetFromKey()); c != 0 {
		return c
	}
	if c := strings.Compare(a.GetToKey(), b.GetToKey()); c != 0 {
		return c
	}
	if c := strings.Compare(a.GetRelation(), b.GetRelation()); c != 0 {
		return c
	}
	return strings.Compare(a.GetOrigin(), b.GetOrigin())
}

// gliFxSortPayload restores the GFO-085 order of every collection and
// renumbers edge_index so each edgeEvidence stays with its edge.
func gliFxSortPayload(p *ingestpb.HostSnapshotV1) {
	sort.SliceStable(p.Assets, func(i, j int) bool { return gliFxAssetKey(p.Assets[i]) < gliFxAssetKey(p.Assets[j]) })
	type pair struct {
		e  *ingestpb.Edge
		ev []*ingestpb.EdgeEvidence
	}
	pairs := make([]pair, len(p.Edges))
	for i, e := range p.Edges {
		pairs[i].e = e
	}
	for _, ev := range p.EdgeEvidence {
		if int(ev.GetEdgeIndex()) < len(pairs) {
			pairs[ev.GetEdgeIndex()].ev = append(pairs[ev.GetEdgeIndex()].ev, ev)
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return gliFxCmpEdge(pairs[i].e, pairs[j].e) < 0 })
	edges := make([]*ingestpb.Edge, 0, len(pairs))
	var evs []*ingestpb.EdgeEvidence
	for i, pr := range pairs {
		edges = append(edges, pr.e)
		for _, ev := range pr.ev {
			ev.EdgeIndex = uint32(i)
			evs = append(evs, ev)
		}
	}
	p.Edges = edges
	p.EdgeEvidence = evs
	sort.SliceStable(p.Observations, func(i, j int) bool { return gliFxCmpObs(p.Observations[i], p.Observations[j]) < 0 })
	sort.SliceStable(p.GpuBindings, func(i, j int) bool {
		a, b := p.GpuBindings[i], p.GpuBindings[j]
		if a.GetBdf() != b.GetBdf() {
			return a.GetBdf() < b.GetBdf()
		}
		return a.GetUuid() < b.GetUuid()
	})
}

// gliPayload builds the minimal valid payload: the GFX-102 BASE topology (7
// assets, 6 edges with evidence, 6 observations, 1 gpuBinding) in GFO-085
// order. All times are at (expires_at = at + 300 s) and all IDs are GFO-084
// values for session and seq. The GPU width pair uses the live expected
// provenance adjacent_capability_min (live has no operator baseline).
func gliPayload(nodeUID, bootID string, session int64, seq uint64, at time.Time) *ingestpb.HostSnapshotV1 {
	p := &ingestpb.HostSnapshotV1{}
	nodeKey := gliFxNodeKey(nodeUID)
	p.Assets = []*ingestpb.Asset{
		gliFxAsset("KubernetesNode", "kubernetes-node-uid:"+nodeUID),
		gliFxAsset("PCIeFunction", "pci-bdf:"+gliFxGPUBDF),
		gliFxAsset("PCIeFunction", "pci-bdf:"+gliFxNICBDF),
		gliFxAsset("PCIeRootPort", "pci-bdf:"+gliFxRPABDF),
		gliFxAsset("PCIeRootPort", "pci-bdf:"+gliFxRPBBDF),
		gliFxAsset("PCIeSwitch", "pci-bdf:"+gliFxSWUBDF),
		gliFxAsset("PCIeSwitch", "pci-bdf:"+gliFxSWDBDF),
	}
	for i, pr := range [][2]string{
		{gliFxFnKey(gliFxGPUBDF), gliFxSWKey(gliFxSWDBDF)},
		{gliFxFnKey(gliFxNICBDF), gliFxRPKey(gliFxRPBBDF)},
		{gliFxRPKey(gliFxRPABDF), nodeKey},
		{gliFxRPKey(gliFxRPBBDF), nodeKey},
		{gliFxSWKey(gliFxSWUBDF), gliFxRPKey(gliFxRPABDF)},
		{gliFxSWKey(gliFxSWDBDF), gliFxSWKey(gliFxSWUBDF)},
	} {
		e, ev := gliFxEdgeParts(session, seq, at, pr[0], pr[1])
		ev.EdgeIndex = uint32(i)
		p.Edges = append(p.Edges, e)
		p.EdgeEvidence = append(p.EdgeEvidence, ev)
	}
	gpuDims := gliFxDims("pcie.expected.provenance", gliFxProvAdj, "pcie.peer.canonical", "pci-bdf:"+gliFxSWDBDF,
		"pcie.peer.kind", "PCIeSwitch", "pcie.root.canonical", "pci-bdf:"+gliFxRPABDF)
	nicDims := gliFxDims("pcie.expected.provenance", gliFxProvAdj, "pcie.peer.canonical", "pci-bdf:"+gliFxRPBBDF,
		"pcie.peer.kind", "PCIeRootPort", "pcie.root.canonical", "pci-bdf:"+gliFxRPBBDF)
	gpu, nic := "pci-bdf:"+gliFxGPUBDF, "pci-bdf:"+gliFxNICBDF
	p.Observations = []*ingestpb.Observation{
		gliFxObservation(session, seq, at, "PCIeFunction", gpu, gliFxSigClass, gliFxParentSrc, gliFxInt(197120), "pci_class", nil),
		gliFxObservation(session, seq, at, "PCIeFunction", gpu, gliFxSigCurrent, gliFxWidthSrc, gliFxInt(16), "lanes", gpuDims),
		gliFxObservation(session, seq, at, "PCIeFunction", gpu, gliFxSigExpect, gliFxWidthSrc, gliFxInt(16), "lanes", gpuDims),
		gliFxObservation(session, seq, at, "PCIeFunction", nic, gliFxSigClass, gliFxParentSrc, gliFxInt(131072), "pci_class", nil),
		gliFxObservation(session, seq, at, "PCIeFunction", nic, gliFxSigCurrent, gliFxWidthSrc, gliFxInt(16), "lanes", nicDims),
		gliFxObservation(session, seq, at, "PCIeFunction", nic, gliFxSigExpect, gliFxWidthSrc, gliFxInt(16), "lanes", nicDims),
	}
	for _, o := range p.Observations {
		o.ExpiresAt = gliFxTS(at.Add(gliFxTTL))
	}
	p.GpuBindings = []*ingestpb.GPUBinding{gliFxBinding(session, seq, at, gliFxUUID, gliFxGPUBDF)}
	gliFxSortPayload(p)
	gliFxIdent.Store(p, [2]string{nodeUID, bootID})
	return p
}

// gliFxIdentityOf returns the (node UID, boot ID) a frame for p carries.
func gliFxIdentityOf(p *ingestpb.HostSnapshotV1) (string, string) {
	if v, ok := gliFxIdent.Load(p); ok {
		id := v.([2]string)
		return id[0], id[1]
	}
	for _, a := range p.GetAssets() {
		if a.GetKind() == "KubernetesNode" {
			return strings.TrimPrefix(a.GetCanonical(), "kubernetes-node-uid:"), gliFxBootID
		}
	}
	return gliFxNodeUID, gliFxBootID
}

// gliFxClone deep-copies a payload and keeps its registered identity.
func gliFxClone(p *ingestpb.HostSnapshotV1) *ingestpb.HostSnapshotV1 {
	c := proto.Clone(p).(*ingestpb.HostSnapshotV1)
	if v, ok := gliFxIdent.Load(p); ok {
		gliFxIdent.Store(c, v)
	}
	return c
}

// gliFrame builds the SnapshotFrame of p: node_uid and boot_id come from the
// identity registered by gliPayload (or the node asset and gliFxBootID);
// payload_digest and bundle_revision are recomputed from CP (GLI-061).
func gliFrame(t *testing.T, p *ingestpb.HostSnapshotV1, session int64, seq uint64, at time.Time, complete bool) *ingestpb.SnapshotFrame {
	t.Helper()
	nodeUID, bootID := gliFxIdentityOf(p)
	c := ingestpb.Completeness_COMPLETE
	if !complete {
		c = ingestpb.Completeness_PARTIAL
	}
	f := &ingestpb.SnapshotFrame{
		NodeUid: nodeUID, BootId: bootID, Session: session, Sequence: seq,
		Completeness: c, ObservedAt: gliFxTS(at), Payload: p,
	}
	gliReseal(t, f)
	return f
}

// gliReseal recomputes payload_digest and bundle_revision after a mutation.
// It never changes IDs, times or any other field.
func gliReseal(t *testing.T, f *ingestpb.SnapshotFrame) {
	t.Helper()
	d := gliDigest(f.GetPayload())
	f.PayloadDigest = d[:]
	f.BundleRevision = fmt.Sprintf("%d:%d:%s", f.GetSession(), f.GetSequence(), hex.EncodeToString(d[:]))
}

// gliFxAddEdge adds an observed edge with its edgeEvidence and restores order.
func gliFxAddEdge(p *ingestpb.HostSnapshotV1, session int64, seq uint64, at time.Time, from, to string) {
	e, ev := gliFxEdgeParts(session, seq, at, from, to)
	ev.EdgeIndex = uint32(len(p.Edges))
	p.Edges = append(p.Edges, e)
	p.EdgeEvidence = append(p.EdgeEvidence, ev)
	gliFxSortPayload(p)
}

// gliFxFindObs returns the observation of subjectKey/signal or nil.
func gliFxFindObs(p *ingestpb.HostSnapshotV1, subjectKey, signal string) *ingestpb.Observation {
	for _, o := range p.GetObservations() {
		if gliFxAssetKey(o.GetSubject()) == subjectKey && o.GetSignal() == signal {
			return o
		}
	}
	return nil
}

// gliFxDropObs removes every observation of the given signal.
func gliFxDropObs(p *ingestpb.HostSnapshotV1, signal string) {
	var keep []*ingestpb.Observation
	for _, o := range p.Observations {
		if o.GetSignal() != signal {
			keep = append(keep, o)
		}
	}
	p.Observations = keep
}

func gliFxShift(ts *timestamppb.Timestamp, d time.Duration) *timestamppb.Timestamp {
	return timestamppb.New(ts.AsTime().Add(d))
}

// gliFxVector2Payload is the payload of the second normative CP vector
// (GLI-061, session 8, sequence 0), built field by field from the spec text.
func gliFxVector2Payload() *ingestpb.HostSnapshotV1 {
	at := gliFxT0
	exp := at.Add(gliFxTTL)
	const (
		session = 8
		boot    = "3f1c2a9e-8d4b-4e0a-9b1f-2c6d5e7a8b90"
		bdf     = "0000:03:00.0"
	)
	fn := "PCIeFunction/pci-bdf:" + bdf
	sw := "PCIeSwitch/pci-bdf:0000:02:08.0"
	peID := gliFxEdgeID(session, 0, fn, "LOCATED_IN", sw, "Observed")
	obID := gliFxObsID(session, 0, fn, "pcie.link.width.expected")
	nbID := gliFxBindID(session, 0, gliFxUUID, bdf)
	entry := func(pod string) *ingestpb.AllocationEntry {
		return &ingestpb.AllocationEntry{
			ResourceName: "nvidia.com/gpu", DeviceId: gliFxUUID, PodNamespace: "ml", PodName: pod, ContainerName: "main",
		}
	}
	return &ingestpb.HostSnapshotV1{
		Assets: []*ingestpb.Asset{
			gliFxAsset("KubernetesNode", "kubernetes-node-uid:"+gliFxNodeUID),
			gliFxAsset("PCIeFunction", "pci-bdf:"+bdf),
			gliFxAsset("PCIeSwitch", "pci-bdf:0000:02:08.0"),
		},
		Edges: []*ingestpb.Edge{{FromKey: fn, Relation: "LOCATED_IN", ToKey: sw, Origin: "Observed"}},
		EdgeEvidence: []*ingestpb.EdgeEvidence{{
			EdgeIndex: 0, Kind: "Observed", SourceType: "agent", SourceName: gliFxParentSrc,
			ObservedAt: gliFxTS(at), ExpiresAt: gliFxTS(exp), EvidenceId: peID,
		}},
		Observations: []*ingestpb.Observation{{
			Id: obID, Source: &ingestpb.SourceRef{Type: "agent", Name: gliFxWidthSrc},
			Subject: gliFxAsset("PCIeFunction", "pci-bdf:"+bdf), Signal: "pcie.link.width.expected",
			Value: gliFxInt(16), Unit: "lanes",
			Dimensions: gliFxDims("pcie.expected.provenance", gliFxProvAdj),
			ObservedAt: gliFxTS(at), ReceivedAt: gliFxTS(at), Sequence: 0, Quality: "Good",
		}},
		GpuBindings: []*ingestpb.GPUBinding{{
			Uuid: gliFxUUID, Bdf: bdf, SourceType: "agent", SourceName: gliFxNVSrc, EvidenceId: nbID,
			ObservedAt: gliFxTS(at), ExpiresAt: gliFxTS(exp),
		}},
		AllocationBatch: &ingestpb.AllocationBatch{
			NodeUid: gliFxNodeUID, BootId: boot, Session: session, Sequence: 0,
			ObservedAt: gliFxTS(at), ExpiresAt: gliFxTS(exp), Complete: true,
			EvidenceRefs: []string{nbID, obID},
			Profile:      ingestpb.AllocationProfile_NVIDIA_PODRESOURCES_UUID,
			Entries:      []*ingestpb.AllocationEntry{entry("b-long-name"), entry("z")},
		},
	}
}

// ---------------------------------------------------------------------------
// Tests: the CP encoder reproduces the normative vectors (GLI-061, GLI-141).
// ---------------------------------------------------------------------------

func gliFxHex(b []byte) string { return hex.EncodeToString(b) }

// GLI-061 vector 1: a payload with only the KubernetesNode asset has the
// 138-byte CP of the spec and its SHA-256.
func TestGLI061_CPVector1MinimalPayload(t *testing.T) {
	p := &ingestpb.HostSnapshotV1{Assets: []*ingestpb.Asset{gliFxAsset("KubernetesNode", "kubernetes-node-uid:"+gliFxNodeUID)}}
	cp := gliCanonical(p)
	if len(cp) != 138 {
		t.Fatalf("GLI-061 vector 1: CP is %d bytes, want 138", len(cp))
	}
	if got := gliFxHex(cp); got != gliCPVec1Hex {
		t.Fatalf("GLI-061 vector 1: CP hex\n got %s\nwant %s", got, gliCPVec1Hex)
	}
	d := gliDigest(p)
	if got := gliFxHex(d[:]); got != gliCPVec1SHA {
		t.Fatalf("GLI-061 vector 1: SHA-256 %s, want %s", got, gliCPVec1SHA)
	}
	if cp[len(cp)-1] != 0x00 {
		t.Errorf("GLI-061: payload without allocationBatch must end with the single 0x00 byte")
	}
}

// GLI-061 vector 2: every list, time, optional time, value kind and the
// allocationBatch extension, section by section, then the whole CP, the
// bundle_revision, and the same payload without allocationBatch (which must
// give the digest of the same-content GFO artifact).
func TestGLI061_CPVector2AllSections(t *testing.T) {
	p := gliFxVector2Payload()
	sections := []struct {
		name string
		got  []byte
		want string
		size int
	}{
		{"header", gliFxCPHeader(), gliCPVec2Header, 35},
		{"assets", gliFxCPAssets(p), gliCPVec2Assets, 172},
		{"edges", gliFxCPEdges(p), gliCPVec2Edges, 102},
		{"edgeEvidence", gliFxCPEdgeEvidence(p), gliCPVec2EdgeEvidence, 123},
		{"observations", gliFxCPObservations(p), gliCPVec2Observations, 282},
		{"gpuBindings", gliFxCPBindings(p), gliCPVec2GPUBindings, 169},
		{"allocationBatch", gliFxCPAlloc(p), gliCPVec2Alloc, 414},
	}
	for _, s := range sections {
		if len(s.got) != s.size {
			t.Errorf("GLI-061 vector 2 %s: %d bytes, want %d", s.name, len(s.got), s.size)
		}
		if gliFxHex(s.got) != s.want {
			t.Errorf("GLI-061 vector 2 %s: hex\n got %s\nwant %s", s.name, gliFxHex(s.got), s.want)
		}
	}
	cp := gliCanonical(p)
	if len(cp) != 1297 {
		t.Fatalf("GLI-061 vector 2: CP is %d bytes, want 1297", len(cp))
	}
	d := gliDigest(p)
	if got := gliFxHex(d[:]); got != gliCPVec2SHA {
		t.Fatalf("GLI-061 vector 2: SHA-256 %s, want %s", got, gliCPVec2SHA)
	}
	f := gliFrame(t, p, 8, 0, gliFxT0, true)
	if f.GetBundleRevision() != gliCPVec2Rev {
		t.Errorf("GLI-061 vector 2: bundle_revision %q, want %q", f.GetBundleRevision(), gliCPVec2Rev)
	}
	if got := gliFxHex(f.GetPayloadDigest()); got != gliCPVec2SHA || len(f.GetPayloadDigest()) != 32 {
		t.Errorf("GLI-061 vector 2: frame payload_digest %s (%d bytes), want %s (32 bytes)", got, len(f.GetPayloadDigest()), gliCPVec2SHA)
	}

	noAlloc := gliFxClone(p)
	noAlloc.AllocationBatch = nil
	cp2 := gliCanonical(noAlloc)
	if len(cp2) != 884 {
		t.Fatalf("GLI-061 vector 2 without allocationBatch: CP is %d bytes, want 884", len(cp2))
	}
	if cp2[len(cp2)-1] != 0x00 {
		t.Errorf("GLI-061: the allocationBatch tail of an absent batch is the single 0x00 byte")
	}
	d2 := gliDigest(noAlloc)
	if got := gliFxHex(d2[:]); got != gliCPVec2NoAllocSHA {
		t.Errorf("GLI-061 vector 2 without allocationBatch: SHA-256 %s, want %s", got, gliCPVec2NoAllocSHA)
	}
}

// GLI-061: the allocation entries are encoded in wire order. Sorting by the
// encoded bytes would put the one-byte pod name z before b-long-name because
// of the length prefix; the CP must differ from that order and the spec order
// (field-wise bytes) is the one of the normative digest.
func TestGLI061_CPAllocationEntriesWireOrder(t *testing.T) {
	p := gliFxVector2Payload()
	entries := p.AllocationBatch.Entries
	if entries[0].GetPodName() != "b-long-name" || entries[1].GetPodName() != "z" {
		t.Fatalf("test bug: vector 2 entries must be b-long-name, z")
	}
	encB, encZ := gliFxCPAllocEntry(entries[0]), gliFxCPAllocEntry(entries[1])
	if string(encZ) >= string(encB) {
		t.Fatalf("test bug: the encoded z entry must sort before the encoded b-long-name entry (length prefix)")
	}
	swapped := gliFxClone(p)
	swapped.AllocationBatch.Entries = []*ingestpb.AllocationEntry{entries[1], entries[0]}
	d1, d2 := gliDigest(p), gliDigest(swapped)
	if d1 == d2 {
		t.Fatalf("GLI-061: swapping the two allocation entries must change the digest (wire order is encoded as is)")
	}
	if gliFxHex(d1[:]) != gliCPVec2SHA {
		t.Fatalf("GLI-061: wire-order digest %s, want the normative %s", gliFxHex(d1[:]), gliCPVec2SHA)
	}
}

// GLI-061, GFO-084: the three IDs of vector 2 and the tuple structure.
func TestGLI061_IDVectors(t *testing.T) {
	fn := "PCIeFunction/pci-bdf:0000:03:00.0"
	sw := "PCIeSwitch/pci-bdf:0000:02:08.0"
	cases := []struct{ name, got, want string }{
		{"edgeEvidence", gliFxEdgeID(8, 0, fn, "LOCATED_IN", sw, "Observed"), "pe:8:0:38a7e6e62e404a118d0aaeebd8bf6bc1"},
		{"observation", gliFxObsID(8, 0, fn, "pcie.link.width.expected"), "ob:8:0:c5af55f110231ddca097bd28f1c39d1d"},
		{"gpuBinding", gliFxBindID(8, 0, gliFxUUID, "0000:03:00.0"), "nb:8:0:a0530a76d007ffbc31f72d5eea2b1ffc"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("GFO-084 %s ID = %s, want %s", c.name, c.got, c.want)
		}
	}
	// The hash part depends on the tuple only; session and sequence are in the prefix.
	a := gliFxObsID(8, 0, fn, "pcie.link.width.expected")
	b := gliFxObsID(9, 3, fn, "pcie.link.width.expected")
	if strings.TrimPrefix(a, "ob:8:0:") != strings.TrimPrefix(b, "ob:9:3:") {
		t.Errorf("GFO-084: H must not depend on session and sequence: %s vs %s", a, b)
	}
	if gliFxObsID(8, 0, fn, "pcie.link.width.current") == a {
		t.Errorf("GFO-084: the signal must be part of the observation ID tuple")
	}
	if gliFxObsID(8, 0, sw, "pcie.link.width.expected") == a {
		t.Errorf("GFO-084: the subject key must be part of the observation ID tuple")
	}
}

// GLI-061: CP is sensitive to every field class. Each mutation of the vector 2
// payload must change the digest; unknown fields never do (CP is built from the
// decoded values).
func TestGLI061_CPFieldSensitivity(t *testing.T) {
	base := gliDigest(gliFxVector2Payload())
	muts := []struct {
		name string
		mut  func(p *ingestpb.HostSnapshotV1)
	}{
		{"asset alias", func(p *ingestpb.HostSnapshotV1) {
			p.Assets[1].Aliases = []*ingestpb.Alias{{Namespace: "lldp-port-id", Value: "p1"}}
		}},
		{"asset canonical", func(p *ingestpb.HostSnapshotV1) { p.Assets[1].Canonical = "pci-bdf:0000:03:00.1" }},
		{"edge origin", func(p *ingestpb.HostSnapshotV1) { p.Edges[0].Origin = "Intended" }},
		{"edgeEvidence index", func(p *ingestpb.HostSnapshotV1) { p.EdgeEvidence[0].EdgeIndex = 1 }},
		{"edgeEvidence observed nanos", func(p *ingestpb.HostSnapshotV1) { p.EdgeEvidence[0].ObservedAt.Nanos = 1 }},
		{"edgeEvidence expires seconds", func(p *ingestpb.HostSnapshotV1) { p.EdgeEvidence[0].ExpiresAt.Seconds++ }},
		{"observation value kind", func(p *ingestpb.HostSnapshotV1) { p.Observations[0].Value = gliFxFloat(16) }},
		{"observation value", func(p *ingestpb.HostSnapshotV1) { p.Observations[0].Value = gliFxInt(8) }},
		{"observation bool value", func(p *ingestpb.HostSnapshotV1) { p.Observations[0].Value = gliFxBool(true) }},
		{"observation string value", func(p *ingestpb.HostSnapshotV1) { p.Observations[0].Value = gliFxStrVal("16") }},
		{"observation unit", func(p *ingestpb.HostSnapshotV1) { p.Observations[0].Unit = "pci_class" }},
		{"observation dimension value", func(p *ingestpb.HostSnapshotV1) { p.Observations[0].Dimensions[0].Value = "operator_verified_wiring" }},
		{"observation extra dimension", func(p *ingestpb.HostSnapshotV1) {
			p.Observations[0].Dimensions = append(p.Observations[0].Dimensions, &ingestpb.Dimension{Key: "z", Value: "1"})
		}},
		{"observation expires present", func(p *ingestpb.HostSnapshotV1) { p.Observations[0].ExpiresAt = gliFxTS(gliFxT0.Add(gliFxTTL)) }},
		{"observation sequence", func(p *ingestpb.HostSnapshotV1) { p.Observations[0].Sequence = 1 }},
		{"observation quality", func(p *ingestpb.HostSnapshotV1) { p.Observations[0].Quality = "Degraded" }},
		{"observation raw digest", func(p *ingestpb.HostSnapshotV1) { p.Observations[0].RawDigest = "abc" }},
		{"binding serial", func(p *ingestpb.HostSnapshotV1) { p.GpuBindings[0].Serial = "S1" }},
		{"binding bdf", func(p *ingestpb.HostSnapshotV1) { p.GpuBindings[0].Bdf = "0000:04:00.0" }},
		{"allocation absent", func(p *ingestpb.HostSnapshotV1) { p.AllocationBatch = nil }},
		{"allocation complete", func(p *ingestpb.HostSnapshotV1) { p.AllocationBatch.Complete = false }},
		{"allocation profile", func(p *ingestpb.HostSnapshotV1) {
			p.AllocationBatch.Profile = ingestpb.AllocationProfile_UNSUPPORTED
		}},
		{"allocation evidence ref", func(p *ingestpb.HostSnapshotV1) { p.AllocationBatch.EvidenceRefs = p.AllocationBatch.EvidenceRefs[:1] }},
		{"allocation entry container", func(p *ingestpb.HostSnapshotV1) { p.AllocationBatch.Entries[1].ContainerName = "side" }},
		{"allocation session", func(p *ingestpb.HostSnapshotV1) { p.AllocationBatch.Session = 9 }},
		{"allocation expires", func(p *ingestpb.HostSnapshotV1) { p.AllocationBatch.ExpiresAt.Nanos = 1 }},
	}
	for _, m := range muts {
		t.Run(m.name, func(t *testing.T) {
			p := gliFxVector2Payload()
			m.mut(p)
			if gliDigest(p) == base {
				t.Fatalf("GLI-061: mutation %q did not change the digest", m.name)
			}
		})
	}
	t.Run("unknown fields are not digest input", func(t *testing.T) {
		p := gliFxVector2Payload()
		p.ProtoReflect().SetUnknown([]byte{0xf8, 0x06, 0x01}) // field 111, varint 1
		p.Assets[0].ProtoReflect().SetUnknown([]byte{0xf8, 0x06, 0x01})
		if gliDigest(p) != base {
			t.Fatalf("GLI-061: unknown fields changed the digest; CP is built from decoded values only")
		}
	})
}

// gliReseal changes exactly digest and revision, consistently (shared helper).
func TestGLI061_GliResealRecomputesDigestAndRevision(t *testing.T) {
	p := gliPayload(gliFxNodeUID, gliFxBootID, 3, 5, gliFxT0)
	f := gliFrame(t, p, 3, 5, gliFxT0, true)
	oldDigest := append([]byte(nil), f.GetPayloadDigest()...)
	oldRev := f.GetBundleRevision()
	if len(oldDigest) != 32 {
		t.Fatalf("gliFrame digest is %d bytes, want 32", len(oldDigest))
	}
	if want := fmt.Sprintf("3:5:%s", gliFxHex(oldDigest)); oldRev != want {
		t.Fatalf("gliFrame revision %q, want %q", oldRev, want)
	}
	if len(oldRev) > 128 {
		t.Fatalf("revision %q is longer than 128 bytes", oldRev)
	}
	if f.GetNodeUid() != gliFxNodeUID || f.GetBootId() != gliFxBootID {
		t.Fatalf("gliFrame identity %q/%q, want %q/%q", f.GetNodeUid(), f.GetBootId(), gliFxNodeUID, gliFxBootID)
	}
	idsBefore := []string{p.Observations[0].GetId(), p.EdgeEvidence[0].GetEvidenceId(), p.GpuBindings[0].GetEvidenceId()}
	p.Observations[1].Value = gliFxInt(8)
	gliReseal(t, f)
	if string(f.GetPayloadDigest()) == string(oldDigest) {
		t.Fatalf("gliReseal kept the old digest after a payload change")
	}
	d := gliDigest(p)
	if string(f.GetPayloadDigest()) != string(d[:]) {
		t.Fatalf("gliReseal digest is not the CP digest of the payload")
	}
	if f.GetBundleRevision() == oldRev || f.GetBundleRevision() != fmt.Sprintf("3:5:%s", gliFxHex(d[:])) {
		t.Fatalf("gliReseal revision %q inconsistent with the new digest", f.GetBundleRevision())
	}
	idsAfter := []string{p.Observations[0].GetId(), p.EdgeEvidence[0].GetEvidenceId(), p.GpuBindings[0].GetEvidenceId()}
	for i := range idsBefore {
		if idsBefore[i] != idsAfter[i] {
			t.Fatalf("gliReseal must not touch IDs")
		}
	}
	// A payload that was not built by gliPayload still gets a frame for its node.
	q := gliFxClone(p)
	g := gliFrame(t, q, 3, 6, gliFxT0, false)
	if g.GetCompleteness() != ingestpb.Completeness_PARTIAL || g.GetNodeUid() != gliFxNodeUID || g.GetBootId() != gliFxBootID {
		t.Fatalf("gliFrame(partial) = %s %q %q", g.GetCompleteness(), g.GetNodeUid(), g.GetBootId())
	}
}

// gliPayload is a valid GFO-085-ordered payload: the order helpers are
// idempotent on it and the observation and edge lists have the BASE sizes.
func TestGLI060_BasePayloadShapeAndOrder(t *testing.T) {
	p := gliPayload(gliFxNodeUID, gliFxBootID, 1, 0, gliFxT0)
	if len(p.Assets) != 7 || len(p.Edges) != 6 || len(p.EdgeEvidence) != 6 || len(p.Observations) != 6 || len(p.GpuBindings) != 1 {
		t.Fatalf("BASE payload sizes assets/edges/evidence/observations/bindings = %d/%d/%d/%d/%d, want 7/6/6/6/1",
			len(p.Assets), len(p.Edges), len(p.EdgeEvidence), len(p.Observations), len(p.GpuBindings))
	}
	before := gliCanonical(p)
	q := gliFxClone(p)
	gliFxSortPayload(q)
	if string(gliCanonical(q)) != string(before) {
		t.Fatalf("gliPayload is not in GFO-085 order (a second sort changed it)")
	}
	for i, ev := range p.EdgeEvidence {
		if int(ev.GetEdgeIndex()) != i {
			t.Errorf("edgeEvidence[%d].edge_index = %d, want %d", i, ev.GetEdgeIndex(), i)
		}
		e := p.Edges[i]
		if want := gliFxEdgeID(1, 0, e.GetFromKey(), e.GetRelation(), e.GetToKey(), e.GetOrigin()); ev.GetEvidenceId() != want {
			t.Errorf("edgeEvidence[%d] id %q, want %q", i, ev.GetEvidenceId(), want)
		}
	}
	for _, o := range p.Observations {
		if o.GetId() != gliFxObsID(1, 0, gliFxAssetKey(o.GetSubject()), o.GetSignal()) {
			t.Errorf("observation %s has a wrong GFO-084 id", o.GetId())
		}
		if o.GetExpiresAt().AsTime().Sub(o.GetObservedAt().AsTime()) != gliFxTTL {
			t.Errorf("observation %s expires_at is not observed_at + 300 s", o.GetId())
		}
	}
	// Adding an edge keeps edge i and evidence i together and in order.
	gliFxAddEdge(q, 1, 0, gliFxT0, gliFxFnKey(gliFxGPUBDF), gliFxSWKey(gliFxSWUBDF))
	if len(q.Edges) != 7 || len(q.EdgeEvidence) != 7 {
		t.Fatalf("after gliFxAddEdge: %d edges and %d evidence entries, want 7 and 7", len(q.Edges), len(q.EdgeEvidence))
	}
	for i := range q.Edges {
		if i > 0 && gliFxCmpEdge(q.Edges[i-1], q.Edges[i]) >= 0 {
			t.Errorf("edges not strictly increasing at %d", i)
		}
		if int(q.EdgeEvidence[i].GetEdgeIndex()) != i {
			t.Errorf("evidence %d has edge_index %d", i, q.EdgeEvidence[i].GetEdgeIndex())
		}
		e := q.Edges[i]
		if want := gliFxEdgeID(1, 0, e.GetFromKey(), e.GetRelation(), e.GetToKey(), e.GetOrigin()); q.EdgeEvidence[i].GetEvidenceId() != want {
			t.Errorf("evidence %d is not the evidence of edge %d", i, i)
		}
	}
}
