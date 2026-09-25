package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"slices"
	"strings"

	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// encoder builds length-prefixed canonical encodings.
type encoder struct{ b []byte }

func (e *encoder) u32(v uint32) { e.b = binary.BigEndian.AppendUint32(e.b, v) }
func (e *encoder) u64(v uint64) { e.b = binary.BigEndian.AppendUint64(e.b, v) }
func (e *encoder) str(s string) {
	e.u32(uint32(len(s)))
	e.b = append(e.b, s...)
}
func (e *encoder) raw(p []byte) { e.b = append(e.b, p...) }

// list writes u32(count) followed by the elements. When sorted is true the
// elements are ordered by their encoded bytes, ascending.
func (e *encoder) list(elems [][]byte, sorted bool) {
	if sorted {
		slices.SortFunc(elems, bytes.Compare)
	}
	e.u32(uint32(len(elems)))
	for _, x := range elems {
		e.raw(x)
	}
}

func digestHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// edgeIdentity is the edge identity key: endpoints, relation and origin.
func edgeIdentity(e graph.Edge) string {
	return e.From.Key() + "\x00" + e.To.Key() + "\x00" + e.Relation.String() + "\x00" + e.Origin.String()
}

// topologyDigest is the SHA-256 of
// str("dpa.TopologyDigest.v1") ‖ str(P) ‖ list(str(asset key)) ‖ list(edge entry),
// where an edge entry is its endpoints, relation and origin followed by the
// list of (kind, source type, source name) of that edge's provenance. Every
// list is ordered by encoded bytes; equal elements are kept.
func topologyDigest(p model.PartitionKey, f *Frame) string {
	provenance := make(map[string][][]byte, len(f.Provenance))
	for _, pr := range f.Provenance {
		var x encoder
		x.str(pr.Kind.String())
		x.str(pr.Source.Type)
		x.str(pr.Source.Name)
		id := edgeIdentity(pr.Edge)
		provenance[id] = append(provenance[id], x.b)
	}
	assets := make([][]byte, 0, len(f.Assets))
	for _, a := range f.Assets {
		var x encoder
		x.str(a.Key())
		assets = append(assets, x.b)
	}
	edges := make([][]byte, 0, len(f.Edges))
	for _, ed := range f.Edges {
		var x encoder
		x.str(ed.From.Key())
		x.str(ed.Relation.String())
		x.str(ed.To.Key())
		x.str(ed.Origin.String())
		x.list(provenance[edgeIdentity(ed)], true)
		edges = append(edges, x.b)
	}
	var e encoder
	e.str("dpa.TopologyDigest.v1")
	e.str(string(p))
	e.list(assets, true)
	e.list(edges, true)
	return digestHex(e.b)
}

// baselineValue encodes an expected width value: an Int, or a finite Float
// holding an integer in int64 range, is str("I") ‖ i64; anything else uses
// the kind-tagged value encoding.
func baselineValue(x *encoder, v model.Value) {
	if n, ok := v.Int(); ok {
		x.str("I")
		x.u64(uint64(n))
		return
	}
	if f, ok := v.Float(); ok {
		if !math.IsNaN(f) && !math.IsInf(f, 0) && math.Trunc(f) == f && f >= -(1<<63) && f < (1<<63) {
			x.str("I")
			x.u64(uint64(int64(f)))
			return
		}
		x.str("Float")
		x.u64(math.Float64bits(f))
		return
	}
	if b, ok := v.Bool(); ok {
		x.str("Bool")
		if b {
			x.raw([]byte{1})
		} else {
			x.raw([]byte{0})
		}
		return
	}
	s, _ := v.Str()
	x.str("String")
	x.str(s)
}

// baselineDigest is the SHA-256 of
// str("dpa.BaselineDigest.v1") ‖ str(P) ‖ list(entry). For every subject of
// the window, the latest observation of each expected-width series is taken
// and, of those, the ones with the latest instant form entries
// str(subject key) ‖ value ‖ list(str(key) ‖ str(value), key order). Equal
// entries are merged and the list is ordered by encoded bytes.
func baselineDigest(p model.PartitionKey, w evidence.Window) string {
	seen := map[string]struct{}{}
	var entries [][]byte
	for _, subject := range w.Subjects() {
		series, err := w.SeriesFor(subject, pcie.SignalLinkWidthExpected)
		if err != nil {
			continue
		}
		var latest []model.Observation
		for _, s := range series {
			o, ok := s.Latest()
			if !ok {
				continue
			}
			switch {
			case len(latest) == 0 || o.ObservedAt.After(latest[0].ObservedAt):
				latest = []model.Observation{o}
			case o.ObservedAt.Equal(latest[0].ObservedAt):
				latest = append(latest, o)
			}
		}
		for _, o := range latest {
			var x encoder
			x.str(subject.Key())
			baselineValue(&x, o.Value)
			keys := make([]string, 0, len(o.Dimensions))
			for k := range o.Dimensions {
				keys = append(keys, k)
			}
			slices.SortFunc(keys, strings.Compare)
			x.u32(uint32(len(keys)))
			for _, k := range keys {
				x.str(k)
				x.str(o.Dimensions[k])
			}
			if _, dup := seen[string(x.b)]; dup {
				continue
			}
			seen[string(x.b)] = struct{}{}
			entries = append(entries, x.b)
		}
	}
	var e encoder
	e.str("dpa.BaselineDigest.v1")
	e.str(string(p))
	e.list(entries, true)
	return digestHex(e.b)
}

// provenanceFor indexes provenance by edge identity.
func provenanceFor(list []fleet.EdgeProvenance) map[string][]fleet.EdgeProvenance {
	m := make(map[string][]fleet.EdgeProvenance, len(list))
	for _, p := range list {
		id := edgeIdentity(p.Edge)
		m[id] = append(m[id], p)
	}
	return m
}
