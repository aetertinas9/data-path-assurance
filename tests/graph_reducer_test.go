// graph_reducer_test.go — single writer 실행 계약 (specs/graph/spec.md 3.7,
// GRF-060~069).
//
// 비동기 대기는 전부 조건 폴링(grfWaitFor·WaitProcessed)이다 — 고정 sleep으로
// "충분히 기다렸겠지"를 가정하지 않는다. 최종 단정은 가능한 한 Run이 반환한
// 뒤(Stop 이후)에 한다 — 그 시점부터는 reducer goroutine이 더 이상 아무것도
// 바꾸지 않으므로 경합 없는 단정이 된다.
package tests

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// GRF-060: 유효한 State·cfg면 reducer를 만들고 초기 snapshot을 즉시 발행한다.
func TestGRF060_NewReducerPublishesInitialSnapshot(t *testing.T) {
	p := grfPartitionA(t)
	initial := grfSyncedState(t, p, 10, []model.AssetRef{grfNIC0(t)},
		[]graph.Edge{grfConnected(t, grfNIC0(t), grfSwitchPort1(t))})

	clock := newGrfFakeClock()
	r, err := graph.NewReducer(initial, graph.ReducerConfig{
		Capacity:      1,
		SnapshotEvery: time.Nanosecond,
		Clock:         clock,
	})
	requireNoErr(t, err, "NewReducer(nil Emit·RequestResync)")
	if r == nil {
		t.Fatalf("NewReducer가 nil reducer와 nil 오류를 함께 반환했다")
	}

	// Run 전에도 Snapshot()은 비-nil이고 초기 State와 관측상 동일하다.
	snap := r.Snapshot()
	if snap == nil {
		t.Fatalf("Run 전의 Snapshot()이 nil이다")
	}
	assertSnapshotUnchanged(t,
		grfSnapshotString(t, initial.Snapshot(), grfNow),
		grfSnapshotString(t, snap, grfNow),
		"Run 전에 발행된 초기 snapshot")

	// Run 전에는 Clock을 건드리지 않는다 (Ticker는 Run 시작 시 요청한다).
	if got := clock.TickerCount(); got != 0 {
		t.Errorf("NewReducer가 Clock.Ticker를 %d회 호출했다, want 0", got)
	}

	// 계수는 전부 0이다.
	assertStats(t, r.Stats(), graph.Stats{}, "NewReducer 직후")
}

// GRF-060 (edge): 무효한 State·cfg는 nil과 model.ErrInvalid 계열 오류다.
func TestGRF060_NewReducerRejectsInvalidConfiguration(t *testing.T) {
	p := grfPartitionA(t)
	good := grfNewState(t, p)
	clock := newGrfFakeClock()

	valid := func() graph.ReducerConfig {
		return graph.ReducerConfig{Capacity: 4, SnapshotEvery: time.Second, Clock: clock}
	}

	cases := []struct {
		name    string
		initial graph.State
		cfg     graph.ReducerConfig
	}{
		{"zero value State", graph.State{}, valid()},
		{"Capacity가 0", good, graph.ReducerConfig{Capacity: 0, SnapshotEvery: time.Second, Clock: clock}},
		{"Capacity가 음수", good, graph.ReducerConfig{Capacity: -1, SnapshotEvery: time.Second, Clock: clock}},
		{"Capacity가 크게 음수", good, graph.ReducerConfig{Capacity: math.MinInt32, SnapshotEvery: time.Second, Clock: clock}},
		{"SnapshotEvery가 0", good, graph.ReducerConfig{Capacity: 4, SnapshotEvery: 0, Clock: clock}},
		{"SnapshotEvery가 음수", good, graph.ReducerConfig{Capacity: 4, SnapshotEvery: -time.Second, Clock: clock}},
		{"Clock이 nil", good, graph.ReducerConfig{Capacity: 4, SnapshotEvery: time.Second, Clock: nil}},
		{"zero value ReducerConfig{}", good, graph.ReducerConfig{}},
		{"전부 위반", graph.State{}, graph.ReducerConfig{Capacity: -1, SnapshotEvery: -1, Clock: nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := graph.NewReducer(tc.initial, tc.cfg)
			requireErrInvalid(t, err, "NewReducer("+tc.name+")")
			if r != nil {
				t.Errorf("무효 구성의 NewReducer가 비-nil reducer를 반환했다")
			}
		})
	}

	t.Run("nil Emit·RequestResync는 유효하다", func(t *testing.T) {
		cfg := valid()
		cfg.Emit = nil
		cfg.RequestResync = nil
		r, err := graph.NewReducer(good, cfg)
		requireNoErr(t, err, "NewReducer(nil 콜백)")
		if r == nil {
			t.Fatalf("NewReducer가 nil reducer와 nil 오류를 함께 반환했다")
		}
	})
}

