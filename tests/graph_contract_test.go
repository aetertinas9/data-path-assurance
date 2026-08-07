// graph_contract_test.go — 공통 규약 (specs/graph/spec.md 2·3.1·3.2,
// GRF-001~007).
package tests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// GRF-001: 의존 경계의 **완전한** 검증은 블랙박스 밖이다 — 스펙 5절이
// `make arch-check`와 verifier의 `go list -deps ./internal/graph`에 배정한다
// (MDL-001·IDN-001·EVD-001과 같은 취급).
//
// 블랙박스가 할 수 있는 부분은 **공개 계약의 표면**이다: 3절의 모든 시그니처가
// 표준 라이브러리·pkg/model·internal/evidence의 타입만으로 표현되는지, 그리고
// graph가 정의한 Clock 포트가 표준 라이브러리만으로 구현 가능한지(⑦ — 죽은
// 메서드 없음)를 컴파일 시점에 고정한다. 아래 선언이 하나라도 어긋나면
// 컴파일이 깨진다.
func TestGRF001_PublicAPISurfaceUsesOnlyAllowedDependencies(t *testing.T) {
	// 3.3·3.4·3.5·3.7의 생성자.
	var (
		_ func(model.AssetRef, model.AssetRef, model.EdgeRelation, model.EdgeOrigin) (graph.Edge, error) = graph.NewEdge
		_ func(model.AssetRef) (model.PartitionKey, error)                                               = graph.PartitionFor
		_ func(model.PartitionKey, uint64, []model.AssetRef, []graph.Edge) (graph.Resync, error)         = graph.NewResync
		_ func(model.PartitionKey, uint64, model.AssetRef) (graph.AssetUpsert, error)                    = graph.NewAssetUpsert
		_ func(model.PartitionKey, uint64, model.AssetRef) (graph.AssetRemove, error)                    = graph.NewAssetRemove
		_ func(model.PartitionKey, uint64, graph.Edge) (graph.EdgeUpsert, error)                         = graph.NewEdgeUpsert
		_ func(model.PartitionKey, uint64, graph.Edge) (graph.EdgeRemove, error)                         = graph.NewEdgeRemove
		_ func(model.PartitionKey, uint64, model.Observation) (graph.ObservationAppend, error)           = graph.NewObservationAppend
		_ func(model.PartitionKey, evidence.Config) (graph.State, error)                                 = graph.NewState
		_ func(graph.State, graph.ReducerConfig) (*graph.Reducer, error)                                 = graph.NewReducer
	)

	// 3.3 Edge의 필드와 메서드.
	var edge graph.Edge
	var (
		_ model.AssetRef     = edge.From
		_ model.AssetRef     = edge.To
		_ model.EdgeRelation = edge.Relation
		_ model.EdgeOrigin   = edge.Origin
		_ func() error       = edge.Validate
	)

	// 3.5 State.
	var state graph.State
	var (
		_ func() model.PartitionKey                                 = state.Partition
		_ func(graph.Event) (graph.State, graph.ApplyResult, error) = state.Apply
		_ func() *graph.Snapshot                                    = state.Snapshot
	)

	// 3.5 ApplyResult·Transition.
	var res graph.ApplyResult
	var tr graph.Transition
	var (
		_ graph.ApplyOutcome   = res.Outcome
		_ []graph.Transition   = res.Transitions
		_ graph.TransitionKind = tr.Kind
		_ model.AssetRef       = tr.Asset
		_ graph.Edge           = tr.Edge
	)

	// 3.6 Snapshot (nil 수신자에서 메서드 값만 얻는다 — 호출하지 않는다).
	var snap *graph.Snapshot
	var (
		_ func() model.PartitionKey                    = snap.Partition
		_ func() (uint64, bool)                        = snap.Sequence
		_ func() bool                                  = snap.Synced
		_ func() []model.AssetRef                      = snap.Assets
		_ func(model.AssetRef) (model.AssetRef, error) = snap.Asset
		_ func() []graph.Edge                          = snap.Edges
		_ func(model.AssetRef) ([]graph.Edge, error)   = snap.EdgesFrom
		_ func(model.AssetRef) ([]graph.Edge, error)   = snap.EdgesTo
		_ func() evidence.Window                       = snap.Window
	)

	// 3.7 Reducer와 구성.
	var reducer *graph.Reducer
	var cfg graph.ReducerConfig
	var stats graph.Stats
	var (
		_ func(graph.Event) error     = reducer.Offer
		_ func(context.Context) error = reducer.Run
		_ func() *graph.Snapshot      = reducer.Snapshot
		_ func() graph.Stats          = reducer.Stats
		_ int                         = cfg.Capacity
		_ time.Duration               = cfg.SnapshotEvery
		_ graph.Clock                 = cfg.Clock
		_ func([]graph.Transition)    = cfg.Emit
		_ func(model.PartitionKey)    = cfg.RequestResync
		_ uint64                      = stats.Enqueued
		_ uint64                      = stats.Evicted
		_ uint64                      = stats.Applied
		_ uint64                      = stats.Resynced
		_ uint64                      = stats.Stale
		_ uint64                      = stats.Gaps
		_ uint64                      = stats.AwaitingResync
		_ uint64                      = stats.ObservationRejected
		_ uint64                      = stats.Invalid
		_ uint64                      = stats.ResyncRequests
	)

	// 3.7 Clock 포트는 표준 라이브러리만으로 구현된다 (결정 지점 ⑦ — Now()는 없다).
	var (
		_ graph.Clock                      = newGrfFakeClock()
		_ graph.Ticker                     = &grfFakeTicker{}
		_ func(time.Duration) graph.Ticker = newGrfFakeClock().Ticker
	)

	// 3.4 이벤트 집합은 이 6종이 전부 Event를 만족한다.
	var (
		_ graph.Event = graph.Resync{}
		_ graph.Event = graph.AssetUpsert{}
		_ graph.Event = graph.AssetRemove{}
		_ graph.Event = graph.EdgeUpsert{}
		_ graph.Event = graph.EdgeRemove{}
		_ graph.Event = graph.ObservationAppend{}
	)

	// 3.2 공개 sentinel.
	var _ error = graph.ErrStopped

	t.Log("GRF-001의 전이적 의존 검증은 make arch-check와 verifier의 go list -deps 소관이다 (스펙 5절)")
}

