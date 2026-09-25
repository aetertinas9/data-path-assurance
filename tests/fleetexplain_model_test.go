package tests_test

// Explanation output model: decoding with the exact §6 key order and
// omission rules, structural checks (GFX-060..069), JSON tree utilities and
// the independent §7 text reconstruction from the JSON output (GFX-070..078).

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// JSON tree construction, encoding and comparison.
// ---------------------------------------------------------------------------

type gfxKV struct {
	K string
	V *gfoJ
}

func gfxObj(kvs ...gfxKV) *gfoJ {
	o := &gfoJ{kind: '{'}
	for _, kv := range kvs {
		o.keys = append(o.keys, kv.K)
		o.vals = append(o.vals, kv.V)
	}
	return o
}

func gfxS(s string) *gfoJ         { return &gfoJ{kind: 's', str: s} }
func gfxN(n int) *gfoJ            { return &gfoJ{kind: 'n', num: strconv.Itoa(n)} }
func gfxBool(b bool) *gfoJ        { return &gfoJ{kind: 'b', b: b} }
func gfxArr(elems ...*gfoJ) *gfoJ { return &gfoJ{kind: '[', elems: append([]*gfoJ{}, elems...)} }

func gfxStrArr(items []string) *gfoJ {
	a := gfxArr()
	for _, s := range items {
		a.elems = append(a.elems, gfxS(s))
	}
	return a
}

// gfxEncode writes the compact JSON form escaping only '"' and '\' (GFX-069).
func gfxEncode(j *gfoJ) string {
	var b strings.Builder
	gfxEncodeTo(&b, j)
	return b.String()
}

func gfxEncodeTo(b *strings.Builder, j *gfoJ) {
	switch j.kind {
	case '{':
		b.WriteByte('{')
		for i, k := range j.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(gfoQuoteOut(k))
			b.WriteByte(':')
			gfxEncodeTo(b, j.vals[i])
		}
		b.WriteByte('}')
	case '[':
		b.WriteByte('[')
		for i, e := range j.elems {
			if i > 0 {
				b.WriteByte(',')
			}
			gfxEncodeTo(b, e)
		}
		b.WriteByte(']')
	case 's':
		b.WriteString(gfoQuoteOut(j.str))
	case 'n':
		b.WriteString(j.num)
	case 'b':
		b.WriteString(strconv.FormatBool(j.b))
	default:
		b.WriteString("null")
	}
}

// gfxDiffJ returns "" when a and b are equal and otherwise a description of
// the first difference with its JSON path.
func gfxDiffJ(a, b *gfoJ, path string) string {
	if a == nil || b == nil {
		if a == b {
			return ""
		}
		return path + ": one side missing"
	}
	if a.kind != b.kind {
		return fmt.Sprintf("%s: got %s, want %s", path, gfxShort(a), gfxShort(b))
	}
	switch a.kind {
	case '{':
		for i := 0; i < len(a.keys) || i < len(b.keys); i++ {
			switch {
			case i >= len(a.keys):
				return fmt.Sprintf("%s: missing key %q (got keys %v)", path, b.keys[i], a.keys)
			case i >= len(b.keys):
				return fmt.Sprintf("%s: unexpected key %q (want keys %v)", path, a.keys[i], b.keys)
			case a.keys[i] != b.keys[i]:
				return fmt.Sprintf("%s: key #%d is %q, want %q (got keys %v, want %v)", path, i, a.keys[i], b.keys[i], a.keys, b.keys)
			}
			if d := gfxDiffJ(a.vals[i], b.vals[i], path+"."+a.keys[i]); d != "" {
				return d
			}
		}
	case '[':
		for i := 0; i < len(a.elems) && i < len(b.elems); i++ {
			if d := gfxDiffJ(a.elems[i], b.elems[i], fmt.Sprintf("%s[%d]", path, i)); d != "" {
				return d
			}
		}
		if len(a.elems) != len(b.elems) {
			return fmt.Sprintf("%s: %d elements, want %d", path, len(a.elems), len(b.elems))
		}
	default:
		if gfxEncode(a) != gfxEncode(b) {
			return fmt.Sprintf("%s: got %s, want %s", path, gfxShort(a), gfxShort(b))
		}
	}
	return ""
}

func gfxShort(j *gfoJ) string {
	s := gfxEncode(j)
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	return s
}

