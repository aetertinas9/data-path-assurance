package explain

import (
	"strconv"
	"strings"

	"github.com/aetertinas9/data-path-assurance/internal/app"
)

// textMode is the evaluation mode shown on the first text line. This build
// only evaluates offline inputs.
const textMode = "offline"

const hexDigits = "0123456789ABCDEF"

// token renders a value as one text token: bytes outside 0x21-0x7E and '%'
// become %XX, a value of exactly "-" becomes %2D, and an empty value is "-".
func token(s string) string {
	if s == "" {
		return "-"
	}
	if s == "-" {
		return "%2D"
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e || c == '%' {
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// optToken renders an optional value; an omitted value is "-".
func optToken(o app.Optional) string {
	if !o.Set {
		return "-"
	}
	return token(o.Value)
}

// freeText renders a free string written at the end of a line: unchanged
// except that leading and trailing spaces become %20.
func freeText(s string) string {
	start, end := 0, len(s)
	for start < end && s[start] == ' ' {
		start++
	}
	for end > start && s[end-1] == ' ' {
		end--
	}
	return strings.Repeat("%20", start) + s[start:end] + strings.Repeat("%20", len(s)-end)
}

type textWriter struct{ b strings.Builder }

// line writes one line of space-separated parts.
func (w *textWriter) line(parts ...string) {
	w.b.WriteString(strings.Join(parts, " "))
	w.b.WriteByte('\n')
}

func kv(k, v string) string { return k + "=" + token(v) }

func kvOpt(k string, v app.Optional) string { return k + "=" + optToken(v) }

// RenderText renders the explanation as text. It reads only what the JSON
// rendering of the same explanation holds.
func RenderText(e app.Explanation) []byte {
	w := &textWriter{}
	gpu := e.Kind == app.TargetGPU
	if gpu {
		w.line("gpu/"+token(e.TargetName), kv("phase", e.Phase), kv("qualification", e.Qualification),
			kvOpt("revision", e.GraphRevision), kv("reason", e.Reason), "mode="+textMode)
	} else {
		w.line("node/"+token(e.TargetName), kv("eligibility", e.NodeEligibility), kv("qualification", e.Qualification),
			kvOpt("revision", e.GraphRevision), kv("eligibilityReason", e.EligibilityReason), "mode="+textMode)
	}

	w.line("PATH")
	if gpu {
		w.line("  target", kv("uid", e.TargetUID), kv("node", e.NodeName), kv("nodeUID", e.NodeUID),
			kv("eligibility", e.NodeEligibility), kv("assessment", e.NodeAssessmentRevision),
			kv("observed", e.ObservedAt), kv("evaluated", e.EvaluatedAt))
		id := e.Identity
		if id == nil {
			id = &app.Identity{}
		}
		sourceType, sourceName := app.Optional{}, app.Optional{}
		if id.Source != nil {
			sourceType = app.Optional{Value: id.Source.Type, Set: true}
			sourceName = app.Optional{Value: id.Source.Name, Set: true}
		}
		w.line("  identity", kv("state", id.State), kv("reason", id.Reason), kvOpt("vendor", id.Vendor),
			kvOpt("uuid", id.UUID), kvOpt("serial", id.Serial), kvOpt("nodeUID", id.NodeUID),
			kvOpt("bootID", id.BootID), kvOpt("bdf", id.BDF), kvOpt("function", id.FunctionKey),
			kvOpt("sourceType", sourceType), kvOpt("sourceName", sourceName), kvOpt("evidence", id.EvidenceID),
			kvOpt("observed", id.ObservedAt), kvOpt("expires", id.ExpiresAt))
	} else {
		w.line("  target", kv("uid", e.TargetUID), kv("assessment", e.NodeAssessmentRevision),
			kv("observed", e.ObservedAt), kv("evaluated", e.EvaluatedAt))
		if len(e.DeviceSummaries) == 0 {
			w.line("  device (none)")
		}
		for _, d := range e.DeviceSummaries {
			w.line("  device", token(d.Name), kv("uid", d.UID), kv("desired", d.DesiredState),
				kv("generation", d.ObservedGeneration), kv("phase", d.LifecyclePhase),
				kv("qualification", d.Qualification), kv("reason", d.Reason))
		}
	}
	if len(e.PathSegments) == 0 {
		w.line("  segment (none)")
	}
	for _, s := range e.PathSegments {
		w.line("  segment", token(s.From), token(s.Relation), token(s.To), kv("origin", s.Origin),
			kv("kind", s.Kind), kv("sourceType", s.Source.Type), kv("sourceName", s.Source.Name),
			kv("evidence", s.EvidenceID), kv("observed", s.ObservedAt), kv("expires", s.ExpiresAt))
	}

	w.line("FINDINGS")
	if len(e.ActiveFindings) == 0 {
		w.line("  (none)")
	}
	for _, f := range e.ActiveFindings {
		w.line("  finding", token(f.ID), kv("type", f.Type), kv("severity", f.Severity), kv("state", f.State),
			kv("confidence", f.Confidence), kv("firstSeen", f.FirstSeen), kv("lastSeen", f.LastSeen))
		for _, s := range f.Scope {
			w.line("    scope", token(s))
		}
		for _, ev := range f.Evidence {
			if ev.Summary == "" {
				w.line("    evidence", token(ev.ObservationID))
			} else {
				w.line("    evidence", token(ev.ObservationID), freeText(ev.Summary))
			}
		}
		for _, m := range f.MissingInputs {
			w.line("    missing", token(m))
		}
		for _, a := range f.Affected {
			w.line("    affected", token(a.Asset), kv("accuracy", a.Accuracy))
		}
		if f.Explanation != "" {
			w.line("    explanation", freeText(f.Explanation))
		}
		if f.SuggestedStep != "" {
			w.line("    suggestedStep", freeText(f.SuggestedStep))
		}
	}

	w.line("COVERAGE")
	if len(e.Coverage) == 0 {
		w.line("  (none)")
	}
	for _, c := range e.Coverage {
		parts := []string{"  coverage", token(c.Name)}
		if !gpu {
			parts = append(parts, kvOpt("device", c.Device))
		}
		parts = append(parts, kv("pathKind", c.PathKind), "required="+strconv.FormatBool(c.Required),
			kv("state", c.State), kv("reason", c.Reason), kvOpt("observed", c.ObservedAt),
			kvOpt("latest", c.LatestObservedAt), kvOpt("expires", c.ExpiresAt))
		w.line(parts...)
		for _, ref := range c.EvidenceRefs {
			w.line("    evidence", token(ref))
		}
	}

	w.line("ALLOCATION")
	if gpu && e.Allocation != nil {
		a := e.Allocation
		w.line("  allocation", kv("state", a.State), kv("reason", a.Reason), kvOpt("profile", a.Profile),
			kvOpt("observed", a.ObservedAt), kvOpt("expires", a.ExpiresAt))
		for _, ref := range a.EvidenceRefs {
			w.line("    evidence", token(ref))
		}
	}
	if len(e.AffectedWorkloads) == 0 {
		w.line("  workload (none)")
	}
	for _, x := range e.AffectedWorkloads {
		w.line("  workload", token(x.Namespace)+"/"+token(x.Name), kv("uid", x.UID), kv("container", x.Container),
			kv("created", x.CreatedAt), kvOpt("deleted", x.DeletedAt))
	}

	w.line("LIMITATIONS")
	if e.Truncated {
		t := e.TotalCounts
		w.line("  truncated", "pathSegments="+strconv.Itoa(t.PathSegments), "activeFindings="+strconv.Itoa(t.ActiveFindings),
			"coverage="+strconv.Itoa(t.Coverage), "affectedWorkloads="+strconv.Itoa(t.AffectedWorkloads),
			"deviceSummaries="+strconv.Itoa(t.DeviceSummaries), "evidenceRefs="+strconv.Itoa(t.EvidenceRefs),
			"limitations="+strconv.Itoa(t.Limitations))
	}
	for _, l := range e.Limitations {
		if l.Subject.Set {
			w.line("  "+token(l.Code), token(l.Subject.Value))
		} else {
			w.line("  " + token(l.Code))
		}
	}
	return []byte(w.b.String())
}