// GRF-002 (무-panic): 어떤 입력에서도 공개 함수·메서드는 panic하지 않는다.
// (시계·난수·환경·I/O 부재 조항은 verifier의 소스 검사 소관 — 스펙 5절.)
func TestGRF002_PublicAPIDoesNotPanic(t *testing.T) {
	p := grfPartitionA(t)
	valid := grfNIC0(t)
	var zeroRef model.AssetRef
	var zeroState graph.State
	var nilSnap *graph.Snapshot

	cases := []struct {
		name string
		fn   func()
	}{
		{"NewEdge(zero 인자)", func() { _, _ = graph.NewEdge(zeroRef, zeroRef, 0, 0) }},
		{"NewEdge(열거 밖 음수)", func() { _, _ = graph.NewEdge(valid, grfSwitchPort1(t), -3, -7) }},
		{"Edge{}.Validate", func() { _ = (graph.Edge{}).Validate() }},
		{"PartitionFor(zero)", func() { _, _ = graph.PartitionFor(zeroRef) }},
		{"PartitionFor(비-anchor)", func() { _, _ = graph.PartitionFor(valid) }},

		{"NewResync(zero 인자)", func() { _, _ = graph.NewResync("", 0, nil, nil) }},
		{"NewResync(무효 원소)", func() { _, _ = graph.NewResync(p, 0, []model.AssetRef{zeroRef}, []graph.Edge{{}}) }},
		{"NewAssetUpsert(zero 인자)", func() { _, _ = graph.NewAssetUpsert("", 0, zeroRef) }},
		{"NewAssetRemove(zero 인자)", func() { _, _ = graph.NewAssetRemove("", 0, zeroRef) }},
		{"NewEdgeUpsert(zero 인자)", func() { _, _ = graph.NewEdgeUpsert("", 0, graph.Edge{}) }},
		{"NewEdgeRemove(zero 인자)", func() { _, _ = graph.NewEdgeRemove("", 0, graph.Edge{}) }},
		{"NewObservationAppend(zero 인자)", func() { _, _ = graph.NewObservationAppend("", 0, model.Observation{}) }},

		{"zero 이벤트의 헤더·접근자", func() {
			for _, ev := range grfZeroEvents() {
				_ = ev.Partition()
				_ = ev.Sequence()
			}
			_ = (graph.Resync{}).Assets()
			_ = (graph.Resync{}).Edges()
			_ = (graph.AssetUpsert{}).Asset()
			_ = (graph.AssetRemove{}).Asset()
			_ = (graph.EdgeUpsert{}).Edge()
			_ = (graph.EdgeRemove{}).Edge()
			_ = (graph.ObservationAppend{}).Observation()
		}},

		{"NewState(zero 인자)", func() { _, _ = graph.NewState("", evidence.Config{}) }},
		{"zero State의 조회", func() {
			_ = zeroState.Partition()
			_ = zeroState.Snapshot()
		}},
		{"zero State의 Apply(nil)", func() { _, _, _ = zeroState.Apply(nil) }},
		{"zero State의 Apply(zero 이벤트)", func() { _, _, _ = zeroState.Apply(graph.AssetUpsert{}) }},
		{"zero State의 Apply(유효 이벤트)", func() {
			_, _, _ = zeroState.Apply(grfAssetUpsert(t, p, 1, valid))
		}},

		{"nil *Snapshot의 모든 메서드", func() {
			_ = nilSnap.Partition()
			_, _ = nilSnap.Sequence()
			_ = nilSnap.Synced()
			_ = nilSnap.Assets()
			_ = nilSnap.Edges()
			_, _ = nilSnap.Asset(valid)
			_, _ = nilSnap.Asset(zeroRef)
			_, _ = nilSnap.EdgesFrom(valid)
			_, _ = nilSnap.EdgesFrom(zeroRef)
			_, _ = nilSnap.EdgesTo(valid)
			_, _ = nilSnap.EdgesTo(zeroRef)
			_ = nilSnap.Window()
		}},

		{"빈 snapshot의 모든 메서드", func() {
			snap := zeroState.Snapshot()
			_ = snap.Partition()
			_, _ = snap.Sequence()
			_ = snap.Synced()
			_ = snap.Assets()
			_ = snap.Edges()
			_, _ = snap.Asset(zeroRef)
			_, _ = snap.EdgesFrom(zeroRef)
			_, _ = snap.EdgesTo(zeroRef)
			_ = snap.Window()
		}},

		{"NewReducer(zero 인자)", func() { _, _ = graph.NewReducer(graph.State{}, graph.ReducerConfig{}) }},

		{"열거형의 String·IsValid (열거 밖 값 포함)", func() {
			outcomes := []graph.ApplyOutcome{
				graph.OutcomeApplied, graph.OutcomeResynced, graph.OutcomeStale,
				graph.OutcomeGap, graph.OutcomeAwaitingResync, graph.OutcomeObservationRejected,
				graph.ApplyOutcome(0), graph.ApplyOutcome(-1), graph.ApplyOutcome(9999),
			}
			for _, o := range outcomes {
				_ = o.String()
				_ = o.IsValid()
			}
			kinds := []graph.TransitionKind{
				graph.TransitionAssetAdded, graph.TransitionAssetRemoved,
				graph.TransitionEdgeAdded, graph.TransitionEdgeRemoved,
				graph.TransitionKind(0), graph.TransitionKind(-1), graph.TransitionKind(9999),
			}
			for _, k := range kinds {
				_ = k.String()
				_ = k.IsValid()
			}
		}},

		{"구성된 reducer의 조회·거부 경로", func() {
			r, err := graph.NewReducer(grfNewState(t, p), graph.ReducerConfig{
				Capacity: 1, SnapshotEvery: time.Second, Clock: newGrfFakeClock(),
			})
			if err != nil {
				return
			}
			_ = r.Snapshot()
			_ = r.Stats()
			_ = r.Offer(nil)
			_ = r.Offer(graph.AssetUpsert{})
			var nilCtx context.Context
			_ = r.Run(nilCtx)
		}},
	}
	for _, tc := range cases {
		mustNotPanic(t, tc.name, tc.fn)
	}
}

