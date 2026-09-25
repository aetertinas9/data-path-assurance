package tests_test

// Artifact decoding, independent re-encoding (GFO-083), canonical digest
// (GFO-086), deterministic IDs (GFO-084), ordering (GFO-085) and the
// consumer transformation M (GFO-090/091), all written from the spec text.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// ---------------------------------------------------------------------------
// Ordered JSON tree (keeps key order and raw number literals).
// ---------------------------------------------------------------------------

type gfoJ struct {
	kind  byte // '{' object, '[' array, 's' string, 'n' number, 'b' bool, 'z' null
	keys  []string
	vals  []*gfoJ
	elems []*gfoJ
	str   string
	num   string
	b     bool
}

func gfoParseJSON(data []byte) (*gfoJ, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := gfoParseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("data after the top-level value")
	}
	return v, nil
}

func gfoParseValue(dec *json.Decoder) (*gfoJ, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			o := &gfoJ{kind: '{'}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				for _, k := range o.keys {
					if k == key {
						return nil, fmt.Errorf("duplicate key %q", key)
					}
				}
				val, err := gfoParseValue(dec)
				if err != nil {
					return nil, err
				}
				o.keys = append(o.keys, key)
				o.vals = append(o.vals, val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			a := &gfoJ{kind: '['}
			for dec.More() {
				e, err := gfoParseValue(dec)
				if err != nil {
					return nil, err
				}
				a.elems = append(a.elems, e)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return a, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", v)
	case string:
		return &gfoJ{kind: 's', str: v}, nil
	case json.Number:
		return &gfoJ{kind: 'n', num: string(v)}, nil
	case bool:
		return &gfoJ{kind: 'b', b: v}, nil
	case nil:
		return &gfoJ{kind: 'z'}, nil
	}
	return nil, fmt.Errorf("unexpected token %T", tok)
}

func gfoFields(j *gfoJ, path string, keys ...string) ([]*gfoJ, error) {
	if j == nil || j.kind != '{' {
		return nil, fmt.Errorf("%s: want an object", path)
	}
	if strings.Join(j.keys, ",") != strings.Join(keys, ",") {
		return nil, fmt.Errorf("%s: keys [%s], want exactly [%s] in this order", path, strings.Join(j.keys, ","), strings.Join(keys, ","))
	}
	return j.vals, nil
}

func gfoGetStr(j *gfoJ, path string) (string, error) {
	if j == nil || j.kind != 's' {
		return "", fmt.Errorf("%s: want a string", path)
	}
	return j.str, nil
}

func gfoGetArr(j *gfoJ, path string) ([]*gfoJ, error) {
	if j == nil || j.kind != '[' {
		return nil, fmt.Errorf("%s: want an array", path)
	}
	return j.elems, nil
}

var gfoDecimalRE = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

func gfoGetInt(j *gfoJ, path string) (int, error) {
	if j == nil || j.kind != 'n' {
		return 0, fmt.Errorf("%s: want a JSON number", path)
	}
	if !gfoDecimalRE.MatchString(j.num) {
		return 0, fmt.Errorf("%s: number %q is not a decimal without sign, fraction, exponent or leading zero", path, j.num)
	}
	v, err := strconv.Atoi(j.num)
	if err != nil {
		return 0, fmt.Errorf("%s: %v", path, err)
	}
	return v, nil
}

func gfoGetBool(j *gfoJ, path string) (bool, error) {
	if j == nil || j.kind != 'b' {
		return false, fmt.Errorf("%s: want a boolean", path)
	}
	return j.b, nil
}

// ---------------------------------------------------------------------------
// Typed artifact (GFO-080..083).
// ---------------------------------------------------------------------------

type gfoArtifact struct {
	Raw           []byte
	SchemaVersion string
	Mode          string
	Limitations   []string
	ClusterID     string
	NodeName      string
	NodeUID       string
	BootID        string
	Trust         gfoTrust
	Frames        []gfoFrame
}

type gfoTrust struct {
	ProfileID, Mode, Session string
	Sources                  []gfoTrustSource
}

type gfoTrustSource struct{ Capability, Type, Name string }

type gfoFrame struct {
	NodeUID, BootID, Session, Sequence, Completeness, ObservedAt string
	Payload                                                      gfoPayload
	PayloadDigest, BundleRevision                                string
	Diagnostics                                                  []gfoDiag
	DiagnosticsTruncated                                         bool
	DiagnosticsTotal                                             int
}

type gfoPayload struct {
	Assets       []gfoAsset
	Edges        []gfoEdge
	EdgeEvidence []gfoEdgeEvidence
	Observations []gfoObs
	GPUBindings  []gfoBinding
}

type gfoAlias struct{ Namespace, Value string }

type gfoAsset struct {
	Kind, Canonical string
	Aliases         []gfoAlias
}

func (a gfoAsset) Key() string { return a.Kind + "/" + a.Canonical }

type gfoEdge struct{ FromKey, Relation, ToKey, Origin string }

type gfoEdgeEvidence struct {
	EdgeIndex                                                       int
	Kind, SourceType, SourceName, ObservedAt, ExpiresAt, EvidenceID string
}

type gfoKV struct{ K, V string }

type gfoObs struct {
	ID                                string
	SourceType, SourceName            string
	Subject                           gfoAsset
	Signal                            string
	ValueKind                         string // the single key of the value object
	ValueJSON                         string // compact JSON text of its member
	Unit                              string
	Dims                              []gfoKV
	ObservedAt, ReceivedAt, ExpiresAt string
	Sequence                          string
	Quality                           string
	RawDigest                         string
}

type gfoBinding struct {
	UUID, Serial, BDF, SourceType, SourceName, EvidenceID, ObservedAt, ExpiresAt string
}

type gfoDiag struct{ Code, Subject string }

// ---------------------------------------------------------------------------
// Decoding.
// ---------------------------------------------------------------------------

// gfoParseArtifact decodes stdout of a successful run, enforcing the exact
// key order of GFO-080..082 and the single trailing newline of GFO-012.
func gfoParseArtifact(raw []byte) (*gfoArtifact, error) {
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return nil, fmt.Errorf("GFO-012/GFO-083: stdout does not end with a single newline")
	}
	body := raw[:len(raw)-1]
	if bytes.IndexByte(body, '\n') >= 0 {
		return nil, fmt.Errorf("GFO-083: stdout has a newline before the final one (not compact)")
	}
	tree, err := gfoParseJSON(body)
	if err != nil {
		return nil, fmt.Errorf("GFO-083: stdout is not one JSON value: %v", err)
	}
	a := &gfoArtifact{Raw: raw}
	top, err := gfoFields(tree, "artifact", "schemaVersion", "mode", "limitations", "clusterID", "node", "bootID", "trust", "frames")
	if err != nil {
		return nil, fmt.Errorf("GFO-080: %v", err)
	}
	if a.SchemaVersion, err = gfoGetStr(top[0], "schemaVersion"); err != nil {
		return nil, err
	}
	if a.Mode, err = gfoGetStr(top[1], "mode"); err != nil {
		return nil, err
	}
	lims, err := gfoGetArr(top[2], "limitations")
	if err != nil {
		return nil, err
	}
	a.Limitations = []string{}
	for i, l := range lims {
		s, err := gfoGetStr(l, fmt.Sprintf("limitations[%d]", i))
		if err != nil {
			return nil, err
		}
		a.Limitations = append(a.Limitations, s)
	}
	if a.ClusterID, err = gfoGetStr(top[3], "clusterID"); err != nil {
		return nil, err
	}
	node, err := gfoFields(top[4], "node", "name", "uid")
	if err != nil {
		return nil, fmt.Errorf("GFO-080: %v", err)
	}
	if a.NodeName, err = gfoGetStr(node[0], "node.name"); err != nil {
		return nil, err
	}
	if a.NodeUID, err = gfoGetStr(node[1], "node.uid"); err != nil {
		return nil, err
	}
	if a.BootID, err = gfoGetStr(top[5], "bootID"); err != nil {
		return nil, err
	}
	if a.Trust, err = gfoParseTrust(top[6]); err != nil {
		return nil, err
	}
	frames, err := gfoGetArr(top[7], "frames")
	if err != nil {
		return nil, err
	}
	for i, fj := range frames {
		fr, err := gfoParseFrame(fj, fmt.Sprintf("frames[%d]", i))
		if err != nil {
			return nil, err
		}
		a.Frames = append(a.Frames, fr)
	}
	return a, nil
}

func gfoParseTrust(j *gfoJ) (gfoTrust, error) {
	var tr gfoTrust
	f, err := gfoFields(j, "trust", "profileID", "mode", "session", "sources")
	if err != nil {
		return tr, fmt.Errorf("GFO-080: %v", err)
	}
	if tr.ProfileID, err = gfoGetStr(f[0], "trust.profileID"); err != nil {
		return tr, err
	}
	if tr.Mode, err = gfoGetStr(f[1], "trust.mode"); err != nil {
		return tr, err
	}
	if tr.Session, err = gfoGetStr(f[2], "trust.session"); err != nil {
		return tr, fmt.Errorf("GFO-083: %v (64-bit session must be a decimal string)", err)
	}
	srcs, err := gfoGetArr(f[3], "trust.sources")
	if err != nil {
		return tr, err
	}
	tr.Sources = []gfoTrustSource{}
	for i, sj := range srcs {
		p := fmt.Sprintf("trust.sources[%d]", i)
		sf, err := gfoFields(sj, p, "capability", "source")
		if err != nil {
			return tr, fmt.Errorf("GFO-080: %v", err)
		}
		var s gfoTrustSource
		if s.Capability, err = gfoGetStr(sf[0], p+".capability"); err != nil {
			return tr, err
		}
		ref, err := gfoFields(sf[1], p+".source", "type", "name")
		if err != nil {
			return tr, fmt.Errorf("GFO-080: %v", err)
		}
		if s.Type, err = gfoGetStr(ref[0], p+".source.type"); err != nil {
			return tr, err
		}
		if s.Name, err = gfoGetStr(ref[1], p+".source.name"); err != nil {
			return tr, err
		}
		tr.Sources = append(tr.Sources, s)
	}
	return tr, nil
}

