// graph_sequence_test.go — sequence 규율과 관측 위임 (specs/graph/spec.md 3.5,
// GRF-040~047).
package tests

import (
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// GRF-040 (baseline 우선 — edge): Resync를 받은 적 없는 State는 topology delta를
// 적용하지 않는다. 관측은 baseline과 무관하게 흐른다.
func TestGRF040_TopologyDeltasWaitForBaselineButObservationsFlow(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0(t)
	edge := grfConnected(t, grfNIC0(t), grfSwitchPort1(t))

	deltas := []struct {
		name string
		ev   graph.Event
	}{
		{"AssetUpsert seq 0", grfAssetUpsert(t, p, 0, nic)},
		{"AssetUpsert seq 1", grfAssetUpsert(t, p, 1, nic)},
		{"AssetUpsert seq 상한", grfAssetUpsert(t, p, grfMaxSeq, nic)},
		{"AssetRemove seq 1", grfAssetRemove(t, p, 1, nic)},
		{"EdgeUpsert seq 1", grfEdgeUpsert(t, p, 1, edge)},
		{"EdgeRemove seq 1", grfEdgeRemove(t, p, 1, edge)},
	}
	for _, tc := range deltas {
		t.Run(tc.name, func(t *testing.T) {
			s := grfNewState(t, p)
			before := grfStateString(t, s, grfNow)

			next, res := grfApply(t, s, tc.ev, graph.OutcomeAwaitingResync, tc.name)
			assertTransitionKeys(t, res.Transitions, nil, tc.name)
			assertSnapshotUnchanged(t, before, grfStateString(t, next, grfNow), tc.name+": snapshot 불변")
			assertSequence(t, next.Snapshot(), 0, false, false, tc.name)
		})
	}

	t.Run("ObservationAppend는 baseline 없이도 window에 적용된다", func(t *testing.T) {
		s := grfNewState(t, p)
		obs := evdFloat(t, evdAt(0), 7)

		// sequence 부기가 없으므로 seq 값과 무관하다.
		s, res := grfApply(t, s, grfObsAppend(t, p, grfMaxSeq, obs), graph.OutcomeApplied, "baseline 없는 관측")
		assertTransitionKeys(t, res.Transitions, nil, "baseline 없는 관측")

		snap := s.Snapshot()
		assertSequence(t, snap, 0, false, false, "관측은 sequence를 전진시키지 않는다")
		assertEmptyTopology(t, snap, "관측은 topology를 바꾸지 않는다")

		series := evdDefaultSeries(t, snap.Window())
		assertObservedAts(t, series, []time.Time{evdAt(0)}, "window에 반영된 관측")

		// 두 번째 관측도 계속 흐른다.
		s, _ = grfApply(t, s, grfObsAppend(t, p, 0, evdFloat(t, evdAt(time.Second), 8)), graph.OutcomeApplied, "두 번째 관측")
		assertObservedAts(t, evdDefaultSeries(t, s.Snapshot().Window()),
			[]time.Time{evdAt(0), evdAt(time.Second)}, "두 관측이 모두 보존된다")
	})
}

// GRF-041 (Resync 적용): topology를 전량 대체하고 baseline을 확립한다.
func TestGRF041_ResyncReplacesTopologyAndEstablishesBaseline(t *testing.T) {
	p := grfPartitionA(t)
	a := grfNIC0(t)
	b := grfNIC1(t)
	c := grfSwitchPort1(t)
	d := grfSwitchPort2(t)

	e1 := grfConnected(t, a, b)
	e2 := grfConnected(t, b, c)
	e3 := grfConnected(t, c, d)

	t.Run("빈 State의 Resync는 추가만 낸다", func(t *testing.T) {
		s := grfNewState(t, p)
		s, res := grfApply(t, s, grfResync(t, p, 42, []model.AssetRef{a, b, c}, []graph.Edge{e1, e2}),
			graph.OutcomeResynced, "빈 State의 Resync")

		want := append(
			grfAssetTransitionKeys(graph.TransitionAssetAdded, []model.AssetRef{a, b, c}),
			grfEdgeTransitionKeys(graph.TransitionEdgeAdded, []graph.Edge{e1, e2})...)
		assertTransitionKeys(t, res.Transitions, want, "빈 State의 Resync")

		snap := s.Snapshot()
		assertSnapshotAssets(t, snap, []model.AssetRef{a, b, c}, "Resync 후")
		assertSnapshotEdges(t, snap, []graph.Edge{e1, e2}, "Resync 후")
		assertSequence(t, snap, 42, true, true, "Resync 후")
	})

	t.Run("기존 topology 대비 diff를 스펙 순서로 낸다", func(t *testing.T) {
		s := grfSyncedState(t, p, 42, []model.AssetRef{a, b, c}, []graph.Edge{e1, e2})

		s, res := grfApply(t, s, grfResync(t, p, 43, []model.AssetRef{b, c, d}, []graph.Edge{e2, e3}),
			graph.OutcomeResynced, "두 번째 Resync")

		// EdgeRemoved → AssetRemoved → AssetAdded → EdgeAdded 순이다.
		want := grfEdgeTransitionKeys(graph.TransitionEdgeRemoved, []graph.Edge{e1})
		want = append(want, grfAssetTransitionKeys(graph.TransitionAssetRemoved, []model.AssetRef{a})...)
		want = append(want, grfAssetTransitionKeys(graph.TransitionAssetAdded, []model.AssetRef{d})...)
		want = append(want, grfEdgeTransitionKeys(graph.TransitionEdgeAdded, []graph.Edge{e3})...)
		assertTransitionKeys(t, res.Transitions, want, "두 번째 Resync")

		snap := s.Snapshot()
		assertSnapshotAssets(t, snap, []model.AssetRef{b, c, d}, "두 번째 Resync 후")
		assertSnapshotEdges(t, snap, []graph.Edge{e2, e3}, "두 번째 Resync 후")
		assertSequence(t, snap, 43, true, true, "두 번째 Resync 후")
	})

	t.Run("내용이 같으면 transition은 길이 0이고 저장값은 교체된다", func(t *testing.T) {
		aliased := grfNIC0Aliased(t, grfAliasA(t))
		s := grfSyncedState(t, p, 1, []model.AssetRef{a, b}, []graph.Edge{e1})

		s, res := grfApply(t, s, grfResync(t, p, 2, []model.AssetRef{a, b}, []graph.Edge{e1}),
			graph.OutcomeResynced, "같은 내용의 Resync")
		assertTransitionKeys(t, res.Transitions, nil, "같은 내용의 Resync")

		// Aliases만 다른 재-Resync도 transition을 내지 않고 저장값만 교체한다.
		s, res = grfApply(t, s, grfResync(t, p, 3, []model.AssetRef{aliased, b}, []graph.Edge{e1}),
			graph.OutcomeResynced, "Aliases만 다른 Resync")
		assertTransitionKeys(t, res.Transitions, nil, "Aliases만 다른 Resync")
		assertAssetLookup(t, s.Snapshot(), a, &aliased, "Aliases만 다른 Resync 후의 저장값")
	})

	t.Run("빈 Resync는 topology를 비우고 baseline을 세운다", func(t *testing.T) {
		s := grfSyncedState(t, p, 1, []model.AssetRef{a, b}, []graph.Edge{e1})

		s, res := grfApply(t, s, grfResync(t, p, 2, nil, nil), graph.OutcomeResynced, "빈 Resync")
		want := grfEdgeTransitionKeys(graph.TransitionEdgeRemoved, []graph.Edge{e1})
		want = append(want, grfAssetTransitionKeys(graph.TransitionAssetRemoved, []model.AssetRef{a, b})...)
		assertTransitionKeys(t, res.Transitions, want, "빈 Resync")

		snap := s.Snapshot()
		assertEmptyTopology(t, snap, "빈 Resync 후")
		assertSequence(t, snap, 2, true, true, "빈 Resync 후")
	})

	t.Run("Resync는 window를 건드리지 않는다", func(t *testing.T) {
		s := grfNewState(t, p)
		s, _ = grfApply(t, s, grfObsAppend(t, p, 1, evdFloat(t, evdAt(0), 1)), graph.OutcomeApplied, "관측 1")
		s, _ = grfApply(t, s, grfObsAppend(t, p, 2, evdFloat(t, evdAt(time.Second), 2)), graph.OutcomeApplied, "관측 2")

		before := evdSnapshot(t, s.Snapshot().Window(), grfNow)

		s, _ = grfApply(t, s, grfResync(t, p, 100, []model.AssetRef{a, b}, []graph.Edge{e1}),
			graph.OutcomeResynced, "관측이 있는 State의 Resync")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, s.Snapshot().Window(), grfNow),
			"Resync 전후의 window")

		// 두 번째 Resync(topology 전량 교체)도 evidence를 지우지 않는다.
		s, _ = grfApply(t, s, grfResync(t, p, 101, nil, nil), graph.OutcomeResynced, "빈 Resync")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, s.Snapshot().Window(), grfNow),
			"빈 Resync 전후의 window")
	})
}

