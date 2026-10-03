package liveingest

import (
	"slices"
	"sync"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/app/framecore"
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// maxEvaluations bounds the per-view cache of evaluations (one per distinct
// freshness); a view that is asked for more is simply recomputed.
const maxEvaluations = 8

// frameRec is one accepted frame as the ring keeps it. It is immutable once
// published: the checked values are never changed and the topology snapshot
// and digest were computed from them at commit.
type frameRec struct {
	c              *Checked
	topology       *graph.Snapshot
	topologyDigest string
}

// view is the immutable observation state of one node that commit publishes
// (GLI-070): the last accepted cursor, the ring of recent accepted frames
// (oldest first, the last one is the last accepted frame) and the Clock time
// the last frame arrived. The only mutable part is the cache of evaluations,
// which cannot be observed.
type view struct {
	cursor     fleet.SnapshotCursor
	receivedAt time.Time
	ring       []*frameRec

	mu    sync.Mutex
	cache map[time.Duration]*evaluation // guarded by mu
}

// newView returns the view that follows prev when rec was accepted at now: the
// ring is prev's ring plus rec, trimmed from the oldest end to at most
// ringFrames frames and ringObservations observations (the last frame always
// stays).
func newView(prev *view, rec *frameRec, cursor fleet.SnapshotCursor, now time.Time) *view {
	ring := make([]*frameRec, 0, ringFrames+1)
	if prev != nil {
		ring = append(ring, prev.ring...)
	}
	ring = append(ring, rec)
	total := 0
	for _, r := range ring {
		total += len(r.c.Observations)
	}
	drop := 0
	for len(ring)-drop > 1 && (len(ring)-drop > ringFrames || total > ringObservations) {
		total -= len(ring[drop].c.Observations)
		drop++
	}
	// A fresh slice, so that dropped frames are not kept alive by its array.
	return &view{
		cursor:     cursor,
		receivedAt: now,
		ring:       slices.Clone(ring[drop:]),
		cache:      map[time.Duration]*evaluation{},
	}
}

// last returns the last accepted frame.
func (v *view) last() *frameRec { return v.ring[len(v.ring)-1] }

// expired reports whether the view is older than retention (GLI-053).
func (v *view) expired(now time.Time, retention time.Duration) bool {
	return now.Sub(v.receivedAt) >= retention
}

// evaluation is what a view yields for one freshness, whatever the assessment
// time: the window W of the ring (GLI-072), its BaselineDigest and the
// evidence records that do not depend on the assessment time. It is immutable.
type evaluation struct {
	window         evidence.Window
	baselineDigest string
	// records are the evidence records of the last frame's edge provenance and
	// gpu bindings and of the width observations of W; ids is their ID set.
	records []app.EvidenceRecord
	ids     map[string]struct{}
	// observations indexes the observations of W by ID for the records of the
	// observations that a finding cites; built on first use.
	obsOnce      sync.Once
	observations map[string]model.Observation
}

// evaluation returns the evaluation of the view for freshness, computing and
// caching it on first use. An error wraps fleet.ErrInvalidInput ([defensive]:
// V2 keeps the window from rejecting any observation).
func (v *view) evaluation(freshness time.Duration) (*evaluation, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if ev := v.cache[freshness]; ev != nil {
		return ev, nil
	}
	w, err := evidence.NewWindow(framecore.WindowConfig(freshness))
	if err != nil {
		return nil, invalidInput("window configuration")
	}
	for _, rec := range v.ring {
		for _, o := range rec.c.Observations {
			if w, err = w.Add(o); err != nil {
				return nil, invalidInput("window")
			}
		}
	}
	last := v.last()
	ev := &evaluation{
		window:         w,
		baselineDigest: framecore.BaselineDigest(last.c.Partition, w),
	}
	ev.records = frameRecords(last.c, w, freshness)
	ev.ids = make(map[string]struct{}, len(ev.records))
	for _, r := range ev.records {
		ev.ids[r.ID] = struct{}{}
	}
	if len(v.cache) >= maxEvaluations {
		clear(v.cache)
	}
	v.cache[freshness] = ev
	return ev, nil
}
