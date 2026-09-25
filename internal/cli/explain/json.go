package explain

import (
	"errors"
	"strconv"

	"github.com/aetertinas9/data-path-assurance/internal/app"
)

// errNonPrintable reports a string that holds a byte outside 0x20-0x7E.
var errNonPrintable = errors.New("explanation string holds a byte outside printable ASCII")

// jsonWriter builds compact JSON with a fixed key order. Strings escape only
// '"' and '\'; any byte outside 0x20-0x7E makes the whole encoding fail.
type jsonWriter struct {
	b   []byte
	bad bool
	// first tracks, per open object or array, whether a member was written.
	first []bool
}

func (w *jsonWriter) str(s string) {
	w.b = append(w.b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c < 0x20 || c > 0x7e:
			w.bad = true
		case c == '"' || c == '\\':
			w.b = append(w.b, '\\', c)
		default:
			w.b = append(w.b, c)
		}
	}
	w.b = append(w.b, '"')
}

func (w *jsonWriter) open(c byte) {
	w.b = append(w.b, c)
	w.first = append(w.first, true)
}

func (w *jsonWriter) close(c byte) {
	w.b = append(w.b, c)
	w.first = w.first[:len(w.first)-1]
}

// sep writes the comma before every member but the first.
func (w *jsonWriter) sep() {
	top := len(w.first) - 1
	if w.first[top] {
		w.first[top] = false
		return
	}
	w.b = append(w.b, ',')
}

func (w *jsonWriter) key(k string) {
	w.sep()
	w.str(k)
	w.b = append(w.b, ':')
}

func (w *jsonWriter) strField(k, v string) {
	w.key(k)
	w.str(v)
}

func (w *jsonWriter) optField(k string, v app.Optional) {
	if v.Set {
		w.strField(k, v.Value)
	}
}

func (w *jsonWriter) boolField(k string, v bool) {
	w.key(k)
	w.b = strconv.AppendBool(w.b, v)
}

func (w *jsonWriter) intField(k string, v int) {
	w.key(k)
	w.b = strconv.AppendInt(w.b, int64(v), 10)
}

func (w *jsonWriter) strList(k string, list []string) {
	w.key(k)
	w.open('[')
	for _, s := range list {
		w.sep()
		w.str(s)
	}
	w.close(']')
}

// array writes an array field whose elements are written by elem.
func (w *jsonWriter) array(k string, n int, elem func(i int)) {
	w.key(k)
	w.open('[')
	for i := 0; i < n; i++ {
		w.sep()
		elem(i)
	}
	w.close(']')
}

func (w *jsonWriter) source(k string, s app.SourceView) {
	w.key(k)
	w.open('{')
	w.strField("type", s.Type)
	w.strField("name", s.Name)
	w.close('}')
}

// EncodeJSON renders the explanation as one line of compact JSON followed by
// a newline.
func EncodeJSON(e app.Explanation) ([]byte, error) {
	w := &jsonWriter{}
	w.open('{')
	w.strField("apiVersion", "v1alpha1")
	if e.Kind == app.TargetGPU {
		w.strField("kind", "GPUExplanation")
	} else {
		w.strField("kind", "NodeExplanation")
	}
	w.strField("targetName", e.TargetName)
	w.strField("targetUID", e.TargetUID)
	w.strField("nodeName", e.NodeName)
	w.strField("nodeUID", e.NodeUID)
	if e.Identity != nil {
		w.key("identity")
		writeIdentity(w, e.Identity)
	}
	w.optField("graphRevision", e.GraphRevision)
	if e.Kind == app.TargetGPU {
		w.strField("phase", e.Phase)
	}
	w.strField("qualification", e.Qualification)
	w.strField("nodeEligibility", e.NodeEligibility)
	w.array("pathSegments", len(e.PathSegments), func(i int) { writeSegment(w, e.PathSegments[i]) })
	w.array("activeFindings", len(e.ActiveFindings), func(i int) { writeFinding(w, e.ActiveFindings[i]) })
	w.array("coverage", len(e.Coverage), func(i int) { writeCoverage(w, e.Coverage[i]) })
	if e.Allocation != nil {
		w.key("allocation")
		writeAllocation(w, e.Allocation)
	}
	w.array("affectedWorkloads", len(e.AffectedWorkloads), func(i int) { writeWorkload(w, e.AffectedWorkloads[i]) })
	w.strField("observedAt", e.ObservedAt)
	w.strField("evaluatedAt", e.EvaluatedAt)
	w.boolField("truncated", e.Truncated)
	w.key("totalCounts")
	w.open('{')
	t := e.TotalCounts
	w.intField("pathSegments", t.PathSegments)
	w.intField("activeFindings", t.ActiveFindings)
	w.intField("coverage", t.Coverage)
	w.intField("affectedWorkloads", t.AffectedWorkloads)
	w.intField("deviceSummaries", t.DeviceSummaries)
	w.intField("evidenceRefs", t.EvidenceRefs)
	w.intField("limitations", t.Limitations)
	w.close('}')
	w.array("limitations", len(e.Limitations), func(i int) {
		l := e.Limitations[i]
		w.open('{')
		w.strField("code", l.Code)
		w.optField("subject", l.Subject)
		w.close('}')
	})
	w.strField("nodeAssessmentRevision", e.NodeAssessmentRevision)
	w.array("deviceSummaries", len(e.DeviceSummaries), func(i int) {
		d := e.DeviceSummaries[i]
		w.open('{')
		w.strField("name", d.Name)
		w.strField("uid", d.UID)
		w.strField("desiredState", d.DesiredState)
		w.strField("observedGeneration", d.ObservedGeneration)
		w.strField("qualification", d.Qualification)
		w.strField("lifecyclePhase", d.LifecyclePhase)
		w.strField("reason", d.Reason)
		w.close('}')
	})
	if e.Kind == app.TargetGPU {
		w.strField("reason", e.Reason)
	} else {
		w.strField("eligibilityReason", e.EligibilityReason)
	}
	w.close('}')
	w.b = append(w.b, '\n')
	if w.bad {
		return nil, errNonPrintable
	}
	return w.b, nil
}