// GRF-042 (연속 적용과 last 전진 — edge): last 전진은 payload 결과와 무관하다.
func TestGRF042_SequenceAdvancesIndependentlyOfPayloadOutcome(t *testing.T) {
	p := grfPartitionA(t)

	t.Run("정상 적용", func(t *testing.T) {
		s := grfSyncedState(t, p, 10, nil, nil)
		s, _ = grfApply(t, s, grfAssetUpsert(t, p, 11, grfNIC0(t)), graph.OutcomeApplied, "seq 11")
		assertSequence(t, s.Snapshot(), 11, true, true, "seq 11 적용 후")

		s, _ = grfApply(t, s, grfAssetUpsert(t, p, 12, grfNIC1(t)), graph.OutcomeApplied, "seq 12")
		assertSequence(t, s.Snapshot(), 12, true, true, "seq 12 적용 후")
	})

	t.Run("no-op AssetRemove도 last를 전진시킨다", func(t *testing.T) {
		s := grfSyncedState(t, p, 10, nil, nil)
		s, res := grfApply(t, s, grfAssetRemove(t, p, 11, grfUnknownAsset(t)), graph.OutcomeApplied, "no-op remove")
		assertTransitionKeys(t, res.Transitions, nil, "no-op remove")
		assertSequence(t, s.Snapshot(), 11, true, true, "no-op remove 후")
	})

	t.Run("no-op EdgeRemove도 last를 전진시킨다", func(t *testing.T) {
		s := grfSyncedState(t, p, 10, nil, nil)
		s, res := grfApply(t, s, grfEdgeRemove(t, p, 11, grfConnected(t, grfNIC0(t), grfSwitchPort1(t))),
			graph.OutcomeApplied, "no-op edge remove")
		assertTransitionKeys(t, res.Transitions, nil, "no-op edge remove")
		assertSequence(t, s.Snapshot(), 11, true, true, "no-op edge remove 후")
	})

	t.Run("window 거부(ObservationRejected)도 last를 전진시킨다", func(t *testing.T) {
		s := grfSyncedState(t, p, 10, nil, nil)
		s, _ = grfApply(t, s, grfObsAppend(t, p, 11, evdFloat(t, evdAt(0), 1)), graph.OutcomeApplied, "관측 수용")
		assertSequence(t, s.Snapshot(), 11, true, true, "관측 수용 후")

		// 같은 시리즈·같은 instant·다른 값 → evidence.ErrConflict 경로.
		s, res := grfApply(t, s, grfObsAppend(t, p, 12, evdFloat(t, evdAt(0), 2)),
			graph.OutcomeObservationRejected, "충돌 관측")
		assertTransitionKeys(t, res.Transitions, nil, "충돌 관측")
		assertSequence(t, s.Snapshot(), 12, true, true, "거부된 관측 뒤의 sequence")
	})
}

