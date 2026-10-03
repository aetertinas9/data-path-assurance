package liveingest_test

// Payload validation (GLI-060 V1 (a)-(h), V2 (i)-(k), GLI-061 digest binding,
// GLI-124 (c) INVALID variants, L-SKEW, L-ALLOC-NESTED, L-SPOOF-NODE frame
// side). Each row mutates a valid BASE payload, reseals it so that only the
// targeted rule is violated, and expects ack INVALID followed by the stream
// status InvalidArgument with no change of node state (GLI-044, GLI-045). A
// boundary twin that must be ACCEPTED accompanies the limit rules.

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

// gliFxMc is the frame context of a payload mutation.
type gliFxMc struct {
	session int64
	seq     uint64
	at      time.Time
}

// gliFxExtraObs adds an observation of the GPU function with the given signal,
// value, unit and dimensions (IDs per GFO-084) and restores the order.
func gliFxExtraObs(p *ingestpb.HostSnapshotV1, m gliFxMc, signal string, v *ingestpb.Value, unit string, dims []*ingestpb.Dimension) *ingestpb.Observation {
	o := gliFxObservation(m.session, m.seq, m.at, "PCIeFunction", "pci-bdf:"+gliFxGPUBDF, signal, gliFxWidthSrc, v, unit, dims)
	o.ExpiresAt = gliFxTS(m.at.Add(gliFxTTL))
	p.Observations = append(p.Observations, o)
	gliFxSortPayload(p)
	return o
}

// gliFxSetEdge applies mod to edge i, recomputes the evidence ID of that edge
// and restores the order.
func gliFxSetEdge(p *ingestpb.HostSnapshotV1, m gliFxMc, i int, mod func(e *ingestpb.Edge)) {
	e := p.Edges[i]
	mod(e)
	p.EdgeEvidence[i].EvidenceId = gliFxEdgeID(m.session, m.seq, e.GetFromKey(), e.GetRelation(), e.GetToKey(), e.GetOrigin())
	gliFxSortPayload(p)
}

// gliFxDropEdgesTo removes every edge whose to_key is key, with its evidence.
func gliFxDropEdgesTo(p *ingestpb.HostSnapshotV1, key string) {
	var edges []*ingestpb.Edge
	var evs []*ingestpb.EdgeEvidence
	for i, e := range p.Edges {
		if e.GetToKey() == key {
			continue
		}
		edges = append(edges, e)
		evs = append(evs, p.EdgeEvidence[i])
	}
	p.Edges, p.EdgeEvidence = edges, evs
	gliFxSortPayload(p)
}

// gliFxV1Row is one payload mutation: ok rows are the boundary twins that must
// be ACCEPTED, the others must be rejected as INVALID.
type gliFxV1Row struct {
	name string
	ok   bool
	mut  func(p *ingestpb.HostSnapshotV1, m gliFxMc)
}

// gliFxRunRows runs rows as baseline frames (sequence 0) on fresh streams.
func gliFxRunRows(t *testing.T, base *gliFxEnv, rows []gliFxV1Row, clause string) {
	t.Helper()
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			before := env.Sig()
			m := gliFxMc{session: c.Session, seq: 0, at: gliFxAt(0)}
			p := gliPayload(c.UID, c.Boot, m.session, m.seq, m.at)
			r.mut(p, m)
			f := gliFrame(t, p, c.Session, 0, m.at, true)
			if r.ok {
				a := c.Ack(f)
				gliFxWantAck(t, a, ingestpb.AckCode_ACCEPTED, c.Session, 1, clause+" boundary twin")
				return
			}
			c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, clause)
			env.WantSigUnchanged(before, clause+" atomicity")
		})
	}
}

func gliFxLong(n int) string { return strings.Repeat("a", n) }