// GRF-061 (Offer 검증 — edge): 유효한 Offer는 Enqueued를 1 증가시키고, 무효한
// Offer는 큐·계수를 바꾸지 않는다.
func TestGRF061_OfferValidatesAndCountsEnqueued(t *testing.T) {
	p := grfPartitionA(t)
	h := grfNewHarness(t, grfNewState(t, p), grfDefaultOpts())

	for i := 1; i <= 3; i++ {
		if err := h.reducer.Offer(grfAssetUpsert(t, p, uint64(i), grfNIC0(t))); err != nil {
			t.Fatalf("유효한 Offer(#%d)가 오류를 냈다: %v", i, err)
		}
		if got := h.reducer.Stats().Enqueued; got != uint64(i) {
			t.Errorf("Offer %d회 뒤 Enqueued = %d, want %d", i, got, i)
		}
	}

	before := h.reducer.Stats()
	invalid := []struct {
		name string
		ev   graph.Event
	}{
		{"nil Event", nil},
		{"zero value 이벤트", graph.AssetUpsert{}},
		{"외부 구현", grfForeignEvent{partition: p, seq: 9}},
		{"타 파티션", grfAssetUpsert(t, grfPartitionB(t), 9, grfNIC0(t))},
	}
	for _, tc := range invalid {
		if err := h.reducer.Offer(tc.ev); !isInvalid(err) {
			t.Errorf("Offer(%s)의 오류 = %v, want model.ErrInvalid 계열", tc.name, err)
		}
	}
	assertStats(t, h.reducer.Stats(), before, "무효 Offer 뒤의 Stats")
}

// GRF-062 (배압 drop-oldest — edge): 큐가 가득 차면 가장 오래된 이벤트가
// 축출되고 새 이벤트는 거부되지 않는다.
func TestGRF062_OfferEvictsOldestWhenQueueIsFull(t *testing.T) {
	p := grfPartitionA(t)
	a := grfNIC0(t)
	b := grfNIC1(t)
	c := grfSwitchPort1(t)
	d := grfSwitchPort2(t)

	opts := grfDefaultOpts()
	opts.capacity = 2
	h := grfNewHarness(t, grfNewState(t, p), opts)

	// 큐를 Capacity까지 채운다 — 축출은 없다.
	h.MustOffer(t,
		grfResync(t, p, 1, []model.AssetRef{a, b}, nil),
		grfResync(t, p, 2, []model.AssetRef{b, c}, nil))
	if got := h.reducer.Stats(); got.Enqueued != 2 || got.Evicted != 0 {
		t.Fatalf("Capacity까지 채운 뒤 Stats = %s, want enqueued=2 evicted=0", grfStatsString(got))
	}

	// 하나 더 넣으면 가장 먼저 enqueue된 것이 축출된다.
	h.MustOffer(t, grfAssetUpsert(t, p, 3, d))
	stats := h.reducer.Stats()
	if stats.Enqueued != 3 || stats.Evicted != 1 {
		t.Fatalf("축출 뒤 Stats = %s, want enqueued=3 evicted=1", grfStatsString(stats))
	}

	// Run은 축출되지 않은 이벤트를 enqueue 순서로 적용한다.
	h.Start(t)
	h.WaitProcessed(t, 2)
	h.TickAndWait(t, "축출 뒤의 snapshot 발행 대기", func(snap *graph.Snapshot) bool {
		return snap != nil && snap.Synced()
	})
	runErr := h.Stop(t)
	if runErr == nil {
		t.Errorf("취소된 Run이 nil을 반환했다")
	}

	snap := h.reducer.Snapshot()
	// 축출된 Resync(1)의 효과(asset a)는 어디에도 나타나지 않는다.
	assertSnapshotAssets(t, snap, []model.AssetRef{b, c, d}, "축출 뒤의 최종 topology")
	assertSequence(t, snap, 3, true, true, "축출 뒤의 최종 sequence")

	final := h.reducer.Stats()
	assertStats(t, final, graph.Stats{Enqueued: 3, Evicted: 1, Applied: 1, Resynced: 1}, "축출 시나리오의 Stats")
}

// GRF-062 (edge): 큐 길이는 항상 Capacity 이하다 — 넘치는 만큼 축출된다.
func TestGRF062_QueueNeverExceedsCapacity(t *testing.T) {
	p := grfPartitionA(t)

	const capacity = 3
	const offered = 10

	opts := grfDefaultOpts()
	opts.capacity = capacity
	h := grfNewHarness(t, grfSyncedState(t, p, 0, nil, nil), opts)

	for i := 1; i <= offered; i++ {
		h.MustOffer(t, grfAssetUpsert(t, p, uint64(i), grfIndexedAsset(t, i)))
	}
	stats := h.reducer.Stats()
	if stats.Enqueued != offered || stats.Evicted != offered-capacity {
		t.Fatalf("Stats = %s, want enqueued=%d evicted=%d", grfStatsString(stats), offered, offered-capacity)
	}

	h.Start(t)
	h.WaitProcessed(t, capacity)
	if err := h.Stop(t); err == nil {
		t.Errorf("취소된 Run이 nil을 반환했다")
	}

	final := h.reducer.Stats()
	if grfProcessed(final) != capacity {
		t.Errorf("처리된 이벤트 %d건, want %d: %s", grfProcessed(final), capacity, grfStatsString(final))
	}
}