// GRF-043 (stale — edge): seq ≤ last인 delta는 조용히 폐기된다.
func TestGRF043_StaleDeltasAreDiscardedWithoutStateChange(t *testing.T) {
	p := grfPartitionA(t)
	base := grfSyncedState(t, p, 100, []model.AssetRef{grfNIC0(t)}, nil)
	before := grfStateString(t, base, grfNow)

	cases := []struct {
		name string
		ev   graph.Event
	}{
		{"중복 seq(=last) AssetUpsert", grfAssetUpsert(t, p, 100, grfNIC1(t))},
		{"역행 seq(last-5) AssetUpsert", grfAssetUpsert(t, p, 95, grfNIC1(t))},
		{"seq 0 AssetUpsert", grfAssetUpsert(t, p, 0, grfNIC1(t))},
		{"중복 seq AssetRemove", grfAssetRemove(t, p, 100, grfNIC0(t))},
		{"역행 seq EdgeUpsert", grfEdgeUpsert(t, p, 1, grfConnected(t, grfNIC0(t), grfSwitchPort1(t)))},
		{"역행 seq EdgeRemove", grfEdgeRemove(t, p, 1, grfConnected(t, grfNIC0(t), grfSwitchPort1(t)))},
		{"중복 seq ObservationAppend", grfObsAppend(t, p, 100, evdFloat(t, evdAt(0), 1))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next, res := grfApply(t, base, tc.ev, graph.OutcomeStale, tc.name)
			assertTransitionKeys(t, res.Transitions, nil, tc.name)
			assertSnapshotUnchanged(t, before, grfStateString(t, next, grfNow), tc.name+": 상태 불변")
			assertSequence(t, next.Snapshot(), 100, true, true, tc.name+": Sequence 불변")
		})
	}

	// at-least-once 재전송이 상태를 두 번 바꾸지 않는다.
	t.Run("같은 이벤트의 재전송은 두 번 적용되지 않는다", func(t *testing.T) {
		s := grfSyncedState(t, p, 10, nil, nil)
		ev := grfAssetUpsert(t, p, 11, grfNIC0(t))

		s, res := grfApply(t, s, ev, graph.OutcomeApplied, "첫 전송")
		assertTransitionKeys(t, res.Transitions,
			grfAssetTransitionKeys(graph.TransitionAssetAdded, []model.AssetRef{grfNIC0(t)}), "첫 전송")
		afterFirst := grfStateString(t, s, grfNow)

		s, res = grfApply(t, s, ev, graph.OutcomeStale, "재전송")
		assertTransitionKeys(t, res.Transitions, nil, "재전송")
		assertSnapshotUnchanged(t, afterFirst, grfStateString(t, s, grfNow), "재전송 뒤의 상태")
	})
}

