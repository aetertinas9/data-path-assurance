package tests_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

func coreAuditBoundaryObservation(t *testing.T, id string, seconds int, value model.Value) model.Observation {
	t.Helper()
	at := time.Unix(1000, 0).Add(time.Duration(seconds) * time.Second)
	o, err := model.NewObservation(model.Observation{
		ID: id, Source: model.SourceRef{Type: "agent", Name: "boundary"},
		Subject: model.AssetRef{Kind: model.KindNICPort, Canonical: "asset:boundary"},
		Signal:  "port.counter", Value: value, Unit: "count",
		ObservedAt: at, ReceivedAt: at, Quality: model.QualityGood,
		Dimensions: map[string]string{"port": "1"}, RawDigest: "original-digest",
	})
	coreAuditNoError(t, err)
	return o
}

func coreAuditBoundaryWindow(t *testing.T) evidence.Window {
	t.Helper()
	w, err := evidence.NewWindow(evidence.Config{MaxAge: 60 * time.Second, Horizon: time.Hour, MaxSamples: 3})
	coreAuditNoError(t, err)
	return w
}

func coreAuditBoundarySeries(t *testing.T, w evidence.Window, o model.Observation) evidence.Series {
	t.Helper()
	series, err := w.SeriesFor(o.Subject, o.Signal)
	coreAuditNoError(t, err)
	if len(series) != 1 {
		t.Fatalf("SeriesFor returned %d series, want 1", len(series))
	}
	return series[0]
}

func TestEVD022_EVD024_CoreAuditTypedRetransmissionAndConflict(t *testing.T) {
	for _, tc := range []struct {
		name              string
		original, changed model.Value
	}{
		{"Int", model.NewIntValue(7), model.NewIntValue(8)},
		{"Bool", model.NewBoolValue(true), model.NewBoolValue(false)},
		{"String", model.NewStringValue("x"), model.NewStringValue("y")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := coreAuditBoundaryObservation(t, "original", 0, tc.original)
			w, err := coreAuditBoundaryWindow(t).Add(o)
			coreAuditNoError(t, err)
			retransmitted := coreAuditBoundaryObservation(t, "retransmitted", 0, tc.original)
			next, err := w.Add(retransmitted)
			coreAuditNoError(t, err)
			conflicting := coreAuditBoundaryObservation(t, "conflicting", 0, tc.changed)
			rejected, err := next.Add(conflicting)
			if !errors.Is(err, evidence.ErrConflict) {
				t.Errorf("Add changed value = %v, want ErrConflict", err)
			}
			for _, state := range []struct {
				name   string
				window evidence.Window
			}{
				{"original", w}, {"retransmission", next}, {"rejected", rejected},
			} {
				got := coreAuditBoundarySeries(t, state.window, o).Observations()
				if !reflect.DeepEqual(got, []model.Observation{o}) {
					t.Errorf("%s observations = %#v, want full original %#v", state.name, got, o)
				}
			}
		})
	}
}

func TestEVD025_EVD026_EVD043_EVD053_EVD054_CoreAuditLateCounterAndEviction(t *testing.T) {
	w := coreAuditBoundaryWindow(t)
	var ordered [4]model.Observation
	for i, sample := range []struct {
		seconds int
		value   int64
	}{{0, 100}, {10, 250}, {20, 30}, {30, 90}} {
		ordered[i] = coreAuditBoundaryObservation(t, strconv.Itoa(i), sample.seconds, model.NewIntValue(sample.value))
	}
	for _, i := range []int{0, 2, 1} {
		var err error
		w, err = w.Add(ordered[i])
		coreAuditNoError(t, err)
	}
	before := w
	check := func(w evidence.Window, want []model.Observation, rate float64) {
		t.Helper()
		s := coreAuditBoundarySeries(t, w, ordered[0])
		if got := s.Observations(); !reflect.DeepEqual(got, want) {
			t.Errorf("ordered observations = %#v, want %#v", got, want)
		}
		got, err := s.Rate()
		coreAuditNoError(t, err)
		if got.PerSecond != rate || got.Discontinuities != 1 || got.Samples != 3 ||
			!got.Start.Equal(want[0].ObservedAt) || !got.End.Equal(want[2].ObservedAt) {
			t.Errorf("Rate = %#v, want rate %v, reset 1, samples 3, start %v, end %v", got, rate, want[0].ObservedAt, want[2].ObservedAt)
		}
	}
	check(before, ordered[:3], 9)
	w, err := w.Add(ordered[3])
	coreAuditNoError(t, err)
	check(w, ordered[1:], 4.5)
	check(before, ordered[:3], 9)
	fresh, err := coreAuditBoundarySeries(t, w, ordered[0]).Fresh(time.Unix(1025, 0))
	coreAuditNoError(t, err)
	if !fresh {
		t.Error("latest at t30 must be fresh at now t25")
	}
}

func TestGRF061_GRF063_GRF069_CoreAuditPreCanceledQueueAndStoppedValidation(t *testing.T) {
	p := model.PartitionKey("boundary")
	s, err := graph.NewState(p, evidence.Config{MaxAge: time.Minute, Horizon: time.Hour, MaxSamples: 3})
	coreAuditNoError(t, err)
	clock := &coreAuditClock{} // nil tick channel: no unsolicited tick can arrive
	r, err := graph.NewReducer(s, graph.ReducerConfig{Capacity: 1, SnapshotEvery: time.Second, Clock: clock})
	coreAuditNoError(t, err)
	ev, err := graph.NewResync(p, 1, []model.AssetRef{{Kind: model.KindNICPort, Canonical: "asset:queued"}}, nil)
	coreAuditNoError(t, err)
	coreAuditNoError(t, r.Offer(ev))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	timer := time.NewTimer(10 * time.Second) // deadlock guard, not a latency assertion
	defer timer.Stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-timer.C:
		t.Fatal("pre-canceled Run did not return")
	}
	wantStats := graph.Stats{Enqueued: 1}
	if got := r.Stats(); got != wantStats {
		t.Errorf("Stats = %#v, want %#v", got, wantStats)
	}
	checkEmpty := func() {
		t.Helper()
		snap := r.Snapshot()
		if snap == nil {
			t.Fatal("final snapshot is nil")
		}
		if len(snap.Assets()) != 0 || len(snap.Edges()) != 0 {
			t.Error("queued topology was applied after cancellation")
		}
		if seq, baseline := snap.Sequence(); seq != 0 || baseline || snap.Synced() {
			t.Error("queued resync established a baseline after cancellation")
		}
	}
	checkEmpty()
	wrong, err := graph.NewResync(model.PartitionKey("other"), 1, nil, nil)
	coreAuditNoError(t, err)
	for _, tc := range []struct {
		name  string
		event graph.Event
		want  error
	}{
		{"valid", ev, graph.ErrStopped}, {"nil", nil, model.ErrInvalid}, {"wrong partition", wrong, model.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.Offer(tc.event); !errors.Is(err, tc.want) {
				t.Errorf("Offer = %v, want %v", err, tc.want)
			}
			if got := r.Stats(); got != wantStats {
				t.Errorf("stopped Offer changed Stats: %#v", got)
			}
		})
	}
	checkEmpty()
}