// GRF-063 (Run 수명주기 — edge).
func TestGRF063_RunLifecycle(t *testing.T) {
	p := grfPartitionA(t)

	t.Run("(a) nil ctx는 model.ErrInvalid 계열 오류다", func(t *testing.T) {
		h := grfNewHarness(t, grfNewState(t, p), grfDefaultOpts())
		var nilCtx context.Context
		if err := grfRunWithTimeout(t, h.reducer, nilCtx, "Run(nil ctx)"); !isInvalid(err) {
			t.Errorf("Run(nil ctx)의 오류 = %v, want model.ErrInvalid 계열", err)
		}
	})

	t.Run("(b) 실행 중·반환 뒤의 재호출은 model.ErrInvalid 계열 오류다", func(t *testing.T) {
		h := grfNewHarness(t, grfNewState(t, p), grfDefaultOpts())
		h.Start(t)

		if err := grfRunWithTimeout(t, h.reducer, context.Background(), "실행 중 Run 재호출"); !isInvalid(err) {
			t.Errorf("실행 중 Run 재호출의 오류 = %v, want model.ErrInvalid 계열", err)
		}

		if err := h.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		if err := grfRunWithTimeout(t, h.reducer, context.Background(), "반환 뒤 Run 재호출"); !isInvalid(err) {
			t.Errorf("반환 뒤 Run 재호출의 오류 = %v, want model.ErrInvalid 계열", err)
		}
	})

	t.Run("(c) 취소 시 최종 snapshot 발행 후 ctx.Err()를 반환한다", func(t *testing.T) {
		const offered = 200

		h := grfNewHarness(t, grfNewState(t, p), grfReducerOpts{capacity: offered, snapshotEvery: grfSnapshotEvery})

		// index 0은 baseline, 이후는 seq i+1의 AssetUpsert다 — 처리된 건수 n에서
		// 최종 상태가 유일하게 정해진다 (FIFO·prefix 처리).
		h.MustOffer(t, grfResync(t, p, 1, nil, nil))
		for i := 2; i <= offered; i++ {
			h.MustOffer(t, grfAssetUpsert(t, p, uint64(i), grfIndexedAsset(t, i)))
		}

		h.Start(t)
		runErr := h.Stop(t)

		// ctx.Err()를 반환한다.
		if runErr == nil {
			t.Fatalf("취소된 Run이 nil을 반환했다")
		}
		if !isContextCanceled(runErr) {
			t.Errorf("Run의 반환값 = %v, want context.Canceled", runErr)
		}

		stats := h.reducer.Stats()
		n := grfProcessed(stats)
		if n > offered {
			t.Fatalf("처리 건수 %d가 offer 건수 %d를 넘었다", n, offered)
		}

		snap := h.reducer.Snapshot()
		if snap == nil {
			t.Fatalf("Run 반환 뒤의 Snapshot()이 nil이다")
		}

		// 취소 전 적용된 모든 이벤트의 효과가 최종 snapshot에 보이고, 큐 잔여
		// 이벤트의 효과는 보이지 않는다.
		if n == 0 {
			assertSequence(t, snap, 0, false, false, "아무것도 처리하지 않은 최종 snapshot")
			assertEmptyTopology(t, snap, "아무것도 처리하지 않은 최종 snapshot")
			return
		}
		assertSequence(t, snap, n, true, true, "최종 snapshot")

		want := make([]model.AssetRef, 0, n)
		for i := 2; i <= int(n); i++ {
			want = append(want, grfIndexedAsset(t, i))
		}
		assertSnapshotAssets(t, snap, want, "최종 snapshot의 topology")
	})

	t.Run("(d) 반환 뒤의 Offer는 ErrStopped다", func(t *testing.T) {
		h := grfNewHarness(t, grfSyncedState(t, p, 0, nil, nil), grfDefaultOpts())
		h.Start(t)
		if err := h.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		requireErrStopped(t, h.reducer.Offer(grfAssetUpsert(t, p, 1, grfNIC0(t))), "정지 후 Offer")

		// §3.7 Offer 규칙 1은 정지 상태 검사보다 먼저 적용된다.
		if err := h.reducer.Offer(nil); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("정지 후 Offer(nil) = %v, want model.ErrInvalid", err)
		}
	})

	t.Run("(e) 건강한 배선에서 Invalid는 증가하지 않는다", func(t *testing.T) {
		// Offer가 Apply와 같은 검증을 먼저 하므로(3.7 Offer 규칙 1) 무효 이벤트는
		// 큐에 들어가지 못한다 — Invalid는 공개 API로 도달할 수 없는 방어선이다
		// (artifacts/discrepancy-notes-tests.md 참조).
		h := grfNewHarness(t, grfNewState(t, p), grfDefaultOpts())
		h.MustOffer(t,
			grfResync(t, p, 1, []model.AssetRef{grfNIC0(t)}, nil),
			grfAssetUpsert(t, p, 2, grfNIC1(t)),
			grfObsAppend(t, p, 3, evdFloat(t, evdAt(0), 1)))
		for _, ev := range []graph.Event{nil, graph.AssetUpsert{}, grfForeignEvent{partition: p, seq: 4}} {
			if err := h.reducer.Offer(ev); !isInvalid(err) {
				t.Errorf("무효 Offer의 오류 = %v, want model.ErrInvalid 계열", err)
			}
		}

		h.Start(t)
		h.WaitProcessed(t, 3)
		if err := h.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		if got := h.reducer.Stats().Invalid; got != 0 {
			t.Errorf("Invalid = %d, want 0 — Offer 선검증이 무효 이벤트를 막는다", got)
		}
	})
}

