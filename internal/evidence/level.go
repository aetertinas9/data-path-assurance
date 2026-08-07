package evidence

import (
	"time"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// Level judges how well the window's own content supports a claim about signal
// on subject at the time now.
//
// The judgement reads the most recent observation of each series and nothing
// else. A pairing with no series at all, and one whose every series has gone
// stale, are both model.LevelUnknown: however rich the evidence once was, a
// claim is not supported by readings too old to stand behind. One fresh series
// is enough for the answer not to be Unknown — fresh evidence is never talked
// down.
//
// Above that, the question is whether independent collectors are seeing the
// same thing. A fresh series counts towards that unless its latest reading is
// marked model.QualityDegraded, and independence is read off the source type:
// two types seeing the subject is model.LevelCorroborated, while two collectors
// of one type are model.LevelObserved, because two readings of one kind are
// usually one measurement read twice and counting them twice inflates
// confidence. Everything else fresh is model.LevelObserved, degraded readings
// included — degraded evidence does not raise confidence, but it is still
// evidence that something was seen.
//
// Whether the fresh series agree with each other is not judged here. Two
// collectors reporting contradictory values are still corroborating that they
// are both watching; what the contradiction means is for the rule that reads
// the values, to which a contradiction is a finding rather than a doubt.
//
// model.LevelInferred and model.LevelOutOfScope are never returned. Inferring
// needs topology and correlation, and being out of scope is a statement about
// the design; neither can be read off a window. An invalid argument — including
// a now of the zero time — returns the zero EvidenceLevel, which is not
// model.LevelUnknown, together with an error wrapping model.ErrInvalid.
func (w Window) Level(subject model.AssetRef, signal model.SignalRef, now time.Time) (model.EvidenceLevel, error) {
	if err := subject.Validate(); err != nil {
		return 0, invalidf("Level subject: %s", err)
	}
	if !signal.IsValid() {
		return 0, invalidf("Level signal %q is not a dotted lowercase signal name", signal.String())
	}
	if now.IsZero() {
		return 0, invalidf("Level: now is the zero time")
	}

	fresh := false
	corroborating := make(map[string]struct{})
	for _, data := range w.matchingSeries(subject.Key(), signal) {
		latest, ok := data.latest()
		if !ok {
			continue
		}
		if !now.Before(deadlineOf(latest, w.cfg.MaxAge)) {
			continue
		}
		fresh = true
		if latest.Quality != model.QualityDegraded {
			corroborating[data.key.sourceType] = struct{}{}
		}
	}

	switch {
	case !fresh:
		return model.LevelUnknown, nil
	case len(corroborating) >= 2:
		return model.LevelCorroborated, nil
	default:
		return model.LevelObserved, nil
	}
}
