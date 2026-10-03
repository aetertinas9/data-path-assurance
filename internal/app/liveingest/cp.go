package liveingest

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"math"
)

// The canonical payload encoding CP is a format-independent byte sequence:
//
//	str   = u32be(len) ‖ bytes
//	u32, u64, i64 = big-endian (two's complement for the signed one)
//	bool  = 1 byte, 0 or 1
//	time  = i64be(Unix seconds) ‖ u32be(nanoseconds)
//	opt time = 0x00 when absent, 0x01 ‖ time when present
//	list  = u32be(count) ‖ elements
//
//	CP = str("dpa.HostSnapshotV1.canonical.v1") ‖ list(asset) ‖ list(edge) ‖
//	     list(edgeEvidence) ‖ list(observation) ‖ list(gpuBinding) ‖ allocation
//
// Every list is written in the order the payload holds it; nothing is sorted.
// The allocation part is 0x00 for a payload without an allocation batch and
// otherwise 0x01 ‖ str(node_uid) ‖ str(boot_id) ‖ i64(session) ‖ u64(sequence)
// ‖ time(observed_at) ‖ time(expires_at) ‖ bool(complete) ‖
// list(str(evidence_ref)) ‖ str(profile) ‖ list(entry), where an entry is the
// five strings of AllocationEntry and profile is the literal
// NVIDIAPodResourcesUUID or Unsupported. The digest of a payload without an
// allocation batch equals the payloadDigest of the offline artifact of the
// same content.

const canonicalTag = "dpa.HostSnapshotV1.canonical.v1"

// cpWriter feeds the canonical encoding into a SHA-256 and counts its length,
// without holding the encoding in memory. Once the length is over the bound it
// stops hashing: the digest is then not wanted.
type cpWriter struct {
	h   hash.Hash
	buf []byte
	n   int64
}

func newCPWriter() *cpWriter {
	return &cpWriter{h: sha256.New(), buf: make([]byte, 0, 32<<10)}
}

func (w *cpWriter) over() bool { return w.n > MaxCanonicalBytes }

func (w *cpWriter) put(b []byte) {
	w.n += int64(len(b))
	if w.over() {
		return
	}
	if len(w.buf)+len(b) > cap(w.buf) {
		w.flush()
		if len(b) > cap(w.buf) {
			_, _ = w.h.Write(b)
			return
		}
	}
	w.buf = append(w.buf, b...)
}

// putString is put for a string, without copying it unless it is larger than
// the buffer.
func (w *cpWriter) putString(s string) {
	w.n += int64(len(s))
	if w.over() {
		return
	}
	if len(w.buf)+len(s) > cap(w.buf) {
		w.flush()
		if len(s) > cap(w.buf) {
			_, _ = w.h.Write([]byte(s))
			return
		}
	}
	w.buf = append(w.buf, s...)
}

func (w *cpWriter) flush() {
	if len(w.buf) > 0 {
		_, _ = w.h.Write(w.buf)
		w.buf = w.buf[:0]
	}
}

func (w *cpWriter) u32(v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	w.put(b[:])
}

func (w *cpWriter) u64(v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	w.put(b[:])
}

func (w *cpWriter) i64(v int64) { w.u64(uint64(v)) }

func (w *cpWriter) str(s string) {
	w.u32(uint32(len(s)))
	w.putString(s)
}

func (w *cpWriter) boolean(v bool) {
	if v {
		w.put([]byte{1})
	} else {
		w.put([]byte{0})
	}
}

// timestamp writes the raw seconds and nanoseconds of t.
func (w *cpWriter) timestamp(t Timestamp) {
	w.i64(t.Seconds)
	w.u32(uint32(t.Nanos))
}

// optionalTimestamp writes 0x00 for an absent timestamp and 0x01 followed by
// the timestamp otherwise.
func (w *cpWriter) optionalTimestamp(t Timestamp) {
	if !t.Present {
		w.put([]byte{0})
		return
	}
	w.put([]byte{1})
	w.timestamp(t)
}

func (w *cpWriter) asset(a Asset) {
	w.str(a.Kind)
	w.str(a.Canonical)
	w.u32(uint32(len(a.Aliases)))
	for _, al := range a.Aliases {
		w.str(al.Namespace)
		w.str(al.Value)
	}
}