// GRF-044 (gap과 desync — edge): gap을 감지한 이벤트 자신도 적용되지 않고,
// 회복은 Resync뿐이다.
func TestGRF044_GapDesyncsAndOnlyResyncRecovers(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0(t)

	gapSeqs := []uint64{102, 150, grfMaxSeq}
	for _, seq := range gapSeqs {
		t.Run("gap seq="+strconv.FormatUint(seq, 10), func(t *testing.T) {
			s := grfSyncedState(t, p, 100, []model.AssetRef{nic}, nil)
			before := grfStateString(t, s, grfNow)

			s, res := grfApply(t, s, grfAssetUpsert(t, p, seq, grfNIC1(t)), graph.OutcomeGap, "gap 이벤트")
			assertTransitionKeys(t, res.Transitions, nil, "gap 이벤트")

			snap := s.Snapshot()
			// gap을 감지한 이벤트 자신도 적용되지 않는다 — topology는 그대로다.
			assertSnapshotAssets(t, snap, []model.AssetRef{nic}, "gap 이벤트 후")
			// Sequence()는 마지막 적용 seq로 남고 Synced()만 거짓이 된다.
			assertSequence(t, snap, 100, true, false, "gap 이벤트 후")

			if grfStateString(t, s, grfNow) == before {
				t.Errorf("gap이 Synced()를 바꾸지 않았다")
			}
		})
	}

	t.Run("desync 중 topology delta는 seq와 무관하게 거부된다", func(t *testing.T) {
		s := grfSyncedState(t, p, 100, []model.AssetRef{nic}, nil)
		s, _ = grfApply(t, s, grfAssetUpsert(t, p, 102, grfNIC1(t)), graph.OutcomeGap, "gap")
		afterGap := grfStateString(t, s, grfNow)

		later := []struct {
			name string
			ev   graph.Event
		}{
			{"원래 기대값 seq 101이 늦게 도착", grfAssetUpsert(t, p, 101, grfNIC1(t))},
			{"gap을 만든 seq 102 재전송", grfAssetUpsert(t, p, 102, grfNIC1(t))},
			{"더 뒤의 seq 103", grfAssetUpsert(t, p, 103, grfNIC1(t))},
			{"역행 seq 1", grfAssetUpsert(t, p, 1, grfNIC1(t))},
			{"AssetRemove", grfAssetRemove(t, p, 101, nic)},
			{"EdgeUpsert", grfEdgeUpsert(t, p, 101, grfConnected(t, nic, grfSwitchPort1(t)))},
			{"EdgeRemove", grfEdgeRemove(t, p, 101, grfConnected(t, nic, grfSwitchPort1(t)))},
		}
		for _, tc := range later {
			next, res := grfApply(t, s, tc.ev, graph.OutcomeAwaitingResync, "desync 중 "+tc.name)
			assertTransitionKeys(t, res.Transitions, nil, "desync 중 "+tc.name)
			assertSnapshotUnchanged(t, afterGap, grfStateString(t, next, grfNow), "desync 중 "+tc.name)
		}
	})

	t.Run("desync 중에도 관측은 계속 흐른다", func(t *testing.T) {
		s := grfSyncedState(t, p, 100, []model.AssetRef{nic}, nil)
		s, _ = grfApply(t, s, grfAssetUpsert(t, p, 102, grfNIC1(t)), graph.OutcomeGap, "gap")

		// sequence 부기가 없으므로 stale이 될 법한 seq도 window에 적용된다.
		s, res := grfApply(t, s, grfObsAppend(t, p, 1, evdFloat(t, evdAt(0), 1)), graph.OutcomeApplied, "desync 중 관측")
		assertTransitionKeys(t, res.Transitions, nil, "desync 중 관측")
		assertObservedAts(t, evdDefaultSeries(t, s.Snapshot().Window()), []time.Time{evdAt(0)}, "desync 중 관측")
		assertSequence(t, s.Snapshot(), 100, true, false, "관측은 sequence·synced를 바꾸지 않는다")

		s, _ = grfApply(t, s, grfObsAppend(t, p, 2, evdFloat(t, evdAt(time.Second), 2)), graph.OutcomeApplied, "desync 중 관측 2")
		assertObservedAts(t, evdDefaultSeries(t, s.Snapshot().Window()),
			[]time.Time{evdAt(0), evdAt(time.Second)}, "desync 중 관측 2")
	})

	t.Run("Resync만이 synced를 회복시킨다", func(t *testing.T) {
		s := grfSyncedState(t, p, 100, []model.AssetRef{nic}, nil)
		s, _ = grfApply(t, s, grfAssetUpsert(t, p, 102, grfNIC1(t)), graph.OutcomeGap, "gap")

		s, _ = grfApply(t, s, grfResync(t, p, 200, []model.AssetRef{grfNIC1(t)}, nil), graph.OutcomeResynced, "회복 Resync")
		assertSequence(t, s.Snapshot(), 200, true, true, "회복 Resync 후")
		assertSnapshotAssets(t, s.Snapshot(), []model.AssetRef{grfNIC1(t)}, "회복 Resync 후")

		// 회복 뒤에는 다시 정상 적용된다.
		s, _ = grfApply(t, s, grfAssetUpsert(t, p, 201, nic), graph.OutcomeApplied, "회복 후 delta")
		assertSequence(t, s.Snapshot(), 201, true, true, "회복 후 delta")
	})
}

