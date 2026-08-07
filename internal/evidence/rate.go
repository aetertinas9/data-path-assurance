package evidence

import (
	"fmt"
	"math"
	"time"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// RateResult is what a counter series says about itself over the span it
// retains: the rate, the span it was computed over, how many samples went into
// it, and how many times the counter was seen to go backwards.
//
// Start and End are the timestamps of the first and last sample used, so a
// reader can see the span the rate speaks for rather than assuming one.
// Samples is at least two. Discontinuities is at least zero and is the count of
// resets the rate had to work around.
type RateResult struct {
	PerSecond       float64
	Start           time.Time
	End             time.Time
	Samples         int
	Discontinuities int
}

// Rate computes the rate of the series read as a counter.
//
// Every retained sample takes part, oldest first. Where the counter rose, the
// increase is what it rose by; where it fell, the counter is taken to have been
// reset and the increase is the value it came back at. That reading of a fall
// is deliberate and does not depend on where the samples came from: a counter
// that restarted at zero is far more likely than one that wrapped, and mistaking
// a restart for a wrap invents an increase near 2^64 — a false alarm of exactly
// the kind this system exists to avoid. Mistaking a wrap for a restart only
// loses what the counter held just before it wrapped, which is the safe way to
// be wrong.
//
// The rate is the total increase over the time actually spanned by the samples.
// Nothing is extrapolated and nothing is interpolated: no value is invented
// before the first sample, after the last one, or in a gap between two of them.
// A gap is an absence of readings, not a reading of zero.
//
// The result is always finite and never negative, whatever the samples are, and
// a series whose value never changed rates exactly zero. The span is never zero
// either — a series cannot hold two observations at one instant — so the
// division is reached only with a positive divisor.
//
// A series holding fewer than two samples has no rate: one counter reading says
// nothing, and [ErrInsufficientSamples] reports that. A series holding a sample
// that is not a number, or one that is negative, is not a counter series at
// all; that is an argument violation. Quality, unit, arrival time and sequence
// take no part in any of this.
func (s Series) Rate() (RateResult, error) {
	var observations []model.Observation
	if s.data != nil {
		observations = s.data.observations
	}
	if len(observations) < 2 {
		return RateResult{}, fmt.Errorf("%w: Rate: the series retains %d sample(s)",
			ErrInsufficientSamples, len(observations))
	}

	values := make([]float64, len(observations))
	for i, o := range observations {
		v, ok := numericValue(o.Value)
		if !ok {
			return RateResult{}, invalidf("Rate: sample %d holds a %s, which is not a counter reading",
				i, o.Value.Kind())
		}
		if v < 0 {
			return RateResult{}, invalidf("Rate: sample %d is %g, and a counter is never negative", i, v)
		}
		values[i] = v
	}

	total := 0.0
	discontinuities := 0
	for i := 1; i < len(values); i++ {
		if values[i] < values[i-1] {
			discontinuities++
			total += values[i]
			continue
		}
		total += values[i] - values[i-1]
	}

	first := observations[0].ObservedAt
	last := observations[len(observations)-1].ObservedAt
	return RateResult{
		PerSecond:       perSecond(total, last.Sub(first)),
		Start:           first,
		End:             last,
		Samples:         len(observations),
		Discontinuities: discontinuities,
	}, nil
}

// numericValue reads a sample as a float64, whether it was reported as one or
// as an integer. An integer beyond what a float64 counts exactly comes back
// approximated, which is close enough for judging a rate by.
func numericValue(v model.Value) (float64, bool) {
	if f, ok := v.Float(); ok {
		return f, true
	}
	if i, ok := v.Int(); ok {
		return float64(i), true
	}
	return 0, false
}

// perSecond divides the counted increase by the time it took.
//
// The divisor is checked rather than trusted to IEEE 754: a rate is only ever
// asked of two or more observations at distinct instants, so a non-positive
// span cannot arise, and the guard is here so that no arithmetic decides what
// happens if that ever stops being true.
//
// A quotient too large for a float64 saturates instead of becoming infinite.
// Every sample is finite and non-negative and the span is positive and finite,
// so an infinity here can only mean the true rate is beyond what a float64
// holds — and a rate, by contract, is always a finite number.
func perSecond(total float64, elapsed time.Duration) float64 {
	seconds := elapsed.Seconds()
	if seconds <= 0 {
		return 0
	}
	rate := total / seconds
	if math.IsNaN(rate) || math.IsInf(rate, 0) {
		return math.MaxFloat64
	}
	return rate
}

// isFinite reports whether f is a number a judgement can be made from.
func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
