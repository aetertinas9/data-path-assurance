package evidence

import (
	"fmt"
	"slices"
	"time"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// Config bounds what a window keeps and how long a reading counts for.
//
// MaxAge is how long after it was observed a reading is still fresh enough to
// judge by. Horizon is how far back of a series is retained, measured from that
// series' own most recent observation. MaxSamples is how many observations of
// one series are retained. All three must be positive; MaxSamples must be at
// least one.
//
// Horizon and MaxAge are independent and neither has to exceed the other. A
// series always retains its most recent observation, so no configuration of the
// two can evict a reading that is still fresh.
type Config struct {
	MaxAge     time.Duration
	Horizon    time.Duration
	MaxSamples int
}

// validate reports the first way in which a configuration is unusable.
func (c Config) validate() error {
	switch {
	case c.MaxAge <= 0:
		return invalidf("Config.MaxAge %s is not positive", c.MaxAge)
	case c.Horizon <= 0:
		return invalidf("Config.Horizon %s is not positive", c.Horizon)
	case c.MaxSamples < 1:
		return invalidf("Config.MaxSamples %d is less than one", c.MaxSamples)
	}
	return nil
}

// Window is the bounded evidence a rule reads: the recent observations, grouped
// into series and kept within the bounds a [Config] sets.
//
// A Window is an immutable value. [Window.Add] and [Window.Prune] return a new
// window and leave the receiver exactly as it was, so a window can be copied,
// passed and held onto freely — what it answers today it answers tomorrow.
//
// The zero Window is an empty window with no configuration. Every query and
// judgement answers as an empty window does, and assembly is refused: without a
// configuration there is nothing to say what should be retained. A window to
// assemble into comes from [NewWindow] or from another window.
type Window struct {
	cfg Config
	// series holds the retained series by key. Neither the map nor any
	// seriesData in it is mutated once a window is published; assembly builds
	// new ones, which is what lets windows share the series they have in
	// common.
	series map[seriesKey]*seriesData
}

// NewWindow returns an empty window bounded by cfg, or the zero Window and an
// error wrapping model.ErrInvalid if cfg asks for bounds that are not bounds.
func NewWindow(cfg Config) (Window, error) {
	if err := cfg.validate(); err != nil {
		return Window{}, err
	}
	return Window{cfg: cfg, series: make(map[seriesKey]*seriesData)}, nil
}

// configured reports whether w came from NewWindow. Only such a window carries
// a configuration, and only a configuration says what to retain.
func (w Window) configured() bool {
	return w.cfg.validate() == nil
}

// Add returns a new window holding o, leaving the receiver as it was.
//
// Where the observation lands is decided by when it was observed and not by
// when it arrived: one that belongs between two already retained is taken as
// readily as one that belongs at the end.
//
// An observation the model calls invalid is refused, and so is one whose value
// is a float that is not a finite number — an infinity or a NaN cannot be
// judged, and a NaN that an adapter means something by is for that adapter to
// translate into a meaning. Retention is then decided per series: what falls
// further back than Horizon from that series' most recent observation is not
// retained, and of what remains only the MaxSamples most recent are. An
// observation that does not itself survive those bounds changes nothing and is
// reported with [ErrOutOfWindow]; older ones falling out to make room for it is
// the window sliding, and is not an error.
//
// Resending a reading already retained changes nothing and succeeds: the two
// are the same reading if they agree on the instant, the value, the unit, the
// quality and the expiry, however differently they are labelled. Two readings
// of one series at one instant that do not agree are a conflict, reported with
// [ErrConflict] — neither replaces nor merges into the other, so what a series
// retains at an instant is decided once.
//
// When more than one of these applies, an invalid observation is reported
// before a conflict and a conflict before a bound. The observation is copied
// on the way in, so what a caller does to it afterwards reaches nothing.
func (w Window) Add(o model.Observation) (Window, error) {
	if !w.configured() {
		return w, invalidf("Add: the zero Window has no bounds to retain by; obtain one from NewWindow")
	}
	if err := o.Validate(); err != nil {
		return w, invalidf("Add: %s", err)
	}
	if f, ok := o.Value.Float(); ok && !isFinite(f) {
		return w, invalidf("Add: Observation.Value is %v, which is not a finite measurement", f)
	}

	key, dimensions := seriesKeyOf(o)
	existing := w.series[key]
	if i, found := existing.indexAt(o.ObservedAt); found {
		if equivalent(existing.observations[i], o) {
			return w, nil
		}
		return w, fmt.Errorf("%w: Add: %s already retains a different observation at %s",
			ErrConflict, key, o.ObservedAt.UTC().Format(time.RFC3339Nano))
	}

	merged := insertObservation(retainedObservations(existing), cloneObservation(o))
	retained := w.cfg.retain(merged)
	if _, kept := slices.BinarySearchFunc(retained, o.ObservedAt, compareObservedAt); !kept {
		return w, fmt.Errorf("%w: Add: %s does not reach back to the observation at %s",
			ErrOutOfWindow, key, o.ObservedAt.UTC().Format(time.RFC3339Nano))
	}
	return w.withSeries(key, &seriesData{
		key:          key,
		source:       o.Source,
		signal:       o.Signal,
		dimensions:   dimensions,
		observations: retained,
	}), nil
}

// Prune returns a new window without the observations older than cutoff,
// leaving the receiver as it was. An observation at the cutoff instant itself
// is kept: what is dropped is what lies strictly before it.
//
// The bounds are carried over, so the returned window is one to keep assembling
// into. A series, subject or source left with nothing retained is gone from
// every query. A cutoff of the zero time is an argument violation, and so is
// pruning the zero Window.
func (w Window) Prune(cutoff time.Time) (Window, error) {
	if !w.configured() {
		return w, invalidf("Prune: the zero Window has no bounds to retain by; obtain one from NewWindow")
	}
	if cutoff.IsZero() {
		return w, invalidf("Prune: cutoff is the zero time")
	}

	pruned := make(map[seriesKey]*seriesData, len(w.series))
	for key, data := range w.series {
		// The observations are ordered by instant, so the search returns the
		// first one at or after the cutoff — the point the series is cut at.
		cut, _ := slices.BinarySearchFunc(data.observations, cutoff, compareObservedAt)
		switch {
		case cut == 0:
			pruned[key] = data
		case cut < len(data.observations):
			kept := *data
			kept.observations = slices.Clip(data.observations[cut:])
			pruned[key] = &kept
		}
	}
	return Window{cfg: w.cfg, series: pruned}, nil
}

// withSeries returns a window like w with one series replaced. The map is
// rebuilt so that the window w published keeps answering as it did; the series
// the two have in common are shared, since none of them is ever mutated.
func (w Window) withSeries(key seriesKey, data *seriesData) Window {
	series := make(map[seriesKey]*seriesData, len(w.series)+1)
	for k, v := range w.series {
		series[k] = v
	}
	series[key] = data
	return Window{cfg: w.cfg, series: series}
}

// retainedObservations returns what a series retains, or nothing for a series
// that does not exist yet.
func retainedObservations(data *seriesData) []model.Observation {
	if data == nil {
		return nil
	}
	return data.observations
}

// insertObservation returns a new slice with o in its place among observations
// ordered by instant. The slice is built afresh, so the one a published window
// holds is untouched.
func insertObservation(observations []model.Observation, o model.Observation) []model.Observation {
	at, _ := slices.BinarySearchFunc(observations, o.ObservedAt, compareObservedAt)
	merged := make([]model.Observation, 0, len(observations)+1)
	merged = append(merged, observations[:at]...)
	merged = append(merged, o)
	merged = append(merged, observations[at:]...)
	return merged
}

// retain applies both bounds to one series' observations, ordered by instant,
// and returns what survives them.
//
// The horizon is measured from the series' own most recent observation, so a
// series is bounded by its own span and not by what other series are doing. Its
// far edge is included: an observation exactly Horizon back is retained. What
// passes the horizon is then cut to the MaxSamples most recent.
func (c Config) retain(observations []model.Observation) []model.Observation {
	if len(observations) == 0 {
		return observations
	}
	newest := observations[len(observations)-1].ObservedAt
	oldest := newest.Add(-c.Horizon)
	from, _ := slices.BinarySearchFunc(observations, oldest, compareObservedAt)
	retained := observations[from:]
	if len(retained) > c.MaxSamples {
		retained = retained[len(retained)-c.MaxSamples:]
	}
	return slices.Clip(retained)
}