// GRF-064 (tick 발행 — 주입 시계): snapshot 발행은 주입된 Clock의 tick으로만
// 일어난다.
func TestGRF064_SnapshotIsPublishedOnInjectedTicks(t *testing.T) {
	p := grfPartitionA(t)
	initial := grfNewState(t, p)

	opts := grfDefaultOpts()
	opts.snapshotEvery = 7 * time.Millisecond
	h := grfNewHarness(t, initial, opts)

	initialRender := grfSnapshotString(t, h.reducer.Snapshot(), grfNow)

	h.MustOffer(t,
		grfResync(t, p, 1, []model.AssetRef{grfNIC0(t)}, nil),
		grfAssetUpsert(t, p, 2, grfNIC1(t)))

	h.Start(t)
	h.WaitProcessed(t, 2)

	// Run은 시작 시 Clock.Ticker(SnapshotEvery)를 정확히 한 번 요청한다.
	if got := h.clock.Requests(); len(got) != 1 || got[0] != opts.snapshotEvery {
		t.Errorf("Clock.Ticker 요청 = %v, want [%s] 한 번", got, opts.snapshotEvery)
	}

	// tick 없이 이벤트만 적용된 동안 Snapshot()은 직전 발행본을 유지한다.
	assertSnapshotUnchanged(t, initialRender, grfSnapshotString(t, h.reducer.Snapshot(), grfNow),
		"tick 전의 Snapshot()")

	// tick이 오면 그 시점 State의 snapshot이 발행된다.
	h.TickAndWait(t, "첫 tick 뒤의 발행 대기", func(snap *graph.Snapshot) bool {
		return snap != nil && len(snap.Assets()) == 2
	})
	afterFirstTick := h.reducer.Snapshot()
	assertSnapshotAssets(t, afterFirstTick, []model.AssetRef{grfNIC0(t), grfNIC1(t)}, "첫 tick 뒤")
	assertSequence(t, afterFirstTick, 2, true, true, "첫 tick 뒤")

	// 두 번째 tick도 같은 규칙으로 동작한다.
	h.MustOffer(t, grfAssetUpsert(t, p, 3, grfSwitchPort1(t)))
	h.WaitProcessed(t, 3)
	assertSnapshotUnchanged(t,
		grfSnapshotString(t, afterFirstTick, grfNow),
		grfSnapshotString(t, h.reducer.Snapshot(), grfNow),
		"세 번째 이벤트 적용 뒤(두 번째 tick 전)의 Snapshot()")

	h.TickAndWait(t, "두 번째 tick 뒤의 발행 대기", func(snap *graph.Snapshot) bool {
		return snap != nil && len(snap.Assets()) == 3
	})

	ticker := h.WaitTicker(t)
	if got := ticker.Stops(); got != 0 {
		t.Errorf("Run 반환 전에 Ticker.Stop()이 %d회 호출되었다, want 0", got)
	}

	if err := h.Stop(t); err == nil {
		t.Errorf("취소된 Run이 nil을 반환했다")
	}

	// 반환 전에 Ticker.Stop()을 호출한다.
	if got := ticker.Stops(); got < 1 {
		t.Errorf("Run 반환 뒤 Ticker.Stop() 호출 %d회, want 1회 이상", got)
	}
	if got := h.clock.TickerCount(); got != 1 {
		t.Errorf("Clock.Ticker 호출 %d회, want 정확히 1회", got)
	}
}