func gfoParseFrame(j *gfoJ, path string) (gfoFrame, error) {
	var fr gfoFrame
	f, err := gfoFields(j, path, "nodeUID", "bootID", "session", "sequence", "completeness", "observedAt",
		"payload", "payloadDigest", "bundleRevision", "diagnostics", "diagnosticsTruncated", "diagnosticsTotal")
	if err != nil {
		return fr, fmt.Errorf("GFO-081: %v", err)
	}
	strs := []*string{&fr.NodeUID, &fr.BootID, &fr.Session, &fr.Sequence, &fr.Completeness, &fr.ObservedAt}
	names := []string{"nodeUID", "bootID", "session", "sequence", "completeness", "observedAt"}
	for i, dst := range strs {
		if *dst, err = gfoGetStr(f[i], path+"."+names[i]); err != nil {
			return fr, fmt.Errorf("GFO-081/GFO-083: %v", err)
		}
	}
	if fr.Payload, err = gfoParsePayload(f[6], path+".payload"); err != nil {
		return fr, err
	}
	if fr.PayloadDigest, err = gfoGetStr(f[7], path+".payloadDigest"); err != nil {
		return fr, err
	}
	if fr.BundleRevision, err = gfoGetStr(f[8], path+".bundleRevision"); err != nil {
		return fr, err
	}
	diags, err := gfoGetArr(f[9], path+".diagnostics")
	if err != nil {
		return fr, err
	}
	fr.Diagnostics = []gfoDiag{}
	for i, dj := range diags {
		p := fmt.Sprintf("%s.diagnostics[%d]", path, i)
		df, err := gfoFields(dj, p, "code", "subject")
		if err != nil {
			return fr, fmt.Errorf("GFO-081: %v", err)
		}
		var d gfoDiag
		if d.Code, err = gfoGetStr(df[0], p+".code"); err != nil {
			return fr, err
		}
		if d.Subject, err = gfoGetStr(df[1], p+".subject"); err != nil {
			return fr, err
		}
		fr.Diagnostics = append(fr.Diagnostics, d)
	}
	if fr.DiagnosticsTruncated, err = gfoGetBool(f[10], path+".diagnosticsTruncated"); err != nil {
		return fr, err
	}
	if fr.DiagnosticsTotal, err = gfoGetInt(f[11], path+".diagnosticsTotal"); err != nil {
		return fr, fmt.Errorf("GFO-083: %v", err)
	}
	return fr, nil
}

func gfoParseAsset(j *gfoJ, path string) (gfoAsset, error) {
	var a gfoAsset
	f, err := gfoFields(j, path, "kind", "canonical", "aliases")
	if err != nil {
		return a, fmt.Errorf("GFO-082: %v", err)
	}
	if a.Kind, err = gfoGetStr(f[0], path+".kind"); err != nil {
		return a, err
	}
	if a.Canonical, err = gfoGetStr(f[1], path+".canonical"); err != nil {
		return a, err
	}
	als, err := gfoGetArr(f[2], path+".aliases")
	if err != nil {
		return a, err
	}
	a.Aliases = []gfoAlias{}
	for i, aj := range als {
		p := fmt.Sprintf("%s.aliases[%d]", path, i)
		af, err := gfoFields(aj, p, "namespace", "value")
		if err != nil {
			return a, fmt.Errorf("GFO-082: %v", err)
		}
		var al gfoAlias
		if al.Namespace, err = gfoGetStr(af[0], p+".namespace"); err != nil {
			return a, err
		}
		if al.Value, err = gfoGetStr(af[1], p+".value"); err != nil {
			return a, err
		}
		a.Aliases = append(a.Aliases, al)
	}
	return a, nil
}

func gfoParsePayload(j *gfoJ, path string) (gfoPayload, error) {
	var p gfoPayload
	f, err := gfoFields(j, path, "assets", "edges", "edgeEvidence", "observations", "gpuBindings")
	if err != nil {
		return p, fmt.Errorf("GFO-082: %v", err)
	}
	assets, err := gfoGetArr(f[0], path+".assets")
	if err != nil {
		return p, err
	}
	p.Assets = []gfoAsset{}
	for i, aj := range assets {
		a, err := gfoParseAsset(aj, fmt.Sprintf("%s.assets[%d]", path, i))
		if err != nil {
			return p, err
		}
		p.Assets = append(p.Assets, a)
	}
	edges, err := gfoGetArr(f[1], path+".edges")
	if err != nil {
		return p, err
	}
	p.Edges = []gfoEdge{}
	for i, ej := range edges {
		q := fmt.Sprintf("%s.edges[%d]", path, i)
		ef, err := gfoFields(ej, q, "fromKey", "relation", "toKey", "origin")
		if err != nil {
			return p, fmt.Errorf("GFO-082: %v", err)
		}
		var e gfoEdge
		for k, dst := range []*string{&e.FromKey, &e.Relation, &e.ToKey, &e.Origin} {
			if *dst, err = gfoGetStr(ef[k], q); err != nil {
				return p, err
			}
		}
		p.Edges = append(p.Edges, e)
	}
	evs, err := gfoGetArr(f[2], path+".edgeEvidence")
	if err != nil {
		return p, err
	}
	p.EdgeEvidence = []gfoEdgeEvidence{}
	for i, vj := range evs {
		q := fmt.Sprintf("%s.edgeEvidence[%d]", path, i)
		vf, err := gfoFields(vj, q, "edgeIndex", "kind", "sourceType", "sourceName", "observedAt", "expiresAt", "evidenceID")
		if err != nil {
			return p, fmt.Errorf("GFO-082: %v", err)
		}
		var ev gfoEdgeEvidence
		if ev.EdgeIndex, err = gfoGetInt(vf[0], q+".edgeIndex"); err != nil {
			return p, fmt.Errorf("GFO-083: %v", err)
		}
		for k, dst := range []*string{&ev.Kind, &ev.SourceType, &ev.SourceName, &ev.ObservedAt, &ev.ExpiresAt, &ev.EvidenceID} {
			if *dst, err = gfoGetStr(vf[k+1], q); err != nil {
				return p, err
			}
		}
		p.EdgeEvidence = append(p.EdgeEvidence, ev)
	}
	obs, err := gfoGetArr(f[3], path+".observations")
	if err != nil {
		return p, err
	}
	p.Observations = []gfoObs{}
	for i, oj := range obs {
		o, err := gfoParseObs(oj, fmt.Sprintf("%s.observations[%d]", path, i))
		if err != nil {
			return p, err
		}
		p.Observations = append(p.Observations, o)
	}
	bs, err := gfoGetArr(f[4], path+".gpuBindings")
	if err != nil {
		return p, err
	}
	p.GPUBindings = []gfoBinding{}
	for i, bj := range bs {
		q := fmt.Sprintf("%s.gpuBindings[%d]", path, i)
		bf, err := gfoFields(bj, q, "uuid", "serial", "bdf", "sourceType", "sourceName", "evidenceID", "observedAt", "expiresAt")
		if err != nil {
			return p, fmt.Errorf("GFO-082: %v", err)
		}
		var b gfoBinding
		for k, dst := range []*string{&b.UUID, &b.Serial, &b.BDF, &b.SourceType, &b.SourceName, &b.EvidenceID, &b.ObservedAt, &b.ExpiresAt} {
			if *dst, err = gfoGetStr(bf[k], q); err != nil {
				return p, err
			}
		}
		p.GPUBindings = append(p.GPUBindings, b)
	}
	return p, nil
}

