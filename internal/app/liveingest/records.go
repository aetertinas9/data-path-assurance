package liveingest

import (
	"strconv"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// maxSummaryBytes is the bound of a record summary (GLI-074).
const maxSummaryBytes = 512

// summary cuts s to the summary bound. Every part of a summary is ASCII
// (GLI-060 (a)), so cutting at a byte is cutting at a character.
func summary(s string) string {
	if len(s) > maxSummaryBytes {
		return s[:maxSummaryBytes]
	}
	return s
}

// valueText renders an observation value for a summary: integers in decimal,
// floats with the shortest representation that round-trips.
func valueText(v model.Value) string {
	if n, ok := v.Int(); ok {
		return strconv.FormatInt(n, 10)
	}
	if f, ok := v.Float(); ok {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	if b, ok := v.Bool(); ok {
		return strconv.FormatBool(b)
	}
	s, _ := v.Str()
	return s
}

// observationRecord is the evidence record of an observation: the summary
// `<signal>=<value> <unit>` and an expiry that is the earlier of the
// observation's own and ObservedAt + freshness.
func observationRecord(o model.Observation, freshness time.Duration) app.EvidenceRecord {
	expires := o.ObservedAt.Add(freshness)
	if !o.ExpiresAt.IsZero() && o.ExpiresAt.Before(expires) {
		expires = o.ExpiresAt
	}
	return app.EvidenceRecord{
		ID:         o.ID,
		Summary:    summary(string(o.Signal) + "=" + valueText(o.Value) + " " + o.Unit),
		ObservedAt: o.ObservedAt,
		ExpiresAt:  expires,
	}
}

// frameRecords returns the evidence records that do not depend on the
// assessment time (GLI-074): the edge provenance and the gpu bindings of the
// last frame and the width observations of the window. Records are
// deduplicated by ID.
func frameRecords(c *Checked, w evidence.Window, freshness time.Duration) []app.EvidenceRecord {
	out := make([]app.EvidenceRecord, 0, len(c.Provenance)+len(c.Bindings))
	seen := make(map[string]struct{}, cap(out))
	add := func(r app.EvidenceRecord) {
		if _, dup := seen[r.ID]; dup {
			return
		}
		seen[r.ID] = struct{}{}
		out = append(out, r)
	}
	for _, p := range c.Provenance {
		add(app.EvidenceRecord{
			ID: p.EvidenceID,
			Summary: summary(p.Edge.From.Kind.String() + " " + p.Edge.Relation.String() + " " +
				p.Edge.To.Kind.String() + " " + strings.ToLower(p.Edge.Origin.String())),
			ObservedAt: p.ObservedAt,
			ExpiresAt:  p.ExpiresAt,
		})
	}
	for _, b := range c.Bindings {
		add(app.EvidenceRecord{
			ID:         b.EvidenceID,
			Summary:    summary("NVIDIA UUID binding " + b.BDF),
			ObservedAt: b.ObservedAt,
			ExpiresAt:  b.ExpiresAt,
		})
	}
	for _, subject := range w.Subjects() {
		for _, signal := range []model.SignalRef{pcie.SignalLinkWidthCurrent, pcie.SignalLinkWidthExpected} {
			series, err := w.SeriesFor(subject, signal)
			if err != nil {
				continue
			}
			for _, s := range series {
				for _, o := range s.Observations() {
					add(observationRecord(o, freshness))
				}
			}
		}
	}
	return out
}

// observationIndex indexes the observations of the window by ID, built on first
// use.
func (ev *evaluation) observationIndex() map[string]model.Observation {
	ev.obsOnce.Do(func() {
		ev.observations = map[string]model.Observation{}
		w := ev.window
		for _, subject := range w.Subjects() {
			signals, err := w.Signals(subject)
			if err != nil {
				continue
			}
			for _, signal := range signals {
				series, err := w.SeriesFor(subject, signal)
				if err != nil {
					continue
				}
				for _, s := range series {
					for _, o := range s.Observations() {
						if _, ok := ev.observations[o.ID]; !ok {
							ev.observations[o.ID] = o
						}
					}
				}
			}
		}
	})
	return ev.observations
}

// recordsFor returns the evidence records of one call: the evaluation's records
// and, for every observation a finding cites that is not among them, the record
// of that observation.
func (ev *evaluation) recordsFor(findings []model.Finding, freshness time.Duration) []app.EvidenceRecord {
	out := append([]app.EvidenceRecord(nil), ev.records...)
	var added map[string]struct{}
	for _, f := range findings {
		for _, ref := range f.Evidence {
			if _, ok := ev.ids[ref.ObservationID]; ok {
				continue
			}
			if _, ok := added[ref.ObservationID]; ok {
				continue
			}
			o, ok := ev.observationIndex()[ref.ObservationID]
			if !ok {
				continue
			}
			if added == nil {
				added = map[string]struct{}{}
			}
			added[o.ID] = struct{}{}
			out = append(out, observationRecord(o, freshness))
		}
	}
	return out
}

// findingRecords returns the finding records of the findings evaluated at the
// assessment time.
func findingRecords(findings []model.Finding) []app.FindingRecord {
	out := make([]app.FindingRecord, 0, len(findings))
	for _, f := range findings {
		out = append(out, app.FindingRecord{
			ID:       f.ID,
			Type:     f.Type.String(),
			Severity: f.Severity.String(),
			State:    f.State.String(),
		})
	}
	return out
}