// GRF-065 (Emit — edge): transition이 있는 Apply마다 batch 하나가 적용 순서대로
// 전달되고, 빈 batch 호출은 없다.
func TestGRF065_EmitDeliversOneBatchPerApplyWithTransitions(t *testing.T) {
	p := grfPartitionA(t)
	a := grfNIC0(t)
	b := grfNIC1(t)
	c := grfSwitchPort1(t)
	aliasedA := grfNIC0Aliased(t, grfAliasA(t))

	events := func(t *testing.T) []graph.Event {
		t.Helper()
		return []graph.Event{
			grfResync(t, p, 1, []model.AssetRef{a, b}, nil), // AssetAdded a, AssetAdded b
			grfAssetUpsert(t, p, 2, c),                      // AssetAdded c
			grfAssetUpsert(t, p, 3, aliasedA),               // 교체 — transition 없음
			grfAssetRemove(t, p, 4, grfUnknownAsset(t)),     // no-op — transition 없음
			grfAssetRemove(t, p, 5, a),                      // AssetRemoved a
		}
	}

	wantBatches := [][]string{
		grfAssetTransitionKeys(graph.TransitionAssetAdded, []model.AssetRef{a, b}),
		grfAssetTransitionKeys(graph.TransitionAssetAdded, []model.AssetRef{c}),
		grfAssetTransitionKeys(graph.TransitionAssetRemoved, []model.AssetRef{a}),
	}

	t.Run("비-nil Emit", func(t *testing.T) {
		h := grfNewHarness(t, grfNewState(t, p), grfDefaultOpts())
		h.MustOffer(t, events(t)...)
		h.Start(t)
		h.WaitProcessed(t, 5)
		if err := h.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		got := h.emit.Batches()
		if len(got) != len(wantBatches) {
			t.Fatalf("Emit batch %d개, want %d개 (got=%v)", len(got), len(wantBatches), got)
		}
		for i := range got {
			if len(got[i]) == 0 {
				t.Errorf("Emit batch[%d]가 비어 있다 — 빈 batch 호출은 없어야 한다", i)
			}
			assertStringsEqual(t, got[i], wantBatches[i], fmt.Sprintf("Emit batch[%d]", i))
		}
	})

	t.Run("nil Emit이어도 다른 동작은 같다", func(t *testing.T) {
		withEmit := grfNewHarness(t, grfNewState(t, p), grfDefaultOpts())
		withEmit.MustOffer(t, events(t)...)
		withEmit.Start(t)
		withEmit.WaitProcessed(t, 5)
		withEmit.TickAndWait(t, "Emit 있는 reducer의 발행 대기", func(snap *graph.Snapshot) bool {
			return snap != nil && snap.Synced()
		})
		if err := withEmit.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		opts := grfDefaultOpts()
		opts.nilEmit = true
		withoutEmit := grfNewHarness(t, grfNewState(t, p), opts)
		withoutEmit.MustOffer(t, events(t)...)
		withoutEmit.Start(t)
		withoutEmit.WaitProcessed(t, 5)
		withoutEmit.TickAndWait(t, "Emit 없는 reducer의 발행 대기", func(snap *graph.Snapshot) bool {
			return snap != nil && snap.Synced()
		})
		if err := withoutEmit.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		assertSnapshotUnchanged(t,
			grfSnapshotString(t, withEmit.reducer.Snapshot(), grfNow),
			grfSnapshotString(t, withoutEmit.reducer.Snapshot(), grfNow),
			"nil Emit 여부와 최종 snapshot")
		assertStats(t, withoutEmit.reducer.Stats(), withEmit.reducer.Stats(), "nil Emit 여부와 Stats")

		if got := withoutEmit.emit.Count(); got != 0 {
			t.Errorf("nil Emit 구성인데 기록기가 %d회 호출되었다", got)
		}
	})

	t.Run("관측만 흐르면 Emit은 호출되지 않는다", func(t *testing.T) {
		h := grfNewHarness(t, grfSyncedState(t, p, 10, nil, nil), grfDefaultOpts())
		h.MustOffer(t,
			grfObsAppend(t, p, 11, evdFloat(t, evdAt(0), 1)),
			grfObsAppend(t, p, 12, evdFloat(t, evdAt(time.Second), 2)),
			grfObsAppend(t, p, 13, evdFloat(t, evdAt(time.Second), 3)), // 충돌 — 거부
		)
		h.Start(t)
		h.WaitProcessed(t, 3)
		if err := h.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		if got := h.emit.Count(); got != 0 {
			t.Errorf("관측만 흐른 구동에서 Emit이 %d회 호출되었다, want 0", got)
		}
		stats := h.reducer.Stats()
		if stats.Applied != 2 || stats.ObservationRejected != 1 {
			t.Errorf("Stats = %s, want applied=2 rejected=1", grfStatsString(stats))
		}
	})
}

// GRF-066 (resync 요청 1회 — edge): desync episode당 최대 1회다.
func TestGRF066_ResyncIsRequestedOncePerDesyncEpisode(t *testing.T) {
	p := grfPartitionA(t)

	t.Run("gap 뒤 delta가 이어져도 한 번만 요청한다", func(t *testing.T) {
		h := grfNewHarness(t, grfSyncedState(t, p, 100, []model.AssetRef{grfNIC0(t)}, nil), grfDefaultOpts())
		h.MustOffer(t,
			grfAssetUpsert(t, p, 200, grfNIC1(t)), // Gap → 요청 1
			grfAssetUpsert(t, p, 201, grfNIC1(t)), // AwaitingResync
			grfAssetUpsert(t, p, 202, grfSwitchPort1(t)),
			grfAssetRemove(t, p, 203, grfNIC0(t)),
		)
		h.Start(t)
		h.WaitProcessed(t, 4)
		if err := h.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		assertStringsEqual(t, h.resync.Keys(), []string{p.String()}, "RequestResync 호출")
		if got := h.reducer.Stats().ResyncRequests; got != 1 {
			t.Errorf("ResyncRequests = %d, want 1", got)
		}
	})

	t.Run("Resync가 episode를 닫고 다음 gap이 두 번째 요청을 낸다", func(t *testing.T) {
		h := grfNewHarness(t, grfSyncedState(t, p, 100, []model.AssetRef{grfNIC0(t)}, nil), grfDefaultOpts())
		h.MustOffer(t,
			grfAssetUpsert(t, p, 200, grfNIC1(t)),                   // Gap → 요청 1
			grfAssetUpsert(t, p, 201, grfNIC1(t)),                   // AwaitingResync
			grfResync(t, p, 300, []model.AssetRef{grfNIC0(t)}, nil), // episode 종료
			grfAssetUpsert(t, p, 400, grfNIC1(t)),                   // Gap → 요청 2
			grfAssetUpsert(t, p, 401, grfNIC1(t)),                   // AwaitingResync
		)
		h.Start(t)
		h.WaitProcessed(t, 5)
		if err := h.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		assertStringsEqual(t, h.resync.Keys(), []string{p.String(), p.String()}, "RequestResync 호출")
		if got := h.reducer.Stats().ResyncRequests; got != 2 {
			t.Errorf("ResyncRequests = %d, want 2", got)
		}
	})

	t.Run("cold start(초기 State)도 같은 규칙으로 한 번 요청한다", func(t *testing.T) {
		h := grfNewHarness(t, grfNewState(t, p), grfDefaultOpts())
		h.MustOffer(t,
			grfAssetUpsert(t, p, 1, grfNIC0(t)), // AwaitingResync → 요청 1
			grfAssetUpsert(t, p, 2, grfNIC1(t)),
			grfEdgeUpsert(t, p, 3, grfConnected(t, grfNIC0(t), grfSwitchPort1(t))),
		)
		h.Start(t)
		h.WaitProcessed(t, 3)
		if err := h.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		assertStringsEqual(t, h.resync.Keys(), []string{p.String()}, "cold start의 RequestResync 호출")
		if got := h.reducer.Stats().ResyncRequests; got != 1 {
			t.Errorf("ResyncRequests = %d, want 1", got)
		}
	})

	t.Run("stale은 요청을 트리거하지 않는다", func(t *testing.T) {
		h := grfNewHarness(t, grfSyncedState(t, p, 100, nil, nil), grfDefaultOpts())
		h.MustOffer(t,
			grfAssetUpsert(t, p, 100, grfNIC0(t)),
			grfAssetUpsert(t, p, 50, grfNIC0(t)),
			grfAssetUpsert(t, p, 101, grfNIC0(t)), // 정상 적용
		)
		h.Start(t)
		h.WaitProcessed(t, 3)
		if err := h.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		if got := h.resync.Count(); got != 0 {
			t.Errorf("stale만 있었는데 RequestResync가 %d회 호출되었다", got)
		}
		if got := h.reducer.Stats().ResyncRequests; got != 0 {
			t.Errorf("ResyncRequests = %d, want 0", got)
		}
	})

	t.Run("nil RequestResync여도 계수는 같은 규칙으로 증가한다", func(t *testing.T) {
		opts := grfDefaultOpts()
		opts.nilResync = true
		h := grfNewHarness(t, grfSyncedState(t, p, 100, nil, nil), opts)
		h.MustOffer(t,
			grfAssetUpsert(t, p, 200, grfNIC1(t)),
			grfAssetUpsert(t, p, 201, grfNIC1(t)),
			grfResync(t, p, 300, nil, nil),
			grfAssetUpsert(t, p, 400, grfNIC1(t)),
		)
		h.Start(t)
		h.WaitProcessed(t, 4)
		if err := h.Stop(t); err == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		if got := h.reducer.Stats().ResyncRequests; got != 2 {
			t.Errorf("nil RequestResync의 ResyncRequests = %d, want 2", got)
		}
		if got := h.resync.Count(); got != 0 {
			t.Errorf("nil RequestResync 구성인데 기록기가 %d회 호출되었다", got)
		}
	})
}