// GRF-045 (Resync 무조건 적용 — edge): 생산자 재채번(낮은 seq)의 Resync도
// 적용되어 baseline을 재설정한다.
func TestGRF045_ResyncAppliesRegardlessOfSequence(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0(t)

	cases := []struct {
		name string
		seq  uint64
	}{
		{"낮은 seq (생산자 재시작)", 5},
		{"seq 0", 0},
		{"같은 seq", 100},
		{"훨씬 앞선 seq", grfMaxSeq},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := grfSyncedState(t, p, 100, []model.AssetRef{nic}, nil)

			s, _ = grfApply(t, s, grfResync(t, p, tc.seq, []model.AssetRef{grfNIC1(t)}, nil),
				graph.OutcomeResynced, tc.name)
			assertSequence(t, s.Snapshot(), tc.seq, true, true, tc.name)
			assertSnapshotAssets(t, s.Snapshot(), []model.AssetRef{grfNIC1(t)}, tc.name)

			// 이후 seq+1의 delta가 정상 적용된다 (상한에서는 wraparound로 0).
			next := tc.seq + 1
			s, _ = grfApply(t, s, grfAssetUpsert(t, p, next, nic), graph.OutcomeApplied, tc.name+": 다음 delta")
			assertSequence(t, s.Snapshot(), next, true, true, tc.name+": 다음 delta")
		})
	}

	t.Run("desync 상태에서도 무조건 적용된다", func(t *testing.T) {
		s := grfSyncedState(t, p, 100, []model.AssetRef{nic}, nil)
		s, _ = grfApply(t, s, grfAssetUpsert(t, p, 500, grfNIC1(t)), graph.OutcomeGap, "gap")
		s, _ = grfApply(t, s, grfResync(t, p, 5, nil, nil), graph.OutcomeResynced, "desync 중 낮은 seq Resync")
		assertSequence(t, s.Snapshot(), 5, true, true, "desync 중 낮은 seq Resync")
	})
}