func gfoParseObs(j *gfoJ, path string) (gfoObs, error) {
	var o gfoObs
	f, err := gfoFields(j, path, "id", "source", "subject", "signal", "value", "unit", "dimensions",
		"observedAt", "receivedAt", "expiresAt", "sequence", "quality", "rawDigest")
	if err != nil {
		return o, fmt.Errorf("GFO-082: %v", err)
	}
	if o.ID, err = gfoGetStr(f[0], path+".id"); err != nil {
		return o, err
	}
	src, err := gfoFields(f[1], path+".source", "type", "name")
	if err != nil {
		return o, fmt.Errorf("GFO-082: %v", err)
	}
	if o.SourceType, err = gfoGetStr(src[0], path+".source.type"); err != nil {
		return o, err
	}
	if o.SourceName, err = gfoGetStr(src[1], path+".source.name"); err != nil {
		return o, err
	}
	if o.Subject, err = gfoParseAsset(f[2], path+".subject"); err != nil {
		return o, err
	}
	if o.Signal, err = gfoGetStr(f[3], path+".signal"); err != nil {
		return o, err
	}
	v := f[4]
	if v == nil || v.kind != '{' || len(v.keys) != 1 {
		return o, fmt.Errorf("GFO-082: %s.value must be an object with exactly one key", path)
	}
	o.ValueKind = v.keys[0]
	switch o.ValueKind {
	case "int", "float", "string":
		if v.vals[0].kind != 's' {
			return o, fmt.Errorf("GFO-082/GFO-083: %s.value.%s must be a JSON string", path, o.ValueKind)
		}
		o.ValueJSON = gfoQuoteOut(v.vals[0].str)
	case "bool":
		if v.vals[0].kind != 'b' {
			return o, fmt.Errorf("GFO-082: %s.value.bool must be a JSON boolean", path)
		}
		o.ValueJSON = strconv.FormatBool(v.vals[0].b)
	default:
		return o, fmt.Errorf("GFO-082: %s.value key %q is not int|float|bool|string", path, o.ValueKind)
	}
	if o.Unit, err = gfoGetStr(f[5], path+".unit"); err != nil {
		return o, err
	}
	d := f[6]
	if d == nil || d.kind != '{' {
		return o, fmt.Errorf("GFO-082/GFO-083: %s.dimensions must be an object", path)
	}
	o.Dims = []gfoKV{}
	for i, k := range d.keys {
		val, err := gfoGetStr(d.vals[i], path+".dimensions."+k)
		if err != nil {
			return o, err
		}
		o.Dims = append(o.Dims, gfoKV{k, val})
	}
	for k, dst := range []*string{&o.ObservedAt, &o.ReceivedAt, &o.ExpiresAt, &o.Sequence, &o.Quality, &o.RawDigest} {
		if *dst, err = gfoGetStr(f[7+k], path); err != nil {
			return o, fmt.Errorf("GFO-082/GFO-083: %v", err)
		}
	}
	return o, nil
}

// ---------------------------------------------------------------------------
// Independent GFO-083 encoder.
// ---------------------------------------------------------------------------

// gfoQuoteOut quotes an output string escaping only '"' and '\' (GFO-083).
func gfoQuoteOut(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('"')
	return b.String()
}

type gfoW struct{ b strings.Builder }

func (w *gfoW) raw(s string)      { w.b.WriteString(s) }
func (w *gfoW) key(k string)      { w.b.WriteString(gfoQuoteOut(k) + ":") }
func (w *gfoW) kv(k, v string)    { w.key(k); w.b.WriteString(gfoQuoteOut(v)) }
func (w *gfoW) kvRaw(k, v string) { w.key(k); w.b.WriteString(v) }
func (w *gfoW) sep(i int)         { gfoSepIf(&w.b, i) }
func gfoSepIf(b *strings.Builder, i int) {
	if i > 0 {
		b.WriteByte(',')
	}
}

func (w *gfoW) asset(a gfoAsset) {
	w.raw("{")
	w.kv("kind", a.Kind)
	w.raw(",")
	w.kv("canonical", a.Canonical)
	w.raw(",")
	w.key("aliases")
	w.raw("[")
	for i, al := range a.Aliases {
		w.sep(i)
		w.raw("{")
		w.kv("namespace", al.Namespace)
		w.raw(",")
		w.kv("value", al.Value)
		w.raw("}")
	}
	w.raw("]}")
}

func (w *gfoW) payload(p gfoPayload) {
	w.raw("{")
	w.key("assets")
	w.raw("[")
	for i, a := range p.Assets {
		w.sep(i)
		w.asset(a)
	}
	w.raw("],")
	w.key("edges")
	w.raw("[")
	for i, e := range p.Edges {
		w.sep(i)
		w.raw("{")
		w.kv("fromKey", e.FromKey)
		w.raw(",")
		w.kv("relation", e.Relation)
		w.raw(",")
		w.kv("toKey", e.ToKey)
		w.raw(",")
		w.kv("origin", e.Origin)
		w.raw("}")
	}
	w.raw("],")
	w.key("edgeEvidence")
	w.raw("[")
	for i, ev := range p.EdgeEvidence {
		w.sep(i)
		w.raw("{")
		w.kvRaw("edgeIndex", strconv.Itoa(ev.EdgeIndex))
		w.raw(",")
		w.kv("kind", ev.Kind)
		w.raw(",")
		w.kv("sourceType", ev.SourceType)
		w.raw(",")
		w.kv("sourceName", ev.SourceName)
		w.raw(",")
		w.kv("observedAt", ev.ObservedAt)
		w.raw(",")
		w.kv("expiresAt", ev.ExpiresAt)
		w.raw(",")
		w.kv("evidenceID", ev.EvidenceID)
		w.raw("}")
	}
	w.raw("],")
	w.key("observations")
	w.raw("[")
	for i, o := range p.Observations {
		w.sep(i)
		w.raw("{")
		w.kv("id", o.ID)
		w.raw(",")
		w.key("source")
		w.raw("{")
		w.kv("type", o.SourceType)
		w.raw(",")
		w.kv("name", o.SourceName)
		w.raw("},")
		w.key("subject")
		w.asset(o.Subject)
		w.raw(",")
		w.kv("signal", o.Signal)
		w.raw(",")
		w.key("value")
		w.raw("{")
		w.kvRaw(o.ValueKind, o.ValueJSON)
		w.raw("},")
		w.kv("unit", o.Unit)
		w.raw(",")
		w.key("dimensions")
		w.raw("{")
		for k, kv := range o.Dims {
			w.sep(k)
			w.kv(kv.K, kv.V)
		}
		w.raw("},")
		w.kv("observedAt", o.ObservedAt)
		w.raw(",")
		w.kv("receivedAt", o.ReceivedAt)
		w.raw(",")
		w.kv("expiresAt", o.ExpiresAt)
		w.raw(",")
		w.kv("sequence", o.Sequence)
		w.raw(",")
		w.kv("quality", o.Quality)
		w.raw(",")
		w.kv("rawDigest", o.RawDigest)
		w.raw("}")
	}
	w.raw("],")
	w.key("gpuBindings")
	w.raw("[")
	for i, b := range p.GPUBindings {
		w.sep(i)
		w.raw("{")
		w.kv("uuid", b.UUID)
		w.raw(",")
		w.kv("serial", b.Serial)
		w.raw(",")
		w.kv("bdf", b.BDF)
		w.raw(",")
		w.kv("sourceType", b.SourceType)
		w.raw(",")
		w.kv("sourceName", b.SourceName)
		w.raw(",")
		w.kv("evidenceID", b.EvidenceID)
		w.raw(",")
		w.kv("observedAt", b.ObservedAt)
		w.raw(",")
		w.kv("expiresAt", b.ExpiresAt)
		w.raw("}")
	}
	w.raw("]}")
}

func (w *gfoW) frame(fr gfoFrame) {
	w.raw("{")
	w.kv("nodeUID", fr.NodeUID)
	w.raw(",")
	w.kv("bootID", fr.BootID)
	w.raw(",")
	w.kv("session", fr.Session)
	w.raw(",")
	w.kv("sequence", fr.Sequence)
	w.raw(",")
	w.kv("completeness", fr.Completeness)
	w.raw(",")
	w.kv("observedAt", fr.ObservedAt)
	w.raw(",")
	w.key("payload")
	w.payload(fr.Payload)
	w.raw(",")
	w.kv("payloadDigest", fr.PayloadDigest)
	w.raw(",")
	w.kv("bundleRevision", fr.BundleRevision)
	w.raw(",")
	w.key("diagnostics")
	w.raw("[")
	for i, d := range fr.Diagnostics {
		w.sep(i)
		w.raw("{")
		w.kv("code", d.Code)
		w.raw(",")
		w.kv("subject", d.Subject)
		w.raw("}")
	}
	w.raw("],")
	w.kvRaw("diagnosticsTruncated", strconv.FormatBool(fr.DiagnosticsTruncated))
	w.raw(",")
	w.kvRaw("diagnosticsTotal", strconv.Itoa(fr.DiagnosticsTotal))
	w.raw("}")
}

// gfoRenderArtifact encodes a typed artifact exactly as GFO-080..083 require,
// including the final newline.
func gfoRenderArtifact(a *gfoArtifact) []byte {
	w := &gfoW{}
	w.raw("{")
	w.kv("schemaVersion", a.SchemaVersion)
	w.raw(",")
	w.kv("mode", a.Mode)
	w.raw(",")
	w.key("limitations")
	w.raw("[")
	for i, l := range a.Limitations {
		w.sep(i)
		w.raw(gfoQuoteOut(l))
	}
	w.raw("],")
	w.kv("clusterID", a.ClusterID)
	w.raw(",")
	w.key("node")
	w.raw("{")
	w.kv("name", a.NodeName)
	w.raw(",")
	w.kv("uid", a.NodeUID)
	w.raw("},")
	w.kv("bootID", a.BootID)
	w.raw(",")
	w.key("trust")
	w.raw("{")
	w.kv("profileID", a.Trust.ProfileID)
	w.raw(",")
	w.kv("mode", a.Trust.Mode)
	w.raw(",")
	w.kv("session", a.Trust.Session)
	w.raw(",")
	w.key("sources")
	w.raw("[")
	for i, s := range a.Trust.Sources {
		w.sep(i)
		w.raw("{")
		w.kv("capability", s.Capability)
		w.raw(",")
		w.key("source")
		w.raw("{")
		w.kv("type", s.Type)
		w.raw(",")
		w.kv("name", s.Name)
		w.raw("}}")
	}
	w.raw("]},")
	w.key("frames")
	w.raw("[")
	for i, fr := range a.Frames {
		w.sep(i)
		w.frame(fr)
	}
	w.raw("]}\n")
	return []byte(w.b.String())
}