// GRF-003: 무효 인자는 model.ErrInvalid 계열 오류 + zero value 반환이고 보존
// 상태를 바꾸지 않는다.
func TestGRF003_InvalidArgumentsReturnErrInvalidAndZeroValues(t *testing.T) {
	p := grfPartitionA(t)
	var zeroRef model.AssetRef

	t.Run("생성자", func(t *testing.T) {
		if got, err := graph.NewEdge(zeroRef, zeroRef, 0, 0); !isInvalid(err) {
			t.Errorf("NewEdge의 오류 = %v, want model.ErrInvalid 계열", err)
		} else {
			assertZeroEdge(t, got, "NewEdge")
		}
		if got, err := graph.PartitionFor(zeroRef); !isInvalid(err) || got != model.PartitionKey("") {
			t.Errorf("PartitionFor = (%q, %v), want (zero, model.ErrInvalid 계열)", got.String(), err)
		}
		if got, err := graph.NewState("", evidence.Config{}); !isInvalid(err) {
			t.Errorf("NewState의 오류 = %v, want model.ErrInvalid 계열", err)
		} else if got.Partition() != model.PartitionKey("") {
			t.Errorf("NewState가 zero State를 반환하지 않았다")
		}
		if got, err := graph.NewReducer(graph.State{}, graph.ReducerConfig{}); !isInvalid(err) || got != nil {
			t.Errorf("NewReducer = (%v, %v), want (nil, model.ErrInvalid 계열)", got, err)
		}
	})

	t.Run("조회", func(t *testing.T) {
		s := grfSyncedState(t, p, 1, []model.AssetRef{grfNIC0(t)}, nil)
		snap := s.Snapshot()
		before := grfSnapshotString(t, snap, grfNow)

		for _, tc := range grfInvalidRefs(t) {
			if _, err := snap.Asset(tc.ref); !isInvalid(err) {
				t.Errorf("Asset(%s)의 오류 = %v, want model.ErrInvalid 계열", tc.name, err)
			}
			if _, err := snap.EdgesFrom(tc.ref); !isInvalid(err) {
				t.Errorf("EdgesFrom(%s)의 오류 = %v, want model.ErrInvalid 계열", tc.name, err)
			}
			if _, err := snap.EdgesTo(tc.ref); !isInvalid(err) {
				t.Errorf("EdgesTo(%s)의 오류 = %v, want model.ErrInvalid 계열", tc.name, err)
			}
		}
		assertSnapshotUnchanged(t, before, grfSnapshotString(t, snap, grfNow), "무효 조회 뒤의 snapshot")
	})

	t.Run("Apply", func(t *testing.T) {
		s := grfSyncedState(t, p, 1, []model.AssetRef{grfNIC0(t)}, nil)
		grfApplyRejected(t, s, nil, "Apply(nil)")
		grfApplyRejected(t, s, graph.EdgeUpsert{}, "Apply(zero 이벤트)")
		grfApplyRejected(t, s, grfForeignEvent{partition: p, seq: 2}, "Apply(외부 구현)")
	})
}

