package tests_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

func TestMDL005_CoreAuditOpenNamespaceConstructorValidation(t *testing.T) {
	for _, ns := range []string{":", "a:", ":a", "a:b", "::", " "} {
		t.Run(ns, func(t *testing.T) {
			id, err := model.NewTypedID(ns, "x")
			coreAuditNoError(t, err)
			coreAuditNoError(t, id.Validate())
			ref, err := model.NewAssetRef(model.KindNICPort, id)
			coreAuditNoError(t, err)
			coreAuditNoError(t, ref.Validate())
			if ref.Canonical != ns+":x" {
				t.Errorf("Canonical = %q, want %q", ref.Canonical, ns+":x")
			}
		})
	}
}

func TestMDL005_CoreAuditMalformedCanonicalBoundaries(t *testing.T) {
	// None admits a colon separator with nonempty namespace AND value.
	for _, canonical := range []string{"", "x", ":", ":x", "x:", "::"} {
		t.Run(canonical, func(t *testing.T) {
			ref := model.AssetRef{Kind: model.KindNICPort, Canonical: canonical}
			if err := ref.Validate(); !errors.Is(err, model.ErrInvalid) {
				t.Errorf("Validate(%q) = %v, want ErrInvalid", canonical, err)
			}
		})
	}
}

func TestIDN060_IDN063_CoreAuditDirectConflictScopeNormalization(t *testing.T) {
	makeRef := func(value string) model.AssetRef {
		return model.AssetRef{Kind: model.KindNICPort, Canonical: "asset:" + value,
			Aliases: []model.TypedID{
				{Namespace: "z", Value: value, Raw: []byte{1}, Source: "raw-z"},
				{Namespace: "asset", Value: value, Raw: []byte{2}, Source: "self"},
				{Namespace: "a", Value: value, Raw: []byte{}, Source: "raw-a"},
			}}
	}
	// Existing sorts AFTER Claimed: Scope order must not be globally sorted.
	c := identity.Conflict{ID: model.TypedID{Namespace: "shared", Value: "x"},
		Existing: makeRef("z"), Claimed: makeRef("a")}
	wantInput := identity.Conflict{ID: c.ID, Existing: makeRef("z"), Claimed: makeRef("a")}
	coreAuditNoError(t, c.Existing.Validate())
	coreAuditNoError(t, c.Claimed.Validate())
	got, err := identity.NewConflictFinding(c, "core-audit", []model.EvidenceRef{{ObservationID: "obs"}}, time.Unix(1, 0))
	coreAuditNoError(t, err)
	coreAuditNoError(t, got.Validate())
	if !reflect.DeepEqual(c, wantInput) {
		t.Errorf("NewConflictFinding mutated input: got %#v, want %#v", c, wantInput)
	}
	wantScope := []model.AssetRef{
		{Kind: model.KindNICPort, Canonical: "asset:z", Aliases: []model.TypedID{{Namespace: "a", Value: "z"}, {Namespace: "z", Value: "z"}}},
		{Kind: model.KindNICPort, Canonical: "asset:a", Aliases: []model.TypedID{{Namespace: "a", Value: "a"}, {Namespace: "z", Value: "a"}}},
	}
	if !reflect.DeepEqual(got.Scope, wantScope) {
		t.Errorf("Scope = %#v, want normalized ordered %#v", got.Scope, wantScope)
	}
	c.Existing.Aliases[0].Raw[0] = 9
	c.Existing.Aliases[0].Value = "changed"
	c.Claimed.Aliases[2].Source = "changed"
	if !reflect.DeepEqual(got.Scope, wantScope) {
		t.Errorf("Scope changed after input mutation: %#v", got.Scope)
	}
}

type coreAuditClock struct {
	ticks    chan time.Time
	calls    int
	stops    int
	duration time.Duration
}

func (c *coreAuditClock) Ticker(d time.Duration) graph.Ticker {
	c.calls++
	c.duration = d
	return c
}
func (c *coreAuditClock) C() <-chan time.Time { return c.ticks }
func (c *coreAuditClock) Stop()               { c.stops++ }

func TestGRF064_CoreAuditTickProgressWithPendingEvents(t *testing.T) {
	p := model.PartitionKey("core-audit")
	s, err := graph.NewState(p, evidence.Config{MaxAge: time.Minute, Horizon: time.Minute, MaxSamples: 1})
	coreAuditNoError(t, err)
	asset := model.AssetRef{Kind: model.KindNICPort, Canonical: "asset:x"}
	const workload = 1024 // finite test input; no wall-clock publication SLA
	events := make([]graph.Event, workload+2)
	for i := range events {
		var assets []model.AssetRef
		if i%2 == 0 {
			assets = []model.AssetRef{asset}
		}
		ev, err := graph.NewResync(p, uint64(i+1), assets, nil)
		coreAuditNoError(t, err)
		events[i] = ev
	}
	clock := &coreAuditClock{ticks: make(chan time.Time, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reducer *graph.Reducer
	var callbackErr error
	var progressed bool
	callbacks := 0
	reducer, err = graph.NewReducer(s, graph.ReducerConfig{
		Capacity: 2, SnapshotEvery: time.Second, Clock: clock,
		Emit: func(_ []graph.Transition) {
			callbacks++
			if callbacks > workload {
				callbackErr = errors.New("events applied after cancellation")
				cancel()
				return
			}
			// Every callback starts with another event already pending; replenish
			// before returning so a drain-until-empty loop cannot reach the tick.
			callbackErr = reducer.Offer(events[callbacks+1])
			if callbackErr != nil {
				cancel()
				return
			}
			if callbacks == 1 {
				if _, baseline := reducer.Snapshot().Sequence(); baseline {
					callbackErr = errors.New("snapshot advanced without a tick")
					cancel()
					return
				}
				clock.ticks <- time.Unix(2, 0)
			}
			if seq, baseline := reducer.Snapshot().Sequence(); baseline && seq > 0 {
				progressed = true // observe BEFORE cancellation's final publication
				cancel()
			} else if callbacks == workload {
				cancel() // finite failure path, even when ticks are starved
			}
		},
	})
	coreAuditNoError(t, err)
	coreAuditNoError(t, reducer.Offer(events[0]))
	coreAuditNoError(t, reducer.Offer(events[1]))
	done := make(chan error, 1)
	go func() { done <- reducer.Run(ctx) }()
	// Deadlock guard only: correctness is checked at callback synchronization
	// points and does not depend on elapsed time or arbitrary sleeps.
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
	case <-timer.C:
		t.Fatal("Run did not finish the finite callback workload")
	}
	coreAuditNoError(t, callbackErr)
	if !progressed {
		t.Error("tick did not publish while events remained pending, before cancellation")
	}
	if clock.calls != 1 || clock.stops != 1 || clock.duration != time.Second {
		t.Errorf("clock calls/stops/duration = %d/%d/%v", clock.calls, clock.stops, clock.duration)
	}
	if stats := reducer.Stats(); stats.Evicted != 0 {
		t.Errorf("bounded pending workload unexpectedly evicted %d events", stats.Evicted)
	}
}

func coreAuditNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
