package ingest

import (
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

// The wire enums and the raw values the neutral types carry must be the same
// numbers. Each pair is checked in both directions at compile time: a
// difference in either direction makes a constant negative, which does not
// compile.
const (
	_ = uint(int32(ingestpb.Completeness_COMPLETENESS_UNSPECIFIED) - liveingest.CompletenessUnspecified)
	_ = uint(liveingest.CompletenessUnspecified - int32(ingestpb.Completeness_COMPLETENESS_UNSPECIFIED))
	_ = uint(int32(ingestpb.Completeness_COMPLETE) - liveingest.CompletenessComplete)
	_ = uint(liveingest.CompletenessComplete - int32(ingestpb.Completeness_COMPLETE))
	_ = uint(int32(ingestpb.Completeness_PARTIAL) - liveingest.CompletenessPartial)
	_ = uint(liveingest.CompletenessPartial - int32(ingestpb.Completeness_PARTIAL))

	_ = uint(int32(ingestpb.AllocationProfile_ALLOCATION_PROFILE_UNSPECIFIED) - liveingest.AllocationProfileUnspecified)
	_ = uint(liveingest.AllocationProfileUnspecified - int32(ingestpb.AllocationProfile_ALLOCATION_PROFILE_UNSPECIFIED))
	_ = uint(int32(ingestpb.AllocationProfile_NVIDIA_PODRESOURCES_UUID) - liveingest.AllocationProfileNVIDIAPodResourcesUUID)
	_ = uint(liveingest.AllocationProfileNVIDIAPodResourcesUUID - int32(ingestpb.AllocationProfile_NVIDIA_PODRESOURCES_UUID))
	_ = uint(int32(ingestpb.AllocationProfile_UNSUPPORTED) - liveingest.AllocationProfileUnsupported)
	_ = uint(liveingest.AllocationProfileUnsupported - int32(ingestpb.AllocationProfile_UNSUPPORTED))
)

// hasUnknown reports whether the message holds unknown fields, preserved by
// the decoder. A nil message holds none.
func hasUnknown(m protoreflect.ProtoMessage) bool {
	return m != nil && len(m.ProtoReflect().GetUnknown()) > 0
}

func timestampOf(t *timestamppb.Timestamp) liveingest.Timestamp {
	if t == nil {
		return liveingest.Timestamp{}
	}
	return liveingest.Timestamp{Present: true, Seconds: t.GetSeconds(), Nanos: t.GetNanos()}
}

// frameFromProto copies a snapshot frame to the wire-neutral type. It is a
// field copy: it interprets nothing and cannot fail (the checks are the
// engine's). cf is the ClientFrame that carried sf.
func frameFromProto(cf *ingestpb.ClientFrame, sf *ingestpb.SnapshotFrame) *liveingest.Frame {
	f := &liveingest.Frame{
		NodeUID:        sf.GetNodeUid(),
		BootID:         sf.GetBootId(),
		Session:        sf.GetSession(),
		Sequence:       sf.GetSequence(),
		Completeness:   int32(sf.GetCompleteness()),
		ObservedAt:     timestampOf(sf.GetObservedAt()),
		PayloadDigest:  sf.GetPayloadDigest(),
		BundleRevision: sf.GetBundleRevision(),
		// The envelope is everything of the frame outside the payload,
		// including the observation time message.
		UnknownFields: hasUnknown(cf) || hasUnknown(sf) || hasUnknown(sf.GetObservedAt()),
	}
	if p := sf.GetPayload(); p != nil {
		f.Payload = payloadFromProto(p)
	}
	return f
}

func payloadFromProto(p *ingestpb.HostSnapshotV1) *liveingest.Payload {
	out := &liveingest.Payload{UnknownFields: hasUnknown(p)}
	if n := len(p.GetAssets()); n > 0 {
		out.Assets = make([]liveingest.Asset, n)
		for i, a := range p.GetAssets() {
			out.Assets[i] = assetFromProto(a, &out.UnknownFields)
		}
	}
	if n := len(p.GetEdges()); n > 0 {
		out.Edges = make([]liveingest.Edge, n)
		for i, e := range p.GetEdges() {
			out.Edges[i] = liveingest.Edge{FromKey: e.GetFromKey(), Relation: e.GetRelation(), ToKey: e.GetToKey(), Origin: e.GetOrigin()}
			markUnknown(&out.UnknownFields, e)
		}
	}
	if n := len(p.GetEdgeEvidence()); n > 0 {
		out.EdgeEvidence = make([]liveingest.EdgeEvidence, n)
		for i, ev := range p.GetEdgeEvidence() {
			out.EdgeEvidence[i] = liveingest.EdgeEvidence{
				EdgeIndex:  ev.GetEdgeIndex(),
				Kind:       ev.GetKind(),
				SourceType: ev.GetSourceType(),
				SourceName: ev.GetSourceName(),
				ObservedAt: timestampOf(ev.GetObservedAt()),
				ExpiresAt:  timestampOf(ev.GetExpiresAt()),
				EvidenceID: ev.GetEvidenceId(),
			}
			markUnknown(&out.UnknownFields, ev, ev.GetObservedAt(), ev.GetExpiresAt())
		}
	}
	if n := len(p.GetObservations()); n > 0 {
		out.Observations = make([]liveingest.Observation, n)
		for i, o := range p.GetObservations() {
			out.Observations[i] = observationFromProto(o, &out.UnknownFields)
		}
	}
	if n := len(p.GetGpuBindings()); n > 0 {
		out.GPUBindings = make([]liveingest.GPUBinding, n)
		for i, b := range p.GetGpuBindings() {
			out.GPUBindings[i] = liveingest.GPUBinding{
				UUID:       b.GetUuid(),
				Serial:     b.GetSerial(),
				BDF:        b.GetBdf(),
				SourceType: b.GetSourceType(),
				SourceName: b.GetSourceName(),
				EvidenceID: b.GetEvidenceId(),
				ObservedAt: timestampOf(b.GetObservedAt()),
				ExpiresAt:  timestampOf(b.GetExpiresAt()),
			}
			markUnknown(&out.UnknownFields, b, b.GetObservedAt(), b.GetExpiresAt())
		}
	}
	if ab := p.GetAllocationBatch(); ab != nil {
		out.Allocation = allocationFromProto(ab, &out.UnknownFields)
	}
	return out
}

// markUnknown sets *flag when any of the messages holds unknown fields.
func markUnknown(flag *bool, msgs ...protoreflect.ProtoMessage) {
	if *flag {
		return
	}
	for _, m := range msgs {
		if hasUnknown(m) {
			*flag = true
			return
		}
	}
}

func assetFromProto(a *ingestpb.Asset, unknown *bool) liveingest.Asset {
	out := liveingest.Asset{Kind: a.GetKind(), Canonical: a.GetCanonical()}
	markUnknown(unknown, a)
	if n := len(a.GetAliases()); n > 0 {
		out.Aliases = make([]liveingest.Alias, n)
		for i, al := range a.GetAliases() {
			out.Aliases[i] = liveingest.Alias{Namespace: al.GetNamespace(), Value: al.GetValue()}
			markUnknown(unknown, al)
		}
	}
	return out
}

func observationFromProto(o *ingestpb.Observation, unknown *bool) liveingest.Observation {
	out := liveingest.Observation{
		ID:         o.GetId(),
		Signal:     o.GetSignal(),
		Unit:       o.GetUnit(),
		ObservedAt: timestampOf(o.GetObservedAt()),
		ReceivedAt: timestampOf(o.GetReceivedAt()),
		ExpiresAt:  timestampOf(o.GetExpiresAt()),
		Sequence:   o.GetSequence(),
		Quality:    o.GetQuality(),
		RawDigest:  o.GetRawDigest(),
	}
	markUnknown(unknown, o, o.GetSource(), o.GetValue(), o.GetObservedAt(), o.GetReceivedAt(), o.GetExpiresAt())
	if src := o.GetSource(); src != nil {
		out.Source = liveingest.SourceRef{Type: src.GetType(), Name: src.GetName()}
	}
	if subject := o.GetSubject(); subject != nil {
		out.Subject = assetFromProto(subject, unknown)
	}
	if v := o.GetValue(); v != nil {
		switch x := v.GetV().(type) {
		case *ingestpb.Value_IntValue:
			out.Value = liveingest.Value{Kind: liveingest.ValueInt, Int: x.IntValue}
		case *ingestpb.Value_FloatValue:
			out.Value = liveingest.Value{Kind: liveingest.ValueFloat, Float: x.FloatValue}
		case *ingestpb.Value_BoolValue:
			out.Value = liveingest.Value{Kind: liveingest.ValueBool, Bool: x.BoolValue}
		case *ingestpb.Value_StringValue:
			out.Value = liveingest.Value{Kind: liveingest.ValueString, Str: x.StringValue}
		}
	}
	if n := len(o.GetDimensions()); n > 0 {
		out.Dimensions = make([]liveingest.Dimension, n)
		for i, d := range o.GetDimensions() {
			out.Dimensions[i] = liveingest.Dimension{Key: d.GetKey(), Value: d.GetValue()}
			markUnknown(unknown, d)
		}
	}
	return out
}

func allocationFromProto(ab *ingestpb.AllocationBatch, unknown *bool) *liveingest.AllocationBatch {
	out := &liveingest.AllocationBatch{
		NodeUID:    ab.GetNodeUid(),
		BootID:     ab.GetBootId(),
		Session:    ab.GetSession(),
		Sequence:   ab.GetSequence(),
		ObservedAt: timestampOf(ab.GetObservedAt()),
		ExpiresAt:  timestampOf(ab.GetExpiresAt()),
		Complete:   ab.GetComplete(),
		Profile:    int32(ab.GetProfile()),
	}
	markUnknown(unknown, ab, ab.GetObservedAt(), ab.GetExpiresAt())
	if refs := ab.GetEvidenceRefs(); len(refs) > 0 {
		out.EvidenceRefs = append([]string(nil), refs...)
	}
	if n := len(ab.GetEntries()); n > 0 {
		out.Entries = make([]liveingest.AllocationEntry, n)
		for i, e := range ab.GetEntries() {
			out.Entries[i] = liveingest.AllocationEntry{
				ResourceName:  e.GetResourceName(),
				DeviceID:      e.GetDeviceId(),
				PodNamespace:  e.GetPodNamespace(),
				PodName:       e.GetPodName(),
				ContainerName: e.GetContainerName(),
			}
			markUnknown(unknown, e)
		}
	}
	return out
}