// GLI-060 V1 (a): character sets and lengths. Value strings are 0x20-0x7E and
// 0-4096 bytes; every other payload string is 0x21-0x7E with its GFX-033 (c)
// length (ID 1-128, source 1-256, others 1-512, raw_digest empty or 1-256).
// Valid UTF-8 outside the sets (e-acute, control characters, spaces in
// identifier strings) is INVALID at F4, not a codec failure (GLI-046).
func TestGLI060_V1CharacterSetsAndLengths(t *testing.T) {
	base := gliFxStart(t)
	extra := func(p *ingestpb.HostSnapshotV1, m gliFxMc, v *ingestpb.Value, unit string, dims []*ingestpb.Dimension) *ingestpb.Observation {
		return gliFxExtraObs(p, m, "gli.extra", v, unit, dims)
	}
	valueRow := func(name string, ok bool, s string) gliFxV1Row {
		return gliFxV1Row{"value string " + name, ok, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { extra(p, m, gliFxStrVal(s), "x", nil) }}
	}
	unitRow := func(name string, ok bool, s string) gliFxV1Row {
		return gliFxV1Row{"unit " + name, ok, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { extra(p, m, gliFxInt(1), s, nil) }}
	}
	dimRow := func(name string, ok bool, k, v string) gliFxV1Row {
		return gliFxV1Row{"dimension " + name, ok, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			extra(p, m, gliFxInt(1), "x", []*ingestpb.Dimension{{Key: k, Value: v}})
		}}
	}
	srcRow := func(name string, ok bool, typ, nm string) gliFxV1Row {
		return gliFxV1Row{"observation source " + name, ok, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			o := extra(p, m, gliFxInt(1), "x", nil)
			o.Source = &ingestpb.SourceRef{Type: typ, Name: nm}
			gliFxSortPayload(p)
		}}
	}
	rawRow := func(name string, ok bool, s string) gliFxV1Row {
		return gliFxV1Row{"raw_digest " + name, ok, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			extra(p, m, gliFxInt(1), "x", nil).RawDigest = s
		}}
	}
	rows := []gliFxV1Row{
		valueRow("with spaces and punctuation is valid", true, "a b !~"),
		valueRow("empty is valid", true, ""),
		valueRow("of 4096 bytes is valid", true, strings.Repeat("v", 4096)),
		valueRow("of 4097 bytes", false, strings.Repeat("v", 4097)),
		valueRow("with DEL", false, "a\x7fb"),
		valueRow("with a unit separator control character", false, "a\x1fb"),
		valueRow("with a tab", false, "a\tb"),
		valueRow("with a newline", false, "a\nb"),
		valueRow("with a valid UTF-8 e-acute", false, "café"),
		unitRow("of 512 bytes is valid", true, gliFxLong(512)),
		unitRow("of 513 bytes", false, gliFxLong(513)),
		unitRow("empty", false, ""),
		unitRow("with a space", false, "la nes"),
		unitRow("with a valid UTF-8 e-acute", false, "lanés"),
		unitRow("with DEL", false, "la\x7fnes"),
		dimRow("value of 512 bytes is valid", true, "k", gliFxLong(512)),
		dimRow("value of 513 bytes", false, "k", gliFxLong(513)),
		dimRow("value empty", false, "k", ""),
		dimRow("value with a space", false, "k", "v v"),
		dimRow("value with a valid UTF-8 e-acute", false, "k", "vé"),
		dimRow("key of 512 bytes is valid", true, gliFxLong(512), "v"),
		dimRow("key of 513 bytes", false, gliFxLong(513), "v"),
		dimRow("key empty", false, "", "v"),
		dimRow("key with a space", false, "k k", "v"),
		srcRow("name of 256 bytes is valid", true, "agent", gliFxLong(256)),
		srcRow("name of 257 bytes", false, "agent", gliFxLong(257)),
		srcRow("type of 256 bytes is valid", true, gliFxLong(256), "n"),
		srcRow("type of 257 bytes", false, gliFxLong(257), "n"),
		srcRow("type empty", false, "", "n"),
		srcRow("name empty", false, "agent", ""),
		srcRow("name with a space", false, "agent", "path agent"),
		rawRow("of 256 bytes is valid", true, gliFxLong(256)),
		rawRow("of 257 bytes", false, gliFxLong(257)),
		rawRow("with a space", false, "ab cd"),
		{"gpuBinding serial is not empty", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.GpuBindings[0].Serial = "S1" }},
		{"gpuBinding source name with a space", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.GpuBindings[0].SourceName = "path nvidia" }},
		{"gpuBinding source name of 257 bytes", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.GpuBindings[0].SourceName = gliFxLong(257) }},
		{"edgeEvidence source name of 257 bytes", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence[0].SourceName = gliFxLong(257) }},
		{"edgeEvidence source name of 256 bytes is valid", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence[0].SourceName = gliFxLong(256) }},
		{"edgeEvidence source type with a space", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence[0].SourceType = "ag ent" }},
	}
	gliFxRunRows(t, base, rows, "GLI-060 (a)")
}

// GLI-060 V1 (b)/(c): enum-like values and ratified validity.
func TestGLI060_V1ValuesAndValidity(t *testing.T) {
	base := gliFxStart(t)
	signalRow := func(sig string) gliFxV1Row {
		return gliFxV1Row{"signal " + fmt.Sprintf("%q", sig), false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxExtraObs(p, m, sig, gliFxInt(1), "x", nil)
		}}
	}
	floatRow := func(name string, ok bool, f float64) gliFxV1Row {
		return gliFxV1Row{"float " + name, ok, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxExtraObs(p, m, "gli.extra", gliFxFloat(f), "x", nil)
		}}
	}
	qualityRow := func(q string, ok bool) gliFxV1Row {
		return gliFxV1Row{fmt.Sprintf("quality %q", q), ok, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxExtraObs(p, m, "gli.extra", gliFxInt(1), "x", nil).Quality = q
		}}
	}
	nodeKey := gliFxNodeKey(gliFxNodeUID)
	rows := []gliFxV1Row{
		{"edge relation CONNECTED_TO", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxSetEdge(p, m, 0, func(e *ingestpb.Edge) { e.Relation = "CONNECTED_TO" })
		}},
		{"edge relation empty", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxSetEdge(p, m, 0, func(e *ingestpb.Edge) { e.Relation = "" })
		}},
		{"edge to itself (graph.NewEdge rejects a self edge)", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxSetEdge(p, m, 0, func(e *ingestpb.Edge) { e.ToKey = e.FromKey })
		}},
		{"edge origin Intended", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxSetEdge(p, m, 0, func(e *ingestpb.Edge) { e.Origin = "Intended" })
		}},
		{"edge origin empty", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxSetEdge(p, m, 0, func(e *ingestpb.Edge) { e.Origin = "" })
		}},
		{"edgeEvidence kind Intended", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence[0].Kind = "Intended" }},
		{"edgeEvidence kind empty", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence[0].Kind = "" }},
		qualityRow("Excellent", false),
		qualityRow("", false),
		qualityRow("good", false),
		qualityRow("Degraded", true),
		qualityRow("Unknown", true),
		floatRow("NaN", false, math.NaN()),
		floatRow("+Inf", false, math.Inf(1)),
		floatRow("-Inf", false, math.Inf(-1)),
		floatRow("16.5 is valid", true, 16.5),
		{"bool value is valid", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxExtraObs(p, m, "gli.extra", gliFxBool(true), "x", nil)
		}},
		{"value absent", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxExtraObs(p, m, "gli.extra", nil, "x", nil)
		}},
		{"value without an alternative", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxExtraObs(p, m, "gli.extra", &ingestpb.Value{}, "x", nil)
		}},
		signalRow("Gli.Extra"),
		signalRow("single"),
		signalRow("gli..extra"),
		signalRow("gli.extra-1"),
		{"observation subject absent", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxExtraObs(p, m, "gli.extra", gliFxInt(1), "x", nil).Subject = nil
		}},
		{"asset kind unknown", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.Assets = append(p.Assets, gliFxAsset("Bogus", "pci-bdf:0000:0a:00.0"))
			gliFxSortPayload(p)
		}},
		{"asset canonical without a namespace", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.Assets = append(p.Assets, gliFxAsset("PCIeFunction", "nonamespace"))
			gliFxSortPayload(p)
		}},
		{"asset alias with an empty namespace", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			a := gliFxAsset("PCIeFunction", "pci-bdf:0000:0a:00.0")
			a.Aliases = []*ingestpb.Alias{{Namespace: "", Value: "p1"}}
			p.Assets = append(p.Assets, a)
			gliFxSortPayload(p)
		}},
		{"no KubernetesNode asset", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxDropEdgesTo(p, nodeKey)
			p.Assets = p.Assets[1:]
		}},
		{"two KubernetesNode assets", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.Assets = append(p.Assets, gliFxAsset("KubernetesNode", "kubernetes-node-uid:"+gliFxOtherUID))
			gliFxSortPayload(p)
		}},
		{"a separate asset is valid", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.Assets = append(p.Assets, gliFxAsset("PCIeFunction", "pci-bdf:0000:0a:00.0"))
			gliFxSortPayload(p)
		}},
	}
	gliFxRunRows(t, base, rows, "GLI-060 (b)(c)")
}