// GRF-004 (결정론): 같은 초기 State + 같은 이벤트 열 → 같은 상태·같은
// ApplyResult 열.
func TestGRF004_ApplyIsDeterministic(t *testing.T) {
	p := grfPartitionA(t)

	build := func() []graph.Event {
		return []graph.Event{
			grfAssetUpsert(t, p, 1, grfNIC0(t)), // AwaitingResync
			grfResync(t, p, 10, []model.AssetRef{grfNIC0(t), grfSwitchPort1(t)},
				[]graph.Edge{grfConnected(t, grfNIC0(t), grfSwitchPort1(t))}),
			grfAssetUpsert(t, p, 11, grfNIC1(t)),
			grfObsAppend(t, p, 12, evdFloat(t, evdAt(0), 1)),
			grfObsAppend(t, p, 13, evdFloat(t, evdAt(0), 2)), // 충돌 → 거부
			grfAssetUpsert(t, p, 13, grfSwitchPort2(t)),      // stale
			grfEdgeUpsert(t, p, 14, grfIntended(t, grfNIC0(t), grfSwitchPort2(t))),
			grfAssetRemove(t, p, 15, grfNIC0(t)), // cascade
			grfAssetUpsert(t, p, 30, grfNIC1(t)), // gap
			grfAssetUpsert(t, p, 31, grfNIC1(t)), // awaiting
			grfObsAppend(t, p, 32, evdFloat(t, evdAt(time.Second), 3)),
			grfResync(t, p, 5, nil, nil),
		}
	}

	run := func() (string, []string) {
		s := grfNewState(t, p)
		evs := build()
		results := make([]string, 0, len(evs))
		for _, ev := range evs {
			next, res, err := s.Apply(ev)
			results = append(results, grfApplyResultString(res, err))
			s = next
		}
		return grfStateString(t, s, grfNow), results
	}

	stateA, resultsA := run()
	stateB, resultsB := run()

	assertSnapshotUnchanged(t, stateA, stateB, "같은 이벤트 열의 최종 State")
	assertStringsEqual(t, resultsB, resultsA, "같은 이벤트 열의 ApplyResult 열")
}

// GRF-004 (결정론 — Reducer): 같은 이벤트 열로 구동된 두 Reducer의 최종 발행
// snapshot과 Emit batch 열이 같다.
func TestGRF004_ReducerIsDeterministic(t *testing.T) {
	p := grfPartitionA(t)

	events := func(t *testing.T) []graph.Event {
		t.Helper()
		return []graph.Event{
			grfResync(t, p, 1, []model.AssetRef{grfNIC0(t), grfNIC1(t)}, nil),
			grfAssetUpsert(t, p, 2, grfSwitchPort1(t)),
			grfEdgeUpsert(t, p, 3, grfConnected(t, grfNIC0(t), grfSwitchPort1(t))),
			grfObsAppend(t, p, 4, evdFloat(t, evdAt(0), 1)),
			grfAssetRemove(t, p, 5, grfNIC0(t)),
			grfAssetUpsert(t, p, 99, grfSwitchPort2(t)), // gap
			grfResync(t, p, 100, []model.AssetRef{grfSwitchPort2(t)}, nil),
		}
	}

	drive := func() (string, string, graph.Stats) {
		// Capacity를 이벤트 수보다 크게 두어 축출이 일어나지 않게 한다.
		opts := grfDefaultOpts()
		opts.capacity = 32
		h := grfNewHarness(t, grfNewState(t, p), opts)
		h.MustOffer(t, events(t)...)
		h.Start(t)
		h.WaitProcessed(t, 7)
		h.TickAndWait(t, "발행 대기", func(snap *graph.Snapshot) bool {
			return snap != nil && snap.Synced()
		})
		if err := h.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}
		return grfSnapshotString(t, h.reducer.Snapshot(), grfNow), h.emit.Rendered(), h.reducer.Stats()
	}

	snapA, emitA, statsA := drive()
	snapB, emitB, statsB := drive()

	assertSnapshotUnchanged(t, snapA, snapB, "두 Reducer의 최종 발행 snapshot")
	assertSnapshotUnchanged(t, emitA, emitB, "두 Reducer의 Emit batch 열")
	assertStats(t, statsB, statsA, "두 Reducer의 Stats")
}