// value writes the kind literal followed by the payload of the kind. A Value
// that holds no alternative is written as an empty String; such a payload is
// rejected by CheckFrame before its digest matters.
func (w *cpWriter) value(v Value) {
	switch v.Kind {
	case ValueInt:
		w.str("Int")
		w.i64(v.Int)
	case ValueFloat:
		w.str("Float")
		w.u64(math.Float64bits(v.Float))
	case ValueBool:
		w.str("Bool")
		w.boolean(v.Bool)
	default:
		w.str("String")
		w.str(v.Str)
	}
}

// profileLiteral returns the CP literal of a wire AllocationProfile value; an
// unknown value is written as the empty string, which CheckFrame rejects.
func profileLiteral(profile int32) string {
	switch profile {
	case AllocationProfileNVIDIAPodResourcesUUID:
		return "NVIDIAPodResourcesUUID"
	case AllocationProfileUnsupported:
		return "Unsupported"
	}
	return ""
}

// Digest returns the SHA-256 of the canonical encoding CP of the payload and
// the length of that encoding. A nil payload is the empty payload. When the
// encoding is longer than MaxCanonicalBytes it returns an error that matches
// ErrInvalidFrame (class "canonical_size") and the digest is not meaningful.
// The encoding follows the wire order of every list, so a payload that
// violates the ordering rules still has a digest; CheckFrame rejects it.
func Digest(p *Payload) (sum [32]byte, length int, err error) {
	if p == nil {
		p = &Payload{}
	}
	w := newCPWriter()
	w.str(canonicalTag)
	w.u32(uint32(len(p.Assets)))
	for _, a := range p.Assets {
		w.asset(a)
	}
	w.u32(uint32(len(p.Edges)))
	for _, e := range p.Edges {
		w.str(e.FromKey)
		w.str(e.Relation)
		w.str(e.ToKey)
		w.str(e.Origin)
	}
	w.u32(uint32(len(p.EdgeEvidence)))
	for _, e := range p.EdgeEvidence {
		w.u32(e.EdgeIndex)
		w.str(e.Kind)
		w.str(e.SourceType)
		w.str(e.SourceName)
		w.timestamp(e.ObservedAt)
		w.timestamp(e.ExpiresAt)
		w.str(e.EvidenceID)
	}
	w.u32(uint32(len(p.Observations)))
	for i := range p.Observations {
		o := &p.Observations[i]
		w.str(o.ID)
		w.str(o.Source.Type)
		w.str(o.Source.Name)
		w.asset(o.Subject)
		w.str(o.Signal)
		w.value(o.Value)
		w.str(o.Unit)
		w.u32(uint32(len(o.Dimensions)))
		for _, d := range o.Dimensions {
			w.str(d.Key)
			w.str(d.Value)
		}
		w.timestamp(o.ObservedAt)
		w.timestamp(o.ReceivedAt)
		w.optionalTimestamp(o.ExpiresAt)
		w.u64(o.Sequence)
		w.str(o.Quality)
		w.str(o.RawDigest)
	}
	w.u32(uint32(len(p.GPUBindings)))
	for _, b := range p.GPUBindings {
		w.str(b.UUID)
		w.str(b.Serial)
		w.str(b.BDF)
		w.str(b.SourceType)
		w.str(b.SourceName)
		w.str(b.EvidenceID)
		w.timestamp(b.ObservedAt)
		w.timestamp(b.ExpiresAt)
	}
	if a := p.Allocation; a == nil {
		w.put([]byte{0})
	} else {
		w.put([]byte{1})
		w.str(a.NodeUID)
		w.str(a.BootID)
		w.i64(a.Session)
		w.u64(a.Sequence)
		w.timestamp(a.ObservedAt)
		w.timestamp(a.ExpiresAt)
		w.boolean(a.Complete)
		w.u32(uint32(len(a.EvidenceRefs)))
		for _, ref := range a.EvidenceRefs {
			w.str(ref)
		}
		w.str(profileLiteral(a.Profile))
		w.u32(uint32(len(a.Entries)))
		for _, e := range a.Entries {
			w.str(e.ResourceName)
			w.str(e.DeviceID)
			w.str(e.PodNamespace)
			w.str(e.PodName)
			w.str(e.ContainerName)
		}
	}
	if w.over() {
		return sum, 0, invalid("canonical_size")
	}
	w.flush()
	copy(sum[:], w.h.Sum(nil))
	return sum, int(w.n), nil
}