// GLI-060 V1 (d): order and uniqueness. Every collection is strictly increasing
// in the GFO-085 order; edgeEvidence has one entry per edge with edge_index i;
// observation IDs and dimension keys are unique. The same UUID on two BDFs is
// structurally valid (S1 judges the conflict, GLI-077).
func TestGLI060_V1OrderAndUniqueness(t *testing.T) {
	base := gliFxStart(t)
	rows := []gliFxV1Row{
		{"assets swapped", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.Assets[1], p.Assets[2] = p.Assets[2], p.Assets[1] }},
		{"asset duplicated", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.Assets = append(p.Assets, gliFxAsset("PCIeSwitch", "pci-bdf:"+gliFxSWDBDF))
		}},
		{"assets in descending order", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			for i, j := 0, len(p.Assets)-1; i < j; i, j = i+1, j-1 {
				p.Assets[i], p.Assets[j] = p.Assets[j], p.Assets[i]
			}
		}},
		{"edges swapped (evidence untouched)", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.Edges[0], p.Edges[1] = p.Edges[1], p.Edges[0] }},
		{"edge duplicated with its evidence", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			e, ev := p.Edges[5], p.EdgeEvidence[5]
			p.Edges = append(p.Edges, &ingestpb.Edge{FromKey: e.FromKey, Relation: e.Relation, ToKey: e.ToKey, Origin: e.Origin})
			p.EdgeEvidence = append(p.EdgeEvidence, &ingestpb.EdgeEvidence{
				EdgeIndex: 6, Kind: ev.Kind, SourceType: ev.SourceType, SourceName: ev.SourceName,
				ObservedAt: ev.ObservedAt, ExpiresAt: ev.ExpiresAt, EvidenceId: ev.EvidenceId,
			})
		}},
		{"edgeEvidence one short", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence = p.EdgeEvidence[:5] }},
		{"edgeEvidence one extra", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			ev := p.EdgeEvidence[5]
			p.EdgeEvidence = append(p.EdgeEvidence, &ingestpb.EdgeEvidence{
				EdgeIndex: 6, Kind: ev.Kind, SourceType: ev.SourceType, SourceName: ev.SourceName,
				ObservedAt: ev.ObservedAt, ExpiresAt: ev.ExpiresAt, EvidenceId: ev.EvidenceId + "x",
			})
		}},
		{"edgeEvidence edge_index 1 at position 0", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence[0].EdgeIndex = 1 }},
		{"edgeEvidence edge_index out of range", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence[5].EdgeIndex = 6 }},
		{"edgeEvidence order swapped", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.EdgeEvidence[0], p.EdgeEvidence[1] = p.EdgeEvidence[1], p.EdgeEvidence[0]
		}},
		{"observations swapped", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.Observations[1], p.Observations[2] = p.Observations[2], p.Observations[1]
		}},
		{"observation duplicated (same id)", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.Observations = append(p.Observations, p.Observations[5])
		}},
		{"observation with a duplicate id at another series", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			o := gliFxExtraObs(p, m, "gli.extra", gliFxInt(1), "x", nil)
			o.Id = gliFxFindObs(p, gliFxFnKey(gliFxGPUBDF), gliFxSigClass).Id
			gliFxSortPayload(p)
		}},
		{"gpuBindings with the same (bdf, uuid) twice", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.GpuBindings = append(p.GpuBindings, gliFxBinding(m.session, m.seq, m.at, gliFxUUID, gliFxGPUBDF))
		}},
		{"gpuBindings unsorted by bdf", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.GpuBindings = append(p.GpuBindings, gliFxBinding(m.session, m.seq, m.at, "GPU-00000000-0000-4000-8000-000000000001", "0000:02:00.0"))
		}},
		{"gpuBindings sorted by bdf then uuid are valid", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.GpuBindings = append(p.GpuBindings, gliFxBinding(m.session, m.seq, m.at, "GPU-00000000-0000-4000-8000-000000000001", "0000:03:00.0"))
			gliFxSortPayload(p)
		}},
		{"gpuBindings with the same uuid on two BDFs are structurally valid", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.GpuBindings = append(p.GpuBindings, gliFxBinding(m.session, m.seq, m.at, gliFxUUID, gliFxNICBDF))
			gliFxSortPayload(p)
		}},
		{"dimensions swapped", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			d := gliFxFindObs(p, gliFxFnKey(gliFxGPUBDF), gliFxSigCurrent).Dimensions
			d[0], d[1] = d[1], d[0]
		}},
		{"dimension key duplicated", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			o := gliFxFindObs(p, gliFxFnKey(gliFxGPUBDF), gliFxSigCurrent)
			o.Dimensions = append(o.Dimensions, &ingestpb.Dimension{Key: "pcie.root.canonical", Value: "pci-bdf:0000:00:09.0"})
		}},
		{"asset aliases unsorted", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.Assets[2].Aliases = []*ingestpb.Alias{{Namespace: "lldp-port-id", Value: "p2"}, {Namespace: "lldp-port-id", Value: "p1"}}
		}},
		{"asset alias duplicated", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.Assets[2].Aliases = []*ingestpb.Alias{{Namespace: "lldp-port-id", Value: "p1"}, {Namespace: "lldp-port-id", Value: "p1"}}
		}},
	}
	gliFxRunRows(t, base, rows, "GLI-060 (d)")
}

