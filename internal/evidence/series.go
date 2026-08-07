package evidence

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// dimension is one entry of an observation's Dimensions, kept as a pair so that
// a set of them can be ordered and compared.
type dimension struct {
	key   string
	value string
}

// seriesKey is what decides whether two observations belong to the same series:
// the source, the subject, the signal and the dimensions, and nothing else. It
// is a comparable value so that it can key the window's map, which is why the
// dimensions arrive here as an encoding rather than as a map.
//
// The subject enters as its Key, so two observations naming one asset with
// different aliases land in the same series, exactly as identity comparison
// elsewhere ignores what an asset is additionally known by.
type seriesKey struct {
	subject    string
	signal     model.SignalRef
	sourceType string
	sourceName string
	dimensions string
}

// String names the series in the terms an error message needs.
func (k seriesKey) String() string {
	return "series " + k.signal.String() + " of " + k.subject +
		" from " + k.sourceType + "/" + k.sourceName
}

// seriesData is the retained content of one series. A value of it is never
// mutated once a window holds it: assembly builds a new one and leaves the old
// one to whatever windows were published with it.
type seriesData struct {
	key    seriesKey
	source model.SourceRef
	signal model.SignalRef
	// dimensions is the same set the key encodes, ordered by key, kept in this
	// form so that the series order and Series.Dimensions can be read off it.
	dimensions []dimension
	// observations is ordered by ObservedAt ascending and holds at most one
	// observation per instant.
	observations []model.Observation
}

// latest returns the most recent retained observation. A series in a window
// always has one; the guard is here because nothing in this package may panic.
func (d *seriesData) latest() (model.Observation, bool) {
	if d == nil || len(d.observations) == 0 {
		return model.Observation{}, false
	}
	return d.observations[len(d.observations)-1], true
}

// indexAt locates the retained observation at instant t, comparing instants
// rather than time.Time values, so that a monotonic reading or a location does
// not decide whether two readings are at the same moment.
func (d *seriesData) indexAt(t time.Time) (int, bool) {
	if d == nil {
		return 0, false
	}
	return slices.BinarySearchFunc(d.observations, t, compareObservedAt)
}

// seriesKeyOf reduces an observation to the series it belongs to, returning the
// key beside the ordered dimensions the key encodes.
func seriesKeyOf(o model.Observation) (seriesKey, []dimension) {
	dimensions := sortedDimensions(o.Dimensions)
	return seriesKey{
		subject:    o.Subject.Key(),
		signal:     o.Signal,
		sourceType: o.Source.Type,
		sourceName: o.Source.Name,
		dimensions: encodeDimensions(dimensions),
	}, dimensions
}

// sortedDimensions orders a dimension set by key, bytes ascending. A map has no
// repeated keys, so the order has no ties to be arbitrary about. An absent set
// and an empty one both come back as nothing, which is what makes them the same
// series.
func sortedDimensions(m map[string]string) []dimension {
	if len(m) == 0 {
		return nil
	}
	dimensions := make([]dimension, 0, len(m))
	for key, value := range m {
		dimensions = append(dimensions, dimension{key: key, value: value})
	}
	slices.SortFunc(dimensions, func(x, y dimension) int {
		return strings.Compare(x.key, y.key)
	})
	return dimensions
}

// encodeDimensions renders an ordered dimension set as a single string that
// two sets share only if they are the same set. Each key and value is written
// with its length in front, so that no arrangement of separators inside a key
// or a value can make two different sets encode alike.
func encodeDimensions(dimensions []dimension) string {
	var b strings.Builder
	for _, d := range dimensions {
		b.WriteString(strconv.Itoa(len(d.key)))
		b.WriteByte(':')
		b.WriteString(d.key)
		b.WriteString(strconv.Itoa(len(d.value)))
		b.WriteByte(':')
		b.WriteString(d.value)
	}
	return b.String()
}

// compareObservedAt orders an observation against an instant. It is the one
// comparison the retained order is built on.
func compareObservedAt(o model.Observation, t time.Time) int {
	return o.ObservedAt.Compare(t)
}

// compareSeries is the total order series are returned in: signal, then source
// type, then source name, then the dimensions.
//
// The subject is the last tiebreaker rather than the first, because the orders
// this package promises are all within one subject or not by this comparison at
// all. It is here only so that the order is total — two series that differ in
// nothing else differ in their subject — which is what keeps a sort of them
// from depending on the order they came out of a map in.
func compareSeries(a, b *seriesData) int {
	if c := strings.Compare(a.signal.String(), b.signal.String()); c != 0 {
		return c
	}
	if c := strings.Compare(a.source.Type, b.source.Type); c != 0 {
		return c
	}
	if c := strings.Compare(a.source.Name, b.source.Name); c != 0 {
		return c
	}
	if c := compareDimensions(a.dimensions, b.dimensions); c != 0 {
		return c
	}
	return strings.Compare(a.key.subject, b.key.subject)
}

// compareDimensions orders two dimension sets lexicographically as sequences of
// pairs, each pair by key first and then by value, bytes ascending. A set that
// is a prefix of the other comes first.
func compareDimensions(a, b []dimension) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := strings.Compare(a[i].key, b[i].key); c != 0 {
			return c
		}
		if c := strings.Compare(a[i].value, b[i].value); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