// GRF-005 (방어적 복사 — edge): 생성자에 넘긴 참조 필드나 반환된 컬렉션을
// 변형해도 이미 만들어진 값과 이후 조회 결과는 바뀌지 않는다.
func TestGRF005_ConstructorsAndQueriesCopyDefensively(t *testing.T) {
	p := grfPartitionA(t)

	t.Run("NewEdge는 endpoint를 깊게 복사한다", func(t *testing.T) {
		ref := grfMutableRef(t)
		e := grfNewEdge(t, ref, grfSwitchPort1(t), model.RelConnectedTo, model.OriginObserved)
		before := grfEdgeString(e)

		ref.Aliases[0].Raw[0] = 0xFF
		ref.Aliases[0] = grfAliasA(t)
		ref.Aliases = nil

		if got := grfEdgeString(e); got != before {
			t.Errorf("NewEdge 뒤 인자 변형이 Edge에 반영되었다\n got=%s\nwant=%s", got, before)
		}
	})

	t.Run("이벤트 생성자는 payload를 깊게 복사한다", func(t *testing.T) {
		ref := grfMutableRef(t)
		au := grfAssetUpsert(t, p, 1, ref)
		ar := grfAssetRemove(t, p, 2, ref)
		beforeUpsert := grfAssetString(au.Asset())
		beforeRemove := grfAssetString(ar.Asset())

		ref.Aliases[0].Raw[0] = 0xFF
		ref.Aliases[0] = grfAliasA(t)

		if got := grfAssetString(au.Asset()); got != beforeUpsert {
			t.Errorf("AssetUpsert의 payload가 변형되었다\n got=[%s]\nwant=[%s]", got, beforeUpsert)
		}
		if got := grfAssetString(ar.Asset()); got != beforeRemove {
			t.Errorf("AssetRemove의 payload가 변형되었다\n got=[%s]\nwant=[%s]", got, beforeRemove)
		}

		// 접근자가 돌려준 값을 변형해도 이벤트는 바뀌지 않는다.
		got := au.Asset()
		if len(got.Aliases) > 0 {
			got.Aliases[0] = grfAliasB(t)
			if len(got.Aliases[0].Raw) > 0 {
				got.Aliases[0].Raw[0] = 0x00
			}
		}
		if again := grfAssetString(au.Asset()); again != beforeUpsert {
			t.Errorf("접근자 반환값 변형이 이벤트에 반영되었다\n got=[%s]\nwant=[%s]", again, beforeUpsert)
		}
	})

	t.Run("ObservationAppend는 관측의 참조 필드를 복사한다", func(t *testing.T) {
		obs := evdFloat(t, evdAt(0), 1)
		obs.Dimensions = map[string]string{"lane": "0"}
		obs.Subject = grfMutableRef(t)

		ev := grfObsAppend(t, p, 1, obs)
		before := evdObsString(ev.Observation())

		obs.Dimensions["lane"] = "변형됨"
		obs.Dimensions["새키"] = "새값"
		obs.Subject.Aliases[0].Raw[0] = 0xFF

		if got := evdObsString(ev.Observation()); got != before {
			t.Errorf("인자 변형이 이벤트에 반영되었다\n got=%s\nwant=%s", got, before)
		}

		returned := ev.Observation()
		if returned.Dimensions != nil {
			returned.Dimensions["lane"] = "또 변형됨"
		}
		if len(returned.Subject.Aliases) > 0 && len(returned.Subject.Aliases[0].Raw) > 0 {
			returned.Subject.Aliases[0].Raw[0] = 0x11
		}
		if got := evdObsString(ev.Observation()); got != before {
			t.Errorf("접근자 반환값 변형이 이벤트에 반영되었다\n got=%s\nwant=%s", got, before)
		}
	})

	t.Run("Resync는 slice와 그 원소를 복사한다", func(t *testing.T) {
		assets := []model.AssetRef{grfMutableRef(t), grfNIC1(t)}
		edges := []graph.Edge{grfConnected(t, grfNIC0(t), grfSwitchPort1(t))}

		ev := grfResync(t, p, 1, assets, edges)
		beforeAssets := grfAssetKeys(ev.Assets())
		beforeEdges := grfEdgeIdentities(ev.Edges())
		beforeFirst := grfAssetString(ev.Assets()[0])

		// 호출자가 넘긴 slice와 원소를 변형한다.
		assets[0] = grfSwitchPort2(t)
		assets[1] = grfUnknownAsset(t)
		edges[0] = grfIntended(t, grfNIC0(t), grfSwitchPort2(t))

		assertStringsEqual(t, grfAssetKeys(ev.Assets()), beforeAssets, "인자 slice 변형 뒤의 Assets()")
		assertStringsEqual(t, grfEdgeIdentities(ev.Edges()), beforeEdges, "인자 slice 변형 뒤의 Edges()")

		// 접근자가 돌려준 컬렉션과 그 원소를 변형한다.
		gotAssets := ev.Assets()
		if len(gotAssets) > 0 {
			gotAssets[0] = grfUnknownAsset(t)
		}
		gotEdges := ev.Edges()
		if len(gotEdges) > 0 {
			gotEdges[0] = grfIntended(t, grfNIC0(t), grfSwitchPort2(t))
		}
		assertStringsEqual(t, grfAssetKeys(ev.Assets()), beforeAssets, "반환 컬렉션 변형 뒤의 Assets()")
		assertStringsEqual(t, grfEdgeIdentities(ev.Edges()), beforeEdges, "반환 컬렉션 변형 뒤의 Edges()")
		if got := grfAssetString(ev.Assets()[0]); got != beforeFirst {
			t.Errorf("반환 원소 변형이 이벤트에 반영되었다\n got=[%s]\nwant=[%s]", got, beforeFirst)
		}
	})

	t.Run("Snapshot이 돌려준 컬렉션은 내부 상태와 공유되지 않는다", func(t *testing.T) {
		ref := grfMutableRef(t)
		edge := grfConnected(t, ref, grfSwitchPort1(t))
		s := grfSyncedState(t, p, 1, []model.AssetRef{ref, grfNIC1(t)}, []graph.Edge{edge})
		snap := s.Snapshot()
		before := grfSnapshotString(t, snap, grfNow)

		assets := snap.Assets()
		if len(assets) > 0 {
			if len(assets[0].Aliases) > 0 && len(assets[0].Aliases[0].Raw) > 0 {
				assets[0].Aliases[0].Raw[0] = 0xFF
			}
			assets[0] = grfUnknownAsset(t)
		}

		edges := snap.Edges()
		if len(edges) > 0 {
			if len(edges[0].From.Aliases) > 0 && len(edges[0].From.Aliases[0].Raw) > 0 {
				edges[0].From.Aliases[0].Raw[0] = 0xFF
			}
			edges[0] = grfIntended(t, grfNIC1(t), grfSwitchPort2(t))
		}

		from, err := snap.EdgesFrom(grfNIC0(t))
		requireNoErr(t, err, "EdgesFrom")
		if len(from) > 0 {
			from[0] = graph.Edge{}
		}
		to, err := snap.EdgesTo(grfSwitchPort1(t))
		requireNoErr(t, err, "EdgesTo")
		if len(to) > 0 {
			to[0] = graph.Edge{}
		}

		got, err := snap.Asset(grfNIC0(t))
		requireNoErr(t, err, "Asset")
		if len(got.Aliases) > 0 && len(got.Aliases[0].Raw) > 0 {
			got.Aliases[0].Raw[0] = 0xFF
		}

		assertSnapshotUnchanged(t, before, grfSnapshotString(t, snap, grfNow), "반환 컬렉션 변형 뒤의 snapshot")
	})

	t.Run("ApplyResult.Transitions를 변형해도 State는 바뀌지 않는다", func(t *testing.T) {
		s := grfSyncedState(t, p, 1, nil, nil)
		next, res := grfApply(t, s, grfAssetUpsert(t, p, 2, grfMutableRef(t)), graph.OutcomeApplied, "transition 변형")
		before := grfStateString(t, next, grfNow)

		for i := range res.Transitions {
			res.Transitions[i] = graph.Transition{}
		}
		assertSnapshotUnchanged(t, before, grfStateString(t, next, grfNow), "Transitions 변형 뒤의 State")
	})
}