// gfoRenderPayload encodes one payload (for cross-run comparisons).
func gfoRenderPayload(p gfoPayload) string {
	w := &gfoW{}
	w.payload(p)
	return w.b.String()
}

// gfoRenderFrame encodes one frame object.
func gfoRenderFrame(fr gfoFrame) string {
	w := &gfoW{}
	w.frame(fr)
	return w.b.String()
}

// ---------------------------------------------------------------------------
// GFO-086 canonical bytes.
// ---------------------------------------------------------------------------

type gfoCPW struct{ b []byte }

func (c *gfoCPW) str(s string) { c.b = gfoAppendU32(c.b, uint32(len(s))); c.b = append(c.b, s...) }
func (c *gfoCPW) u32(v uint32) { c.b = gfoAppendU32(c.b, v) }
func (c *gfoCPW) u64(v uint64) { c.b = gfoAppendU64(c.b, v) }
func (c *gfoCPW) tm(t time.Time) {
	c.u64(uint64(t.Unix()))
	c.u32(uint32(t.Nanosecond()))
}

func (c *gfoCPW) asset(a gfoAsset) {
	c.str(a.Kind)
	c.str(a.Canonical)
	c.u32(uint32(len(a.Aliases)))
	for _, al := range a.Aliases {
		c.str(al.Namespace)
		c.str(al.Value)
	}
}

// gfoCanonicalPayload returns CP for a decoded payload.
func gfoCanonicalPayload(p gfoPayload) ([]byte, error) {
	c := &gfoCPW{}
	c.str("dpa.HostSnapshotV1.canonical.v1")
	c.u32(uint32(len(p.Assets)))
	for _, a := range p.Assets {
		c.asset(a)
	}
	c.u32(uint32(len(p.Edges)))
	for _, e := range p.Edges {
		c.str(e.FromKey)
		c.str(e.Relation)
		c.str(e.ToKey)
		c.str(e.Origin)
	}
	c.u32(uint32(len(p.EdgeEvidence)))
	for _, ev := range p.EdgeEvidence {
		obs, err1 := gfoTime(ev.ObservedAt)
		exp, err2 := gfoTime(ev.ExpiresAt)
		if err := errors.Join(err1, err2); err != nil {
			return nil, err
		}
		c.u32(uint32(ev.EdgeIndex))
		c.str(ev.Kind)
		c.str(ev.SourceType)
		c.str(ev.SourceName)
		c.tm(obs)
		c.tm(exp)
		c.str(ev.EvidenceID)
	}
	c.u32(uint32(len(p.Observations)))
	for _, o := range p.Observations {
		obs, err1 := gfoTime(o.ObservedAt)
		rec, err2 := gfoTime(o.ReceivedAt)
		exp, err3 := gfoTime(o.ExpiresAt)
		seq, err4 := strconv.ParseUint(o.Sequence, 10, 64)
		if err := errors.Join(err1, err2, err3, err4); err != nil {
			return nil, err
		}
		c.str(o.ID)
		c.str(o.SourceType)
		c.str(o.SourceName)
		c.asset(o.Subject)
		c.str(o.Signal)
		switch o.ValueKind {
		case "int":
			v, err := strconv.ParseInt(strings.Trim(o.ValueJSON, `"`), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("value int %s: %v", o.ValueJSON, err)
			}
			c.str("Int")
			c.u64(uint64(v))
		case "float":
			v, err := strconv.ParseFloat(strings.Trim(o.ValueJSON, `"`), 64)
			if err != nil {
				return nil, fmt.Errorf("value float %s: %v", o.ValueJSON, err)
			}
			c.str("Float")
			c.u64(math.Float64bits(v))
		case "bool":
			c.str("Bool")
			if o.ValueJSON == "true" {
				c.b = append(c.b, 1)
			} else {
				c.b = append(c.b, 0)
			}
		case "string":
			var s string
			if err := json.Unmarshal([]byte(o.ValueJSON), &s); err != nil {
				return nil, err
			}
			c.str("String")
			c.str(s)
		}
		c.str(o.Unit)
		dims := append([]gfoKV(nil), o.Dims...)
		sort.Slice(dims, func(i, j int) bool { return dims[i].K < dims[j].K })
		c.u32(uint32(len(dims)))
		for _, kv := range dims {
			c.str(kv.K)
			c.str(kv.V)
		}
		c.tm(obs)
		c.tm(rec)
		if exp.IsZero() {
			c.b = append(c.b, 0)
		} else {
			c.b = append(c.b, 1)
			c.tm(exp)
		}
		c.u64(seq)
		c.str(o.Quality)
		c.str(o.RawDigest)
	}
	c.u32(uint32(len(p.GPUBindings)))
	for _, b := range p.GPUBindings {
		obs, err1 := gfoTime(b.ObservedAt)
		exp, err2 := gfoTime(b.ExpiresAt)
		if err := errors.Join(err1, err2); err != nil {
			return nil, err
		}
		c.str(b.UUID)
		c.str(b.Serial)
		c.str(b.BDF)
		c.str(b.SourceType)
		c.str(b.SourceName)
		c.str(b.EvidenceID)
		c.tm(obs)
		c.tm(exp)
	}
	c.b = append(c.b, 0x00)
	return c.b, nil
}

func gfoPayloadDigest(p gfoPayload) (string, error) {
	cp, err := gfoCanonicalPayload(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cp)
	return hex.EncodeToString(sum[:]), nil
}

// ---------------------------------------------------------------------------
// Time helpers (GFO-033, GFO-083).
// ---------------------------------------------------------------------------

var gfoOutTimeRE = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]*[1-9])?Z$`)

// gfoTime parses an output timestamp and requires the GFO-083 form.
func gfoTime(s string) (time.Time, error) {
	if !gfoOutTimeRE.MatchString(s) {
		return time.Time{}, fmt.Errorf("GFO-083: time %q is not UTC RFC3339Nano (Z, trimmed fraction, 4-digit year)", s)
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("GFO-083: time %q: %v", s, err)
	}
	if t.UTC().Format(time.RFC3339Nano) != s {
		return time.Time{}, fmt.Errorf("GFO-083: time %q is not in canonical RFC3339Nano form", s)
	}
	return t.UTC(), nil
}

// gfoUTC converts a manifest observedAt (valid input) to the output form.
func gfoUTC(t *testing.T, in string) string {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, in)
	if err != nil {
		t.Fatalf("test bug: cannot parse %q: %v", in, err)
	}
	return v.UTC().Format(time.RFC3339Nano)
}

func gfoAddSeconds(t *testing.T, out string, secs int64) string {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, out)
	if err != nil {
		t.Fatalf("test bug: cannot parse %q: %v", out, err)
	}
	return v.Add(time.Duration(secs) * time.Second).UTC().Format(time.RFC3339Nano)
}

// ---------------------------------------------------------------------------
// Frame queries.
// ---------------------------------------------------------------------------

func (fr gfoFrame) HasDiag(code, subject string) bool {
	for _, d := range fr.Diagnostics {
		if d.Code == code && d.Subject == subject {
			return true
		}
	}
	return false
}

func (fr gfoFrame) CountCode(code string) int {
	n := 0
	for _, d := range fr.Diagnostics {
		if d.Code == code {
			n++
		}
	}
	return n
}

func (fr gfoFrame) DiagString() string {
	var parts []string
	for _, d := range fr.Diagnostics {
		parts = append(parts, d.Code+"("+d.Subject+")")
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func (p gfoPayload) HasAsset(key string) bool {
	for _, a := range p.Assets {
		if a.Key() == key {
			return true
		}
	}
	return false
}

func (p gfoPayload) AssetKeys() []string {
	keys := []string{}
	for _, a := range p.Assets {
		keys = append(keys, a.Key())
	}
	return keys
}

func (p gfoPayload) HasEdge(from, to string) bool {
	for _, e := range p.Edges {
		if e.FromKey == from && e.ToKey == to {
			return true
		}
	}
	return false
}

func (p gfoPayload) EdgesFrom(from string) []gfoEdge {
	var out []gfoEdge
	for _, e := range p.Edges {
		if e.FromKey == from {
			out = append(out, e)
		}
	}
	return out
}

func (p gfoPayload) EdgeStrings() []string {
	out := []string{}
	for _, e := range p.Edges {
		out = append(out, e.FromKey+" -> "+e.ToKey)
	}
	return out
}

func (p gfoPayload) Obs(subjectKey, signal string) []gfoObs {
	var out []gfoObs
	for _, o := range p.Observations {
		if o.Subject.Key() == subjectKey && o.Signal == signal {
			out = append(out, o)
		}
	}
	return out
}

func (o gfoObs) Dim(k string) string {
	for _, kv := range o.Dims {
		if kv.K == k {
			return kv.V
		}
	}
	return ""
}

// IntValue returns the Int value of an observation ("" when not Int).
func (o gfoObs) IntValue() string {
	if o.ValueKind != "int" {
		return ""
	}
	return strings.Trim(o.ValueJSON, `"`)
}