// GRF-067 (Stats 정확·단조): 알려진 이벤트 열의 계수가 3.7의 정의와 정확히
// 일치한다.
func TestGRF067_StatsAreExactAndMonotone(t *testing.T) {
	p := grfPartitionA(t)
	a := grfNIC0(t)
	b := grfNIC1(t)
	c := grfSwitchPort1(t)
	d := grfSwitchPort2(t)

	h := grfNewHarness(t, grfNewState(t, p), grfDefaultOpts())
	h.MustOffer(t,
		grfAssetUpsert(t, p, 5, a),                       // AwaitingResync (baseline 없음) → 요청 1
		grfResync(t, p, 10, []model.AssetRef{a, b}, nil), // Resynced
		grfAssetUpsert(t, p, 11, c),                      // Applied
		grfAssetUpsert(t, p, 11, d),                      // Stale
		grfAssetUpsert(t, p, 20, d),                      // Gap → 요청 2
		grfAssetUpsert(t, p, 21, d),                      // AwaitingResync
		grfObsAppend(t, p, 30, evdFloat(t, evdAt(0), 1)), // Applied (desync 중 관측)
		grfObsAppend(t, p, 31, evdFloat(t, evdAt(0), 2)), // ObservationRejected (충돌)
	)

	h.Start(t)
	h.WaitProcessed(t, 8)
	if err := h.Stop(t); err == nil {
		t.Errorf("취소된 Run이 nil을 반환했다")
	}

	want := graph.Stats{
		Enqueued:            8,
		Evicted:             0,
		Applied:             2,
		Resynced:            1,
		Stale:               1,
		Gaps:                1,
		AwaitingResync:      2,
		ObservationRejected: 1,
		Invalid:             0,
		ResyncRequests:      2,
	}
	got := h.reducer.Stats()
	assertStats(t, got, want, "알려진 이벤트 열의 Stats")

	// Outcome 계수의 합은 Apply가 오류 없이 처리한 이벤트 수와 같다.
	if grfOutcomeTotal(got) != 8 {
		t.Errorf("Outcome 계수의 합 = %d, want 8: %s", grfOutcomeTotal(got), grfStatsString(got))
	}

	// Stats()는 복사본이다 — 반환값을 바꿔도 다음 조회에 영향이 없다.
	mutated := got
	mutated.Applied = 9999
	assertStats(t, h.reducer.Stats(), want, "반환값 변형 뒤의 Stats")
}