// GRF-046 (uint64 경계 — edge): last+1은 uint64 wraparound 산술이다.
func TestGRF046_SequenceWraparoundAtUint64Boundary(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0(t)

	// last = 2^64−1인 synced State.
	base := grfSyncedState(t, p, grfMaxSeq, []model.AssetRef{nic}, nil)
	assertSequence(t, base.Snapshot(), grfMaxSeq, true, true, "baseline이 2^64−1")

	t.Run("seq 0은 last+1이므로 적용된다", func(t *testing.T) {
		s, res := grfApply(t, base, grfAssetUpsert(t, p, 0, grfNIC1(t)), graph.OutcomeApplied, "wraparound seq 0")
		assertTransitionKeys(t, res.Transitions,
			grfAssetTransitionKeys(graph.TransitionAssetAdded, []model.AssetRef{grfNIC1(t)}), "wraparound seq 0")
		assertSequence(t, s.Snapshot(), 0, true, true, "wraparound seq 0 적용 후")

		// 0 다음은 1이다 — 부기가 정상으로 이어진다.
		s, _ = grfApply(t, s, grfAssetUpsert(t, p, 1, grfSwitchPort1(t)), graph.OutcomeApplied, "wraparound 뒤 seq 1")
		assertSequence(t, s.Snapshot(), 1, true, true, "wraparound 뒤 seq 1")
	})

	t.Run("seq 5는 last 이하이므로 stale이다", func(t *testing.T) {
		s, res := grfApply(t, base, grfAssetUpsert(t, p, 5, grfNIC1(t)), graph.OutcomeStale, "seq 5")
		assertTransitionKeys(t, res.Transitions, nil, "seq 5")
		assertSequence(t, s.Snapshot(), grfMaxSeq, true, true, "seq 5는 부기를 바꾸지 않는다")
	})

	t.Run("seq 2^64−1(중복)도 stale이다", func(t *testing.T) {
		s, _ := grfApply(t, base, grfAssetUpsert(t, p, grfMaxSeq, grfNIC1(t)), graph.OutcomeStale, "seq 상한 중복")
		assertSequence(t, s.Snapshot(), grfMaxSeq, true, true, "seq 상한 중복")
	})

	t.Run("last=0에서 seq 1은 적용되고 seq 0은 stale이다", func(t *testing.T) {
		zeroBase := grfSyncedState(t, p, 0, []model.AssetRef{nic}, nil)
		assertSequence(t, zeroBase.Snapshot(), 0, true, true, "baseline이 0")

		s, _ := grfApply(t, zeroBase, grfAssetUpsert(t, p, 1, grfNIC1(t)), graph.OutcomeApplied, "seq 1")
		assertSequence(t, s.Snapshot(), 1, true, true, "seq 1")

		// seq 0은 last와 같으므로 stale이며 sentinel로 해석되지 않는다.
		s, _ = grfApply(t, zeroBase, grfAssetUpsert(t, p, 0, grfNIC1(t)), graph.OutcomeStale, "seq 0")
		assertSequence(t, s.Snapshot(), 0, true, true, "seq 0")

		// 2^64−1은 gap이다 (0 + 1 != 2^64−1).
		s, _ = grfApply(t, zeroBase, grfAssetUpsert(t, p, grfMaxSeq, grfNIC1(t)), graph.OutcomeGap, "seq 상한")
		assertSequence(t, s.Snapshot(), 0, true, false, "seq 상한은 gap이다")
	})
}