// WidthPair returns (current, expected) observations of F, or ok=false when
// neither exists. Having exactly one is reported by gfoCheckArtifact.
func (p gfoPayload) WidthPair(bdf string) (cur, exp gfoObs, ok bool) {
	c := p.Obs(gfoFnKey(bdf), gfoSigCurrent)
	e := p.Obs(gfoFnKey(bdf), gfoSigExpect)
	if len(c) == 1 && len(e) == 1 {
		return c[0], e[0], true
	}
	return gfoObs{}, gfoObs{}, false
}

func (p gfoPayload) Binding(bdf string) (gfoBinding, bool) {
	for _, b := range p.GPUBindings {
		if b.BDF == bdf {
			return b, true
		}
	}
	return gfoBinding{}, false
}

// ---------------------------------------------------------------------------
// Invariants applied to every exit-0 artifact.
// ---------------------------------------------------------------------------

var (
	gfoHex64RE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gfoBDFRE     = regexp.MustCompile(`^([0-9a-f]{4}|[1-9a-f][0-9a-f]{4,7}):[0-9a-f]{2}:[01][0-9a-f]\.[0-7]$`)
	gfoUUIDRE    = regexp.MustCompile(`^GPU-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	gfoSubjectRE = regexp.MustCompile(`^([\x21-\x24\x26-\x7e]|%[0-9A-F]{2})+$`)
	gfoWidthSet  = map[string]bool{"1": true, "2": true, "4": true, "8": true, "12": true, "16": true, "32": true}
	gfoIntJSONRE = regexp.MustCompile(`^"(0|-?[1-9][0-9]*)"$`)
	// gfoDiagCodes is the GFO-051 closed code set.
	gfoDiagCodes = map[string]bool{
		"sysfs_list_failed": true, "sysfs_entry_name_invalid": true, "sysfs_link_invalid": true, "sysfs_link_escape": true,
		"sysfs_ancestor_missing": true, "sysfs_ancestor_not_bridge": true, "sysfs_layout_unsupported": true,
		"sysfs_attr_missing": true, "sysfs_attr_permission": true, "sysfs_attr_unreadable": true, "sysfs_attr_malformed": true,
		"sysfs_attr_nodata": true, "topology_contradiction": true, "width_omitted": true, "baseline_peer_mismatch": true,
		"baseline_unmatched": true, "nvidia_output_absent": true, "nvidia_output_unreadable": true, "nvidia_output_limit": true,
		"nvidia_empty": true, "nvidia_malformed": true, "nvidia_duplicate": true, "nvidia_bdf_not_gpu": true, "nvidia_gpu_unlisted": true,
	}
	// gfoAlwaysPart are the GFO-051 codes that always make a frame PARTIAL.
	gfoAlwaysPart = map[string]bool{
		"sysfs_list_failed": true, "sysfs_entry_name_invalid": true, "sysfs_link_invalid": true,
		"sysfs_link_escape": true, "sysfs_ancestor_missing": true, "sysfs_ancestor_not_bridge": true,
	}
)

func gfoCompareDims(a, b []gfoKV) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := strings.Compare(a[i].K, b[i].K); c != 0 {
			return c
		}
		if c := strings.Compare(a[i].V, b[i].V); c != 0 {
			return c
		}
	}
	return len(a) - len(b)
}

// gfoCompareObs is the GFO-085 observation order.
func gfoCompareObs(a, b gfoObs) int {
	for _, c := range []int{
		strings.Compare(a.Subject.Key(), b.Subject.Key()),
		strings.Compare(a.Signal, b.Signal),
		strings.Compare(a.SourceType, b.SourceType),
		strings.Compare(a.SourceName, b.SourceName),
		gfoCompareDims(a.Dims, b.Dims),
	} {
		if c != 0 {
			return c
		}
	}
	ta, _ := gfoTime(a.ObservedAt)
	tb, _ := gfoTime(b.ObservedAt)
	if c := ta.Compare(tb); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

func gfoCompareEdge(a, b gfoEdge) int {
	for _, c := range []int{
		strings.Compare(a.FromKey, b.FromKey), strings.Compare(a.ToKey, b.ToKey),
		strings.Compare(a.Relation, b.Relation), strings.Compare(a.Origin, b.Origin),
	} {
		if c != 0 {
			return c
		}
	}
	return 0
}

// gfoCheckArtifact applies every artifact-wide rule of GFO-012, 047, 051,
// 060(a), 064, 080..087, 091 and 095. m (optional) enables manifest checks
// (GFO-032..035).
func gfoCheckArtifact(t *testing.T, a *gfoArtifact, m *gfoManifest, clause string) {
	t.Helper()
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf(clause+": "+format, args...)
	}
	// GFO-083: bytes, escaping, compact form (independent re-encoding).
	for i, c := range a.Raw[:len(a.Raw)-1] {
		if c < 0x20 || c > 0x7e {
			fail("GFO-083: output byte %d is 0x%02x, want 0x20..0x7e", i, c)
			break
		}
	}
	if re := gfoRenderArtifact(a); !bytes.Equal(re, a.Raw) {
		fail("GFO-083: stdout differs from the spec encoding of its own content at byte %d\n got: %s\nwant: %s",
			gfoDiffAt(a.Raw, re), gfoAround(a.Raw, gfoDiffAt(a.Raw, re)), gfoAround(re, gfoDiffAt(a.Raw, re)))
	}
	// GFO-080.
	if a.SchemaVersion != gfoSchemaOut || a.Mode != "offline" || len(a.Limitations) != 1 || a.Limitations[0] != "offline" {
		fail("GFO-080: schemaVersion/mode/limitations = %q/%q/%q", a.SchemaVersion, a.Mode, a.Limitations)
	}
	if a.Trust.Mode != "Offline" {
		fail("GFO-080: trust.mode %q, want Offline", a.Trust.Mode)
	}
	for i := 1; i < len(a.Trust.Sources); i++ {
		p, q := a.Trust.Sources[i-1], a.Trust.Sources[i]
		if strings.Join([]string{p.Capability, p.Type, p.Name}, "\x00") >= strings.Join([]string{q.Capability, q.Type, q.Name}, "\x00") {
			fail("GFO-080: trust.sources not in strict (capability,type,name) byte order at %d", i)
		}
	}
	if !gfoDecimalRE.MatchString(a.Trust.Session) {
		fail("GFO-083: trust.session %q is not a decimal string", a.Trust.Session)
	}
	if m != nil {
		gfoCheckManifestEcho(t, a, m, clause)
	}
	if len(a.Frames) == 0 || len(a.Frames) > 16 {
		fail("GFO-081: %d frames, want 1..16", len(a.Frames))
	}
	var prev time.Time
	for i, fr := range a.Frames {
		obs, err := gfoTime(fr.ObservedAt)
		if err != nil {
			fail("frame %d: %v", i, err)
		}
		if i > 0 && !obs.After(prev) {
			fail("GFO-033/GFO-081: frame %d observedAt %s not after previous", i, fr.ObservedAt)
		}
		prev = obs
		gfoCheckFrame(t, a, i, m, clause)
	}
	gfoCheckRatified(t, a, clause)
}

// gfoCheckManifestEcho checks GFO-032/034/080/081 echoes of manifest values.
func gfoCheckManifestEcho(t *testing.T, a *gfoArtifact, m *gfoManifest, clause string) {
	t.Helper()
	if a.ClusterID != m.Cluster || a.NodeName != m.NodeName || a.NodeUID != m.NodeUID || a.BootID != m.BootID {
		t.Errorf("%s: GFO-080: identity %q/%q/%q/%q, want manifest %q/%q/%q/%q", clause,
			a.ClusterID, a.NodeName, a.NodeUID, a.BootID, m.Cluster, m.NodeName, m.NodeUID, m.BootID)
	}
	if a.Trust.ProfileID != m.Profile || a.Trust.Session != m.Session {
		t.Errorf("%s: GFO-034: trust profile/session %q/%q, want %q/%q", clause, a.Trust.ProfileID, a.Trust.Session, m.Profile, m.Session)
	}
	want := []string{}
	for _, s := range m.Sources {
		want = append(want, s.Cap+"\x00"+s.Type+"\x00"+s.Name)
	}
	sort.Strings(want)
	got := []string{}
	for _, s := range a.Trust.Sources {
		got = append(got, s.Capability+"\x00"+s.Type+"\x00"+s.Name)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s: GFO-034/GFO-080: trust.sources %q, want the manifest sources sorted %q", clause, got, want)
	}
	if len(a.Frames) != len(m.Frames) {
		t.Errorf("%s: GFO-081: %d frames, want %d", clause, len(a.Frames), len(m.Frames))
		return
	}
	ttl, ok := m.ttlSeconds()
	for i, fr := range a.Frames {
		if w := gfoUTC(t, m.Frames[i]); fr.ObservedAt != w {
			t.Errorf("%s: GFO-033: frame %d observedAt %q, want %q", clause, i, fr.ObservedAt, w)
		}
		if !ok {
			continue
		}
		wantExp := gfoAddSeconds(t, gfoUTC(t, m.Frames[i]), ttl)
		for _, ev := range fr.Payload.EdgeEvidence {
			if ev.ExpiresAt != wantExp {
				t.Errorf("%s: GFO-033: frame %d edgeEvidence expiresAt %q, want %q", clause, i, ev.ExpiresAt, wantExp)
				break
			}
		}
		for _, o := range fr.Payload.Observations {
			if o.ExpiresAt != wantExp {
				t.Errorf("%s: GFO-033/GFO-064: frame %d observation expiresAt %q, want %q", clause, i, o.ExpiresAt, wantExp)
				break
			}
		}
		for _, b := range fr.Payload.GPUBindings {
			if b.ExpiresAt != wantExp {
				t.Errorf("%s: GFO-033/GFO-075: frame %d binding expiresAt %q, want %q", clause, i, b.ExpiresAt, wantExp)
				break
			}
		}
		// GFO-061: a baseline for a selected F with a pair must win.
		for _, bl := range m.Baselines {
			if cur, exp, ok := fr.Payload.WidthPair(bl.Function); ok {
				if exp.Dim("pcie.expected.provenance") != gfoProvOp || exp.IntValue() != bl.Width ||
					exp.Dim("pcie.peer.canonical") != "pci-bdf:"+bl.Peer || cur.Dim("pcie.expected.provenance") != gfoProvOp {
					t.Errorf("%s: GFO-061: frame %d F %s has a width pair %s=%s peer %s, want %s=%s peer %s", clause, i, bl.Function,
						exp.Dim("pcie.expected.provenance"), exp.IntValue(), exp.Dim("pcie.peer.canonical"), gfoProvOp, bl.Width, "pci-bdf:"+bl.Peer)
				}
			}
		}
	}
}

func gfoCheckFrame(t *testing.T, a *gfoArtifact, i int, m *gfoManifest, clause string) {
	t.Helper()
	fr := a.Frames[i]
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf("%s: frame %d: "+format, append([]any{clause, i}, args...)...)
	}
	// GFO-081.
	if fr.NodeUID != a.NodeUID || fr.BootID != a.BootID || fr.Session != a.Trust.Session || fr.Sequence != strconv.Itoa(i) {
		fail("GFO-081: nodeUID/bootID/session/sequence = %q/%q/%q/%q, want %q/%q/%q/%q",
			fr.NodeUID, fr.BootID, fr.Session, fr.Sequence, a.NodeUID, a.BootID, a.Trust.Session, strconv.Itoa(i))
	}
	if fr.Completeness != "COMPLETE" && fr.Completeness != "PARTIAL" {
		fail("GFO-081: completeness %q", fr.Completeness)
	}
	// GFO-086/087.
	if !gfoHex64RE.MatchString(fr.PayloadDigest) {
		fail("GFO-081: payloadDigest %q is not 64 lower-case hex", fr.PayloadDigest)
	}
	if d, err := gfoPayloadDigest(fr.Payload); err != nil {
		fail("GFO-086: cannot build canonical bytes: %v", err)
	} else if d != fr.PayloadDigest {
		fail("GFO-086: payloadDigest %s, independent canonical digest %s", fr.PayloadDigest, d)
	}
	if want := fr.Session + ":" + fr.Sequence + ":" + fr.PayloadDigest; fr.BundleRevision != want || len(fr.BundleRevision) > 128 {
		fail("GFO-087: bundleRevision %q, want %q (<=128 bytes)", fr.BundleRevision, want)
	}
	obsAt := fr.ObservedAt
	var expAt string
	// GFO-047, GFO-085 assets.
	p := fr.Payload
	kinds := map[string]string{}
	nodes := 0
	for k, as := range p.Assets {
		if k > 0 && p.Assets[k-1].Key() >= as.Key() {
			fail("GFO-085: assets not in strict Key() byte order at %d (%s, %s)", k, p.Assets[k-1].Key(), as.Key())
		}
		if len(as.Aliases) != 0 {
			fail("GFO-047: asset %s has %d aliases, want 0", as.Key(), len(as.Aliases))
		}
		switch as.Kind {
		case "KubernetesNode":
			nodes++
			if as.Canonical != "kubernetes-node-uid:"+a.NodeUID {
				fail("GFO-047: node asset canonical %q", as.Canonical)
			}
		case "PCIeFunction", "PCIeRootPort", "PCIeSwitch":
			if !strings.HasPrefix(as.Canonical, "pci-bdf:") || !gfoBDFRE.MatchString(strings.TrimPrefix(as.Canonical, "pci-bdf:")) {
				fail("GFO-047/GFO-040: asset canonical %q is not pci-bdf:<canonical BDF>", as.Canonical)
			}
		default:
			fail("GFO-047: unexpected asset kind %q", as.Kind)
		}
		kinds[as.Key()] = as.Kind
	}
	if nodes != 1 {
		fail("GFO-047: %d KubernetesNode assets, want exactly 1", nodes)
	}
	nodeKey := gfoNodeKey(a.NodeUID)
	// Edges and edgeEvidence (GFO-048, 082, 084, 085, 091).
	out := map[string][]string{}
	for k, e := range p.Edges {
		if k > 0 && gfoCompareEdge(p.Edges[k-1], e) >= 0 {
			fail("GFO-085: edges not in strict graph edge order at %d", k)
		}
		if e.Relation != "LOCATED_IN" || e.Origin != "Observed" {
			fail("GFO-048/GFO-082: edge %s -> %s relation/origin %q/%q", e.FromKey, e.ToKey, e.Relation, e.Origin)
		}
		if kinds[e.FromKey] == "" || kinds[e.ToKey] == "" {
			fail("GFO-091: edge endpoint missing from payload assets: %s -> %s", e.FromKey, e.ToKey)
		}
		if e.FromKey == e.ToKey || e.FromKey == nodeKey {
			fail("GFO-048/GFO-049: invalid edge %s -> %s", e.FromKey, e.ToKey)
		}
		out[e.FromKey] = append(out[e.FromKey], e.ToKey)
	}
	if len(p.EdgeEvidence) != len(p.Edges) {
		fail("GFO-082: %d edgeEvidence for %d edges, want exactly one each", len(p.EdgeEvidence), len(p.Edges))
	}
	ids := map[string]bool{}
	for k, ev := range p.EdgeEvidence {
		if ev.EdgeIndex != k || ev.Kind != "Observed" || ev.SourceType != "agent" || ev.SourceName != gfoParentSrc {
			fail("GFO-082: edgeEvidence[%d] = index %d kind %q source %q/%q", k, ev.EdgeIndex, ev.Kind, ev.SourceType, ev.SourceName)
		}
		if ev.ObservedAt != obsAt {
			fail("GFO-082: edgeEvidence[%d] observedAt %q, want frame %q", k, ev.ObservedAt, obsAt)
		}
		if expAt == "" {
			expAt = ev.ExpiresAt
		}
		if ev.ExpiresAt != expAt {
			fail("GFO-033: expiresAt differs inside the frame (%q vs %q)", ev.ExpiresAt, expAt)
		}
		if k < len(p.Edges) {
			if want := gfoEdgeEvidenceID(fr.Session, fr.Sequence, p.Edges[k]); ev.EvidenceID != want {
				fail("GFO-084: edgeEvidence[%d] evidenceID %q, want %q", k, ev.EvidenceID, want)
			}
		}
		if ids[ev.EvidenceID] || len(ev.EvidenceID) > 128 {
			fail("GFO-084: evidenceID %q duplicated or longer than 128 bytes", ev.EvidenceID)
		}
		ids[ev.EvidenceID] = true
	}
	// Observations (GFO-060(a), 064, 084, 085).
	classes := map[string]int{}
	for k, o := range p.Observations {
		if k > 0 && gfoCompareObs(p.Observations[k-1], o) >= 0 {
			fail("GFO-085: observations not in strict GFO-085 order at %d", k)
		}
		key := o.Subject.Key()
		if want := gfoObservationID(fr.Session, fr.Sequence, key, o.Signal); o.ID != want {
			fail("GFO-084: observation %s/%s id %q, want %q", key, o.Signal, o.ID, want)
		}
		if ids[o.ID] || len(o.ID) > 128 {
			fail("GFO-084: observation id %q duplicated or longer than 128 bytes", o.ID)
		}
		ids[o.ID] = true
		if kinds[key] != "PCIeFunction" || o.Subject.Kind != "PCIeFunction" || len(o.Subject.Aliases) != 0 {
			fail("GFO-064: observation subject %s is not a payload PCIeFunction asset without aliases", key)
		}
		if o.ObservedAt != obsAt || o.ReceivedAt != obsAt || o.Sequence != fr.Sequence || o.Quality != "Good" || o.RawDigest != "" {
			fail("GFO-064: observation %s/%s observedAt/receivedAt/sequence/quality/rawDigest = %q/%q/%q/%q/%q",
				key, o.Signal, o.ObservedAt, o.ReceivedAt, o.Sequence, o.Quality, o.RawDigest)
		}
		if expAt == "" {
			expAt = o.ExpiresAt
		}
		if o.ExpiresAt != expAt {
			fail("GFO-064: observation expiresAt %q, want frame expiresAt %q", o.ExpiresAt, expAt)
		}
		for d := 1; d < len(o.Dims); d++ {
			if o.Dims[d-1].K >= o.Dims[d].K {
				fail("GFO-083: dimensions keys not in strict byte order for %s/%s", key, o.Signal)
			}
		}
		if o.ValueKind != "int" || !gfoIntJSONRE.MatchString(o.ValueJSON) {
			fail("GFO-064/GFO-083: observation %s/%s value %s=%s, want int as decimal string", key, o.Signal, o.ValueKind, o.ValueJSON)
		}
		switch o.Signal {
		case gfoSigClass:
			classes[key]++
			v, _ := strconv.ParseInt(o.IntValue(), 10, 64)
			if o.SourceType != "agent" || o.SourceName != gfoParentSrc || o.Unit != "pci_class" || len(o.Dims) != 0 || v < 0 || v > 0xffffff {
				fail("GFO-064: class observation of %s = source %s/%s unit %q dims %v value %s", key, o.SourceType, o.SourceName, o.Unit, o.Dims, o.IntValue())
			}
		case gfoSigCurrent, gfoSigExpect:
			if o.SourceType != "agent" || o.SourceName != gfoWidthSrc || o.Unit != "lanes" || !gfoWidthSet[o.IntValue()] {
				fail("GFO-064: width observation %s/%s = source %s/%s unit %q value %s", key, o.Signal, o.SourceType, o.SourceName, o.Unit, o.IntValue())
			}
			var dk []string
			for _, kv := range o.Dims {
				dk = append(dk, kv.K)
			}
			if strings.Join(dk, ",") != "pcie.expected.provenance,pcie.peer.canonical,pcie.peer.kind,pcie.root.canonical" {
				fail("GFO-064: width observation %s dimensions keys %v", key, dk)
			}
			if prov := o.Dim("pcie.expected.provenance"); prov != gfoProvOp && prov != gfoProvAdj {
				fail("GFO-064: provenance %q", prov)
			}
		default:
			fail("GFO-064: unexpected signal %q", o.Signal)
		}
	}
	for key, kind := range kinds {
		if kind == "PCIeFunction" && classes[key] != 1 {
			fail("GFO-064: selected function %s has %d class observations, want 1", key, classes[key])
		}
	}
	// Width pairs: both or none, identical dimensions, GFO-060(a) chain.
	for key, kind := range kinds {
		if kind != "PCIeFunction" {
			continue
		}
		c, e := p.Obs(key, gfoSigCurrent), p.Obs(key, gfoSigExpect)
		if len(c) != len(e) || len(c) > 1 {
			fail("GFO-060: %s has %d current and %d expected width observations, want a pair or none", key, len(c), len(e))
			continue
		}
		if len(c) == 0 {
			continue
		}
		if gfoCompareDims(c[0].Dims, e[0].Dims) != 0 {
			fail("GFO-064: %s width pair dimensions differ", key)
		}
		// Walk the unique chain F -> ... -> node.
		cur, seen, chain := key, map[string]bool{key: true}, []string{key}
		ok := true
		for cur != nodeKey {
			nx := out[cur]
			if len(nx) != 1 || seen[nx[0]] {
				ok = false
				break
			}
			cur = nx[0]
			seen[cur] = true
			chain = append(chain, cur)
		}
		if !ok || len(chain) < 3 {
			fail("GFO-060(a): %s has a width pair but no unique non-cyclic chain with a parent to the node (chain %v)", key, chain)
			continue
		}
		parent, rp := chain[1], chain[len(chain)-2]
		if kinds[rp] != "PCIeRootPort" {
			fail("GFO-060(a): %s node-preceding asset %s is %s, want PCIeRootPort", key, rp, kinds[rp])
		}
		pk := kinds[parent]
		if c[0].Dim("pcie.peer.canonical") != strings.TrimPrefix(parent, pk+"/") || c[0].Dim("pcie.peer.kind") != pk ||
			c[0].Dim("pcie.root.canonical") != strings.TrimPrefix(rp, "PCIeRootPort/") {
			fail("GFO-064: %s width dimensions %v, want peer %s (%s) and root %s", key, c[0].Dims, parent, pk, rp)
		}
	}
	// gpuBindings (GFO-075, 084, 085).
	for k, b := range p.GPUBindings {
		if k > 0 && (p.GPUBindings[k-1].BDF+"\x00"+p.GPUBindings[k-1].UUID) >= (b.BDF+"\x00"+b.UUID) {
			fail("GFO-085: gpuBindings not in strict (bdf, uuid) order at %d", k)
		}
		if !gfoUUIDRE.MatchString(b.UUID) || b.Serial != "" || b.SourceType != "agent" || b.SourceName != gfoNVIDIASrc {
			fail("GFO-075: binding %+v", b)
		}
		if kinds[gfoFnKey(b.BDF)] != "PCIeFunction" {
			fail("GFO-075: binding BDF %s is not a payload PCIeFunction", b.BDF)
		}
		if b.ObservedAt != obsAt || (expAt != "" && b.ExpiresAt != expAt) {
			fail("GFO-075: binding times %q/%q, want frame %q/%q", b.ObservedAt, b.ExpiresAt, obsAt, expAt)
		}
		if want := gfoBindingID(fr.Session, fr.Sequence, b.UUID, b.BDF); b.EvidenceID != want {
			fail("GFO-084: binding evidenceID %q, want %q", b.EvidenceID, want)
		}
		if ids[b.EvidenceID] || len(b.EvidenceID) > 128 {
			fail("GFO-084: binding evidenceID %q duplicated or longer than 128 bytes", b.EvidenceID)
		}
		ids[b.EvidenceID] = true
	}
	// GFO-095 payload bounds.
	if len(p.Assets) > 4096 || len(p.Edges) > 8192 || len(p.EdgeEvidence) > 8192 || len(p.Observations) > 32768 || len(p.GPUBindings) > 256 {
		fail("GFO-095: exit 0 with payload counts over the bound (%d assets, %d edges, %d obs, %d bindings)",
			len(p.Assets), len(p.Edges), len(p.Observations), len(p.GPUBindings))
	}
	// GFO-051 diagnostics.
	for k, d := range fr.Diagnostics {
		if !gfoDiagCodes[d.Code] {
			fail("GFO-051: diagnostic code %q not in the closed set", d.Code)
		}
		if !gfoSubjectRE.MatchString(d.Subject) {
			fail("GFO-051: diagnostic subject %q is empty or not escaped as %%XX", d.Subject)
		}
		if k > 0 {
			prev := fr.Diagnostics[k-1]
			if prev.Code > d.Code || (prev.Code == d.Code && prev.Subject >= d.Subject) {
				fail("GFO-051: diagnostics not merged and sorted by (code, subject) at %d", k)
			}
		}
		if fr.Completeness == "COMPLETE" && gfoAlwaysPart[d.Code] {
			fail("GFO-050: COMPLETE frame has PARTIAL-inducing diagnostic %s(%s)", d.Code, d.Subject)
		}
		if fr.Completeness == "COMPLETE" && strings.HasPrefix(d.Code, "sysfs_attr_") &&
			(strings.HasSuffix(d.Subject, "/class") || strings.HasSuffix(d.Subject, "/vendor") || strings.HasSuffix(d.Subject, "/physfn")) {
			fail("GFO-050: COMPLETE frame has class/vendor/physfn diagnostic %s(%s)", d.Code, d.Subject)
		}
	}
	if len(fr.Diagnostics) > 256 || fr.DiagnosticsTotal < len(fr.Diagnostics) ||
		fr.DiagnosticsTruncated != (fr.DiagnosticsTotal > 256) ||
		(!fr.DiagnosticsTruncated && fr.DiagnosticsTotal != len(fr.Diagnostics)) ||
		(fr.DiagnosticsTruncated && len(fr.Diagnostics) != 256) {
		fail("GFO-051: %d diagnostics, total %d, truncated %v", len(fr.Diagnostics), fr.DiagnosticsTotal, fr.DiagnosticsTruncated)
	}
	if fr.Completeness == "PARTIAL" && fr.DiagnosticsTotal == 0 {
		fail("GFO-050/GFO-051: PARTIAL frame without any diagnostic")
	}
	_ = m
}

func gfoDiffAt(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func gfoAround(b []byte, i int) string {
	lo, hi := i-80, i+80
	if lo < 0 {
		lo = 0
	}
	if hi > len(b) {
		hi = len(b)
	}
	return string(b[lo:hi])
}

// ---------------------------------------------------------------------------
// GFO-090 transformation M and GFO-091 validation.
// ---------------------------------------------------------------------------

// gfoVendorNVIDIA is the consumer NVIDIA vendor constant V of GFO-090 (T-05).
const gfoVendorNVIDIA = "NVIDIA"

type gfoRatified struct {
	Envelope     fleet.SnapshotEnvelope
	Profile      fleet.CollectorTrustProfile
	Node         model.AssetRef
	Assets       []model.AssetRef
	ByKey        map[string]model.AssetRef
	Edges        []graph.Edge
	Provenance   []fleet.EdgeProvenance
	Observations []model.Observation
	Bindings     []fleet.ObservedBinding
}

func gfoKindOf(s string) (model.AssetKind, bool) {
	for _, k := range []model.AssetKind{model.KindKubernetesNode, model.KindPCIeRootPort, model.KindPCIeSwitch, model.KindPCIeFunction} {
		if k.String() == s {
			return k, true
		}
	}
	return 0, false
}

func gfoCapabilityOf(s string) (fleet.TrustCapability, bool) {
	for _, c := range []fleet.TrustCapability{
		fleet.TrustNVIDIAUUIDBinding, fleet.TrustSysfsPhysicalParent, fleet.TrustSysfsPCIeWidth,
		fleet.TrustPodResourcesUUIDAllocation, fleet.TrustOperatorBaseline, fleet.TrustExternalFence,
	} {
		if c.String() == s {
			return c, true
		}
	}
	return 0, false
}

func gfoRatifyAsset(a gfoAsset) (model.AssetRef, error) {
	kind, ok := gfoKindOf(a.Kind)
	if !ok {
		return model.AssetRef{}, fmt.Errorf("unknown kind %q", a.Kind)
	}
	id, err := identity.ParseCanonical(a.Canonical)
	if err != nil {
		return model.AssetRef{}, fmt.Errorf("identity.ParseCanonical(%q): %w", a.Canonical, err)
	}
	return model.NewAssetRef(kind, id)
}

// gfoApplyM applies the GFO-090 transformation to frame i.
func gfoApplyM(a *gfoArtifact, i int) (gfoRatified, error) {
	var r gfoRatified
	fr := a.Frames[i]
	session, err := strconv.ParseInt(fr.Session, 10, 64)
	if err != nil {
		return r, fmt.Errorf("session %q: %v", fr.Session, err)
	}
	seq, err := strconv.ParseUint(fr.Sequence, 10, 64)
	if err != nil {
		return r, fmt.Errorf("sequence %q: %v", fr.Sequence, err)
	}
	obsAt, err := gfoTime(fr.ObservedAt)
	if err != nil {
		return r, err
	}
	completeness := fleet.CompletenessComplete
	if fr.Completeness == "PARTIAL" {
		completeness = fleet.CompletenessPartial
	}
	r.Envelope = fleet.SnapshotEnvelope{
		NodeUID: fr.NodeUID, BootID: fr.BootID, PayloadDigest: fr.PayloadDigest, BundleRevision: fr.BundleRevision,
		Session: session, Sequence: seq, Completeness: completeness, ObservedAt: obsAt,
	}
	trustSession, err := strconv.ParseInt(a.Trust.Session, 10, 64)
	if err != nil {
		return r, fmt.Errorf("trust.session %q: %v", a.Trust.Session, err)
	}
	r.Profile = fleet.CollectorTrustProfile{ID: a.Trust.ProfileID, Mode: fleet.TrustModeOffline, ClusterID: a.ClusterID, NodeUID: a.NodeUID, Session: trustSession}
	for _, s := range a.Trust.Sources {
		c, ok := gfoCapabilityOf(s.Capability)
		if !ok {
			return r, fmt.Errorf("capability %q has no Trust constant", s.Capability)
		}
		r.Profile.Sources = append(r.Profile.Sources, fleet.TrustedSource{Capability: c, Source: model.SourceRef{Type: s.Type, Name: s.Name}})
	}
	r.ByKey = map[string]model.AssetRef{}
	for _, as := range fr.Payload.Assets {
		ref, err := gfoRatifyAsset(as)
		if err != nil {
			return r, fmt.Errorf("asset %s: %w", as.Key(), err)
		}
		if ref.Key() != as.Key() {
			return r, fmt.Errorf("asset %s: ratified Key() %s", as.Key(), ref.Key())
		}
		r.Assets = append(r.Assets, ref)
		r.ByKey[as.Key()] = ref
		if as.Kind == "KubernetesNode" {
			r.Node = ref
		}
	}
	for _, e := range fr.Payload.Edges {
		from, ok1 := r.ByKey[e.FromKey]
		to, ok2 := r.ByKey[e.ToKey]
		if !ok1 || !ok2 {
			return r, fmt.Errorf("edge %s -> %s endpoint not in payload assets", e.FromKey, e.ToKey)
		}
		edge, err := graph.NewEdge(from, to, model.RelLocatedIn, model.OriginObserved)
		if err != nil {
			return r, fmt.Errorf("graph.NewEdge(%s, %s): %w", e.FromKey, e.ToKey, err)
		}
		r.Edges = append(r.Edges, edge)
	}
	for _, ev := range fr.Payload.EdgeEvidence {
		if ev.EdgeIndex < 0 || ev.EdgeIndex >= len(r.Edges) {
			return r, fmt.Errorf("edgeEvidence index %d out of range", ev.EdgeIndex)
		}
		o, err1 := gfoTime(ev.ObservedAt)
		x, err2 := gfoTime(ev.ExpiresAt)
		if err := errors.Join(err1, err2); err != nil {
			return r, err
		}
		r.Provenance = append(r.Provenance, fleet.EdgeProvenance{
			Edge: r.Edges[ev.EdgeIndex], EvidenceID: ev.EvidenceID, BundleRevision: fr.BundleRevision,
			CollectorProfileID: a.Trust.ProfileID, Kind: fleet.EdgeEvidenceObserved,
			Source: model.SourceRef{Type: ev.SourceType, Name: ev.SourceName}, ObservedAt: o, ExpiresAt: x,
		})
	}
	for _, o := range fr.Payload.Observations {
		subject, err := gfoRatifyAsset(o.Subject)
		if err != nil {
			return r, fmt.Errorf("observation subject: %w", err)
		}
		v, err := strconv.ParseInt(o.IntValue(), 10, 64)
		if err != nil {
			return r, fmt.Errorf("observation %s value: %v", o.ID, err)
		}
		oa, err1 := gfoTime(o.ObservedAt)
		ra, err2 := gfoTime(o.ReceivedAt)
		xa, err3 := gfoTime(o.ExpiresAt)
		oseq, err4 := strconv.ParseUint(o.Sequence, 10, 64)
		if err := errors.Join(err1, err2, err3, err4); err != nil {
			return r, err
		}
		dims := map[string]string{}
		for _, kv := range o.Dims {
			dims[kv.K] = kv.V
		}
		quality := model.QualityUnknown
		if o.Quality == model.QualityGood.String() {
			quality = model.QualityGood
		}
		obs, err := model.NewObservation(model.Observation{
			ID: o.ID, Source: model.SourceRef{Type: o.SourceType, Name: o.SourceName}, Subject: subject,
			Signal: model.SignalRef(o.Signal), Value: model.NewIntValue(v), Unit: o.Unit, Dimensions: dims,
			ObservedAt: oa, ReceivedAt: ra, ExpiresAt: xa, Sequence: oseq, Quality: quality, RawDigest: o.RawDigest,
		})
		if err != nil {
			return r, fmt.Errorf("model.NewObservation(%s): %w", o.ID, err)
		}
		r.Observations = append(r.Observations, obs)
	}
	for _, b := range fr.Payload.GPUBindings {
		fn, ok := r.ByKey["PCIeFunction/pci-bdf:"+b.BDF]
		if !ok {
			return r, fmt.Errorf("binding BDF %s has no PCIeFunction asset", b.BDF)
		}
		o, err1 := gfoTime(b.ObservedAt)
		x, err2 := gfoTime(b.ExpiresAt)
		if err := errors.Join(err1, err2); err != nil {
			return r, err
		}
		r.Bindings = append(r.Bindings, fleet.ObservedBinding{
			Node: fleet.NodeRef{ClusterID: a.ClusterID, Name: a.NodeName, UID: a.NodeUID}, BootID: a.BootID, BDF: b.BDF,
			Function: fn,
			Claim:    fleet.InventoryClaim{Vendor: gfoVendorNVIDIA, UUID: b.UUID, Serial: "", Source: b.SourceName, EvidenceID: b.EvidenceID},
			Source:   model.SourceRef{Type: b.SourceType, Name: b.SourceName}, EvidenceID: b.EvidenceID,
			BundleRevision: fr.BundleRevision, CollectorProfileID: a.Trust.ProfileID, ObservedAt: o, ExpiresAt: x,
		})
	}
	return r, nil
}

// gfoCheckRatified applies M to every frame and requires every ratified value
// to validate and graph.NewResync to accept the topology (GFO-091).
func gfoCheckRatified(t *testing.T, a *gfoArtifact, clause string) {
	t.Helper()
	for i := range a.Frames {
		r, err := gfoApplyM(a, i)
		if err != nil {
			t.Errorf("%s: GFO-090/GFO-091: frame %d: %v", clause, i, err)
			continue
		}
		if err := r.Envelope.Validate(); err != nil {
			t.Errorf("%s: GFO-091: frame %d SnapshotEnvelope.Validate: %v", clause, i, err)
		}
		if err := r.Profile.Validate(); err != nil {
			t.Errorf("%s: GFO-091: CollectorTrustProfile.Validate: %v", clause, err)
		}
		for _, as := range r.Assets {
			if err := as.Validate(); err != nil {
				t.Errorf("%s: GFO-091: frame %d asset %s Validate: %v", clause, i, as.Key(), err)
			}
		}
		for _, e := range r.Edges {
			if err := e.Validate(); err != nil {
				t.Errorf("%s: GFO-091: frame %d edge Validate: %v", clause, i, err)
			}
		}
		for _, pv := range r.Provenance {
			if err := pv.Validate(); err != nil {
				t.Errorf("%s: GFO-091: frame %d EdgeProvenance.Validate(%s): %v", clause, i, pv.EvidenceID, err)
			}
		}
		for _, o := range r.Observations {
			if err := o.Validate(); err != nil {
				t.Errorf("%s: GFO-091: frame %d Observation.Validate(%s): %v", clause, i, o.ID, err)
			}
		}
		for _, b := range r.Bindings {
			if err := b.Validate(); err != nil {
				t.Errorf("%s: GFO-091: frame %d ObservedBinding.Validate(%s): %v", clause, i, b.EvidenceID, err)
			}
		}
		part, err := graph.PartitionFor(r.Node)
		if err != nil {
			t.Errorf("%s: GFO-091: frame %d graph.PartitionFor(node): %v", clause, i, err)
			continue
		}
		if _, err := graph.NewResync(part, r.Envelope.Sequence, r.Assets, r.Edges); err != nil {
			t.Errorf("%s: GFO-091: frame %d graph.NewResync: %v", clause, i, err)
		}
	}
}