// GLI-060 V1 (e)/(f): references and deterministic IDs. An edge endpoint must be
// a payload asset; every evidence_id and observation id is the GFO-084 value of
// the stream session and the frame sequence; gpuBinding bdf is canonical and
// uuid has the GFO-070 grammar.
func TestGLI060_V1ReferencesAndIDs(t *testing.T) {
	base := gliFxStart(t)
	rows := []gliFxV1Row{
		{"edge to_key not in the assets", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxSetEdge(p, m, 0, func(e *ingestpb.Edge) { e.ToKey = gliFxSWKey("0000:0f:00.0") })
		}},
		{"edge from_key not in the assets", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxSetEdge(p, m, 0, func(e *ingestpb.Edge) { e.FromKey = gliFxFnKey("0000:0f:00.0") })
		}},
		{"edgeEvidence id with one changed character", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			id := p.EdgeEvidence[0].EvidenceId
			last := id[len(id)-1]
			rep := byte('0')
			if last == '0' {
				rep = '1'
			}
			p.EdgeEvidence[0].EvidenceId = id[:len(id)-1] + string(rep)
		}},
		{"edgeEvidence id of another session", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			e := p.Edges[0]
			p.EdgeEvidence[0].EvidenceId = gliFxEdgeID(m.session+1, m.seq, e.FromKey, e.Relation, e.ToKey, e.Origin)
		}},
		{"edgeEvidence id of another sequence", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			e := p.Edges[0]
			p.EdgeEvidence[0].EvidenceId = gliFxEdgeID(m.session, m.seq+1, e.FromKey, e.Relation, e.ToKey, e.Origin)
		}},
		{"edgeEvidence id empty", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence[0].EvidenceId = "" }},
		{"observation id of another session", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			o := p.Observations[0]
			o.Id = gliFxObsID(m.session+1, m.seq, gliFxAssetKey(o.Subject), o.Signal)
		}},
		{"observation id of another signal", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			o := p.Observations[0]
			o.Id = gliFxObsID(m.session, m.seq, gliFxAssetKey(o.Subject), "pcie.link.width.current")
		}},
		{"observation id upper-cased", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.Observations[0].Id = strings.ToUpper(p.Observations[0].Id)
		}},
		{"gpuBinding evidence_id of another uuid", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.GpuBindings[0].EvidenceId = gliFxBindID(m.session, m.seq, "GPU-00000000-0000-4000-8000-000000000001", gliFxGPUBDF)
		}},
		{"gpuBinding bdf with an eight digit domain", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			b := p.GpuBindings[0]
			b.Bdf = "00000000:03:00.0"
			b.EvidenceId = gliFxBindID(m.session, m.seq, b.Uuid, b.Bdf)
		}},
		{"gpuBinding bdf with upper-case hex", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			b := p.GpuBindings[0]
			b.Bdf = "0000:0A:00.0"
			b.EvidenceId = gliFxBindID(m.session, m.seq, b.Uuid, b.Bdf)
		}},
		{"gpuBinding bdf with device 0x20", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			b := p.GpuBindings[0]
			b.Bdf = "0000:03:20.0"
			b.EvidenceId = gliFxBindID(m.session, m.seq, b.Uuid, b.Bdf)
		}},
		{"gpuBinding bdf with function 8", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			b := p.GpuBindings[0]
			b.Bdf = "0000:03:00.8"
			b.EvidenceId = gliFxBindID(m.session, m.seq, b.Uuid, b.Bdf)
		}},
		{"gpuBinding uuid upper-case", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			b := p.GpuBindings[0]
			b.Uuid = strings.ToUpper(b.Uuid)
			b.EvidenceId = gliFxBindID(m.session, m.seq, b.Uuid, b.Bdf)
		}},
		{"gpuBinding uuid MIG prefix", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			b := p.GpuBindings[0]
			b.Uuid = "MIG-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d"
			b.EvidenceId = gliFxBindID(m.session, m.seq, b.Uuid, b.Bdf)
		}},
		{"gpuBinding uuid too short", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			b := p.GpuBindings[0]
			b.Uuid = "GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2"
			b.EvidenceId = gliFxBindID(m.session, m.seq, b.Uuid, b.Bdf)
		}},
		{"gpuBinding with a canonical bdf and an uppercase-free uuid is valid", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			b := p.GpuBindings[0]
			b.Uuid = "GPU-00000000-0000-4000-8000-00000000000a"
			b.EvidenceId = gliFxBindID(m.session, m.seq, b.Uuid, b.Bdf)
		}},
	}
	gliFxRunRows(t, base, rows, "GLI-060 (e)(f)")
	// The IDs of the previous frame are not valid for the next sequence.
	t.Run("IDs of the previous sequence", func(t *testing.T) {
		env := base.For(t)
		c := env.ConnectDefault()
		c.AcceptSeq(0, gliFxAt(0), true)
		before := env.Sig()
		stale := gliPayload(c.UID, c.Boot, c.Session, 0, gliFxAt(1)) // IDs of sequence 0, times of the next frame
		f := gliFrame(t, stale, c.Session, 1, gliFxAt(1), true)
		c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, "GLI-060 (f)")
		env.WantSigUnchanged(before, "GLI-045")
	})
}

