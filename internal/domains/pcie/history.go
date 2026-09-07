package pcie

import (
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
	"maps"
	"slices"
	"strings"
	"time"
)

func (b *widthBatch) degraded() bool {
	return b.valid && b.currentWidth.Cmp(b.expectedWidth) < 0
}

func (b *widthBatch) fresh(now time.Time) (bool, error) {
	if !b.valid || b.at.After(now) {
		return false, nil
	}
	for _, r := range []widthReading{b.current, b.expected} {
		fresh, err := r.series.Fresh(now)
		if err != nil || !fresh {
			return false, err
		}
	}
	return true, nil
}

func evaluateSubject(w evidence.Window, subject model.AssetRef, now time.Time) (model.Finding, bool) {
	histories := make(map[model.SourceRef]map[time.Time]*widthBatch)
	var latest time.Time
	for _, signal := range []model.SignalRef{SignalLinkWidthCurrent, SignalLinkWidthExpected} {
		series, err := w.SeriesFor(subject, signal)
		if err != nil {
			return model.Finding{}, false
		}
		for _, s := range series {
			for _, o := range s.Observations() {
				at := o.ObservedAt.Round(0).UTC()
				if latest.IsZero() || at.After(latest) {
					latest = at
				}
				if histories[o.Source] == nil {
					histories[o.Source] = make(map[time.Time]*widthBatch)
				}
				if histories[o.Source][at] == nil {
					histories[o.Source][at] = &widthBatch{at: at}
				}
				batch := histories[o.Source][at]
				batch.readings = append(batch.readings, widthReading{o, s})
			}
		}
	}
	if latest.IsZero() || latest.After(now) {
		return model.Finding{}, false
	}
	sources := make([]model.SourceRef, 0, len(histories))
	for source := range histories {
		sources = append(sources, source)
	}
	slices.SortFunc(sources, func(a, b model.SourceRef) int {
		if c := strings.Compare(a.Type, b.Type); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	var agreed *widthBatch
	var selected []*widthBatch
	for _, source := range sources {
		batches := make([]*widthBatch, 0, len(histories[source]))
		for _, batch := range histories[source] {
			batch.validate()
			batches = append(batches, batch)
		}
		slices.SortFunc(batches, func(a, b *widthBatch) int { return a.at.Compare(b.at) })
		head := batches[len(batches)-1]
		fresh, err := head.fresh(now)
		if err != nil || (head.at.Equal(latest) && !fresh) {
			return model.Finding{}, false
		}
		if !fresh {
			continue
		}
		if agreed != nil && (!sameBaseline(agreed, head) || agreed.degraded() != head.degraded()) {
			return model.Finding{}, false
		}
		agreed = head
		if selected != nil || !head.at.Equal(latest) || !head.degraded() {
			continue
		}
		start := len(batches) - 1
		for start > 0 {
			previous, next := batches[start-1], batches[start]
			if !previous.degraded() || !sameBaseline(previous, next) || next.at.Sub(previous.at) > LinkWidthMaxGap {
				break
			}
			start--
		}
		suffix := batches[start:]
		if len(suffix) >= LinkWidthMinSamples && head.at.Sub(suffix[0].at) >= LinkWidthMinDuration {
			selected = suffix
		}
	}
	if selected == nil {
		return model.Finding{}, false
	}
	finding, err := makeWidthFinding(selected)
	return finding, err == nil
}

func sameBaseline(a, b *widthBatch) bool {
	return a.valid && b.valid && a.expectedWidth.Cmp(b.expectedWidth) == 0 &&
		maps.Equal(a.current.observation.Dimensions, b.current.observation.Dimensions)
}