// equivalent reports whether two observations of the same series are the same
// reading resent.
//
// What is compared is what was measured: the instant, the value, the unit, the
// quality and the expiry. What the collector happened to call it or when it
// happened to arrive — the ID, ReceivedAt, Sequence, RawDigest, and the aliases
// the subject was named with — is not compared, so a resend that differs only
// in those is the same reading. A digest is not compared either: it says two
// payloads were the same, which is neither necessary nor sufficient for two
// readings to be.
//
// The caller establishes that both belong to the same series; that is the rest
// of the definition.
func equivalent(a, b model.Observation) bool {
	return a.ObservedAt.Equal(b.ObservedAt) &&
		valuesEqual(a.Value, b.Value) &&
		a.Unit == b.Unit &&
		a.Quality == b.Quality &&
		a.ExpiresAt.Equal(b.ExpiresAt)
}

// valuesEqual reports whether two values hold the same kind and the same
// payload. It reads through the accessors rather than comparing the opaque
// value, so that what counts as the same value stays this package's statement
// and not a consequence of how model.Value is laid out.
func valuesEqual(a, b model.Value) bool {
	if a.Kind() != b.Kind() {
		return false
	}
	switch a.Kind() {
	case model.ValueFloat:
		x, _ := a.Float()
		y, _ := b.Float()
		return x == y
	case model.ValueInt:
		x, _ := a.Int()
		y, _ := b.Int()
		return x == y
	case model.ValueBool:
		x, _ := a.Bool()
		y, _ := b.Bool()
		return x == y
	case model.ValueString:
		x, _ := a.Str()
		y, _ := b.Str()
		return x == y
	default:
		// A value of no kind holds nothing to be equal to. No such value is
		// ever retained: Add refuses an observation the model calls invalid.
		return false
	}
}

// Series is one series of a window seen as a value: the retained observations
// of one signal about one subject, from one source, under one set of
// dimensions, together with the judgements that read them.
//
// A Series is immutable and independent of the window it came from. Later
// assembly builds new series and leaves this one as it was.
//
// The zero Series is an empty series: it holds no observations, has no source,
// subject or signal worth speaking of, is not fresh, and has no rate.
type Series struct {
	// maxAge is the window's freshness bound, carried along so that a series
	// can answer for its own freshness away from the window.
	maxAge time.Duration
	data   *seriesData
}

// Source reports which collector the series came from.
func (s Series) Source() model.SourceRef {
	if s.data == nil {
		return model.SourceRef{}
	}
	return s.data.source
}

// Subject reports which asset the series is about, as the most recent retained
// observation named it. Instants are unique within a series, so there is one
// most recent observation and no tie to break.
func (s Series) Subject() model.AssetRef {
	latest, ok := s.data.latest()
	if !ok {
		return model.AssetRef{}
	}
	return cloneAssetRef(latest.Subject)
}

// Signal reports what the series measures.
func (s Series) Signal() model.SignalRef {
	if s.data == nil {
		return ""
	}
	return s.data.signal
}

// Dimensions returns a copy of the dimensions that set this series apart from
// the others of the same source, subject and signal. A series carrying none
// returns an empty map rather than nothing.
func (s Series) Dimensions() map[string]string {
	var dimensions []dimension
	if s.data != nil {
		dimensions = s.data.dimensions
	}
	m := make(map[string]string, len(dimensions))
	for _, d := range dimensions {
		m[d.key] = d.value
	}
	return m
}

// Observations returns every retained observation, oldest first by ObservedAt.
// The observations are copies: mutating them, or the slice, reaches nothing.
//
// This is content rather than judgement, so it does not take a time and does
// not hide anything: an observation too old to judge by is still shown, with
// the timestamp that says how old it is.
func (s Series) Observations() []model.Observation {
	if s.data == nil {
		return []model.Observation{}
	}
	return cloneObservations(s.data.observations)
}

// Latest returns the most recent retained observation as a copy, or (zero
// observation, false) for an empty series. A series obtained from a window
// always holds at least one observation.
func (s Series) Latest() (model.Observation, bool) {
	latest, ok := s.data.latest()
	if !ok {
		return model.Observation{}, false
	}
	return cloneObservation(latest), true
}

// Fresh reports whether the series is recent enough at now to judge by, which
// is to say whether its most recent observation is.
//
// An observation is fresh until its deadline and stale from it: the deadline
// instant itself is already stale. The deadline is the earlier of the
// observation's own ExpiresAt, when it has one, and the window's MaxAge after
// it was observed — the earlier, so that an expiry an adapter got wrong can
// only shorten the window's own bound and never stretch it.
//
// An observation timestamped in the future is fresh. Freshness only judges what
// is too old; a clock that runs ahead is for the quality an adapter attaches to
// its readings to speak to. When it was received says nothing about freshness
// either: what is judged is the age of the measurement, not of the delivery.
//
// An empty series is not fresh and that is not an error. A now of the zero time
// is an argument violation.
func (s Series) Fresh(now time.Time) (bool, error) {
	if now.IsZero() {
		return false, invalidf("Fresh: now is the zero time")
	}
	latest, ok := s.data.latest()
	if !ok {
		return false, nil
	}
	return now.Before(deadlineOf(latest, s.maxAge)), nil
}

// deadlineOf returns the instant at which o stops being fresh: the earlier of
// the expiry it carries, if it carries one, and maxAge after it was observed.
func deadlineOf(o model.Observation, maxAge time.Duration) time.Time {
	deadline := o.ObservedAt.Add(maxAge)
	if !o.ExpiresAt.IsZero() && o.ExpiresAt.Before(deadline) {
		return o.ExpiresAt
	}
	return deadline
}