// GRF-067 (단조): 구동 중 반복 조회에서 어떤 필드도 감소하지 않는다.
func TestGRF067_StatsNeverDecrease(t *testing.T) {
	p := grfPartitionA(t)

	opts := grfDefaultOpts()
	opts.capacity = 8
	h := grfNewHarness(t, grfNewState(t, p), opts)
	h.Start(t)

	// 픽스처는 테스트 goroutine에서 미리 만든다 — 헬퍼가 t.Fatalf를 쓰므로
	// 보조 goroutine에서 만들면 안 된다.
	const total = 120
	pending := make([]graph.Event, 0, total)
	for i := 1; i <= total; i++ {
		pending = append(pending, grfAssetUpsert(t, p, uint64(i), grfIndexedAsset(t, i)))
	}

	var offerWG sync.WaitGroup
	offerWG.Add(1)
	go func() {
		defer offerWG.Done()
		for _, ev := range pending {
			if err := h.reducer.Offer(ev); err != nil {
				t.Errorf("구동 중 Offer가 오류를 냈다: %v", err)
				return
			}
		}
	}()

	prev := h.reducer.Stats()
	for i := 0; i < 2000; i++ {
		next := h.reducer.Stats()
		assertStatsMonotone(t, prev, next, "구동 중 반복 조회")
		prev = next
	}

	offerWG.Wait()
	grfWaitFor(t, "모든 Offer 반영 대기", func() bool {
		return h.reducer.Stats().Enqueued >= total
	})
	if err := h.Stop(t); err == nil {
		t.Errorf("취소된 Run이 nil을 반환했다")
	}

	final := h.reducer.Stats()
	assertStatsMonotone(t, prev, final, "정지 후의 Stats")
	if final.Enqueued != total {
		t.Errorf("Enqueued = %d, want %d", final.Enqueued, total)
	}
	if final.Evicted+grfProcessed(final) > final.Enqueued {
		t.Errorf("Evicted(%d) + 처리(%d)가 Enqueued(%d)를 넘었다",
			final.Evicted, grfProcessed(final), final.Enqueued)
	}
}

// GRF-068 (동시성 — edge): Offer·Snapshot·Stats의 동시 호출은 안전하고,
// Snapshot()은 항상 어떤 일관된 적용 지점의 완전한 snapshot이다.
//
// 일관성 판정: 모든 Resync 이벤트는 "asset 수 == seq, edge 수 == asset 수 − 1"을
// 만족하는 자기일관 스냅샷이다. 발행된 snapshot이 이 불변식을 깨면 찢어진
// 읽기다. (스펙이 판정 기준을 주지 않으므로 픽스처가 기준을 만든다 —
// artifacts/discrepancy-notes-tests.md 참조.)
func TestGRF068_ConcurrentOfferSnapshotAndStatsAreSafe(t *testing.T) {
	p := grfPartitionA(t)

	const (
		writers  = 4
		readers  = 4
		maxCount = 12
		rounds   = 40
	)

	// 픽스처는 전부 테스트 goroutine에서 미리 만든다 — 헬퍼가 t.Fatalf를 쓰므로
	// 보조 goroutine에서 만들면 안 된다.
	chains := make([]graph.Event, maxCount+1)
	for k := 1; k <= maxCount; k++ {
		chains[k] = grfResync(t, p, uint64(k), grfChainAssets(t, k), grfChainEdges(t, k))
	}

	opts := grfDefaultOpts()
	opts.capacity = 8
	opts.snapshotEvery = time.Millisecond
	h := grfNewHarness(t, grfNewState(t, p), opts)
	h.Start(t)

	// tick을 계속 흘려 발행이 자주 일어나게 하는 goroutine.
	tickDone := make(chan struct{})
	ticker := h.WaitTicker(t)
	var tickWG sync.WaitGroup
	tickWG.Add(1)
	go func() {
		defer tickWG.Done()
		for {
			select {
			case <-tickDone:
				return
			case ticker.ch <- grfTickAt:
			}
		}
	}()

	var writeWG sync.WaitGroup
	for w := 0; w < writers; w++ {
		writeWG.Add(1)
		go func(id int) {
			defer writeWG.Done()
			for r := 0; r < rounds; r++ {
				k := (id+r)%maxCount + 1
				if err := h.reducer.Offer(chains[k]); err != nil {
					t.Errorf("동시 Offer가 오류를 냈다: %v", err)
					return
				}
			}
		}(w)
	}

	readDone := make(chan struct{})
	var readWG sync.WaitGroup
	for r := 0; r < readers; r++ {
		readWG.Add(1)
		go func() {
			defer readWG.Done()
			prev := h.reducer.Stats()
			for {
				select {
				case <-readDone:
					return
				default:
				}
				snap := h.reducer.Snapshot()
				if snap == nil {
					t.Errorf("Snapshot()이 nil을 반환했다")
					return
				}
				assertConsistentChainSnapshot(t, snap)

				next := h.reducer.Stats()
				assertStatsMonotone(t, prev, next, "동시 조회 중의 Stats")
				prev = next
			}
		}()
	}

	writeWG.Wait()
	close(readDone)
	readWG.Wait()
	close(tickDone)
	tickWG.Wait()

	if err := h.Stop(t); err == nil {
		t.Errorf("취소된 Run이 nil을 반환했다")
	}

	// 최종 snapshot도 일관된 적용 지점이다.
	assertConsistentChainSnapshot(t, h.reducer.Snapshot())

	stats := h.reducer.Stats()
	if stats.Enqueued != writers*rounds {
		t.Errorf("Enqueued = %d, want %d", stats.Enqueued, writers*rounds)
	}
	if stats.Evicted+grfProcessed(stats) > stats.Enqueued {
		t.Errorf("Evicted(%d) + 처리(%d)가 Enqueued(%d)를 넘었다",
			stats.Evicted, grfProcessed(stats), stats.Enqueued)
	}
}