// GRF-006 (불변성·항등): 발행된 값은 이후 연산에 영향받지 않고, 같은 조회는
// 몇 번을 어떤 순서로 해도 같은 결과다.
func TestGRF006_PublishedValuesAreImmutableAndQueriesAreIdempotent(t *testing.T) {
	p := grfPartitionA(t)

	base := grfSyncedState(t, p, 10, []model.AssetRef{grfNIC0(t), grfNIC1(t)},
		[]graph.Edge{grfConnected(t, grfNIC0(t), grfSwitchPort1(t))})
	base, _ = grfApply(t, base, grfObsAppend(t, p, 11, evdFloat(t, evdAt(0), 1)), graph.OutcomeApplied, "관측")

	stateBefore := grfStateString(t, base, grfNow)
	snap := base.Snapshot()
	snapBefore := grfSnapshotString(t, snap, grfNow)
	series := evdDefaultSeries(t, snap.Window())
	seriesBefore := evdSeriesString(series, grfNow, "series")

	event := grfResync(t, p, 20, []model.AssetRef{grfSwitchPort2(t)}, nil)
	eventBefore := grfAssetKeys(event.Assets())

	// 파생 연산을 여러 번 한다.
	derived := base
	derived, _ = grfApply(t, derived, event, graph.OutcomeResynced, "파생 1")
	derived, _ = grfApply(t, derived, grfObsAppend(t, p, 21, evdFloat(t, evdAt(time.Second), 2)), graph.OutcomeApplied, "파생 2")
	derived, _ = grfApply(t, derived, grfAssetRemove(t, p, 22, grfSwitchPort2(t)), graph.OutcomeApplied, "파생 3")

	assertSnapshotUnchanged(t, stateBefore, grfStateString(t, base, grfNow), "원래 State")
	assertSnapshotUnchanged(t, snapBefore, grfSnapshotString(t, snap, grfNow), "발행된 snapshot")
	assertSnapshotUnchanged(t, seriesBefore, evdSeriesString(series, grfNow, "series"), "발행된 Series")
	assertStringsEqual(t, grfAssetKeys(event.Assets()), eventBefore, "이미 만들어진 이벤트")

	// 값 복사·인자 전달도 안전하다.
	copyOfBase := base
	assertSnapshotUnchanged(t, stateBefore, grfStateString(t, copyOfBase, grfNow), "State의 값 복사본")
	func(inner graph.State) {
		assertSnapshotUnchanged(t, stateBefore, grfStateString(t, inner, grfNow), "인자로 전달된 State")
	}(base)

	// 같은 조회를 순서를 바꿔 반복해도 결과는 같다.
	for i := 0; i < 3; i++ {
		assertSnapshotUnchanged(t, snapBefore, grfSnapshotString(t, snap, grfNow), "반복 조회")
		assertStringsEqual(t, grfEdgeIdentities(snap.Edges()), grfEdgeIdentities(snap.Edges()), "Edges() 반복")
		assertStringsEqual(t, grfAssetKeys(snap.Assets()), grfAssetKeys(snap.Assets()), "Assets() 반복")
	}
}

