package evidence

import (
	"slices"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// Subjects returns every asset the window retains an observation about, ordered
// by Key, bytes ascending. Distinct subjects have distinct keys, so the order
// has no ties to be arbitrary about.
//
// One asset can be named with different aliases by different collectors, so the
// reference returned for it is the one the most recent of its retained
// observations carried; among observations at the same instant, the one from
// the series that comes first in the order [Window.SeriesFor] returns series in.
// The references are copies.
func (w Window) Subjects() []model.AssetRef {
	// A pick is the reference chosen for one subject so far, with the instant
	// that won it the place.
	type pick struct {
		ref model.AssetRef
		at  time.Time
	}
	picks := make(map[string]pick, len(w.series))
	// The series are walked in order and a pick only gives way to a strictly
	// later observation, which is what makes the series order break the ties.
	for _, data := range w.orderedSeries() {
		latest, ok := data.latest()
		if !ok {
			continue
		}
		if held, taken := picks[data.key.subject]; taken && !latest.ObservedAt.After(held.at) {
			continue
		}
		picks[data.key.subject] = pick{ref: latest.Subject, at: latest.ObservedAt}
	}

	subjects := make([]model.AssetRef, 0, len(picks))
	for _, p := range picks {
		subjects = append(subjects, cloneAssetRef(p.ref))
	}
	slices.SortFunc(subjects, func(x, y model.AssetRef) int {
		return strings.Compare(x.Key(), y.Key())
	})
	return subjects
}

// Signals returns every signal the window retains an observation of about
// subject, ordered bytes ascending.
//
// Subjects are matched by Key, so a reference carrying different aliases finds
// the same subject. A subject the window knows nothing about is not an error:
// it has no signals. An invalid subject is an argument violation.
func (w Window) Signals(subject model.AssetRef) ([]model.SignalRef, error) {
	if err := subject.Validate(); err != nil {
		return nil, invalidf("Signals subject: %s", err)
	}
	key := subject.Key()
	seen := make(map[model.SignalRef]struct{})
	for series, data := range w.series {
		if series.subject != key {
			continue
		}
		if _, ok := data.latest(); !ok {
			continue
		}
		seen[series.signal] = struct{}{}
	}
	signals := make([]model.SignalRef, 0, len(seen))
	for signal := range seen {
		signals = append(signals, signal)
	}
	slices.Sort(signals)
	return signals, nil
}

// SeriesFor returns every series of signal about subject, ordered by source
// type, then source name, then dimensions, all bytes ascending. Dimensions are
// compared as sequences of pairs ordered by key, each pair by key and then by
// value, with a set that is a prefix of another coming first.
//
// Subjects are matched by Key. A pairing the window knows nothing about is not
// an error: it has no series. An invalid subject or signal is an argument
// violation.
func (w Window) SeriesFor(subject model.AssetRef, signal model.SignalRef) ([]Series, error) {
	if err := subject.Validate(); err != nil {
		return nil, invalidf("SeriesFor subject: %s", err)
	}
	if !signal.IsValid() {
		return nil, invalidf("SeriesFor signal %q is not a dotted lowercase signal name", signal.String())
	}
	matching := w.matchingSeries(subject.Key(), signal)
	series := make([]Series, 0, len(matching))
	for _, data := range matching {
		series = append(series, Series{maxAge: w.cfg.MaxAge, data: data})
	}
	return series, nil
}

// Sources returns every collector the window retains an observation from,
// ordered by type and then by name, bytes ascending.
func (w Window) Sources() []model.SourceRef {
	seen := make(map[model.SourceRef]struct{}, len(w.series))
	for series, data := range w.series {
		if _, ok := data.latest(); !ok {
			continue
		}
		seen[model.SourceRef{Type: series.sourceType, Name: series.sourceName}] = struct{}{}
	}
	sources := make([]model.SourceRef, 0, len(seen))
	for source := range seen {
		sources = append(sources, source)
	}
	slices.SortFunc(sources, func(x, y model.SourceRef) int {
		if c := strings.Compare(x.Type, y.Type); c != 0 {
			return c
		}
		return strings.Compare(x.Name, y.Name)
	})
	return sources
}

// SourceLastObserved returns the most recent instant src observed anything the
// window still retains.
//
// A source the window retains nothing from returns the zero time and no error.
// A retained observation is never at the zero time, so the zero time means
// exactly that: nothing retained. Both parts of the reference have to match —
// two collectors of one type are two sources. An invalid reference is an
// argument violation.
func (w Window) SourceLastObserved(src model.SourceRef) (time.Time, error) {
	if err := src.Validate(); err != nil {
		return time.Time{}, invalidf("SourceLastObserved source: %s", err)
	}
	var last time.Time
	// The series are walked in order and only a strictly later observation
	// takes the place, so which observation the instant is read from is settled
	// by the series order rather than by map iteration.
	for _, data := range w.orderedSeries() {
		if data.key.sourceType != src.Type || data.key.sourceName != src.Name {
			continue
		}
		latest, ok := data.latest()
		if !ok {
			continue
		}
		if last.IsZero() || latest.ObservedAt.After(last) {
			last = latest.ObservedAt
		}
	}
	return last, nil
}

// matchingSeries returns the series of one signal about one subject, in the
// order series are returned in.
func (w Window) matchingSeries(subjectKey string, signal model.SignalRef) []*seriesData {
	var matching []*seriesData
	for series, data := range w.series {
		if series.subject != subjectKey || series.signal != signal {
			continue
		}
		if _, ok := data.latest(); !ok {
			continue
		}
		matching = append(matching, data)
	}
	slices.SortFunc(matching, compareSeries)
	return matching
}

// orderedSeries returns every retained series in the total order series are
// compared by, so that a walk over the window's content never depends on map
// iteration order.
func (w Window) orderedSeries() []*seriesData {
	all := make([]*seriesData, 0, len(w.series))
	for _, data := range w.series {
		all = append(all, data)
	}
	slices.SortFunc(all, compareSeries)
	return all
}