func writeIdentity(w *jsonWriter, id *app.Identity) {
	w.open('{')
	w.strField("state", id.State)
	w.strField("reason", id.Reason)
	w.optField("vendor", id.Vendor)
	w.optField("uuid", id.UUID)
	w.optField("serial", id.Serial)
	w.optField("nodeUID", id.NodeUID)
	w.optField("bootID", id.BootID)
	w.optField("bdf", id.BDF)
	w.optField("functionKey", id.FunctionKey)
	if id.Source != nil {
		w.source("source", *id.Source)
	}
	w.optField("evidenceID", id.EvidenceID)
	w.optField("observedAt", id.ObservedAt)
	w.optField("expiresAt", id.ExpiresAt)
	w.close('}')
}

func writeSegment(w *jsonWriter, s app.PathSegment) {
	w.open('{')
	w.strField("from", s.From)
	w.strField("relation", s.Relation)
	w.strField("to", s.To)
	w.strField("origin", s.Origin)
	w.strField("kind", s.Kind)
	w.source("source", s.Source)
	w.strField("evidenceID", s.EvidenceID)
	w.strField("observedAt", s.ObservedAt)
	w.strField("expiresAt", s.ExpiresAt)
	w.close('}')
}

func writeFinding(w *jsonWriter, f app.FindingView) {
	w.open('{')
	w.strField("id", f.ID)
	w.strField("type", f.Type)
	w.strField("severity", f.Severity)
	w.strField("state", f.State)
	w.strField("confidence", f.Confidence)
	w.strList("scope", f.Scope)
	w.array("evidence", len(f.Evidence), func(i int) {
		w.open('{')
		w.strField("observationID", f.Evidence[i].ObservationID)
		w.strField("summary", f.Evidence[i].Summary)
		w.close('}')
	})
	w.strList("missingInputs", f.MissingInputs)
	w.array("affected", len(f.Affected), func(i int) {
		w.open('{')
		w.strField("asset", f.Affected[i].Asset)
		w.strField("accuracy", f.Affected[i].Accuracy)
		w.close('}')
	})
	w.strField("firstSeen", f.FirstSeen)
	w.strField("lastSeen", f.LastSeen)
	w.strField("explanation", f.Explanation)
	w.strField("suggestedStep", f.SuggestedStep)
	w.close('}')
}

func writeCoverage(w *jsonWriter, c app.CoverageView) {
	w.open('{')
	w.optField("device", c.Device)
	w.strField("name", c.Name)
	w.strField("pathKind", c.PathKind)
	w.boolField("required", c.Required)
	w.strField("state", c.State)
	w.strField("reason", c.Reason)
	w.strList("evidenceRefs", c.EvidenceRefs)
	w.optField("observedAt", c.ObservedAt)
	w.optField("latestObservedAt", c.LatestObservedAt)
	w.optField("expiresAt", c.ExpiresAt)
	w.close('}')
}

func writeAllocation(w *jsonWriter, a *app.AllocationView) {
	w.open('{')
	w.strField("state", a.State)
	w.strField("reason", a.Reason)
	w.optField("profile", a.Profile)
	w.optField("observedAt", a.ObservedAt)
	w.optField("expiresAt", a.ExpiresAt)
	w.strList("evidenceRefs", a.EvidenceRefs)
	w.array("affectedWorkloads", len(a.AffectedWorkloads), func(i int) { writeWorkload(w, a.AffectedWorkloads[i]) })
	w.close('}')
}

func writeWorkload(w *jsonWriter, x app.WorkloadView) {
	w.open('{')
	w.strField("namespace", x.Namespace)
	w.strField("name", x.Name)
	w.strField("uid", x.UID)
	w.strField("container", x.Container)
	w.strField("createdAt", x.CreatedAt)
	w.optField("deletedAt", x.DeletedAt)
	w.close('}')
}