func gfxGet(j *gfoJ, key string) (*gfoJ, bool) {
	if j == nil || j.kind != '{' {
		return nil, false
	}
	for i, k := range j.keys {
		if k == key {
			return j.vals[i], true
		}
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Decoded explanation.
// ---------------------------------------------------------------------------

type gfxSeg struct {
	From, Relation, To, Origin, Kind, SourceType, SourceName, EvidenceID, ObservedAt, ExpiresAt string
}

type gfxEvRef struct{ ObservationID, Summary string }

type gfxAffected struct{ Asset, Accuracy string }

type gfxFindingOut struct {
	ID, Type, Severity, State, Confidence string
	Scope                                 []string
	Evidence                              []gfxEvRef
	MissingInputs                         []string
	Affected                              []gfxAffected
	FirstSeen, LastSeen                   string
	Explanation, SuggestedStep            string
}

type gfxCovOut struct {
	HasDevice                               bool
	Device, Name, PathKind                  string
	Required                                bool
	State, Reason                           string
	EvidenceRefs                            []string
	ObservedAt, LatestObservedAt, ExpiresAt string // "" when omitted
	HasObservedAt, HasLatest, HasExpires    bool
}

type gfxLimOut struct {
	Code, Subject string
	HasSubject    bool
}

func (l gfxLimOut) String() string {
	if l.HasSubject {
		return l.Code + " " + l.Subject
	}
	return l.Code
}

type gfxDevSum struct{ Name, UID, Desired, Generation, Qualification, Phase, Reason string }

type gfxWorkload struct {
	Namespace, Name, UID, Container, CreatedAt, DeletedAt string
	HasDeletedAt                                          bool
}

type gfxExpl struct {
	Raw  []byte
	Tree *gfoJ

	APIVersion, Kind, TargetName, TargetUID, NodeName, NodeUID string
	Identity                                                   map[string]string // flattened: "source.type", "source.name"
	IdentityKeys                                               []string
	GraphRevision                                              string
	HasGraphRevision                                           bool
	Phase                                                      string
	HasPhase                                                   bool
	Qualification, NodeEligibility                             string
	Segments                                                   []gfxSeg
	Findings                                                   []gfxFindingOut
	Coverage                                                   []gfxCovOut
	Allocation                                                 map[string]string
	HasAllocation                                              bool
	AllocationEvidence                                         []string
	AllocationWorkloads                                        []gfxWorkload
	Workloads                                                  []gfxWorkload
	ObservedAt, EvaluatedAt                                    string
	Truncated                                                  bool
	Totals                                                     map[string]int
	Limitations                                                []gfxLimOut
	AssessmentRevision                                         string
	Devices                                                    []gfxDevSum
	Reason, EligibilityReason                                  string
	HasReason, HasEligibilityReason                            bool
	Text                                                       []byte // text output of the same request (set by gfxCase.Check)
}

func (e *gfxExpl) IsGPU() bool { return e.Kind == "GPUExplanation" }

func (e *gfxExpl) Lims() []string {
	out := []string{}
	for _, l := range e.Limitations {
		out = append(out, l.String())
	}
	return out
}

func (e *gfxExpl) HasLim(code, subject string) bool {
	for _, l := range e.Limitations {
		if l.Code == code && (subject == "*" || (l.HasSubject && l.Subject == subject) || (!l.HasSubject && subject == "")) {
			return true
		}
	}
	return false
}

func (e *gfxExpl) CountLim(code string) int {
	n := 0
	for _, l := range e.Limitations {
		if l.Code == code {
			n++
		}
	}
	return n
}

func (e *gfxExpl) Cov(name string) (gfxCovOut, bool) {
	for _, c := range e.Coverage {
		if c.Name == name {
			return c, true
		}
	}
	return gfxCovOut{}, false
}

func (e *gfxExpl) SegStrings() []string {
	out := []string{}
	for _, s := range e.Segments {
		out = append(out, s.From+" -> "+s.To)
	}
	return out
}

// Key orders of §6 (GFX-060, 062..067).
var (
	gfxTopGPU = []string{"apiVersion", "kind", "targetName", "targetUID", "nodeName", "nodeUID", "identity", "graphRevision",
		"phase", "qualification", "nodeEligibility", "pathSegments", "activeFindings", "coverage", "allocation",
		"affectedWorkloads", "observedAt", "evaluatedAt", "truncated", "totalCounts", "limitations",
		"nodeAssessmentRevision", "deviceSummaries", "reason"}
	gfxTopNode = []string{"apiVersion", "kind", "targetName", "targetUID", "nodeName", "nodeUID", "graphRevision",
		"qualification", "nodeEligibility", "pathSegments", "activeFindings", "coverage",
		"affectedWorkloads", "observedAt", "evaluatedAt", "truncated", "totalCounts", "limitations",
		"nodeAssessmentRevision", "deviceSummaries", "eligibilityReason"}
	gfxIdentityKeys = []string{"state", "reason", "vendor", "uuid", "serial", "nodeUID", "bootID", "bdf", "functionKey",
		"source", "evidenceID", "observedAt", "expiresAt"}
	gfxSegKeys      = []string{"from", "relation", "to", "origin", "kind", "source", "evidenceID", "observedAt", "expiresAt"}
	gfxFindingKeys  = []string{"id", "type", "severity", "state", "confidence", "scope", "evidence", "missingInputs", "affected", "firstSeen", "lastSeen", "explanation", "suggestedStep"}
	gfxCovKeys      = []string{"device", "name", "pathKind", "required", "state", "reason", "evidenceRefs", "observedAt", "latestObservedAt", "expiresAt"}
	gfxAllocKeys    = []string{"state", "reason", "profile", "observedAt", "expiresAt", "evidenceRefs", "affectedWorkloads"}
	gfxWorkloadKeys = []string{"namespace", "name", "uid", "container", "createdAt", "deletedAt"}
	gfxTotalKeys    = []string{"pathSegments", "activeFindings", "coverage", "affectedWorkloads", "deviceSummaries", "evidenceRefs", "limitations"}
	gfxDevSumKeys   = []string{"name", "uid", "desiredState", "observedGeneration", "qualification", "lifecyclePhase", "reason"}
	gfxTimeRE       = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]*[1-9])?Z$`)
	gfxHex64        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func gfxSet(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// gfxKeyOrder checks that the keys of j are a subsequence of order in that
// order, containing every key not listed in optional.
func gfxKeyOrder(j *gfoJ, path string, order []string, optional map[string]bool) error {
	if j == nil || j.kind != '{' {
		return fmt.Errorf("%s: want an object", path)
	}
	idx := 0
	for _, k := range j.keys {
		for idx < len(order) && order[idx] != k {
			if !optional[order[idx]] {
				return fmt.Errorf("%s: key %q missing or out of order (keys %v, order %v)", path, order[idx], j.keys, order)
			}
			idx++
		}
		if idx == len(order) {
			return fmt.Errorf("%s: unexpected or out-of-order key %q (keys %v, order %v)", path, k, j.keys, order)
		}
		idx++
	}
	for ; idx < len(order); idx++ {
		if !optional[order[idx]] {
			return fmt.Errorf("%s: key %q missing (keys %v)", path, order[idx], j.keys)
		}
	}
	return nil
}

type gfxDecoder struct{ errs []string }

func (d *gfxDecoder) fail(format string, a ...any) {
	d.errs = append(d.errs, fmt.Sprintf(format, a...))
}

func (d *gfxDecoder) str(j *gfoJ, path string) string {
	if j == nil || j.kind != 's' {
		d.fail("GFX-061: %s: want a JSON string", path)
		return ""
	}
	return j.str
}

func (d *gfxDecoder) time(j *gfoJ, path string) string {
	s := d.str(j, path)
	if j != nil && j.kind == 's' {
		if _, err := gfoTime(s); err != nil || !gfxTimeRE.MatchString(s) {
			d.fail("GFX-061: %s: %q is not a T-form time", path, s)
		}
	}
	return s
}

func (d *gfxDecoder) arr(j *gfoJ, path string) []*gfoJ {
	if j == nil || j.kind != '[' {
		d.fail("GFX-061: %s: want a JSON array ([] when empty, never null)", path)
		return nil
	}
	return j.elems
}

func (d *gfxDecoder) strs(j *gfoJ, path string) []string {
	out := []string{}
	for i, e := range d.arr(j, path) {
		out = append(out, d.str(e, fmt.Sprintf("%s[%d]", path, i)))
	}
	return out
}

func (d *gfxDecoder) keys(j *gfoJ, path string, order []string, optional map[string]bool) bool {
	if err := gfxKeyOrder(j, path, order, optional); err != nil {
		d.fail("GFX-060/GFX-069: %v", err)
		return false
	}
	return true
}

func (d *gfxDecoder) field(j *gfoJ, key string) *gfoJ {
	v, _ := gfxGet(j, key)
	return v
}

func (d *gfxDecoder) source(j *gfoJ, path string) (string, string) {
	if !d.keys(j, path, []string{"type", "name"}, nil) {
		return "", ""
	}
	return d.str(d.field(j, "type"), path+".type"), d.str(d.field(j, "name"), path+".name")
}

// gfxDecode decodes an explanation JSON output. Structural violations are
// returned as messages; a non-nil error means the output is not decodable.
func gfxDecode(raw []byte) (*gfxExpl, []string, error) {
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return nil, nil, fmt.Errorf("GFX-015/GFX-069: JSON output does not end with a newline")
	}
	body := raw[:len(raw)-1]
	if i := bytes.IndexByte(body, '\n'); i >= 0 {
		return nil, nil, fmt.Errorf("GFX-015/GFX-069: JSON output is not a single line (newline at byte %d)", i)
	}
	tree, err := gfoParseJSON(body)
	if err != nil {
		return nil, nil, fmt.Errorf("GFX-069: JSON output does not decode: %v", err)
	}
	d := &gfxDecoder{}
	for i, c := range body {
		if c < 0x20 || c > 0x7e {
			d.fail("GFX-069: output byte %d is 0x%02x, want 0x20..0x7e", i, c)
			break
		}
	}
	if re := gfxEncode(tree); re != string(body) {
		i := gfoDiffAt(body, []byte(re))
		d.fail("GFX-069: output is not the compact encoding escaping only '\"' and '\\' at byte %d: got %q want %q", i, gfoAround(body, i), gfoAround([]byte(re), i))
	}
	gfxNoNull(d, tree, "$")
	e := &gfxExpl{Raw: raw, Tree: tree, Identity: map[string]string{}, Allocation: map[string]string{}, Totals: map[string]int{}}
	if tree.kind != '{' {
		return nil, nil, fmt.Errorf("GFX-060: top level is not an object")
	}
	kindJ, _ := gfxGet(tree, "kind")
	e.Kind = d.str(kindJ, "kind")
	switch e.Kind {
	case "GPUExplanation":
		d.keys(tree, "$", gfxTopGPU, gfxSet("graphRevision"))
	case "NodeExplanation":
		d.keys(tree, "$", gfxTopNode, gfxSet("graphRevision"))
	default:
		d.fail("GFX-060: kind %q, want GPUExplanation or NodeExplanation", e.Kind)
	}
	get := func(k string) *gfoJ { return d.field(tree, k) }
	e.APIVersion = d.str(get("apiVersion"), "apiVersion")
	e.TargetName = d.str(get("targetName"), "targetName")
	e.TargetUID = d.str(get("targetUID"), "targetUID")
	e.NodeName = d.str(get("nodeName"), "nodeName")
	e.NodeUID = d.str(get("nodeUID"), "nodeUID")
	if v, ok := gfxGet(tree, "graphRevision"); ok {
		e.HasGraphRevision = true
		e.GraphRevision = d.str(v, "graphRevision")
	}
	if v, ok := gfxGet(tree, "phase"); ok {
		e.HasPhase = true
		e.Phase = d.str(v, "phase")
	}
	e.Qualification = d.str(get("qualification"), "qualification")
	e.NodeEligibility = d.str(get("nodeEligibility"), "nodeEligibility")
	if v, ok := gfxGet(tree, "identity"); ok {
		gfxDecodeIdentity(d, e, v)
	}
	for i, sj := range d.arr(get("pathSegments"), "pathSegments") {
		p := fmt.Sprintf("pathSegments[%d]", i)
		if !d.keys(sj, p, gfxSegKeys, nil) {
			continue
		}
		var s gfxSeg
		s.From = d.str(d.field(sj, "from"), p+".from")
		s.Relation = d.str(d.field(sj, "relation"), p+".relation")
		s.To = d.str(d.field(sj, "to"), p+".to")
		s.Origin = d.str(d.field(sj, "origin"), p+".origin")
		s.Kind = d.str(d.field(sj, "kind"), p+".kind")
		s.SourceType, s.SourceName = d.source(d.field(sj, "source"), p+".source")
		s.EvidenceID = d.str(d.field(sj, "evidenceID"), p+".evidenceID")
		s.ObservedAt = d.time(d.field(sj, "observedAt"), p+".observedAt")
		s.ExpiresAt = d.time(d.field(sj, "expiresAt"), p+".expiresAt")
		e.Segments = append(e.Segments, s)
	}
	for i, fj := range d.arr(get("activeFindings"), "activeFindings") {
		e.Findings = append(e.Findings, gfxDecodeFinding(d, fj, fmt.Sprintf("activeFindings[%d]", i)))
	}
	for i, cj := range d.arr(get("coverage"), "coverage") {
		e.Coverage = append(e.Coverage, gfxDecodeCoverage(d, cj, fmt.Sprintf("coverage[%d]", i), e.Kind == "NodeExplanation"))
	}
	if v, ok := gfxGet(tree, "allocation"); ok {
		e.HasAllocation = true
		if d.keys(v, "allocation", gfxAllocKeys, gfxSet("profile", "observedAt", "expiresAt")) {
			for _, k := range []string{"state", "reason", "profile"} {
				if x, ok := gfxGet(v, k); ok {
					e.Allocation[k] = d.str(x, "allocation."+k)
				}
			}
			for _, k := range []string{"observedAt", "expiresAt"} {
				if x, ok := gfxGet(v, k); ok {
					e.Allocation[k] = d.time(x, "allocation."+k)
				}
			}
			e.AllocationEvidence = d.strs(d.field(v, "evidenceRefs"), "allocation.evidenceRefs")
			e.AllocationWorkloads = gfxDecodeWorkloads(d, d.field(v, "affectedWorkloads"), "allocation.affectedWorkloads")
		}
	}
	e.Workloads = gfxDecodeWorkloads(d, get("affectedWorkloads"), "affectedWorkloads")
	e.ObservedAt = d.time(get("observedAt"), "observedAt")
	e.EvaluatedAt = d.time(get("evaluatedAt"), "evaluatedAt")
	if tj := get("truncated"); tj == nil || tj.kind != 'b' {
		d.fail("GFX-061: truncated: want a JSON boolean")
	} else {
		e.Truncated = tj.b
	}
	if tc := get("totalCounts"); d.keys(tc, "totalCounts", gfxTotalKeys, nil) {
		for _, k := range gfxTotalKeys {
			n, err := gfoGetInt(d.field(tc, k), "totalCounts."+k)
			if err != nil {
				d.fail("GFX-061: %v", err)
			}
			e.Totals[k] = n
		}
	}
	for i, lj := range d.arr(get("limitations"), "limitations") {
		p := fmt.Sprintf("limitations[%d]", i)
		if !d.keys(lj, p, []string{"code", "subject"}, gfxSet("subject")) {
			continue
		}
		l := gfxLimOut{Code: d.str(d.field(lj, "code"), p+".code")}
		if v, ok := gfxGet(lj, "subject"); ok {
			l.HasSubject = true
			l.Subject = d.str(v, p+".subject")
		}
		e.Limitations = append(e.Limitations, l)
	}
	e.AssessmentRevision = d.str(get("nodeAssessmentRevision"), "nodeAssessmentRevision")
	for i, sj := range d.arr(get("deviceSummaries"), "deviceSummaries") {
		p := fmt.Sprintf("deviceSummaries[%d]", i)
		if !d.keys(sj, p, gfxDevSumKeys, nil) {
			continue
		}
		var s gfxDevSum
		s.Name = d.str(d.field(sj, "name"), p+".name")
		s.UID = d.str(d.field(sj, "uid"), p+".uid")
		s.Desired = d.str(d.field(sj, "desiredState"), p+".desiredState")
		s.Generation = d.str(d.field(sj, "observedGeneration"), p+".observedGeneration")
		s.Qualification = d.str(d.field(sj, "qualification"), p+".qualification")
		s.Phase = d.str(d.field(sj, "lifecyclePhase"), p+".lifecyclePhase")
		s.Reason = d.str(d.field(sj, "reason"), p+".reason")
		e.Devices = append(e.Devices, s)
	}
	if v, ok := gfxGet(tree, "reason"); ok {
		e.HasReason = true
		e.Reason = d.str(v, "reason")
	}
	if v, ok := gfxGet(tree, "eligibilityReason"); ok {
		e.HasEligibilityReason = true
		e.EligibilityReason = d.str(v, "eligibilityReason")
	}
	return e, d.errs, nil
}

func gfxNoNull(d *gfxDecoder, j *gfoJ, path string) {
	switch j.kind {
	case 'z':
		d.fail("GFX-061: %s is null", path)
	case '{':
		for i, k := range j.keys {
			gfxNoNull(d, j.vals[i], path+"."+k)
		}
	case '[':
		for i, e := range j.elems {
			gfxNoNull(d, e, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

func gfxDecodeIdentity(d *gfxDecoder, e *gfxExpl, v *gfoJ) {
	if v == nil || v.kind != '{' {
		d.fail("GFX-062: identity is not an object")
		return
	}
	e.IdentityKeys = append([]string{}, v.keys...)
	state := ""
	if s, ok := gfxGet(v, "state"); ok {
		state = d.str(s, "identity.state")
	}
	if state == "Bound" {
		d.keys(v, "identity", gfxIdentityKeys, gfxSet("serial"))
	} else {
		d.keys(v, "identity", []string{"state", "reason"}, nil)
	}
	for i, k := range v.keys {
		switch k {
		case "source":
			e.Identity["source.type"], e.Identity["source.name"] = d.source(v.vals[i], "identity.source")
		case "observedAt", "expiresAt":
			e.Identity[k] = d.time(v.vals[i], "identity."+k)
		default:
			e.Identity[k] = d.str(v.vals[i], "identity."+k)
		}
	}
}

func gfxDecodeFinding(d *gfxDecoder, fj *gfoJ, p string) gfxFindingOut {
	var f gfxFindingOut
	if !d.keys(fj, p, gfxFindingKeys, nil) {
		return f
	}
	f.ID = d.str(d.field(fj, "id"), p+".id")
	f.Type = d.str(d.field(fj, "type"), p+".type")
	f.Severity = d.str(d.field(fj, "severity"), p+".severity")
	f.State = d.str(d.field(fj, "state"), p+".state")
	f.Confidence = d.str(d.field(fj, "confidence"), p+".confidence")
	f.Scope = d.strs(d.field(fj, "scope"), p+".scope")
	f.Evidence = []gfxEvRef{}
	for i, ej := range d.arr(d.field(fj, "evidence"), p+".evidence") {
		q := fmt.Sprintf("%s.evidence[%d]", p, i)
		if !d.keys(ej, q, []string{"observationID", "summary"}, nil) {
			continue
		}
		f.Evidence = append(f.Evidence, gfxEvRef{d.str(d.field(ej, "observationID"), q+".observationID"), d.str(d.field(ej, "summary"), q+".summary")})
	}
	f.MissingInputs = d.strs(d.field(fj, "missingInputs"), p+".missingInputs")
	f.Affected = []gfxAffected{}
	for i, aj := range d.arr(d.field(fj, "affected"), p+".affected") {
		q := fmt.Sprintf("%s.affected[%d]", p, i)
		if !d.keys(aj, q, []string{"asset", "accuracy"}, nil) {
			continue
		}
		f.Affected = append(f.Affected, gfxAffected{d.str(d.field(aj, "asset"), q+".asset"), d.str(d.field(aj, "accuracy"), q+".accuracy")})
	}
	f.FirstSeen = d.time(d.field(fj, "firstSeen"), p+".firstSeen")
	f.LastSeen = d.time(d.field(fj, "lastSeen"), p+".lastSeen")
	f.Explanation = d.str(d.field(fj, "explanation"), p+".explanation")
	f.SuggestedStep = d.str(d.field(fj, "suggestedStep"), p+".suggestedStep")
	return f
}

func gfxDecodeCoverage(d *gfxDecoder, cj *gfoJ, p string, node bool) gfxCovOut {
	var c gfxCovOut
	opt := gfxSet("observedAt", "latestObservedAt", "expiresAt")
	if !node {
		opt["device"] = true
	}
	if !d.keys(cj, p, gfxCovKeys, opt) {
		return c
	}
	if v, ok := gfxGet(cj, "device"); ok {
		c.HasDevice = true
		c.Device = d.str(v, p+".device")
		if !node {
			d.fail("GFX-065: %s: gpu coverage element has a device key", p)
		}
	}
	c.Name = d.str(d.field(cj, "name"), p+".name")
	c.PathKind = d.str(d.field(cj, "pathKind"), p+".pathKind")
	if r := d.field(cj, "required"); r == nil || r.kind != 'b' {
		d.fail("GFX-061: %s.required: want a JSON boolean", p)
	} else {
		c.Required = r.b
	}
	c.State = d.str(d.field(cj, "state"), p+".state")
	c.Reason = d.str(d.field(cj, "reason"), p+".reason")
	c.EvidenceRefs = d.strs(d.field(cj, "evidenceRefs"), p+".evidenceRefs")
	if v, ok := gfxGet(cj, "observedAt"); ok {
		c.HasObservedAt, c.ObservedAt = true, d.time(v, p+".observedAt")
	}
	if v, ok := gfxGet(cj, "latestObservedAt"); ok {
		c.HasLatest, c.LatestObservedAt = true, d.time(v, p+".latestObservedAt")
	}
	if v, ok := gfxGet(cj, "expiresAt"); ok {
		c.HasExpires, c.ExpiresAt = true, d.time(v, p+".expiresAt")
	}
	return c
}

func gfxDecodeWorkloads(d *gfxDecoder, j *gfoJ, path string) []gfxWorkload {
	out := []gfxWorkload{}
	for i, wj := range d.arr(j, path) {
		p := fmt.Sprintf("%s[%d]", path, i)
		if !d.keys(wj, p, gfxWorkloadKeys, gfxSet("deletedAt")) {
			continue
		}
		w := gfxWorkload{
			Namespace: d.str(d.field(wj, "namespace"), p+".namespace"), Name: d.str(d.field(wj, "name"), p+".name"),
			UID: d.str(d.field(wj, "uid"), p+".uid"), Container: d.str(d.field(wj, "container"), p+".container"),
			CreatedAt: d.time(d.field(wj, "createdAt"), p+".createdAt"),
		}
		if v, ok := gfxGet(wj, "deletedAt"); ok {
			w.HasDeletedAt, w.DeletedAt = true, d.time(v, p+".deletedAt")
		}
		out = append(out, w)
	}
	return out
}

// gfxDecodeChecked decodes stdout and reports every structural violation of
// GFX-060..069 (independent of the oracle).
func gfxDecodeChecked(t *testing.T, raw []byte, clause string) *gfxExpl {
	t.Helper()
	e, errs, err := gfxDecode(raw)
	if err != nil {
		t.Fatalf("%s: %v\nstdout head: %s", clause, err, gfoTail(raw))
	}
	for _, m := range errs {
		t.Errorf("%s: %s", clause, m)
	}
	for _, m := range gfxStructure(e) {
		t.Errorf("%s: %s", clause, m)
	}
	return e
}

// gfxStructure applies the value, order, sort and count rules of §6 that do
// not need the oracle.
func gfxStructure(e *gfxExpl) []string {
	var errs []string
	fail := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	gpu := e.IsGPU()
	if e.APIVersion != "v1alpha1" {
		fail("GFX-060: apiVersion %q, want v1alpha1", e.APIVersion)
	}
	if !gpu && (e.TargetName != e.NodeName || e.TargetUID != e.NodeUID) {
		fail("GFX-060: node targetName/targetUID %q/%q differ from nodeName/nodeUID %q/%q", e.TargetName, e.TargetUID, e.NodeName, e.NodeUID)
	}
	if gpu != e.HasPhase {
		fail("GFX-060: phase present=%v for kind %s", e.HasPhase, e.Kind)
	}
	if gpu && len(e.IdentityKeys) == 0 {
		fail("GFX-060/GFX-062: gpu explanation without identity")
	}
	if !gpu && len(e.IdentityKeys) != 0 {
		fail("GFX-060: node explanation has identity")
	}
	if gpu != e.HasAllocation {
		fail("GFX-060: allocation present=%v for kind %s", e.HasAllocation, e.Kind)
	}
	if gpu != e.HasReason || gpu == e.HasEligibilityReason {
		fail("GFX-060: reason/eligibilityReason presence %v/%v for kind %s", e.HasReason, e.HasEligibilityReason, e.Kind)
	}
	if e.HasGraphRevision && !regexp.MustCompile(`^[1-9][0-9]*:(0|[1-9][0-9]*):[0-9a-f]{64}$`).MatchString(e.GraphRevision) {
		fail("GFX-060: graphRevision %q is not a GFO-087 bundleRevision", e.GraphRevision)
	}
	if !gfxHex64.MatchString(e.AssessmentRevision) {
		fail("GFX-060: nodeAssessmentRevision %q is not 64 lower-case hex", e.AssessmentRevision)
	}
	if e.ObservedAt != e.EvaluatedAt {
		fail("GFX-041/GFX-060: observedAt %q differs from evaluatedAt %q (E = t_R)", e.ObservedAt, e.EvaluatedAt)
	}
	if len(e.Workloads) != 0 {
		fail("GFX-066: affectedWorkloads has %d elements, want []", len(e.Workloads))
	}
	if gpu && len(e.Devices) != 0 {
		fail("GFX-060: gpu deviceSummaries has %d elements, want []", len(e.Devices))
	}
	if !gfxIn(e.Qualification, "Qualified", "Disqualified", "Unknown") {
		fail("GFX-061: qualification %q is not a Qualification String()", e.Qualification)
	}
	if !gfxIn(e.NodeEligibility, "Eligible", "Ineligible", "Unknown") {
		fail("GFX-061: nodeEligibility %q is not an Eligibility String()", e.NodeEligibility)
	}
	if gpu {
		if !gfxIn(e.Phase, "Pending", "Validating", "Ready", "Degraded", "MaintenancePending", "MaintenanceReady", "Retiring", "Retired", "Unknown") {
			fail("GFX-061: phase %q is not a LifecyclePhase String()", e.Phase)
		}
		if e.Phase == "MaintenanceReady" || e.Phase == "Retired" {
			fail("GFX-052: offline phase %q is impossible without fence and allocation", e.Phase)
		}
		gfxCheckIdentity(e, fail)
		if e.Allocation["state"] != "Unknown" || e.Allocation["reason"] != "AllocationUnknown" || len(e.AllocationEvidence) != 0 || len(e.AllocationWorkloads) != 0 {
			fail("GFX-066: allocation %v evidenceRefs %v affectedWorkloads %v, want Unknown/AllocationUnknown with [] lists", e.Allocation, e.AllocationEvidence, e.AllocationWorkloads)
		}
		for _, k := range []string{"profile", "observedAt", "expiresAt"} {
			if _, ok := e.Allocation[k]; ok {
				fail("GFX-066: allocation.%s present, want omitted offline", k)
			}
		}
		if e.Reason == "" {
			fail("GFX-061: reason is empty")
		}
	} else if e.EligibilityReason == "" {
		fail("GFX-061: eligibilityReason is empty")
	}
	for i, s := range e.Segments {
		if s.Relation != "LOCATED_IN" || s.Origin != "Observed" || s.Kind != "Observed" {
			fail("GFX-063: pathSegments[%d] relation/origin/kind %s/%s/%s, want LOCATED_IN/Observed/Observed", i, s.Relation, s.Origin, s.Kind)
		}
		if s.ObservedAt != e.ObservedAt {
			fail("GFX-063: pathSegments[%d].observedAt %s, want the R frame time %s (one revision)", i, s.ObservedAt, e.ObservedAt)
		}
		if !strings.HasPrefix(s.EvidenceID, "pe:") {
			fail("GFX-063: pathSegments[%d].evidenceID %q is not an edge evidence ID", i, s.EvidenceID)
		}
		for j := 0; j < i; j++ {
			o := e.Segments[j]
			if o.From == s.From && o.To == s.To && o.Relation == s.Relation && o.Origin == s.Origin {
				fail("GFX-063: pathSegments[%d] repeats pathSegments[%d]", i, j)
			}
		}
	}
	if gpu && e.Identity["state"] == "Bound" && !e.Truncated {
		if msg := gfxSegmentOrder(e.Segments, e.Identity["functionKey"]); msg != "" {
			fail("GFX-063/GFX-068: %s", msg)
		}
	}
	if gpu && e.Identity["state"] != "Bound" && len(e.Segments) != 0 {
		fail("GFX-063: gpu target is not Bound but pathSegments has %d elements", len(e.Segments))
	}
	if !e.HasGraphRevision && (len(e.Segments) != 0 || len(e.Findings) != 0) {
		fail("GFX-063/GFX-064: A = empty (no graphRevision) but pathSegments/activeFindings are %d/%d", len(e.Segments), len(e.Findings))
	}
	for i, f := range e.Findings {
		if i > 0 && !(e.Findings[i-1].ID < f.ID) {
			fail("GFX-064/GFX-068: activeFindings not in strictly increasing id order at %d (%q, %q)", i, e.Findings[i-1].ID, f.ID)
		}
		if f.State != "Active" || len(f.MissingInputs) != 0 || len(f.Affected) != 0 {
			fail("GFX-064: activeFindings[%d] state %q missingInputs %v affected %v, want Active/[]/[] (PCIE-019)", i, f.State, f.MissingInputs, f.Affected)
		}
	}
	devUID := map[string]string{}
	for i, s := range e.Devices {
		devUID[s.Name] = s.UID
		if i > 0 && !(e.Devices[i-1].UID < s.UID) {
			fail("GFX-067/GFX-068: deviceSummaries not in strictly increasing uid order at %d", i)
		}
		if !regexp.MustCompile(`^(0|[1-9][0-9]*)$`).MatchString(s.Generation) {
			fail("GFX-061/GFX-067: deviceSummaries[%d].observedGeneration %q is not a decimal string", i, s.Generation)
		}
		if s.Reason == "" {
			fail("GFX-061/GFX-067: deviceSummaries[%d].reason is empty", i)
		}
	}
	for i, c := range e.Coverage {
		if c.State == "" || c.Reason == "" {
			fail("GFX-078: coverage[%d] state/reason %q/%q must not be empty", i, c.State, c.Reason)
		}
		if !gfxIn(c.State, "Normal", "Missing", "Unknown", "Unsupported") {
			fail("GFX-061: coverage[%d].state %q is not a CoverageState String()", i, c.State)
		}
		for k := 1; k < len(c.EvidenceRefs); k++ {
			if !(c.EvidenceRefs[k-1] < c.EvidenceRefs[k]) {
				fail("GFX-065/GFX-068: coverage[%d].evidenceRefs not strictly increasing (sorted, deduplicated) at %d", i, k)
			}
		}
		if len(c.EvidenceRefs) > 256 {
			fail("GFX-090: coverage[%d].evidenceRefs has %d elements, bound 256", i, len(c.EvidenceRefs))
		}
		if !gpu {
			if !c.HasDevice || !c.Required || c.State == "Normal" {
				fail("GFX-065: node coverage[%d] device=%v required=%v state=%s, want device key, required=true, state != Normal", i, c.HasDevice, c.Required, c.State)
			}
			if _, ok := devUID[c.Device]; !ok {
				fail("GFX-065: node coverage[%d].device %q is not in deviceSummaries", i, c.Device)
			}
		}
		if i > 0 {
			prev := e.Coverage[i-1]
			less := prev.Name < c.Name
			if !gpu {
				pu, cu := devUID[prev.Device], devUID[c.Device]
				less = pu < cu || (pu == cu && prev.Name < c.Name)
			}
			if !less {
				fail("GFX-065/GFX-068: coverage not strictly ordered by %s at %d", map[bool]string{true: "name", false: "(device UID, name)"}[gpu], i)
			}
		}
	}
	for i, l := range e.Limitations {
		if i > 0 {
			p := e.Limitations[i-1]
			if !(p.Code < l.Code || (p.Code == l.Code && p.Subject < l.Subject)) {
				fail("GFX-068/GFX-080: limitations not strictly ordered by (code, subject) at %d: %q then %q", i, p.String(), l.String())
			}
		}
		if !gfxLimCode(l.Code) {
			fail("GFX-080: limitations[%d].code %q is not a GFX-080 code", i, l.Code)
		}
	}
	if len(e.Segments) > 512 || len(e.Findings) > 64 || len(e.Coverage) > 32 || len(e.Devices) > 256 || len(e.Limitations) > 2048 {
		fail("GFX-090: list lengths exceed bounds: segments %d findings %d coverage %d devices %d limitations %d",
			len(e.Segments), len(e.Findings), len(e.Coverage), len(e.Devices), len(e.Limitations))
	}
	lens := map[string]int{"pathSegments": len(e.Segments), "activeFindings": len(e.Findings), "coverage": len(e.Coverage),
		"affectedWorkloads": len(e.Workloads), "deviceSummaries": len(e.Devices), "limitations": len(e.Limitations)}
	refs := len(e.AllocationEvidence)
	for _, f := range e.Findings {
		refs += len(f.Evidence)
	}
	for _, c := range e.Coverage {
		refs += len(c.EvidenceRefs)
	}
	lens["evidenceRefs"] = refs
	anyShort := false
	for k, n := range lens {
		if e.Totals[k] < n {
			fail("GFX-068: totalCounts.%s = %d is below the output length %d", k, e.Totals[k], n)
		}
		if e.Totals[k] != n {
			anyShort = true
		}
	}
	if !e.Truncated && anyShort {
		fail("GFX-068: truncated=false but totalCounts %v differ from output lengths %v", e.Totals, lens)
	}
	if e.Truncated && !anyShort {
		fail("GFX-068: truncated=true but every totalCounts value equals its output length %v", lens)
	}
	for _, need := range []string{"offline", "traffic_path_unverified"} {
		if !e.HasLim(need, "") && !e.Truncated {
			fail("GFX-080: limitation %q missing", need)
		}
	}
	return errs
}

func gfxCheckIdentity(e *gfxExpl, fail func(string, ...any)) {
	st := e.Identity["state"]
	switch st {
	case "Bound":
		if e.Identity["reason"] != "Ready" {
			fail("GFX-062: Bound identity reason %q, want Ready", e.Identity["reason"])
		}
		if _, ok := e.Identity["serial"]; ok {
			fail("GFX-062: offline Bound identity has serial %q, want omitted", e.Identity["serial"])
		}
		if e.Identity["functionKey"] != "PCIeFunction/pci-bdf:"+e.Identity["bdf"] {
			fail("GFX-062: functionKey %q does not match bdf %q", e.Identity["functionKey"], e.Identity["bdf"])
		}
		if e.Identity["nodeUID"] != e.NodeUID {
			fail("GFX-062: identity.nodeUID %q, want %q", e.Identity["nodeUID"], e.NodeUID)
		}
	case "Unknown", "Conflict":
		if e.Identity["reason"] == "" {
			fail("GFX-062/GFX-078: %s identity without reason", st)
		}
	default:
		fail("GFX-062: identity.state %q is not a BindingState String()", st)
	}
}

// gfxSegmentOrder verifies the GFX-068 order of the segments of one bound
// device: depth is the shortest hop count from fn to the From of the edge,
// computed over the output segments themselves.
func gfxSegmentOrder(segs []gfxSeg, fn string) string {
	dist := map[string]int{fn: 0}
	queue := []string{fn}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, s := range segs {
			if s.From == u {
				if _, ok := dist[s.To]; !ok {
					dist[s.To] = dist[u] + 1
					queue = append(queue, s.To)
				}
			}
		}
	}
	for i, s := range segs {
		if _, ok := dist[s.From]; !ok {
			return fmt.Sprintf("pathSegments[%d] %s -> %s is not reachable from %s over the listed segments", i, s.From, s.To, fn)
		}
		if i == 0 {
			continue
		}
		p := segs[i-1]
		a := []string{fmt.Sprintf("%08d", dist[p.From]), p.From, p.To, p.Relation, p.Origin}
		b := []string{fmt.Sprintf("%08d", dist[s.From]), s.From, s.To, s.Relation, s.Origin}
		if strings.Join(a, "\x00") >= strings.Join(b, "\x00") {
			return fmt.Sprintf("pathSegments not ordered by (depth, from, to, relation, origin) at %d", i)
		}
	}
	return ""
}

func gfxIn(s string, set ...string) bool {
	for _, x := range set {
		if s == x {
			return true
		}
	}
	return false
}

var gfxLimCodes = gfxSet("allocation_unavailable", "diagnostics_outside_digest", "diagnostics_truncated", "fence_unavailable",
	"finding_not_applied", "finding_source_untrusted", "frame_partial", "no_admitted_frame", "no_devices", "node_name_differs",
	"nvidia_classification_unknown", "offline", "offline_trust_not_live", "path_contradiction", "path_untrusted",
	"root_port_absent", "traffic_path_unverified", "width_pair_absent")

func gfxLimCode(c string) bool {
	if strings.HasPrefix(c, "collector:") {
		return gfoDiagCodes[strings.TrimPrefix(c, "collector:")]
	}
	return gfxLimCodes[c]
}

// ---------------------------------------------------------------------------
// §7 text reconstruction from the JSON output (GFX-070..078).
// ---------------------------------------------------------------------------

// gfxTok renders a key=value value or positional token (GFX-071).
func gfxTok(s string, present bool) string {
	if !present || s == "" {
		return "-"
	}
	if s == "-" {
		return "%2D"
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e || c == '%' {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// gfxFree renders a free string: verbatim except leading and trailing 0x20
// bytes, which become %20 (GFX-071).
func gfxFree(s string) string {
	i := 0
	for i < len(s) && s[i] == ' ' {
		i++
	}
	j := len(s)
	for j > i && s[j-1] == ' ' {
		j--
	}
	return strings.Repeat("%20", i) + s[i:j] + strings.Repeat("%20", len(s)-j)
}

func gfxRenderText(e *gfxExpl) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(s); b.WriteByte('\n') }
	kv := func(k, v string, present bool) string { return k + "=" + gfxTok(v, present) }
	idv := func(k string) (string, bool) { v, ok := e.Identity[k]; return v, ok }
	gpu := e.IsGPU()
	if gpu {
		line(strings.Join([]string{"gpu/" + gfxTok(e.TargetName, true), kv("phase", e.Phase, e.HasPhase), kv("qualification", e.Qualification, true),
			kv("revision", e.GraphRevision, e.HasGraphRevision), kv("reason", e.Reason, e.HasReason), "mode=offline"}, " "))
	} else {
		line(strings.Join([]string{"node/" + gfxTok(e.TargetName, true), kv("eligibility", e.NodeEligibility, true), kv("qualification", e.Qualification, true),
			kv("revision", e.GraphRevision, e.HasGraphRevision), kv("eligibilityReason", e.EligibilityReason, e.HasEligibilityReason), "mode=offline"}, " "))
	}
	line("PATH")
	if gpu {
		line("  " + strings.Join([]string{"target", kv("uid", e.TargetUID, true), kv("node", e.NodeName, true), kv("nodeUID", e.NodeUID, true),
			kv("eligibility", e.NodeEligibility, true), kv("assessment", e.AssessmentRevision, true), kv("observed", e.ObservedAt, true),
			kv("evaluated", e.EvaluatedAt, true)}, " "))
		parts := []string{"identity"}
		for _, p := range [][2]string{{"state", "state"}, {"reason", "reason"}, {"vendor", "vendor"}, {"uuid", "uuid"}, {"serial", "serial"},
			{"nodeUID", "nodeUID"}, {"bootID", "bootID"}, {"bdf", "bdf"}, {"function", "functionKey"}, {"sourceType", "source.type"},
			{"sourceName", "source.name"}, {"evidence", "evidenceID"}, {"observed", "observedAt"}, {"expires", "expiresAt"}} {
			v, ok := idv(p[1])
			parts = append(parts, kv(p[0], v, ok))
		}
		line("  " + strings.Join(parts, " "))
	} else {
		line("  " + strings.Join([]string{"target", kv("uid", e.TargetUID, true), kv("assessment", e.AssessmentRevision, true),
			kv("observed", e.ObservedAt, true), kv("evaluated", e.EvaluatedAt, true)}, " "))
		if len(e.Devices) == 0 {
			line("  device (none)")
		}
		for _, d := range e.Devices {
			line("  " + strings.Join([]string{"device", gfxTok(d.Name, true), kv("uid", d.UID, true), kv("desired", d.Desired, true),
				kv("generation", d.Generation, true), kv("phase", d.Phase, true), kv("qualification", d.Qualification, true), kv("reason", d.Reason, true)}, " "))
		}
	}
	if len(e.Segments) == 0 {
		line("  segment (none)")
	}
	for _, s := range e.Segments {
		line("  " + strings.Join([]string{"segment", gfxTok(s.From, true), gfxTok(s.Relation, true), gfxTok(s.To, true), kv("origin", s.Origin, true),
			kv("kind", s.Kind, true), kv("sourceType", s.SourceType, true), kv("sourceName", s.SourceName, true), kv("evidence", s.EvidenceID, true),
			kv("observed", s.ObservedAt, true), kv("expires", s.ExpiresAt, true)}, " "))
	}
	line("FINDINGS")
	if len(e.Findings) == 0 {
		line("  (none)")
	}
	for _, f := range e.Findings {
		line("  " + strings.Join([]string{"finding", gfxTok(f.ID, true), kv("type", f.Type, true), kv("severity", f.Severity, true), kv("state", f.State, true),
			kv("confidence", f.Confidence, true), kv("firstSeen", f.FirstSeen, true), kv("lastSeen", f.LastSeen, true)}, " "))
		for _, s := range f.Scope {
			line("    scope " + gfxTok(s, true))
		}
		for _, ev := range f.Evidence {
			l := "    evidence " + gfxTok(ev.ObservationID, true)
			if ev.Summary != "" {
				l += " " + gfxFree(ev.Summary)
			}
			line(l)
		}
		for _, m := range f.MissingInputs {
			line("    missing " + gfxTok(m, true))
		}
		for _, a := range f.Affected {
			line("    affected " + gfxTok(a.Asset, true) + " " + kv("accuracy", a.Accuracy, true))
		}
		if f.Explanation != "" {
			line("    explanation " + gfxFree(f.Explanation))
		}
		if f.SuggestedStep != "" {
			line("    suggestedStep " + gfxFree(f.SuggestedStep))
		}
	}
	line("COVERAGE")
	if len(e.Coverage) == 0 {
		line("  (none)")
	}
	for _, c := range e.Coverage {
		parts := []string{"coverage", gfxTok(c.Name, true)}
		if !gpu {
			parts = append(parts, kv("device", c.Device, c.HasDevice))
		}
		parts = append(parts, kv("pathKind", c.PathKind, true), "required="+strconv.FormatBool(c.Required), kv("state", c.State, true),
			kv("reason", c.Reason, true), kv("observed", c.ObservedAt, c.HasObservedAt), kv("latest", c.LatestObservedAt, c.HasLatest),
			kv("expires", c.ExpiresAt, c.HasExpires))
		line("  " + strings.Join(parts, " "))
		for _, r := range c.EvidenceRefs {
			line("    evidence " + gfxTok(r, true))
		}
	}
	line("ALLOCATION")
	if gpu {
		av := func(k string) (string, bool) { v, ok := e.Allocation[k]; return v, ok }
		st, stOK := av("state")
		rs, rsOK := av("reason")
		pf, pfOK := av("profile")
		ob, obOK := av("observedAt")
		ex, exOK := av("expiresAt")
		line("  " + strings.Join([]string{"allocation", kv("state", st, stOK), kv("reason", rs, rsOK), kv("profile", pf, pfOK),
			kv("observed", ob, obOK), kv("expires", ex, exOK)}, " "))
		for _, r := range e.AllocationEvidence {
			line("    evidence " + gfxTok(r, true))
		}
	}
	if len(e.Workloads) == 0 {
		line("  workload (none)")
	}
	for _, w := range e.Workloads {
		line("  " + strings.Join([]string{"workload", gfxTok(w.Namespace, true) + "/" + gfxTok(w.Name, true), kv("uid", w.UID, true),
			kv("container", w.Container, true), kv("created", w.CreatedAt, true), kv("deleted", w.DeletedAt, w.HasDeletedAt)}, " "))
	}
	line("LIMITATIONS")
	if e.Truncated {
		parts := []string{"truncated"}
		for _, k := range gfxTotalKeys {
			parts = append(parts, k+"="+strconv.Itoa(e.Totals[k]))
		}
		line("  " + strings.Join(parts, " "))
	}
	for _, l := range e.Limitations {
		s := "  " + gfxTok(l.Code, true)
		if l.HasSubject {
			s += " " + gfxTok(l.Subject, true)
		}
		line(s)
	}
	return b.String()
}

// gfxTextRules checks the GFX-071 line rules of a text output.
func gfxTextRules(text []byte) []string {
	var errs []string
	if len(text) == 0 || text[len(text)-1] != '\n' {
		errs = append(errs, "GFX-071: text output does not end with a newline")
		return errs
	}
	for i, c := range text {
		if (c < 0x20 || c > 0x7e) && c != '\n' {
			errs = append(errs, fmt.Sprintf("GFX-071: text byte %d is 0x%02x", i, c))
			break
		}
	}
	lines := strings.Split(string(text[:len(text)-1]), "\n")
	heads := []string{}
	for i, l := range lines {
		if l != strings.TrimRight(l, " ") {
			errs = append(errs, fmt.Sprintf("GFX-071: line %d has trailing spaces", i+1))
		}
		if i > 0 && l != "" && l[0] != ' ' {
			heads = append(heads, l)
		}
		if i > 0 && strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "  ") {
			errs = append(errs, fmt.Sprintf("GFX-071: line %d is indented by one space", i+1))
		}
	}
	if strings.Join(heads, ",") != "PATH,FINDINGS,COVERAGE,ALLOCATION,LIMITATIONS" {
		errs = append(errs, fmt.Sprintf("GFX-071: section heads %v, want PATH, FINDINGS, COVERAGE, ALLOCATION, LIMITATIONS once each in order", heads))
	}
	return errs
}

// gfxMatchExample matches a text output against a normative example in
// which each "…" stands for one or more printable non-space bytes.
func gfxMatchExample(got, example string) (bool, int) {
	gl := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	el := strings.Split(strings.TrimSuffix(example, "\n"), "\n")
	n := len(el)
	if len(gl) < n {
		n = len(gl)
	}
	for i := 0; i < n; i++ {
		parts := strings.Split(el[i], "…")
		for k := range parts {
			parts[k] = regexp.QuoteMeta(parts[k])
		}
		re := regexp.MustCompile("^" + strings.Join(parts, `[\x21-\x7e]+`) + "$")
		if !re.MatchString(gl[i]) {
			return false, i + 1
		}
	}
	if len(gl) != len(el) {
		return false, n + 1
	}
	return true, 0
}

func gfxSorted(items []string) []string {
	out := append([]string{}, items...)
	sort.Strings(out)
	return out
}
