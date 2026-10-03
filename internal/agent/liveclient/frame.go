package liveclient

import (
	"compress/gzip"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aetertinas9/data-path-assurance/internal/agent"
	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// The two size limits of one message (GLI-050): the payload length on the wire
// (compressed when compressed) and the length after decompression.
const (
	maxWireBytes         = 4 << 20
	maxDecompressedBytes = 16 << 20
	// gzipOverheadBytes covers the gzip header and trailer and the worst-case
	// stored-block overhead of 4 MiB of incompressible data, so a message
	// that is this much smaller than the wire limit is under it compressed,
	// whatever it holds.
	gzipOverheadBytes = 1 << 10
)

// Fixed class words of a frame that is not sent (logs carry these only).
const (
	classTooLarge        = "frame_too_large"
	classMarshal         = "frame_marshal"
	classCanonicalDigest = "frame_digest"
)

// frameParams binds a collection to the stream it is sent on.
type frameParams struct {
	nodeUID, bootID string
	session         int64
	sequence        uint64
	observedAt      time.Time
	ttl             time.Duration
}

// buildFrame turns one live collection into the neutral frame of the wire: the
// payload with its deterministic IDs and instants, the canonical digest and the
// bundle revision. Every ID, the digest and the revision come from package
// liveingest, the single implementation the controller checks them with.
func buildFrame(lf agent.LiveFrame, p frameParams) (*liveingest.Frame, error) {
	at := liveingest.TimestampOf(p.observedAt)
	expires := liveingest.TimestampOf(p.observedAt.Add(p.ttl))
	relation, origin := model.RelLocatedIn.String(), model.OriginObserved.String()
	quality := model.QualityGood.String()

	payload := &liveingest.Payload{}
	for _, a := range lf.Assets {
		payload.Assets = append(payload.Assets, neutralAsset(a))
	}
	for i, e := range lf.Edges {
		payload.Edges = append(payload.Edges, liveingest.Edge{
			FromKey: e.From.Key(), Relation: relation, ToKey: e.To.Key(), Origin: origin,
		})
		payload.EdgeEvidence = append(payload.EdgeEvidence, liveingest.EdgeEvidence{
			EdgeIndex:  uint32(i),
			Kind:       fleet.EdgeEvidenceObserved.String(),
			SourceType: lf.EdgeSource.Type,
			SourceName: lf.EdgeSource.Name,
			ObservedAt: at,
			ExpiresAt:  expires,
			EvidenceID: liveingest.EdgeEvidenceID(p.session, p.sequence, e.From.Key(), relation, e.To.Key(), origin),
		})
	}
	for _, o := range lf.Observations {
		obs := liveingest.Observation{
			ID:         liveingest.ObservationID(p.session, p.sequence, o.Subject.Key(), o.Signal),
			Source:     liveingest.SourceRef{Type: o.Source.Type, Name: o.Source.Name},
			Subject:    neutralAsset(o.Subject),
			Signal:     o.Signal,
			Value:      liveingest.Value{Kind: liveingest.ValueInt, Int: o.Value},
			Unit:       o.Unit,
			ObservedAt: at,
			ReceivedAt: at,
			ExpiresAt:  expires,
			Sequence:   p.sequence,
			Quality:    quality,
		}
		for _, d := range o.Dimensions {
			obs.Dimensions = append(obs.Dimensions, liveingest.Dimension{Key: d.Key, Value: d.Value})
		}
		payload.Observations = append(payload.Observations, obs)
	}
	for _, b := range lf.Bindings {
		payload.GPUBindings = append(payload.GPUBindings, liveingest.GPUBinding{
			UUID:       b.UUID,
			BDF:        b.BDF,
			SourceType: lf.BindingSource.Type,
			SourceName: lf.BindingSource.Name,
			EvidenceID: liveingest.BindingEvidenceID(p.session, p.sequence, b.UUID, b.BDF),
			ObservedAt: at,
			ExpiresAt:  expires,
		})
	}

	sum, _, err := liveingest.Digest(payload)
	if err != nil {
		return nil, err
	}
	completeness := liveingest.CompletenessComplete
	if !lf.Complete {
		completeness = liveingest.CompletenessPartial
	}
	return &liveingest.Frame{
		NodeUID:        p.nodeUID,
		BootID:         p.bootID,
		Session:        p.session,
		Sequence:       p.sequence,
		Completeness:   completeness,
		ObservedAt:     at,
		Payload:        payload,
		PayloadDigest:  sum[:],
		BundleRevision: liveingest.Revision(p.session, p.sequence, sum),
	}, nil
}

// neutralAsset converts an asset reference; the collector never sets aliases.
func neutralAsset(a model.AssetRef) liveingest.Asset {
	out := liveingest.Asset{Kind: a.Kind.String(), Canonical: a.Canonical}
	for _, al := range a.Aliases {
		out.Aliases = append(out.Aliases, liveingest.Alias{Namespace: al.Namespace, Value: al.Value})
	}
	return out
}

// snapshotMessage converts the neutral frame to the wire message. The
// conversion copies every field; the client never sets the allocation batch.
func snapshotMessage(f *liveingest.Frame) *ingestpb.SnapshotFrame {
	msg := &ingestpb.SnapshotFrame{
		NodeUid:        f.NodeUID,
		BootId:         f.BootID,
		Session:        f.Session,
		Sequence:       f.Sequence,
		Completeness:   ingestpb.Completeness(f.Completeness),
		ObservedAt:     timestampMessage(f.ObservedAt),
		PayloadDigest:  f.PayloadDigest,
		BundleRevision: f.BundleRevision,
	}
	p := f.Payload
	if p == nil {
		return msg
	}
	payload := &ingestpb.HostSnapshotV1{}
	for _, a := range p.Assets {
		payload.Assets = append(payload.Assets, assetMessage(a))
	}
	for _, e := range p.Edges {
		payload.Edges = append(payload.Edges, &ingestpb.Edge{
			FromKey: e.FromKey, Relation: e.Relation, ToKey: e.ToKey, Origin: e.Origin,
		})
	}
	for _, ev := range p.EdgeEvidence {
		payload.EdgeEvidence = append(payload.EdgeEvidence, &ingestpb.EdgeEvidence{
			EdgeIndex:  ev.EdgeIndex,
			Kind:       ev.Kind,
			SourceType: ev.SourceType,
			SourceName: ev.SourceName,
			ObservedAt: timestampMessage(ev.ObservedAt),
			ExpiresAt:  timestampMessage(ev.ExpiresAt),
			EvidenceId: ev.EvidenceID,
		})
	}
	for _, o := range p.Observations {
		obs := &ingestpb.Observation{
			Id:         o.ID,
			Source:     &ingestpb.SourceRef{Type: o.Source.Type, Name: o.Source.Name},
			Subject:    assetMessage(o.Subject),
			Signal:     o.Signal,
			Value:      valueMessage(o.Value),
			Unit:       o.Unit,
			ObservedAt: timestampMessage(o.ObservedAt),
			ReceivedAt: timestampMessage(o.ReceivedAt),
			ExpiresAt:  timestampMessage(o.ExpiresAt),
			Sequence:   o.Sequence,
			Quality:    o.Quality,
			RawDigest:  o.RawDigest,
		}
		for _, d := range o.Dimensions {
			obs.Dimensions = append(obs.Dimensions, &ingestpb.Dimension{Key: d.Key, Value: d.Value})
		}
		payload.Observations = append(payload.Observations, obs)
	}
	for _, b := range p.GPUBindings {
		payload.GpuBindings = append(payload.GpuBindings, &ingestpb.GPUBinding{
			Uuid:       b.UUID,
			Serial:     b.Serial,
			Bdf:        b.BDF,
			SourceType: b.SourceType,
			SourceName: b.SourceName,
			EvidenceId: b.EvidenceID,
			ObservedAt: timestampMessage(b.ObservedAt),
			ExpiresAt:  timestampMessage(b.ExpiresAt),
		})
	}
	msg.Payload = payload
	return msg
}

func assetMessage(a liveingest.Asset) *ingestpb.Asset {
	out := &ingestpb.Asset{Kind: a.Kind, Canonical: a.Canonical}
	for _, al := range a.Aliases {
		out.Aliases = append(out.Aliases, &ingestpb.Alias{Namespace: al.Namespace, Value: al.Value})
	}
	return out
}

// valueMessage converts a neutral value to its wire alternative.
func valueMessage(v liveingest.Value) *ingestpb.Value {
	switch v.Kind {
	case liveingest.ValueInt:
		return &ingestpb.Value{V: &ingestpb.Value_IntValue{IntValue: v.Int}}
	case liveingest.ValueFloat:
		return &ingestpb.Value{V: &ingestpb.Value_FloatValue{FloatValue: v.Float}}
	case liveingest.ValueBool:
		return &ingestpb.Value{V: &ingestpb.Value_BoolValue{BoolValue: v.Bool}}
	case liveingest.ValueString:
		return &ingestpb.Value{V: &ingestpb.Value_StringValue{StringValue: v.Str}}
	default:
		return &ingestpb.Value{}
	}
}

// timestampMessage converts a neutral timestamp; an absent one is nil.
func timestampMessage(t liveingest.Timestamp) *timestamppb.Timestamp {
	if !t.Present {
		return nil
	}
	return &timestamppb.Timestamp{Seconds: t.Seconds, Nanos: t.Nanos}
}

// errTooLarge reports a message over a GLI-050 limit.
var errTooLarge = errors.New("liveclient: the frame message is over a size limit")

// checkMessageSize applies the two limits of GLI-050 to a message that is sent
// gzip-compressed: its length after decompression (the marshaled length) is at
// most 16 MiB and its compressed length at most 4 MiB. The compressed length is
// measured with the default-level gzip writer that gRPC's registered gzip
// compressor also uses. A message that fits the wire limit with the gzip
// overhead to spare cannot exceed it compressed, so it is not compressed here.
func checkMessageSize(msg *ingestpb.ClientFrame) error {
	raw, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	if len(raw) > maxDecompressedBytes {
		return errTooLarge
	}
	if len(raw) <= maxWireBytes-gzipOverheadBytes {
		return nil
	}
	var n countWriter
	zw := gzip.NewWriter(&n)
	if _, err := zw.Write(raw); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if n.n > maxWireBytes {
		return errTooLarge
	}
	return nil
}

// countWriter counts the bytes written to it.
type countWriter struct{ n int }

func (c *countWriter) Write(p []byte) (int, error) {
	c.n += len(p)
	return len(p), nil
}
