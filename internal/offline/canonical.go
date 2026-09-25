package offline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// canonicalWriter feeds the format-independent canonical encoding into a
// SHA-256 and counts its length, without holding the encoding in memory.
type canonicalWriter struct {
	h   hash.Hash
	buf []byte
	n   int64
}

func newCanonicalWriter() *canonicalWriter {
	return &canonicalWriter{h: sha256.New(), buf: make([]byte, 0, 32<<10)}
}

func (w *canonicalWriter) put(b []byte) {
	w.n += int64(len(b))
	if len(w.buf)+len(b) > cap(w.buf) {
		w.flush()
		if len(b) > cap(w.buf) {
			_, _ = w.h.Write(b)
			return
		}
	}
	w.buf = append(w.buf, b...)
}

func (w *canonicalWriter) flush() {
	if len(w.buf) > 0 {
		_, _ = w.h.Write(w.buf)
		w.buf = w.buf[:0]
	}
}

func (w *canonicalWriter) u32(v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	w.put(b[:])
}

func (w *canonicalWriter) u64(v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	w.put(b[:])
}

func (w *canonicalWriter) i64(v int64) { w.u64(uint64(v)) }

func (w *canonicalWriter) str(s string) {
	w.u32(uint32(len(s)))
	w.put([]byte(s))
}

func (w *canonicalWriter) boolean(v bool) {
	if v {
		w.put([]byte{1})
	} else {
		w.put([]byte{0})
	}
}

// timestamp writes i64be(Unix seconds, floor) followed by u32be(nanoseconds).
func (w *canonicalWriter) timestamp(t time.Time) {
	w.i64(t.Unix())
	w.u32(uint32(t.Nanosecond()))
}

// optionalTimestamp writes 0x00 for the zero time and 0x01 followed by the
// timestamp otherwise.
func (w *canonicalWriter) optionalTimestamp(t time.Time) {
	if t.IsZero() {
		w.put([]byte{0})
		return
	}
	w.put([]byte{1})
	w.timestamp(t)
}

func (w *canonicalWriter) sum() string {
	w.flush()
	return hex.EncodeToString(w.h.Sum(nil))
}

// canonicalValue writes str(kind name) followed by the kind's payload.
func (w *canonicalWriter) canonicalValue(v rawValue) {
	switch v.kind {
	case "int":
		w.str("Int")
		w.i64(v.integer)
	case "float":
		w.str("Float")
		w.u64(math.Float64bits(v.float))
	case "bool":
		w.str("Bool")
		w.boolean(v.boolean)
	default:
		w.str("String")
		w.str(v.str)
	}
}

func (w *canonicalWriter) canonicalAsset(a rawAsset) {
	w.str(a.kind)
	w.str(a.canonical)
	w.u32(uint32(len(a.aliases)))
	for _, al := range a.aliases {
		w.str(al.namespace)
		w.str(al.value)
	}
}

// payloadCanonical computes the frame canonical bytes' SHA-256 (lowercase
// hex) and their length. Parsed times come from the checked frame.
func payloadCanonical(p *checkedPayload) (string, int64) {
	w := newCanonicalWriter()
	w.str("dpa.HostSnapshotV1.canonical.v1")
	w.u32(uint32(len(p.raw.assets)))
	for _, a := range p.raw.assets {
		w.canonicalAsset(a)
	}
	w.u32(uint32(len(p.raw.edges)))
	for _, e := range p.raw.edges {
		w.str(e.fromKey)
		w.str(e.relation)
		w.str(e.toKey)
		w.str(e.origin)
	}
	w.u32(uint32(len(p.raw.edgeEvidence)))
	for i, e := range p.raw.edgeEvidence {
		w.u32(uint32(e.edgeIndex))
		w.str(e.kind)
		w.str(e.sourceType)
		w.str(e.sourceName)
		w.timestamp(p.observedAt)
		w.timestamp(p.edgeEvidenceExpires[i])
		w.str(e.evidenceID)
	}
	w.u32(uint32(len(p.raw.observations)))
	for i, o := range p.raw.observations {
		w.str(o.id)
		w.str(o.sourceType)
		w.str(o.sourceName)
		w.canonicalAsset(o.subject)
		w.str(o.signal)
		w.canonicalValue(o.value)
		w.str(o.unit)
		w.u32(uint32(len(o.dimensions)))
		for _, d := range o.dimensions {
			w.str(d.key)
			w.str(d.value)
		}
		w.timestamp(p.observedAt)
		w.timestamp(p.observedAt)
		w.optionalTimestamp(p.observationExpires[i])
		w.u64(o.sequence)
		w.str(o.quality)
		w.str(o.rawDigest)
	}
	w.u32(uint32(len(p.raw.gpuBindings)))
	for i, b := range p.raw.gpuBindings {
		w.str(b.uuid)
		w.str(b.serial)
		w.str(b.bdf)
		w.str(b.sourceType)
		w.str(b.sourceName)
		w.str(b.evidenceID)
		w.timestamp(p.observedAt)
		w.timestamp(p.bindingExpires[i])
	}
	w.put([]byte{0}) // no allocation batch
	return w.sum(), w.n
}

// deterministicID builds <prefix>:<session>:<sequence>:<H>, where H is the
// first 16 bytes (lowercase hex) of the SHA-256 of the length-prefixed parts.
func deterministicID(prefix string, session, sequence uint64, parts ...string) string {
	h := sha256.New()
	var b [4]byte
	for _, p := range parts {
		binary.BigEndian.PutUint32(b[:], uint32(len(p)))
		_, _ = h.Write(b[:])
		_, _ = h.Write([]byte(p))
	}
	sum := h.Sum(nil)
	return prefix + ":" + strconv.FormatUint(session, 10) + ":" + strconv.FormatUint(sequence, 10) + ":" + hex.EncodeToString(sum[:16])
}

// sortDimensions orders dimensions by key, bytes ascending.
func sortDimensions(d []rawDimension) {
	slices.SortFunc(d, func(a, b rawDimension) int { return strings.Compare(a.key, b.key) })
}

// compareDimensionSets orders two key-sorted dimension sets as sequences of
// (key, value) pairs, a proper prefix first.
func compareDimensionSets(a, b []rawDimension) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := strings.Compare(a[i].key, b[i].key); c != 0 {
			return c
		}
		if c := strings.Compare(a[i].value, b[i].value); c != 0 {
			return c
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}