// GRF-007 (정렬 계약): 컬렉션의 순서는 삽입·적용 순서와 무관하게 항상 같다.
func TestGRF007_CollectionsAreSortedByContract(t *testing.T) {
	p := grfPartitionA(t)

	assets := []model.AssetRef{
		grfSwitchPort2(t), grfNIC0(t), grfRootPort(t), grfSwitchAnchor(t), grfNIC1(t),
	}
	edges := []graph.Edge{
		grfNewEdge(t, grfNIC0(t), grfSwitchPort2(t), model.RelConnectedTo, model.OriginObserved),
		grfNewEdge(t, grfNIC0(t), grfSwitchPort1(t), model.RelUpstreamOf, model.OriginObserved),
		grfNewEdge(t, grfNIC0(t), grfSwitchPort1(t), model.RelConnectedTo, model.OriginIntended),
		grfNewEdge(t, grfNIC0(t), grfSwitchPort1(t), model.RelConnectedTo, model.OriginObserved),
		grfNewEdge(t, grfSwitchAnchor(t), grfNIC0(t), model.RelConnectedTo, model.OriginObserved),
		grfNewEdge(t, grfNIC1(t), grfSwitchPort1(t), model.RelConnectedTo, model.OriginObserved),
	}

	wantAssetKeys := grfSortedAssetKeys(assets)
	wantEdgeIDs := grfSortedEdgeIdentities(edges)

	t.Run("적용 순서와 무관하게 같은 순서로 노출된다", func(t *testing.T) {
		// 순열 전수(5!·6!)는 불필요하게 크므로 앞쪽 표본만 쓴다 — 순서 무관성은
		// 표본으로도 충분히 드러나고, 정렬 자체는 wantAssetKeys·wantEdgeIDs가
		// 스펙 규칙으로 독립 계산한다.
		assetOrders := grfIndexPermutations(len(assets))[:12]
		edgeOrders := grfIndexPermutations(len(edges))[:6]
		for _, assetOrder := range assetOrders {
			for _, edgeOrder := range edgeOrders {
				s := grfSyncedState(t, p, 0, nil, nil)
				seq := uint64(0)
				for _, i := range assetOrder {
					seq++
					s, _ = grfApply(t, s, grfAssetUpsert(t, p, seq, assets[i]), graph.OutcomeApplied, "asset upsert")
				}
				for _, i := range edgeOrder {
					seq++
					s, _ = grfApply(t, s, grfEdgeUpsert(t, p, seq, edges[i]), graph.OutcomeApplied, "edge upsert")
				}

				snap := s.Snapshot()
				assertStringsEqual(t, grfAssetKeys(snap.Assets()), wantAssetKeys, "Assets() 정렬")
				assertStringsEqual(t, grfEdgeIdentities(snap.Edges()), wantEdgeIDs, "Edges() 정렬")
			}
		}
	})

	t.Run("EdgesFrom·EdgesTo도 edge 전순서다", func(t *testing.T) {
		s := grfSyncedState(t, p, 0, assets, edges)
		snap := s.Snapshot()

		fromNIC0 := make([]graph.Edge, 0, len(edges))
		toPort1 := make([]graph.Edge, 0, len(edges))
		for _, e := range edges {
			if e.From.Key() == grfNIC0(t).Key() {
				fromNIC0 = append(fromNIC0, e)
			}
			if e.To.Key() == grfSwitchPort1(t).Key() {
				toPort1 = append(toPort1, e)
			}
		}
		assertEdgesFrom(t, snap, grfNIC0(t), fromNIC0, "정렬 계약")
		assertEdgesTo(t, snap, grfSwitchPort1(t), toPort1, "정렬 계약")
	})

	t.Run("Resync 접근자도 같은 순서다", func(t *testing.T) {
		ev := grfResync(t, p, 1, assets, edges)
		assertStringsEqual(t, grfAssetKeys(ev.Assets()), wantAssetKeys, "Resync.Assets() 정렬")
		assertStringsEqual(t, grfEdgeIdentities(ev.Edges()), wantEdgeIDs, "Resync.Edges() 정렬")
	})
}