// L-SPOOF-NODE, GLI-023, GLI-060 (c): a payload that names another node is
// INVALID (InvalidArgument) while the envelope node_uid of another node is a
// PermissionDenied (F2); the other node's state never changes.
func TestGLI023_PayloadNodeSpoofing(t *testing.T) {
	env := gliFxStart(t)
	b := env.Connect(gliFxOtherName, gliFxOtherUID, gliFxBootID)
	b.AcceptSeq(0, gliFxAt(0), true)
	bBefore := env.SigOf(gliFxOtherName, gliFxOtherUID)
	if bBefore == "no observation" {
		t.Fatalf("test bug: node B has no accepted view")
	}
	rows := []struct {
		name   string
		mut    func(t *testing.T, f *ingestpb.SnapshotFrame)
		ack    ingestpb.AckCode
		stream codes.Code
	}{
		{"payload KubernetesNode asset is node B (envelope node_uid is A)", func(t *testing.T, f *ingestpb.SnapshotFrame) {
			f.Payload = gliPayload(gliFxOtherUID, gliFxBootID, f.Session, f.Sequence, gliFxAt(0))
			gliReseal(t, f)
		}, ingestpb.AckCode_INVALID, codes.InvalidArgument},
		{"envelope node_uid is node B", func(t *testing.T, f *ingestpb.SnapshotFrame) { f.NodeUid = gliFxOtherUID }, ingestpb.AckCode_INVALID, codes.PermissionDenied},
		{"both are node B", func(t *testing.T, f *ingestpb.SnapshotFrame) {
			f.Payload = gliPayload(gliFxOtherUID, gliFxBootID, f.Session, f.Sequence, gliFxAt(0))
			f.NodeUid = gliFxOtherUID
			gliReseal(t, f)
		}, ingestpb.AckCode_INVALID, codes.PermissionDenied},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			e := env.For(t)
			a := e.ConnectDefault()
			aBefore := e.Sig()
			f := a.Frame(0, gliFxAt(0), true)
			r.mut(t, f)
			a.Reject(f, r.ack, r.stream, "GLI-023 L-SPOOF-NODE")
			e.WantSigUnchanged(aBefore, "GLI-045 node A")
			if got := e.SigOf(gliFxOtherName, gliFxOtherUID); got != bBefore {
				t.Errorf("L-SPOOF-NODE: node B's observation state changed by a frame of node A's certificate\nbefore:\n%s\nafter:\n%s", bBefore, got)
			}
		})
	}
}

// GLI-060 V2 (i)/(j): frame observed_at needs 86400 s of room at both ends of
// the GFO-033 range; every edgeEvidence, gpuBinding and observation time equals
// the frame observed_at; every expires_at is later; observation sequence equals
// the frame sequence. The boundary twins are accepted.
func TestGLI060_V2TimeEqualitiesAndRange(t *testing.T) {
	base := gliFxStart(t)
	lo := time.Date(1, 1, 2, 0, 0, 0, 1, time.UTC)
	hi := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC).Add(-86400 * time.Second)
	t.Run("frame time margins", func(t *testing.T) {
		cases := []struct {
			name string
			at   time.Time
			ok   bool
		}{
			{"lower margin exactly 86400 s above the minimum instant", lo, true},
			{"lower margin one nanosecond short", lo.Add(-time.Nanosecond), false},
			{"upper margin exactly 86400 s below the maximum instant", hi, true},
			{"upper margin one nanosecond short", hi.Add(time.Nanosecond), false},
			{"a frame time in the first day of the range", time.Date(1, 1, 1, 12, 0, 0, 0, time.UTC), false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				env := base.For(t)
				c := env.ConnectDefault()
				before := env.Sig()
				p := gliPayload(c.UID, c.Boot, c.Session, 0, tc.at)
				f := gliFrame(t, p, c.Session, 0, tc.at, true)
				if tc.ok {
					a := c.Ack(f)
					gliFxWantAck(t, a, ingestpb.AckCode_ACCEPTED, c.Session, 1, "GLI-060 (i) boundary twin")
					return
				}
				c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, "GLI-060 (i)")
				env.WantSigUnchanged(before, "GLI-045")
			})
		}
	})
	ns := time.Nanosecond
	rows := []gliFxV1Row{
		{"edgeEvidence observed_at one nanosecond late", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence[0].ObservedAt = gliFxTS(m.at.Add(ns)) }},
		{"edgeEvidence observed_at one nanosecond early", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence[0].ObservedAt = gliFxTS(m.at.Add(-ns)) }},
		{"gpuBinding observed_at one nanosecond late", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.GpuBindings[0].ObservedAt = gliFxTS(m.at.Add(ns)) }},
		{"gpuBinding observed_at one nanosecond early", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.GpuBindings[0].ObservedAt = gliFxTS(m.at.Add(-ns)) }},
		{"observation observed_at one nanosecond late", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.Observations[0].ObservedAt = gliFxTS(m.at.Add(ns)) }},
		{"observation received_at one nanosecond late", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.Observations[0].ReceivedAt = gliFxTS(m.at.Add(ns)) }},
		{"observation received_at one nanosecond early", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.Observations[0].ReceivedAt = gliFxTS(m.at.Add(-ns)) }},
		{"edgeEvidence expires_at equal to observed_at", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence[0].ExpiresAt = gliFxTS(m.at) }},
		{"edgeEvidence expires_at before observed_at", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.EdgeEvidence[0].ExpiresAt = gliFxTS(m.at.Add(-time.Second))
		}},
		{"edgeEvidence expires_at one nanosecond after observed_at is valid", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.EdgeEvidence[0].ExpiresAt = gliFxTS(m.at.Add(ns)) }},
		{"gpuBinding expires_at equal to observed_at", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.GpuBindings[0].ExpiresAt = gliFxTS(m.at) }},
		{"gpuBinding expires_at one nanosecond after observed_at is valid", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.GpuBindings[0].ExpiresAt = gliFxTS(m.at.Add(ns)) }},
		{"observation expires_at equal to observed_at", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.Observations[0].ExpiresAt = gliFxTS(m.at) }},
		{"observation expires_at before observed_at", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.Observations[0].ExpiresAt = gliFxTS(m.at.Add(-time.Second))
		}},
		{"observation expires_at one nanosecond after observed_at is valid", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.Observations[0].ExpiresAt = gliFxTS(m.at.Add(ns)) }},
		{"observation expires_at absent is valid", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.Observations[0].ExpiresAt = nil }},
		{"observation expires_at present as the zero instant", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			p.Observations[0].ExpiresAt = gliFxTS(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC))
		}},
		{"observation sequence is frame sequence + 1", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.Observations[0].Sequence = m.seq + 1 }},
	}
	gliFxRunRows(t, base, rows, "GLI-060 (j)")
	t.Run("envelope observed_at one nanosecond away from the payload times", func(t *testing.T) {
		env := base.For(t)
		c := env.ConnectDefault()
		before := env.Sig()
		f := c.Frame(0, gliFxAt(0), true)
		f.ObservedAt = gliFxTS(gliFxAt(0).Add(ns))
		c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, "GLI-060 (j)")
		env.WantSigUnchanged(before, "GLI-045")
	})
	t.Run("observation sequence is stale after the baseline", func(t *testing.T) {
		env := base.For(t)
		c := env.ConnectDefault()
		c.AcceptSeq(0, gliFxAt(0), true)
		before := env.Sig()
		p := gliPayload(c.UID, c.Boot, c.Session, 1, gliFxAt(1))
		p.Observations[0].Sequence = 0
		f := gliFrame(t, p, c.Session, 1, gliFxAt(1), true)
		c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, "GLI-060 (j)")
		env.WantSigUnchanged(before, "GLI-045")
	})
}