// GRF-047 (관측 위임 — edge): 수용·동등 재전송은 Applied, 거부는
// ObservationRejected이며 어떤 경우에도 transition은 없다.
func TestGRF047_ObservationAppendDelegatesToWindow(t *testing.T) {
	p := grfPartitionA(t)

	t.Run("(a) 수용되면 Applied이고 window에 반영된다", func(t *testing.T) {
		s := grfSyncedState(t, p, 10, nil, nil)
		s, res := grfApply(t, s, grfObsAppend(t, p, 11, evdFloat(t, evdAt(0), 1)), graph.OutcomeApplied, "(a)")
		assertTransitionKeys(t, res.Transitions, nil, "(a)")
		assertObservedAts(t, evdDefaultSeries(t, s.Snapshot().Window()), []time.Time{evdAt(0)}, "(a)")

		s, res = grfApply(t, s, grfObsAppend(t, p, 12, evdFloat(t, evdAt(time.Second), 2)), graph.OutcomeApplied, "(a) 두 번째")
		assertTransitionKeys(t, res.Transitions, nil, "(a) 두 번째")
		assertObservedAts(t, evdDefaultSeries(t, s.Snapshot().Window()),
			[]time.Time{evdAt(0), evdAt(time.Second)}, "(a) 두 번째")
	})

	t.Run("(b) 동등한 재전송은 window no-op이고 Applied다", func(t *testing.T) {
		s := grfSyncedState(t, p, 10, nil, nil)
		first := evdFloat(t, evdAt(0), 1)
		s, _ = grfApply(t, s, grfObsAppend(t, p, 11, first), graph.OutcomeApplied, "(b) 최초 적용")
		before := evdSnapshot(t, s.Snapshot().Window(), grfNow)

		// ID·ReceivedAt·Sequence·RawDigest는 동등성 판정에 관여하지 않는다.
		retransmit := first
		retransmit.ID = "obs-retransmitted"
		retransmit.ReceivedAt = evdAt(30 * time.Second)
		retransmit.Sequence = 999
		retransmit.RawDigest = "sha256:다른값"

		s, res := grfApply(t, s, grfObsAppend(t, p, 12, retransmit), graph.OutcomeApplied, "(b) 동등 재전송")
		assertTransitionKeys(t, res.Transitions, nil, "(b)")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, s.Snapshot().Window(), grfNow), "(b) window no-op")
		assertSequence(t, s.Snapshot(), 12, true, true, "(b) sequence는 전진한다")
	})

	t.Run("(c) 거부는 오류가 아니라 ObservationRejected다", func(t *testing.T) {
		t.Run("ErrConflict 경로", func(t *testing.T) {
			s := grfSyncedState(t, p, 10, []model.AssetRef{grfNIC0(t)}, nil)
			s, _ = grfApply(t, s, grfObsAppend(t, p, 11, evdFloat(t, evdAt(0), 1)), graph.OutcomeApplied, "선행 관측")
			before := grfTopologyString(t, s.Snapshot(), grfNow)

			next, res := grfApply(t, s, grfObsAppend(t, p, 12, evdFloat(t, evdAt(0), 2)),
				graph.OutcomeObservationRejected, "충돌 관측")
			assertTransitionKeys(t, res.Transitions, nil, "충돌 관측")
			assertSnapshotUnchanged(t, before, grfTopologyString(t, next.Snapshot(), grfNow),
				"충돌 관측: window·topology 불변")
			assertSequence(t, next.Snapshot(), 12, true, true, "충돌 관측: sequence는 전진한다")
		})

		t.Run("ErrOutOfWindow 경로 (horizon)", func(t *testing.T) {
			s := grfSyncedState(t, p, 10, []model.AssetRef{grfNIC0(t)}, nil)
			s, _ = grfApply(t, s, grfObsAppend(t, p, 11, evdFloat(t, evdAt(0), 1)), graph.OutcomeApplied, "기준 관측")
			before := grfTopologyString(t, s.Snapshot(), grfNow)

			// evdConfig의 Horizon은 1시간이므로 2시간 전의 관측은 보존되지 않는다.
			next, res := grfApply(t, s, grfObsAppend(t, p, 12, evdFloat(t, evdAt(-2*time.Hour), 1)),
				graph.OutcomeObservationRejected, "horizon 밖 관측")
			assertTransitionKeys(t, res.Transitions, nil, "horizon 밖 관측")
			assertSnapshotUnchanged(t, before, grfTopologyString(t, next.Snapshot(), grfNow),
				"horizon 밖 관측: window·topology 불변")
		})

		t.Run("ErrOutOfWindow 경로 (capacity)", func(t *testing.T) {
			cfg := evidence.Config{MaxAge: 5 * time.Minute, Horizon: time.Hour, MaxSamples: 1}
			s := grfNewStateWith(t, p, cfg)
			s, _ = grfApply(t, s, grfResync(t, p, 10, nil, nil), graph.OutcomeResynced, "baseline")
			s, _ = grfApply(t, s, grfObsAppend(t, p, 11, evdFloat(t, evdAt(0), 1)), graph.OutcomeApplied, "기준 관측")
			before := grfTopologyString(t, s.Snapshot(), grfNow)

			next, res := grfApply(t, s, grfObsAppend(t, p, 12, evdFloat(t, evdAt(-time.Second), 1)),
				graph.OutcomeObservationRejected, "capacity에 밀린 관측")
			assertTransitionKeys(t, res.Transitions, nil, "capacity에 밀린 관측")
			assertSnapshotUnchanged(t, before, grfTopologyString(t, next.Snapshot(), grfNow),
				"capacity에 밀린 관측: window·topology 불변")
		})

		nonFinite := []struct {
			name string
			v    float64
		}{
			{"NaN", math.NaN()},
			{"+Inf", math.Inf(1)},
			{"-Inf", math.Inf(-1)},
		}
		for _, tc := range nonFinite {
			t.Run("model.ErrInvalid 경로 ("+tc.name+")", func(t *testing.T) {
				s := grfSyncedState(t, p, 10, []model.AssetRef{grfNIC0(t)}, nil)
				before := grfTopologyString(t, s.Snapshot(), grfNow)

				next, res := grfApply(t, s, grfObsAppend(t, p, 11, evdFloat(t, evdAt(0), tc.v)),
					graph.OutcomeObservationRejected, tc.name+" 관측")
				assertTransitionKeys(t, res.Transitions, nil, tc.name+" 관측")
				assertSnapshotUnchanged(t, before, grfTopologyString(t, next.Snapshot(), grfNow),
					tc.name+" 관측: window·topology 불변")
				assertSnapshotAssets(t, next.Snapshot(), []model.AssetRef{grfNIC0(t)}, tc.name+": topology 불변")
			})
		}
	})

	t.Run("(d) 관측은 어떤 경우에도 transition을 내지 않는다", func(t *testing.T) {
		s := grfNewState(t, p)

		// 이 서브테스트의 주장은 **관측 스텝**의 Transitions가 항상 길이 0이라는
		// 것이다. 사이에 낀 셋업 스텝(Resync)은 주장의 대상이 아니며, 자기 몫의
		// transition을 낸다 — GRF-041·3.5 diff 규칙상 asset을 실은 Resync는
		// TransitionAssetAdded를 반드시 낸다. 따라서 기대 transition을 스텝별로
		// 적고, 관측 스텝에는 "무조건 0"을 따로 한 번 더 단정한다.
		steps := []struct {
			name          string
			ev            graph.Event
			want          graph.ApplyOutcome
			wantKeys      []string
			isObservation bool
		}{
			{"baseline 없음", grfObsAppend(t, p, 5, evdFloat(t, evdAt(0), 1)), graph.OutcomeApplied, nil, true},
			{
				name:     "baseline 확립 (셋업 — 관측 스텝이 아니다)",
				ev:       grfResync(t, p, 10, []model.AssetRef{grfNIC0(t)}, nil),
				want:     graph.OutcomeResynced,
				wantKeys: grfAssetTransitionKeys(graph.TransitionAssetAdded, []model.AssetRef{grfNIC0(t)}),
			},
			{"synced 수용", grfObsAppend(t, p, 11, evdFloat(t, evdAt(time.Second), 2)), graph.OutcomeApplied, nil, true},
			{"synced 거부", grfObsAppend(t, p, 12, evdFloat(t, evdAt(time.Second), 3)), graph.OutcomeObservationRejected, nil, true},
			{"stale", grfObsAppend(t, p, 3, evdFloat(t, evdAt(2*time.Second), 4)), graph.OutcomeStale, nil, true},
			{"gap", grfObsAppend(t, p, 999, evdFloat(t, evdAt(2*time.Second), 4)), graph.OutcomeGap, nil, true},
			{"desync 중", grfObsAppend(t, p, 1, evdFloat(t, evdAt(2*time.Second), 4)), graph.OutcomeApplied, nil, true},
		}

		observationSteps := 0
		for _, step := range steps {
			var res graph.ApplyResult
			s, res = grfApply(t, s, step.ev, step.want, "(d) "+step.name)
			assertTransitionKeys(t, res.Transitions, step.wantKeys, "(d) "+step.name)

			if step.isObservation {
				observationSteps++
				if len(res.Transitions) != 0 {
					t.Errorf("(d) %s: ObservationAppend가 transition %d개를 냈다 (%v) — 관측은 어떤 Outcome에서도 transition을 내지 않는다",
						step.name, len(res.Transitions), grfTransitionKeys(res.Transitions))
				}
			}
		}

		// Outcome 6종(Applied·Resynced를 뺀 관측 경로 전부)을 실제로 지났는지
		// 확인한다 — 스텝 표가 줄어들면 주장이 조용히 약해지기 때문이다.
		if observationSteps != 6 {
			t.Errorf("관측 스텝 %d개, want 6 (baseline 없음·수용·거부·stale·gap·desync)", observationSteps)
		}
	})
}