// GRF-069 (정지 후 — edge): Run 반환 뒤에도 Snapshot()·Stats()는 마지막 값을
// 유지하고 Offer는 ErrStopped이며 계수는 더 변하지 않는다.
func TestGRF069_ReducerIsFrozenAfterRunReturns(t *testing.T) {
	p := grfPartitionA(t)

	h := grfNewHarness(t, grfNewState(t, p), grfDefaultOpts())
	h.MustOffer(t,
		grfResync(t, p, 1, []model.AssetRef{grfNIC0(t)}, nil),
		grfAssetUpsert(t, p, 2, grfNIC1(t)),
		grfObsAppend(t, p, 3, evdFloat(t, evdAt(0), 1)))

	h.Start(t)
	h.WaitProcessed(t, 3)
	if err := h.Stop(t); err == nil {
		t.Errorf("취소된 Run이 nil을 반환했다")
	}

	frozenSnapshot := grfSnapshotString(t, h.reducer.Snapshot(), grfNow)
	frozenStats := h.reducer.Stats()
	emitBefore := h.emit.Count()

	// 정지 후 Offer는 ErrStopped다.
	rejected := []graph.Event{
		grfAssetUpsert(t, p, 4, grfSwitchPort1(t)),
		grfResync(t, p, 5, nil, nil),
		grfObsAppend(t, p, 6, evdFloat(t, evdAt(time.Second), 2)),
	}
	for i, ev := range rejected {
		requireErrStopped(t, h.reducer.Offer(ev), fmt.Sprintf("정지 후 Offer(#%d)", i))
	}

	// 반복 조회는 같은 값을 준다.
	for i := 0; i < 5; i++ {
		assertSnapshotUnchanged(t, frozenSnapshot, grfSnapshotString(t, h.reducer.Snapshot(), grfNow),
			"정지 후의 Snapshot()")
		assertStats(t, h.reducer.Stats(), frozenStats, "정지 후의 Stats()")
	}
	if got := h.emit.Count(); got != emitBefore {
		t.Errorf("정지 후 Emit이 %d회에서 %d회로 늘었다", emitBefore, got)
	}
}

// --- 이 파일 전용 보조 ---

// grfRunWithTimeout은 Run을 별도 goroutine에서 호출하고 상한 안에 반환하는지
// 확인한다 — 계약 위반으로 블로킹되면 테스트가 멈추지 않고 실패한다.
func grfRunWithTimeout(t *testing.T, r *graph.Reducer, ctx context.Context, what string) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	timer := time.NewTimer(grfWaitTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		t.Fatalf("%s가 %s 안에 반환하지 않았다", what, grfWaitTimeout)
		return nil
	}
}

func isContextCanceled(err error) bool { return errors.Is(err, context.Canceled) }

// grfIndexedAsset은 i로 구별되는 유효한 asset이다 (동시성·용량 테스트용).
func grfIndexedAsset(t *testing.T, i int) model.AssetRef {
	t.Helper()
	return mustAssetRef(t, model.KindNICPort, mustTypedID(t, "graph-test", fmt.Sprintf("asset-%04d", i)))
}

// grfChainAssets는 asset k개를, grfChainEdges는 그것을 잇는 k−1개의 edge를 만든다
// — Resync 하나가 항상 자기일관 스냅샷이 되게 하는 픽스처다 (GRF-068).
func grfChainAssets(t *testing.T, k int) []model.AssetRef {
	t.Helper()
	out := make([]model.AssetRef, 0, k)
	for i := 0; i < k; i++ {
		out = append(out, grfIndexedAsset(t, i))
	}
	return out
}

func grfChainEdges(t *testing.T, k int) []graph.Edge {
	t.Helper()
	if k < 2 {
		return nil
	}
	out := make([]graph.Edge, 0, k-1)
	for i := 0; i < k-1; i++ {
		out = append(out, grfConnected(t, grfIndexedAsset(t, i), grfIndexedAsset(t, i+1)))
	}
	return out
}

// assertConsistentChainSnapshot은 발행된 snapshot이 어떤 하나의 Resync 지점에
// 정확히 대응하는지 검사한다 — 찢어진 읽기가 있으면 여기서 드러난다.
func assertConsistentChainSnapshot(t *testing.T, snap *graph.Snapshot) {
	t.Helper()
	if snap == nil {
		t.Errorf("Snapshot()이 nil이다")
		return
	}

	seq, hasBaseline := snap.Sequence()
	assets := len(snap.Assets())
	edges := len(snap.Edges())

	if !hasBaseline {
		if assets != 0 || edges != 0 {
			t.Errorf("baseline이 없는 snapshot에 asset %d개·edge %d개가 있다", assets, edges)
		}
		return
	}
	if uint64(assets) != seq {
		t.Errorf("찢어진 읽기: Sequence()=%d인데 asset이 %d개다", seq, assets)
	}
	if assets > 0 && edges != assets-1 {
		t.Errorf("찢어진 읽기: asset %d개인데 edge가 %d개다 (want %d)", assets, edges, assets-1)
	}
	if !snap.Synced() {
		t.Errorf("Resync만 흐른 구동인데 Synced()가 거짓이다")
	}
}