// GLI-060 V2 (k), L-SKEW: after the baseline a frame whose sequence and digest
// are valid but whose observed_at is not strictly later than the previous
// accepted frame (partial frames count) is INVALID with InvalidArgument and
// changes nothing; one nanosecond later is accepted.
func TestGLI060_LSKEWV2ObservedAtMonotonicity(t *testing.T) {
	base := gliFxStart(t)
	rows := []struct {
		name     string
		accepted []bool
		at       time.Time
		ok       bool
	}{
		{"equal to the baseline", []bool{true}, gliFxAt(0), false},
		{"earlier than the baseline", []bool{true}, gliFxAt(0).Add(-time.Second), false},
		{"one nanosecond earlier than the baseline", []bool{true}, gliFxAt(0).Add(-time.Nanosecond), false},
		{"one nanosecond later than the baseline", []bool{true}, gliFxAt(0).Add(time.Nanosecond), true},
		{"fifteen seconds later (control)", []bool{true}, gliFxAt(1), true},
		{"earlier than a partial previous frame", []bool{true, false}, gliFxAt(1).Add(-5 * time.Second), false},
		{"equal to a partial previous frame", []bool{true, false}, gliFxAt(1), false},
		{"one nanosecond later than a partial previous frame", []bool{true, false}, gliFxAt(1).Add(time.Nanosecond), true},
		{"earlier than the baseline but later than a partial (non monotonic chain)", []bool{true, false}, gliFxAt(0).Add(time.Second), false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			for i, complete := range r.accepted {
				c.AcceptSeq(uint64(i), gliFxAt(uint64(i)), complete)
			}
			before := env.Sig()
			seq := uint64(len(r.accepted))
			f := c.Frame(seq, r.at, true)
			if r.ok {
				if got := c.Classified(f, "GLI-060 (k) boundary twin"); got != ingestpb.AckCode_ACCEPTED {
					t.Fatalf("S1 classification %s, want ACCEPTED", got)
				}
				return
			}
			// S1 accepts it (it does not look at observed_at order); V2 (k) rejects it.
			if got, _ := (&gliFxOracle{session: c.Session, prev: c.oracle.prev}).Admit(f); got != ingestpb.AckCode_ACCEPTED {
				t.Fatalf("test bug: S1 classification %s, want ACCEPTED (so that only V2 (k) can reject)", got)
			}
			c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, "GLI-060 (k) L-SKEW")
			env.WantSigUnchanged(before, "GLI-045 L-SKEW")
		})
	}
}

