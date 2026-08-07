// graph_snapshot_test.go — lock-free 읽기 뷰 (specs/graph/spec.md 3.6,
// GRF-050~054).
package tests

import (
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// GRF-050 (발행 불변 — edge): 발행된 snapshot의 조회 결과는 이후의 어떤 Apply로도
// 변하지 않는다.
func TestGRF050_PublishedSnapshotIsUnaffectedByLaterApplies(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0(t)
	port := grfSwitchPort1(t)
	edge := grfConnected(t, nic, port)

	s := grfSyncedState(t, p, 10, []model.AssetRef{nic}, []graph.Edge{edge})
	s, _ = grfApply(t, s, grfObsAppend(t, p, 11, evdFloat(t, evdAt(0), 1)), graph.OutcomeApplied, "초기 관측")

	snap := s.Snapshot()
	before := grfSnapshotString(t, snap, grfNow)

	// s에서 파생된 여러 Apply가 topology와 window를 모두 바꾼다.
	derived := s
	derived, _ = grfApply(t, derived, grfAssetUpsert(t, p, 12, grfNIC1(t)), graph.OutcomeApplied, "asset 추가")
	derived, _ = grfApply(t, derived, grfEdgeRemove(t, p, 13, edge), graph.OutcomeApplied, "edge 제거")
	derived, _ = grfApply(t, derived, grfObsAppend(t, p, 14, evdFloat(t, evdAt(time.Second), 2)), graph.OutcomeApplied, "관측 추가")
	derived, _ = grfApply(t, derived, grfResync(t, p, 100, nil, nil), graph.OutcomeResynced, "전량 대체")

	assertSnapshotUnchanged(t, before, grfSnapshotString(t, snap, grfNow), "먼저 발행된 snapshot")

	// 파생 State는 실제로 달라졌다 (검사가 공허하지 않다는 확인).
	if grfSnapshotString(t, derived.Snapshot(), grfNow) == before {
		t.Errorf("파생 State의 snapshot이 원래 snapshot과 구별되지 않는다")
	}

	// 같은 조회를 반복해도 결과는 매번 같다 (GRF-006).
	assertSnapshotUnchanged(t, before, grfSnapshotString(t, snap, grfNow), "반복 조회")
}

// GRF-051 (nil — edge): nil *Snapshot의 모든 메서드는 panic 없이 빈 snapshot과
// 같은 결과를 낸다.
func TestGRF051_NilSnapshotBehavesAsEmpty(t *testing.T) {
	var snap *graph.Snapshot

	mustNotPanic(t, "nil *Snapshot의 메서드", func() {
		if got := snap.Partition(); got != model.PartitionKey("") {
			t.Errorf("nil Snapshot의 Partition() = %q, want zero 값", got.String())
		}
		seq, ok := snap.Sequence()
		if seq != 0 || ok {
			t.Errorf("nil Snapshot의 Sequence() = (%d, %t), want (0, false)", seq, ok)
		}
		if snap.Synced() {
			t.Errorf("nil Snapshot의 Synced()가 참이다")
		}
		if got := snap.Assets(); len(got) != 0 {
			t.Errorf("nil Snapshot의 Assets() 길이 %d, want 0", len(got))
		}
		if got := snap.Edges(); len(got) != 0 {
			t.Errorf("nil Snapshot의 Edges() 길이 %d, want 0", len(got))
		}

		ref := grfNIC0(t)
		got, err := snap.Asset(ref)
		requireNoErr(t, err, "nil Snapshot의 Asset(유효 ref)")
		if !grfIsZeroAsset(got) {
			t.Errorf("nil Snapshot의 Asset() = [%s], want zero", grfAssetString(got))
		}

		from, err := snap.EdgesFrom(ref)
		requireNoErr(t, err, "nil Snapshot의 EdgesFrom(유효 ref)")
		if len(from) != 0 {
			t.Errorf("nil Snapshot의 EdgesFrom() 길이 %d, want 0", len(from))
		}

		to, err := snap.EdgesTo(ref)
		requireNoErr(t, err, "nil Snapshot의 EdgesTo(유효 ref)")
		if len(to) != 0 {
			t.Errorf("nil Snapshot의 EdgesTo() 길이 %d, want 0", len(to))
		}

		// Window()는 zero Window다 — 조회 안전·조립 불가.
		assertZeroWindow(t, snap.Window(), grfNow, "nil Snapshot의 Window()")
	})

	t.Run("무효 ref에는 model.ErrInvalid 계열 오류다", func(t *testing.T) {
		for _, tc := range grfInvalidRefs(t) {
			got, err := snap.Asset(tc.ref)
			requireErrInvalid(t, err, "nil Snapshot의 Asset("+tc.name+")")
			if !grfIsZeroAsset(got) {
				t.Errorf("%s: 오류 시 zero AssetRef를 기대했다", tc.name)
			}
			if _, err := snap.EdgesFrom(tc.ref); !isInvalid(err) {
				t.Errorf("nil Snapshot의 EdgesFrom(%s)의 오류 = %v, want model.ErrInvalid 계열", tc.name, err)
			}
			if _, err := snap.EdgesTo(tc.ref); !isInvalid(err) {
				t.Errorf("nil Snapshot의 EdgesTo(%s)의 오류 = %v, want model.ErrInvalid 계열", tc.name, err)
			}
		}
	})

	t.Run("빈(미구성) snapshot과 모든 조회 결과가 같다", func(t *testing.T) {
		var zeroState graph.State
		assertSnapshotUnchanged(t,
			grfSnapshotString(t, zeroState.Snapshot(), grfNow),
			grfSnapshotString(t, snap, grfNow),
			"nil Snapshot과 zero State의 빈 snapshot")
	})
}

// GRF-052 (조회 — edge): 조회는 Key() 기준이고, 없는 Key는 오류가 아니다.
func TestGRF052_LookupsMatchByKeyAndTreatAbsenceAsNonError(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0Aliased(t, grfAliasA(t))
	port1 := grfSwitchPort1(t)
	port2 := grfSwitchPort2(t)

	out := grfConnected(t, nic, port1)
	in := grfConnected(t, port2, nic)

	s := grfSyncedState(t, p, 10, []model.AssetRef{nic, port1}, []graph.Edge{out, in})
	snap := s.Snapshot()

	t.Run("Aliases만 다른 ref로도 같은 결과를 찾는다", func(t *testing.T) {
		probe := grfNIC0Aliased(t, grfAliasB(t))
		if probe.Key() != nic.Key() {
			t.Fatalf("픽스처 오류: probe의 Key()가 다르다")
		}

		// 저장값(마지막 upsert의 Aliases)을 그대로 돌려준다.
		assertAssetLookup(t, snap, probe, &nic, "Aliases만 다른 ref의 Asset()")
		assertEdgesFrom(t, snap, probe, []graph.Edge{out}, "Aliases만 다른 ref")
		assertEdgesTo(t, snap, probe, []graph.Edge{in}, "Aliases만 다른 ref")

		// Aliases가 아예 없는 ref로도 같다.
		bare := grfNIC0(t)
		assertAssetLookup(t, snap, bare, &nic, "Aliases 없는 ref의 Asset()")
		assertEdgesFrom(t, snap, bare, []graph.Edge{out}, "Aliases 없는 ref")
		assertEdgesTo(t, snap, bare, []graph.Edge{in}, "Aliases 없는 ref")
	})

	t.Run("없는 Key의 유효 ref는 오류가 아니다", func(t *testing.T) {
		unknown := grfUnknownAsset(t)
		assertAssetLookup(t, snap, unknown, nil, "없는 Key의 Asset()")
		assertEdgesFrom(t, snap, unknown, nil, "없는 Key")
		assertEdgesTo(t, snap, unknown, nil, "없는 Key")

		// 저장 asset이지만 그 방향의 edge가 없는 경우도 빈 목록이다.
		assertEdgesTo(t, snap, port1, []graph.Edge{out}, "port1의 EdgesTo")
		assertEdgesFrom(t, snap, port1, nil, "port1의 EdgesFrom")
	})

	t.Run("무효 ref는 model.ErrInvalid 계열 오류다", func(t *testing.T) {
		for _, tc := range grfInvalidRefs(t) {
			got, err := snap.Asset(tc.ref)
			requireErrInvalid(t, err, "Asset("+tc.name+")")
			if !grfIsZeroAsset(got) {
				t.Errorf("%s: 오류 시 zero AssetRef를 기대했으나 [%s]를 받았다", tc.name, grfAssetString(got))
			}

			edges, err := snap.EdgesFrom(tc.ref)
			requireErrInvalid(t, err, "EdgesFrom("+tc.name+")")
			if len(edges) != 0 {
				t.Errorf("%s: 오류 시 EdgesFrom 길이 %d, want 0", tc.name, len(edges))
			}

			edges, err = snap.EdgesTo(tc.ref)
			requireErrInvalid(t, err, "EdgesTo("+tc.name+")")
			if len(edges) != 0 {
				t.Errorf("%s: 오류 시 EdgesTo 길이 %d, want 0", tc.name, len(edges))
			}
		}
	})
}

// GRF-053 (일관 지점): snapshot의 topology와 Window()는 정확히 같은 적용
// 지점의 값이다.
func TestGRF053_SnapshotTopologyAndWindowShareOneApplyPoint(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0(t)
	port := grfSwitchPort1(t)

	// topology delta와 ObservationAppend를 섞은 열. 각 지점에서 snapshot을 잡는다.
	type step struct {
		name       string
		ev         graph.Event
		want       graph.ApplyOutcome
		wantAssets int
		wantObs    int
	}
	steps := []step{
		{"baseline", grfResync(t, p, 100, nil, nil), graph.OutcomeResynced, 0, 0},
		{"관측 1", grfObsAppend(t, p, 101, evdFloat(t, evdAt(0), 1)), graph.OutcomeApplied, 0, 1},
		{"asset 1", grfAssetUpsert(t, p, 102, nic), graph.OutcomeApplied, 1, 1},
		{"관측 2", grfObsAppend(t, p, 103, evdFloat(t, evdAt(time.Second), 2)), graph.OutcomeApplied, 1, 2},
		{"asset 2", grfAssetUpsert(t, p, 104, port), graph.OutcomeApplied, 2, 2},
		{"관측 3", grfObsAppend(t, p, 105, evdFloat(t, evdAt(2*time.Second), 3)), graph.OutcomeApplied, 2, 3},
		{"asset 제거", grfAssetRemove(t, p, 106, nic), graph.OutcomeApplied, 1, 3},
	}

	s := grfNewState(t, p)
	snaps := make([]*graph.Snapshot, 0, len(steps)+1)
	rendered := make([]string, 0, len(steps)+1)

	capture := func(label string, wantAssets, wantObs int) {
		snap := s.Snapshot()
		if snap == nil {
			t.Fatalf("%s: Snapshot()이 nil이다", label)
		}
		if got := len(snap.Assets()); got != wantAssets {
			t.Errorf("%s: Assets() 길이 %d, want %d", label, got, wantAssets)
		}

		obsCount := 0
		if wantObs > 0 {
			series := evdDefaultSeries(t, snap.Window())
			obsCount = len(series.Observations())
		} else if len(snap.Window().Subjects()) != 0 {
			t.Errorf("%s: 관측이 없어야 하는데 Subjects()가 비어 있지 않다", label)
		}
		if obsCount != wantObs {
			t.Errorf("%s: window 관측 %d개, want %d", label, obsCount, wantObs)
		}

		snaps = append(snaps, snap)
		rendered = append(rendered, grfSnapshotString(t, snap, grfNow))
	}

	capture("초기", 0, 0)
	for _, st := range steps {
		s, _ = grfApply(t, s, st.ev, st.want, st.name)
		capture(st.name, st.wantAssets, st.wantObs)
	}

	// 열이 끝난 뒤에도 각 지점의 snapshot은 그 지점의 값을 그대로 유지한다 —
	// 이후 이벤트의 효과가 부분적으로 섞여 보이는 경우는 없다.
	for i := range snaps {
		label := "초기"
		if i > 0 {
			label = steps[i-1].name
		}
		assertSnapshotUnchanged(t, rendered[i], grfSnapshotString(t, snaps[i], grfNow),
			"지점 "+label+"의 snapshot")
	}

	// 서로 다른 지점의 snapshot은 실제로 구별된다.
	for i := 1; i < len(rendered); i++ {
		if rendered[i] == rendered[i-1] {
			t.Errorf("지점 %d와 %d의 snapshot이 구별되지 않는다", i-1, i)
		}
	}
}

// GRF-054 (Sequence·Synced 노출): 노출은 3.5 규율과 항상 합치한다.
func TestGRF054_SequenceAndSyncedExposure(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0(t)

	s := grfNewState(t, p)
	assertSequence(t, s.Snapshot(), 0, false, false, "초기 State")

	// 관측만 흘러도 baseline은 서지 않는다.
	s, _ = grfApply(t, s, grfObsAppend(t, p, 7, evdFloat(t, evdAt(0), 1)), graph.OutcomeApplied, "관측")
	assertSequence(t, s.Snapshot(), 0, false, false, "관측 뒤")

	// topology delta도 baseline을 세우지 못한다.
	s, _ = grfApply(t, s, grfAssetUpsert(t, p, 7, nic), graph.OutcomeAwaitingResync, "baseline 전 delta")
	assertSequence(t, s.Snapshot(), 0, false, false, "baseline 전 delta 뒤")

	// Resync(S)가 baseline을 세운다.
	s, _ = grfApply(t, s, grfResync(t, p, 50, []model.AssetRef{nic}, nil), graph.OutcomeResynced, "Resync(50)")
	assertSequence(t, s.Snapshot(), 50, true, true, "Resync(50) 뒤")

	// 정상 delta는 last를 전진시킨다.
	s, _ = grfApply(t, s, grfAssetUpsert(t, p, 51, grfNIC1(t)), graph.OutcomeApplied, "delta 51")
	assertSequence(t, s.Snapshot(), 51, true, true, "delta 51 뒤")

	// gap은 last를 남기고 Synced()만 떨어뜨린다.
	s, _ = grfApply(t, s, grfAssetUpsert(t, p, 60, grfSwitchPort1(t)), graph.OutcomeGap, "gap 60")
	assertSequence(t, s.Snapshot(), 51, true, false, "gap 60 뒤")

	// desync 중의 stale·awaiting은 노출을 바꾸지 않는다.
	s, _ = grfApply(t, s, grfAssetUpsert(t, p, 52, grfSwitchPort1(t)), graph.OutcomeAwaitingResync, "desync 중 delta")
	assertSequence(t, s.Snapshot(), 51, true, false, "desync 중 delta 뒤")

	// 다음 Resync가 회복시킨다.
	s, _ = grfApply(t, s, grfResync(t, p, 5, nil, nil), graph.OutcomeResynced, "회복 Resync(5)")
	assertSequence(t, s.Snapshot(), 5, true, true, "회복 Resync(5) 뒤")
}

// --- 이 파일 전용 보조 ---

type grfRefCase struct {
	name string
	ref  model.AssetRef
}

// grfInvalidRefs는 조회 인자로서 무효한 AssetRef 목록이다 (3.6 인자 규약).
func grfInvalidRefs(t *testing.T) []grfRefCase {
	t.Helper()
	return []grfRefCase{
		{"zero AssetRef", model.AssetRef{}},
		{"Kind가 무효", model.AssetRef{Kind: model.AssetKind(0), Canonical: canonicalString}},
		{"Canonical이 비어 있음", model.AssetRef{Kind: model.KindNICPort}},
		{"Canonical이 TypedID 형식이 아님", model.AssetRef{Kind: model.KindNICPort, Canonical: "형식아님"}},
		{"무효 alias 포함", model.AssetRef{
			Kind:      model.KindNICPort,
			Canonical: canonicalString,
			Aliases:   []model.TypedID{{Namespace: "ns"}},
		}},
	}
}