// 3.5 (열거 규약): ApplyOutcome·TransitionKind는 model의 열거 규약을 따른다.
func TestSpec35_OutcomeAndTransitionKindEnums(t *testing.T) {
	outcomes := []struct {
		v    graph.ApplyOutcome
		want string
	}{
		{graph.OutcomeApplied, "Applied"},
		{graph.OutcomeResynced, "Resynced"},
		{graph.OutcomeStale, "Stale"},
		{graph.OutcomeGap, "Gap"},
		{graph.OutcomeAwaitingResync, "AwaitingResync"},
		{graph.OutcomeObservationRejected, "ObservationRejected"},
	}
	seen := make(map[string]bool, len(outcomes))
	for _, tc := range outcomes {
		if got := tc.v.String(); got != tc.want {
			t.Errorf("ApplyOutcome.String() = %q, want %q", got, tc.want)
		}
		if !tc.v.IsValid() {
			t.Errorf("%s의 IsValid()가 거짓이다", tc.want)
		}
		if seen[tc.want] {
			t.Errorf("ApplyOutcome 상수 %q가 중복이다", tc.want)
		}
		seen[tc.want] = true
	}

	kinds := []struct {
		v    graph.TransitionKind
		want string
	}{
		{graph.TransitionAssetAdded, "AssetAdded"},
		{graph.TransitionAssetRemoved, "AssetRemoved"},
		{graph.TransitionEdgeAdded, "EdgeAdded"},
		{graph.TransitionEdgeRemoved, "EdgeRemoved"},
	}
	seenKinds := make(map[string]bool, len(kinds))
	for _, tc := range kinds {
		if got := tc.v.String(); got != tc.want {
			t.Errorf("TransitionKind.String() = %q, want %q", got, tc.want)
		}
		if !tc.v.IsValid() {
			t.Errorf("%s의 IsValid()가 거짓이다", tc.want)
		}
		if seenKinds[tc.want] {
			t.Errorf("TransitionKind 상수 %q가 중복이다", tc.want)
		}
		seenKinds[tc.want] = true
	}

	// zero value와 열거 밖 값(음수 포함 — 기반 타입은 부호 있는 정수 계열이다)은
	// 무효다.
	for _, v := range []graph.ApplyOutcome{0, -1, -128, 9999} {
		if v.IsValid() {
			t.Errorf("열거 밖 ApplyOutcome(%d)의 IsValid()가 참이다", int(v))
		}
	}
	for _, v := range []graph.TransitionKind{0, -1, -128, 9999} {
		if v.IsValid() {
			t.Errorf("열거 밖 TransitionKind(%d)의 IsValid()가 참이다", int(v))
		}
	}
}

// 3.2: ErrStopped는 model.ErrInvalid와 구별되는 공개 sentinel이다.
func TestSpec32_ErrStoppedIsADistinctSentinel(t *testing.T) {
	if graph.ErrStopped == nil {
		t.Fatalf("graph.ErrStopped가 nil이다")
	}
	if errors.Is(graph.ErrStopped, model.ErrInvalid) {
		t.Errorf("ErrStopped가 model.ErrInvalid 계열로 분류된다")
	}
	if errors.Is(model.ErrInvalid, graph.ErrStopped) {
		t.Errorf("model.ErrInvalid가 ErrStopped 계열로 분류된다")
	}
	if !errors.Is(graph.ErrStopped, graph.ErrStopped) {
		t.Errorf("errors.Is(ErrStopped, ErrStopped)가 거짓이다")
	}

	// evidence의 sentinel은 graph의 공개 오류 계약에 나타나지 않는다 (3.2) —
	// window 거부는 OutcomeObservationRejected로 드러난다 (GRF-047에서 검증).
	if errors.Is(graph.ErrStopped, evidence.ErrConflict) || errors.Is(graph.ErrStopped, evidence.ErrOutOfWindow) {
		t.Errorf("ErrStopped가 evidence sentinel과 얽혀 있다")
	}
}

// --- 이 파일 전용 보조 ---

// grfMutableRef는 Raw를 가진 alias를 하나 달고 있는 AssetRef다 — 호출자가
// 생성 후에 변형할 수 있는 참조 필드를 제공한다 (GRF-005).
func grfMutableRef(t *testing.T) model.AssetRef {
	t.Helper()
	return mustAssetRef(t, model.KindNICPort,
		mustTypedID(t, string(model.NamespacePCIBDF), "0000:af:00.0"),
		grfAliasWithRaw([]byte{1, 2, 3}))
}

// grfIndexPermutations는 0..n-1의 모든 순열을 만든다 (GRF-007 삽입 순서 무관 검증).
func grfIndexPermutations(n int) [][]int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	return grfPermute(idx)
}

func grfPermute(in []int) [][]int {
	if len(in) <= 1 {
		return [][]int{append([]int(nil), in...)}
	}
	var out [][]int
	for i := range in {
		rest := make([]int, 0, len(in)-1)
		rest = append(rest, in[:i]...)
		rest = append(rest, in[i+1:]...)
		for _, perm := range grfPermute(rest) {
			next := make([]int, 0, len(in))
			next = append(next, in[i])
			next = append(next, perm...)
			out = append(out, next)
		}
	}
	return out
}