// GLI-060 V1 (h), GLI-061, L-ALLOC-NESTED: allocation_batch rules and its place
// in the digest. The nested node_uid, boot_id, session and sequence must equal
// the frame's; the profile is a known enum; times are valid and expires_at is
// later; entries and evidence_refs are strictly increasing by raw bytes field
// by field (a short prefix first), not by their length-prefixed encoding.
func TestGLI060_AllocationBatchRulesAndDigest(t *testing.T) {
	base := gliFxStart(t)
	entry := func(res, dev, ns, pod string) *ingestpb.AllocationEntry {
		return &ingestpb.AllocationEntry{ResourceName: res, DeviceId: dev, PodNamespace: ns, PodName: pod, ContainerName: "main"}
	}
	validBatch := func(c *gliFxConn, p *ingestpb.HostSnapshotV1, seq uint64, at time.Time) *ingestpb.AllocationBatch {
		return &ingestpb.AllocationBatch{
			NodeUid: c.UID, BootId: c.Boot, Session: c.Session, Sequence: seq,
			ObservedAt: gliFxTS(at), ExpiresAt: gliFxTS(at.Add(gliFxTTL)), Complete: true,
			EvidenceRefs: []string{p.GpuBindings[0].EvidenceId, p.Observations[0].Id},
			Profile:      ingestpb.AllocationProfile_NVIDIA_PODRESOURCES_UUID,
			Entries: []*ingestpb.AllocationEntry{
				entry("nvidia.com/gpu", gliFxUUID, "ml", "b-long-name"),
				entry("nvidia.com/gpu", gliFxUUID, "ml", "z"),
			},
		}
	}
	rows := []struct {
		name string
		ok   bool
		mut  func(ab *ingestpb.AllocationBatch, c *gliFxConn)
	}{
		{"valid batch with the vector 2 entries", true, func(ab *ingestpb.AllocationBatch, c *gliFxConn) {}},
		{"empty entries and refs", true, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.Entries, ab.EvidenceRefs = nil, nil }},
		{"complete false", true, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.Complete = false }},
		{"profile UNSUPPORTED", true, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.Profile = ingestpb.AllocationProfile_UNSUPPORTED }},
		{"batch times differ from the frame time", true, func(ab *ingestpb.AllocationBatch, c *gliFxConn) {
			ab.ObservedAt = gliFxTS(gliFxAt(0).Add(-time.Hour))
			ab.ExpiresAt = gliFxTS(gliFxAt(0).Add(time.Hour))
		}},
		{"entries ordered by the first differing field (device_id GPU-10 before GPU-2)", true, func(ab *ingestpb.AllocationBatch, c *gliFxConn) {
			ab.Entries = []*ingestpb.AllocationEntry{entry("nvidia.com/gpu", "GPU-10", "ml", "p"), entry("nvidia.com/gpu", "GPU-2", "ml", "p")}
		}},
		{"entries ordered by pod_namespace before pod_name", true, func(ab *ingestpb.AllocationBatch, c *gliFxConn) {
			ab.Entries = []*ingestpb.AllocationEntry{entry("nvidia.com/gpu", gliFxUUID, "ml", "z"), entry("nvidia.com/gpu", gliFxUUID, "mm", "a")}
		}},
		{"entries ordered by resource_name first", true, func(ab *ingestpb.AllocationBatch, c *gliFxConn) {
			ab.Entries = []*ingestpb.AllocationEntry{entry("a/b", gliFxUUID, "zz", "z"), entry("nvidia.com/gpu", gliFxUUID, "aa", "a")}
		}},
		{"evidence_refs ordered by raw bytes (ev:10 before ev:2)", true, func(ab *ingestpb.AllocationBatch, c *gliFxConn) {
			ab.EvidenceRefs = []string{"ev:10", "ev:2"}
		}},
		{"nested node_uid of another node", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.NodeUid = gliFxOtherUID }},
		{"nested node_uid empty", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.NodeUid = "" }},
		{"nested boot_id of another boot", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.BootId = "00000000-0000-4000-8000-000000000000" }},
		{"nested session + 1", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.Session = c.Session + 1 }},
		{"nested session - 1", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.Session = c.Session - 1 }},
		{"nested session 0", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.Session = 0 }},
		{"nested sequence + 1", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.Sequence = 1 }},
		{"profile UNSPECIFIED", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) {
			ab.Profile = ingestpb.AllocationProfile_ALLOCATION_PROFILE_UNSPECIFIED
		}},
		{"profile with an unknown number", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.Profile = ingestpb.AllocationProfile(3) }},
		{"observed_at absent", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.ObservedAt = nil }},
		{"expires_at absent", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.ExpiresAt = nil }},
		{"expires_at equal to observed_at", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.ExpiresAt = gliFxTS(ab.ObservedAt.AsTime()) }},
		{"expires_at before observed_at", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) {
			ab.ExpiresAt = gliFxTS(ab.ObservedAt.AsTime().Add(-time.Second))
		}},
		{"entries swapped (b-long-name after z)", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) {
			ab.Entries[0], ab.Entries[1] = ab.Entries[1], ab.Entries[0]
		}},
		{"entry duplicated", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.Entries[1] = ab.Entries[0] }},
		{"entries device_id GPU-2 before GPU-10", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) {
			ab.Entries = []*ingestpb.AllocationEntry{entry("nvidia.com/gpu", "GPU-2", "ml", "p"), entry("nvidia.com/gpu", "GPU-10", "ml", "p")}
		}},
		{"entries by pod_name before pod_namespace", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) {
			ab.Entries = []*ingestpb.AllocationEntry{entry("nvidia.com/gpu", gliFxUUID, "mm", "a"), entry("nvidia.com/gpu", gliFxUUID, "ml", "z")}
		}},
		{"evidence_refs unsorted", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) {
			ab.EvidenceRefs[0], ab.EvidenceRefs[1] = ab.EvidenceRefs[1], ab.EvidenceRefs[0]
		}},
		{"evidence_refs duplicated", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.EvidenceRefs[1] = ab.EvidenceRefs[0] }},
		{"evidence_refs ev:2 before ev:10", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.EvidenceRefs = []string{"ev:2", "ev:10"} }},
		{"entry pod_name with a space", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.Entries[1].PodName = "z z" }},
		{"entry pod_name with a valid UTF-8 e-acute", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.Entries[1].PodName = "zé" }},
		{"entry resource_name empty", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.Entries[0].ResourceName = "" }},
		{"evidence_ref with a space", false, func(ab *ingestpb.AllocationBatch, c *gliFxConn) { ab.EvidenceRefs[1] = "ob: x" }},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			before := env.Sig()
			p := gliPayload(c.UID, c.Boot, c.Session, 0, gliFxAt(0))
			p.AllocationBatch = validBatch(c, p, 0, gliFxAt(0))
			r.mut(p.AllocationBatch, c)
			f := gliFrame(t, p, c.Session, 0, gliFxAt(0), true)
			if r.ok {
				a := c.Ack(f)
				gliFxWantAck(t, a, ingestpb.AckCode_ACCEPTED, c.Session, 1, "GLI-060 (h) boundary twin")
				// S3b-1 decodes, validates and digests allocationBatch only (GLI-002).
				b := env.One(gliFxAt(0).Add(time.Second), gliFxFresh)
				if b.Bundle.Allocation != nil || b.Bundle.Fence != nil || b.Bundle.FenceTrust != nil {
					t.Errorf("GLI-002: Bundle.Allocation/Fence/FenceTrust are filled by S3b-2, got %v/%v/%v", b.Bundle.Allocation, b.Bundle.Fence, b.Bundle.FenceTrust)
				}
				return
			}
			c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, "GLI-060 (h) L-ALLOC-NESTED")
			env.WantSigUnchanged(before, "GLI-045")
		})
	}
	// The allocationBatch is part of the canonical digest (GLI-061).
	digestRows := []struct {
		name   string
		digest func(p *ingestpb.HostSnapshotV1) [32]byte
	}{
		{"digest computed without the allocationBatch", func(p *ingestpb.HostSnapshotV1) [32]byte {
			q := gliFxClone(p)
			q.AllocationBatch = nil
			return gliDigest(q)
		}},
		{"digest computed with the entries swapped (wire order is encoded as is)", func(p *ingestpb.HostSnapshotV1) [32]byte {
			q := gliFxClone(p)
			e := q.AllocationBatch.Entries
			e[0], e[1] = e[1], e[0]
			return gliDigest(q)
		}},
		{"digest computed with the wire enum name instead of the profile literal", func(p *ingestpb.HostSnapshotV1) [32]byte {
			cp := gliCanonical(p)
			old := gliFxAppendStr(nil, "NVIDIAPodResourcesUUID")
			if bytes.Count(cp, old) != 1 {
				t.Fatalf("test bug: the profile literal occurs %d times in CP", bytes.Count(cp, old))
			}
			return sha256.Sum256(bytes.Replace(cp, old, gliFxAppendStr(nil, "NVIDIA_PODRESOURCES_UUID"), 1))
		}},
	}
	for _, r := range digestRows {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectDefault()
			p := gliPayload(c.UID, c.Boot, c.Session, 0, gliFxAt(0))
			p.AllocationBatch = validBatch(c, p, 0, gliFxAt(0))
			f := gliFrame(t, p, c.Session, 0, gliFxAt(0), true)
			d := r.digest(p)
			f.PayloadDigest = d[:]
			f.BundleRevision = fmt.Sprintf("%d:%d:%s", c.Session, 0, gliFxHex(d[:]))
			c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, "GLI-061 digest input")
		})
	}
}

// gliFxRawTS is a Timestamp exactly as written: timestamppb.New normalizes the
// seconds and nanos it is given, which would hide a CheckValid failure.
func gliFxRawTS(seconds int64, nanos int32) *timestamppb.Timestamp {
	return &timestamppb.Timestamp{Seconds: seconds, Nanos: nanos}
}

// GLI-011 (c), GLI-060 (c): a time that fails Timestamp.CheckValid (seconds
// outside 0001-01-01..9999-12-31, nanos outside 0..999999999) is INVALID for
// every element time field. The values are raw (not normalized) and the digest
// is built from the raw seconds and nanos, so that only the invalid time can be
// the reason; the values just inside the limits are accepted.
func TestGLI011_TimestampCheckValidFailuresOfExpiresAt(t *testing.T) {
	base := gliFxStart(t)
	batch := func(m gliFxMc) *ingestpb.AllocationBatch {
		return &ingestpb.AllocationBatch{
			NodeUid: gliFxNodeUID, BootId: gliFxBootID, Session: m.session, Sequence: m.seq,
			ObservedAt: gliFxTS(m.at), ExpiresAt: gliFxTS(m.at.Add(gliFxTTL)), Complete: true,
			Profile: ingestpb.AllocationProfile_NVIDIA_PODRESOURCES_UUID,
		}
	}
	targets := []struct {
		name string
		set  func(p *ingestpb.HostSnapshotV1, m gliFxMc, ts *timestamppb.Timestamp)
	}{
		{"edgeEvidence expires_at", func(p *ingestpb.HostSnapshotV1, m gliFxMc, ts *timestamppb.Timestamp) {
			p.EdgeEvidence[0].ExpiresAt = ts
		}},
		{"gpuBinding expires_at", func(p *ingestpb.HostSnapshotV1, m gliFxMc, ts *timestamppb.Timestamp) {
			p.GpuBindings[0].ExpiresAt = ts
		}},
		{"observation expires_at", func(p *ingestpb.HostSnapshotV1, m gliFxMc, ts *timestamppb.Timestamp) {
			p.Observations[0].ExpiresAt = ts
		}},
		{"allocation_batch expires_at", func(p *ingestpb.HostSnapshotV1, m gliFxMc, ts *timestamppb.Timestamp) {
			p.AllocationBatch = batch(m)
			p.AllocationBatch.ExpiresAt = ts
		}},
	}
	// secs is the valid expiry second of the frame (observed_at + 300 s).
	secs := func(m gliFxMc) int64 { return m.at.Add(gliFxTTL).Unix() }
	values := []struct {
		name string
		ok   bool
		mk   func(m gliFxMc) *timestamppb.Timestamp
	}{
		{"seconds 253402300800 (one above the maximum)", false, func(m gliFxMc) *timestamppb.Timestamp { return gliFxRawTS(253402300800, 0) }},
		{"seconds -62135596801 (one below the minimum)", false, func(m gliFxMc) *timestamppb.Timestamp { return gliFxRawTS(-62135596801, 0) }},
		{"nanos 1000000000 with valid seconds", false, func(m gliFxMc) *timestamppb.Timestamp { return gliFxRawTS(secs(m), 1000000000) }},
		{"nanos 1000000000 alone", false, func(m gliFxMc) *timestamppb.Timestamp { return gliFxRawTS(0, 1000000000) }},
		{"nanos -1 with valid seconds", false, func(m gliFxMc) *timestamppb.Timestamp { return gliFxRawTS(secs(m), -1) }},
		{"nanos -1 alone", false, func(m gliFxMc) *timestamppb.Timestamp { return gliFxRawTS(0, -1) }},
		{"seconds 253402300799 (the maximum) is valid", true, func(m gliFxMc) *timestamppb.Timestamp { return gliFxRawTS(253402300799, 0) }},
		{"nanos 999999999 is valid", true, func(m gliFxMc) *timestamppb.Timestamp { return gliFxRawTS(secs(m), 999999999) }},
		{"nanos 0 is valid", true, func(m gliFxMc) *timestamppb.Timestamp { return gliFxRawTS(secs(m), 0) }},
	}
	var rows []gliFxV1Row
	for _, tg := range targets {
		for _, v := range values {
			rows = append(rows, gliFxV1Row{tg.name + " " + v.name, v.ok, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
				tg.set(p, m, v.mk(m))
			}})
		}
	}
	gliFxRunRows(t, base, rows, "GLI-011 (c) CheckValid")
}
