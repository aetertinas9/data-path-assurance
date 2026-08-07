// graph_state_test.go — State 생성과 Apply 일반·topology 의미론
// (specs/graph/spec.md 3.5, GRF-030~036).
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

// GRF-030: 유효한 키와 config면 빈 State를 만든다 — topology 없음·baseline
// 없음·빈 window.
func TestGRF030_NewStateWithValidArguments(t *testing.T) {
	cfgs := []struct {
		name string
		cfg  evidence.Config
	}{
		{"기본 구성", evdConfig()},
		{"최소 구성", evidence.Config{MaxAge: time.Nanosecond, Horizon: time.Nanosecond, MaxSamples: 1}},
		{"Horizon < MaxAge", evidence.Config{MaxAge: time.Hour, Horizon: time.Second, MaxSamples: 4}},
	}
	partitions := []model.PartitionKey{grfPartitionA(t), grfPartitionB(t)}

	for _, p := range partitions {
		for _, tc := range cfgs {
			t.Run(p.String()+"/"+tc.name, func(t *testing.T) {
				s, err := graph.NewState(p, tc.cfg)
				requireNoErr(t, err, "NewState")

				if got := s.Partition(); got != p {
					t.Errorf("Partition() = %q, want %q", got.String(), p.String())
				}

				snap := s.Snapshot()
				if snap == nil {
					t.Fatalf("Snapshot()이 nil이다 — 항상 비-nil이어야 한다")
				}
				if got := snap.Partition(); got != p {
					t.Errorf("Snapshot().Partition() = %q, want %q", got.String(), p.String())
				}
				assertEmptyTopology(t, snap, "NewState 직후")
				assertSequence(t, snap, 0, false, false, "NewState 직후")
				assertEmptyWindow(t, snap.Window(), grfNow, "NewState 직후의 Window()")
			})
		}
	}
}

// GRF-030 (edge): 무효 키 또는 evidence.NewWindow 요건 위반 config는 zero State와
// model.ErrInvalid 계열 오류다.
func TestGRF030_NewStateRejectsInvalidArguments(t *testing.T) {
	valid := grfPartitionA(t)

	cases := []struct {
		name string
		p    model.PartitionKey
		cfg  evidence.Config
	}{
		{"빈 파티션 키", model.PartitionKey(""), evdConfig()},
		{"zero Config{}", valid, evidence.Config{}},
		{"MaxAge가 0", valid, evidence.Config{MaxAge: 0, Horizon: time.Hour, MaxSamples: 4}},
		{"MaxAge가 음수", valid, evidence.Config{MaxAge: -time.Second, Horizon: time.Hour, MaxSamples: 4}},
		{"Horizon이 0", valid, evidence.Config{MaxAge: time.Minute, Horizon: 0, MaxSamples: 4}},
		{"Horizon이 음수", valid, evidence.Config{MaxAge: time.Minute, Horizon: -time.Second, MaxSamples: 4}},
		{"MaxSamples가 0", valid, evidence.Config{MaxAge: time.Minute, Horizon: time.Hour, MaxSamples: 0}},
		{"MaxSamples가 음수", valid, evidence.Config{MaxAge: time.Minute, Horizon: time.Hour, MaxSamples: math.MinInt32}},
		{"키와 config 둘 다 위반", model.PartitionKey(""), evidence.Config{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := graph.NewState(tc.p, tc.cfg)
			requireErrInvalid(t, err, "NewState("+tc.name+")")

			// 반환값은 zero State다 — 미구성 State의 계약(GRF-031)을 그대로 따른다.
			assertZeroState(t, got, "무효 인자의 NewState 반환값")
		})
	}
}

// GRF-031 (zero State — edge): var s graph.State는 조회에 안전하고 Apply는
// model.ErrInvalid 계열 오류다.
func TestGRF031_ZeroStateIsReadableButNotApplicable(t *testing.T) {
	var s graph.State
	assertZeroState(t, s, "var s graph.State")

	p := grfPartitionA(t)
	events := []graph.Event{
		grfResync(t, p, 1, []model.AssetRef{grfNIC0(t)}, nil),
		grfAssetUpsert(t, p, 2, grfNIC0(t)),
		grfAssetRemove(t, p, 3, grfNIC0(t)),
		grfEdgeUpsert(t, p, 4, grfConnected(t, grfNIC0(t), grfSwitchPort1(t))),
		grfEdgeRemove(t, p, 5, grfConnected(t, grfNIC0(t), grfSwitchPort1(t))),
		grfObsAppend(t, p, 6, evdFloat(t, evdAt(0), 1)),
		nil,
	}
	for i, ev := range events {
		grfApplyRejected(t, s, ev, "zero State의 Apply(이벤트 #"+strconv.Itoa(i)+")")
	}

	// 값 복사·인자 전달도 안전하다.
	copyOfS := s
	assertZeroState(t, copyOfS, "zero State의 복사본")
	func(inner graph.State) { assertZeroState(t, inner, "인자로 전달된 zero State") }(s)
}

// GRF-032 (오류 우선·파티션 불일치 — edge): 파티션 불일치는 Outcome이 아니라
// 오류다. 사유가 겹쳐도 model.ErrInvalid 계열 하나만 나온다.
func TestGRF032_PartitionMismatchIsAnErrorNotAnOutcome(t *testing.T) {
	pa := grfPartitionA(t)
	pb := grfPartitionB(t)
	state := grfSyncedState(t, pa, 10, []model.AssetRef{grfNIC0(t)}, nil)

	foreign := []struct {
		name string
		ev   graph.Event
	}{
		{"Resync", grfResync(t, pb, 11, []model.AssetRef{grfNIC1(t)}, nil)},
		{"AssetUpsert", grfAssetUpsert(t, pb, 11, grfNIC1(t))},
		{"AssetRemove", grfAssetRemove(t, pb, 11, grfNIC0(t))},
		{"EdgeUpsert", grfEdgeUpsert(t, pb, 11, grfConnected(t, grfNIC0(t), grfSwitchPort1(t)))},
		{"EdgeRemove", grfEdgeRemove(t, pb, 11, grfConnected(t, grfNIC0(t), grfSwitchPort1(t)))},
		{"ObservationAppend", grfObsAppend(t, pb, 11, evdFloat(t, evdAt(0), 1))},
		// sequence상으로는 stale·gap이 될 값이어도 판정은 오류가 먼저다.
		{"stale seq의 타 파티션 이벤트", grfAssetUpsert(t, pb, 1, grfNIC1(t))},
		{"gap seq의 타 파티션 이벤트", grfAssetUpsert(t, pb, 9999, grfNIC1(t))},
	}

	t.Run("Apply", func(t *testing.T) {
		for _, tc := range foreign {
			grfApplyRejected(t, state, tc.ev, "타 파티션 "+tc.name)
		}
	})

	t.Run("Offer도 같은 이벤트를 같은 오류로 거부한다", func(t *testing.T) {
		h := grfNewHarness(t, state, grfDefaultOpts())
		before := h.reducer.Stats()
		for _, tc := range foreign {
			if err := h.reducer.Offer(tc.ev); !isInvalid(err) {
				t.Errorf("Offer(타 파티션 %s)의 오류 = %v, want model.ErrInvalid 계열", tc.name, err)
			}
		}
		assertStats(t, h.reducer.Stats(), before, "타 파티션 Offer 뒤의 Stats")
	})

	t.Run("사유가 겹쳐도 model.ErrInvalid 계열 하나다", func(t *testing.T) {
		var zero graph.State
		// 미구성 수신자 + 타 파티션 이벤트.
		grfApplyRejected(t, zero, grfAssetUpsert(t, pb, 1, grfNIC1(t)), "미구성 수신자 + 타 파티션")
		// 미구성 수신자 + nil 이벤트.
		grfApplyRejected(t, zero, nil, "미구성 수신자 + nil 이벤트")
		// 구성된 수신자 + 외부 구현 + 타 파티션.
		grfApplyRejected(t, state, grfForeignEvent{partition: pb, seq: 11}, "외부 구현 + 타 파티션")
	})
}

// GRF-033 (함수형 갱신): Apply는 새 State를 반환하고 수신자는 변하지 않는다.
func TestGRF033_ApplyIsAFunctionalUpdate(t *testing.T) {
	p := grfPartitionA(t)
	s := grfSyncedState(t, p, 10, []model.AssetRef{grfNIC0(t)}, nil)
	before := grfStateString(t, s, grfNow)

	s2, res, err := s.Apply(grfAssetUpsert(t, p, 11, grfNIC1(t)))
	requireNoErr(t, err, "Apply")
	if res.Outcome != graph.OutcomeApplied {
		t.Fatalf("Outcome = %s, want Applied", res.Outcome.String())
	}

	assertSnapshotUnchanged(t, before, grfStateString(t, s, grfNow), "수신자 State")
	if grfStateString(t, s2, grfNow) == before {
		t.Errorf("파생 State가 수신자와 구별되지 않는다 — 적용이 일어나지 않았다")
	}
	assertSnapshotAssets(t, s2.Snapshot(), []model.AssetRef{grfNIC0(t), grfNIC1(t)}, "파생 State")
	assertSnapshotAssets(t, s.Snapshot(), []model.AssetRef{grfNIC0(t)}, "수신자 State")

	// 같은 수신자에서 두 갈래로 파생해도 서로 영향을 주지 않는다.
	branchA, _ := grfApply(t, s, grfAssetUpsert(t, p, 11, grfNIC1(t)), graph.OutcomeApplied, "가지 A")
	branchB, _ := grfApply(t, s, grfAssetUpsert(t, p, 11, grfSwitchPort1(t)), graph.OutcomeApplied, "가지 B")
	assertSnapshotAssets(t, branchA.Snapshot(), []model.AssetRef{grfNIC0(t), grfNIC1(t)}, "가지 A")
	assertSnapshotAssets(t, branchB.Snapshot(), []model.AssetRef{grfNIC0(t), grfSwitchPort1(t)}, "가지 B")
	assertSnapshotUnchanged(t, before, grfStateString(t, s, grfNow), "두 갈래 파생 뒤의 수신자")

	// 오류 시에도 반환 State는 수신자와 관측상 동일하다 (3.2 공통 규약).
	grfApplyRejected(t, s, nil, "오류 시의 State 반환 위치")
}

// GRF-034 (a)(b): AssetUpsert의 신규 추가와 교체.
func TestGRF034_AssetUpsertAddsThenReplaces(t *testing.T) {
	p := grfPartitionA(t)
	s := grfSyncedState(t, p, 10, nil, nil)

	nic := grfNIC0(t)
	s, res := grfApply(t, s, grfAssetUpsert(t, p, 11, nic), graph.OutcomeApplied, "(a) 새 Key의 upsert")
	assertTransitionKeys(t, res.Transitions,
		grfAssetTransitionKeys(graph.TransitionAssetAdded, []model.AssetRef{nic}), "(a)")
	assertSnapshotAssets(t, s.Snapshot(), []model.AssetRef{nic}, "(a)")
	assertAssetLookup(t, s.Snapshot(), nic, &nic, "(a)")

	// (b) 같은 Key·다른 Aliases의 재-upsert는 저장값을 교체하고 transition이 없다.
	aliased := grfNIC0Aliased(t, grfAliasA(t), grfAliasB(t))
	s, res = grfApply(t, s, grfAssetUpsert(t, p, 12, aliased), graph.OutcomeApplied, "(b) 재-upsert")
	assertTransitionKeys(t, res.Transitions, nil, "(b)")
	assertSnapshotAssets(t, s.Snapshot(), []model.AssetRef{nic}, "(b)")
	assertAssetLookup(t, s.Snapshot(), nic, &aliased, "(b) 저장값이 교체되었다")

	// Assets()가 노출하는 값도 교체된 값이다.
	got := s.Snapshot().Assets()
	if len(got) != 1 || grfAssetString(got[0]) != grfAssetString(aliased) {
		t.Errorf("(b) Assets()[0] = [%s], want [%s]", grfAssetString(got[0]), grfAssetString(aliased))
	}

	// Aliases를 다시 비운 재-upsert도 교체다.
	s, res = grfApply(t, s, grfAssetUpsert(t, p, 13, nic), graph.OutcomeApplied, "(b) Aliases 제거 재-upsert")
	assertTransitionKeys(t, res.Transitions, nil, "(b) Aliases 제거")
	assertAssetLookup(t, s.Snapshot(), nic, &nic, "(b) Aliases가 비워졌다")
}

// GRF-034 (c)(d): AssetRemove의 cascade와 no-op.
func TestGRF034_AssetRemoveCascadesEdgesByKey(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0Aliased(t, grfAliasA(t))
	port1 := grfSwitchPort1(t)
	port2 := grfSwitchPort2(t)

	out := grfConnected(t, nic, port1) // NIC이 From
	in := grfConnected(t, port2, nic)  // NIC이 To
	unrelated := grfNewEdge(t, port1, port2, model.RelUpstreamOf, model.OriginObserved)

	s := grfSyncedState(t, p, 10,
		[]model.AssetRef{nic, port1, port2},
		[]graph.Edge{out, in, unrelated})

	// (c) 연결된 asset의 제거는 매칭 edge 전부와 asset 자신을 지운다.
	s, res := grfApply(t, s, grfAssetRemove(t, p, 11, grfNIC0(t)), graph.OutcomeApplied, "(c) 연결된 asset 제거")

	want := append(
		grfEdgeTransitionKeys(graph.TransitionEdgeRemoved, []graph.Edge{out, in}),
		grfAssetTransitionKeys(graph.TransitionAssetRemoved, []model.AssetRef{nic})...)
	assertTransitionKeys(t, res.Transitions, want, "(c)")

	// GRF-036: TransitionAssetRemoved의 Asset은 마지막 저장값(Aliases 포함)이다.
	last := res.Transitions[len(res.Transitions)-1]
	if last.Kind != graph.TransitionAssetRemoved {
		t.Fatalf("(c) 마지막 transition의 Kind = %s, want AssetRemoved", last.Kind.String())
	}
	if grfAssetString(last.Asset) != grfAssetString(nic) {
		t.Errorf("(c) AssetRemoved의 Asset\n got=[%s]\nwant=[%s] (마지막 저장값)",
			grfAssetString(last.Asset), grfAssetString(nic))
	}

	snap := s.Snapshot()
	assertSnapshotAssets(t, snap, []model.AssetRef{port1, port2}, "(c)")
	assertSnapshotEdges(t, snap, []graph.Edge{unrelated}, "(c)")
	assertAssetLookup(t, snap, grfNIC0(t), nil, "(c) 제거된 asset")

	// (d) 저장된 적 없는 asset의 제거는 no-op이다 — OutcomeApplied·transition 0.
	s, res = grfApply(t, s, grfAssetRemove(t, p, 12, grfUnknownAsset(t)), graph.OutcomeApplied, "(d) 미저장 asset 제거")
	assertTransitionKeys(t, res.Transitions, nil, "(d)")
	assertSnapshotEdges(t, s.Snapshot(), []graph.Edge{unrelated}, "(d)")
	assertSnapshotAssets(t, s.Snapshot(), []model.AssetRef{port1, port2}, "(d)")
}

// GRF-034 (d) 후단: asset이 저장되어 있지 않아도 그 Key를 참조하는 edge는
// cascade로 제거된다 (3.5 "X의 저장 여부와 무관하게 Key 매칭으로").
func TestGRF034_AssetRemoveCascadesEvenWhenAssetIsNotStored(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0(t)
	remote := grfSwitchPort1(t) // assets에 넣지 않는 원격 endpoint

	edgeOut := grfConnected(t, nic, remote)
	edgeIn := grfConnected(t, remote, nic)

	s := grfSyncedState(t, p, 10, []model.AssetRef{nic}, []graph.Edge{edgeOut, edgeIn})
	assertAssetLookup(t, s.Snapshot(), remote, nil, "원격 endpoint는 저장되어 있지 않다")

	s, res := grfApply(t, s, grfAssetRemove(t, p, 11, remote), graph.OutcomeApplied, "미저장 asset의 cascade")

	// AssetRemoved는 나지 않고 EdgeRemoved만 난다.
	assertTransitionKeys(t, res.Transitions,
		grfEdgeTransitionKeys(graph.TransitionEdgeRemoved, []graph.Edge{edgeOut, edgeIn}), "미저장 asset의 cascade")

	snap := s.Snapshot()
	assertSnapshotAssets(t, snap, []model.AssetRef{nic}, "cascade 후")
	assertSnapshotEdges(t, snap, nil, "cascade 후")
}

// GRF-034 (e): EdgeUpsert·EdgeRemove의 신규/중복/부재 대칭.
func TestGRF034_EdgeUpsertAndRemoveSymmetry(t *testing.T) {
	p := grfPartitionA(t)
	e := grfConnected(t, grfNIC0(t), grfSwitchPort1(t))
	other := grfIntended(t, grfNIC0(t), grfSwitchPort2(t))

	s := grfSyncedState(t, p, 10, nil, nil)

	// 부재 edge의 remove는 no-op이다.
	s, res := grfApply(t, s, grfEdgeRemove(t, p, 11, e), graph.OutcomeApplied, "부재 edge remove")
	assertTransitionKeys(t, res.Transitions, nil, "부재 edge remove")
	assertSnapshotEdges(t, s.Snapshot(), nil, "부재 edge remove")

	// 신규 upsert는 EdgeAdded 하나다.
	s, res = grfApply(t, s, grfEdgeUpsert(t, p, 12, e), graph.OutcomeApplied, "신규 upsert")
	assertTransitionKeys(t, res.Transitions,
		grfEdgeTransitionKeys(graph.TransitionEdgeAdded, []graph.Edge{e}), "신규 upsert")

	// 같은 정체성의 재-upsert는 교체이며 transition이 없다.
	s, res = grfApply(t, s, grfEdgeUpsert(t, p, 13, e), graph.OutcomeApplied, "중복 upsert")
	assertTransitionKeys(t, res.Transitions, nil, "중복 upsert")
	assertSnapshotEdges(t, s.Snapshot(), []graph.Edge{e}, "중복 upsert")

	// 다른 정체성은 독립으로 추가된다.
	s, res = grfApply(t, s, grfEdgeUpsert(t, p, 14, other), graph.OutcomeApplied, "다른 정체성 upsert")
	assertTransitionKeys(t, res.Transitions,
		grfEdgeTransitionKeys(graph.TransitionEdgeAdded, []graph.Edge{other}), "다른 정체성 upsert")
	assertSnapshotEdges(t, s.Snapshot(), []graph.Edge{e, other}, "두 정체성 공존")

	// 저장된 edge의 remove는 EdgeRemoved 하나다.
	s, res = grfApply(t, s, grfEdgeRemove(t, p, 15, e), graph.OutcomeApplied, "저장 edge remove")
	assertTransitionKeys(t, res.Transitions,
		grfEdgeTransitionKeys(graph.TransitionEdgeRemoved, []graph.Edge{e}), "저장 edge remove")
	assertSnapshotEdges(t, s.Snapshot(), []graph.Edge{other}, "remove 후")

	// 같은 remove의 반복은 no-op이다.
	s, res = grfApply(t, s, grfEdgeRemove(t, p, 16, e), graph.OutcomeApplied, "remove 반복")
	assertTransitionKeys(t, res.Transitions, nil, "remove 반복")
	assertSnapshotEdges(t, s.Snapshot(), []graph.Edge{other}, "remove 반복")
}

// GRF-035 (endpoint 비강제 — edge): 저장 asset이 아닌 endpoint를 가리키는 edge도
// 보존되고, edge는 asset을 암묵 생성하지 않는다.
func TestGRF035_EdgesDoNotRequireOrCreateStoredAssets(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0(t)
	remoteAnchor := grfSwitchAnchor(t) // 다른 파티션의 anchor
	remotePort := grfSwitchPort1(t)

	toRemote := grfConnected(t, nic, remotePort)
	fromRemote := grfNewEdge(t, remoteAnchor, nic, model.RelUpstreamOf, model.OriginObserved)

	s := grfSyncedState(t, p, 10, []model.AssetRef{nic}, nil)
	s, _ = grfApply(t, s, grfEdgeUpsert(t, p, 11, toRemote), graph.OutcomeApplied, "원격 endpoint로의 edge")
	s, _ = grfApply(t, s, grfEdgeUpsert(t, p, 12, fromRemote), graph.OutcomeApplied, "원격 endpoint로부터의 edge")

	snap := s.Snapshot()

	// edge는 보존된다.
	assertSnapshotEdges(t, snap, []graph.Edge{toRemote, fromRemote}, "원격 endpoint edge")
	assertEdgesFrom(t, snap, nic, []graph.Edge{toRemote}, "EdgesFrom(로컬)")
	assertEdgesTo(t, snap, nic, []graph.Edge{fromRemote}, "EdgesTo(로컬)")
	assertEdgesFrom(t, snap, remoteAnchor, []graph.Edge{fromRemote}, "EdgesFrom(원격)")
	assertEdgesTo(t, snap, remotePort, []graph.Edge{toRemote}, "EdgesTo(원격)")

	// asset은 암묵 생성되지 않는다.
	assertSnapshotAssets(t, snap, []model.AssetRef{nic}, "원격 endpoint는 Assets()에 없다")
	assertAssetLookup(t, snap, remotePort, nil, "Asset(원격 SwitchPort)")
	assertAssetLookup(t, snap, remoteAnchor, nil, "Asset(원격 anchor)")

	// Resync로 들어온 원격 endpoint edge도 같다 (GRF-022).
	viaResync := grfSyncedState(t, p, 20, []model.AssetRef{nic}, []graph.Edge{toRemote, fromRemote})
	assertSnapshotAssets(t, viaResync.Snapshot(), []model.AssetRef{nic}, "Resync 경로의 Assets()")
	assertSnapshotEdges(t, viaResync.Snapshot(), []graph.Edge{toRemote, fromRemote}, "Resync 경로의 Edges()")
}

// GRF-036 (transition populate — edge): asset 계열은 Edge가 zero, edge 계열은
// Asset이 zero이며, 같은 입력의 Transitions 순서는 항상 같다.
func TestGRF036_TransitionsArePopulatedAndOrderedDeterministically(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0Aliased(t, grfAliasA(t))
	port1 := grfSwitchPort1(t)
	port2 := grfSwitchPort2(t)

	edges := []graph.Edge{
		grfConnected(t, nic, port1),
		grfConnected(t, port2, nic),
		grfNewEdge(t, nic, port2, model.RelUpstreamOf, model.OriginObserved),
		grfNewEdge(t, nic, port1, model.RelConnectedTo, model.OriginIntended),
	}

	build := func() (graph.State, graph.ApplyResult) {
		s := grfSyncedState(t, p, 10, []model.AssetRef{nic, port1, port2}, edges)
		return grfApply(t, s, grfAssetRemove(t, p, 11, grfNIC0(t)), graph.OutcomeApplied, "cascade 제거")
	}

	_, res := build()
	assertTransitionShape(t, res.Transitions, "cascade 제거")

	// 네 edge 전부가 nic의 Key를 참조하므로 전부 제거되고, 그 뒤 AssetRemoved 하나다.
	want := append(
		grfEdgeTransitionKeys(graph.TransitionEdgeRemoved, edges),
		grfAssetTransitionKeys(graph.TransitionAssetRemoved, []model.AssetRef{nic})...)
	assertTransitionKeys(t, res.Transitions, want, "cascade 제거")

	// 같은 입력을 다시 돌려도 Transitions 열은 값 수준까지 동일하다.
	_, again := build()
	assertStringsEqual(t, grfTransitionStrings(again.Transitions), grfTransitionStrings(res.Transitions),
		"같은 입력의 Transitions 재현")
}

// GRF-036 (v1.1 명확화 — asset·edge 대칭): 추가 계열 transition의 값은 그 추가를
// 일으킨 **이벤트의 값**이고, 제거 계열의 값은 **마지막 저장값**이다.
//
// endpoint의 Aliases는 edge 정체성에 관여하지 않으므로(3.1) 저장된 edge와 제거를
// 지시한 이벤트의 edge는 정체성이 같으면서 Aliases가 다를 수 있다 — 그 지점이
// 이 계약이 갈리는 곳이다.
func TestGRF036_TransitionValuesFollowEventOnAddAndStoredOnRemove(t *testing.T) {
	p := grfPartitionA(t)
	port := grfSwitchPort1(t)

	withA := grfNIC0Aliased(t, grfAliasA(t))
	withB := grfNIC0Aliased(t, grfAliasB(t))
	bare := grfNIC0(t)

	edgeA := grfConnected(t, withA, port)
	edgeB := grfConnected(t, withB, port)
	edgeBare := grfConnected(t, bare, port)

	// 셋 다 같은 정체성이어야 이 테스트가 의미를 갖는다.
	if grfEdgeIdentity(edgeA) != grfEdgeIdentity(edgeB) || grfEdgeIdentity(edgeA) != grfEdgeIdentity(edgeBare) {
		t.Fatalf("픽스처 오류: 세 edge의 정체성이 같아야 한다")
	}

	s := grfSyncedState(t, p, 10, nil, nil)

	// --- 추가: 값은 이벤트의 값이다 ---
	s, res := grfApply(t, s, grfAssetUpsert(t, p, 11, withA), graph.OutcomeApplied, "asset 추가")
	added := grfSoleTransition(t, res, graph.TransitionAssetAdded, "asset 추가")
	if got, want := evdAliasesString(added.Asset), evdAliasesString(withA); got != want {
		t.Errorf("TransitionAssetAdded.Asset의 Aliases = %q, want %q (추가를 일으킨 이벤트의 값)", got, want)
	}

	s, res = grfApply(t, s, grfEdgeUpsert(t, p, 12, edgeA), graph.OutcomeApplied, "edge 추가")
	addedEdge := grfSoleTransition(t, res, graph.TransitionEdgeAdded, "edge 추가")
	if got, want := grfEdgeString(addedEdge.Edge), grfEdgeString(edgeA); got != want {
		t.Errorf("TransitionEdgeAdded.Edge\n got=%s\nwant=%s (추가를 일으킨 이벤트의 값)", got, want)
	}

	// --- 교체: 저장값이 B로 바뀌고 transition은 없다 ---
	s, res = grfApply(t, s, grfAssetUpsert(t, p, 13, withB), graph.OutcomeApplied, "asset 교체")
	assertTransitionKeys(t, res.Transitions, nil, "asset 교체")
	s, res = grfApply(t, s, grfEdgeUpsert(t, p, 14, edgeB), graph.OutcomeApplied, "edge 교체")
	assertTransitionKeys(t, res.Transitions, nil, "edge 교체")

	// --- 제거: 값은 마지막 저장값(B)이지 제거를 지시한 이벤트의 값이 아니다 ---
	s, res = grfApply(t, s, grfEdgeRemove(t, p, 15, edgeBare), graph.OutcomeApplied, "edge 제거")
	removedEdge := grfSoleTransition(t, res, graph.TransitionEdgeRemoved, "edge 제거")
	if got, want := grfEdgeString(removedEdge.Edge), grfEdgeString(edgeB); got != want {
		t.Errorf("TransitionEdgeRemoved.Edge\n got=%s\nwant=%s (마지막 저장값)", got, want)
	}

	s, res = grfApply(t, s, grfAssetRemove(t, p, 16, bare), graph.OutcomeApplied, "asset 제거")
	removedAsset := grfSoleTransition(t, res, graph.TransitionAssetRemoved, "asset 제거")
	if got, want := evdAliasesString(removedAsset.Asset), evdAliasesString(withB); got != want {
		t.Errorf("TransitionAssetRemoved.Asset의 Aliases = %q, want %q (마지막 저장값)", got, want)
	}
}

// --- 이 파일 전용 보조 ---

// grfSoleTransition은 Transitions가 기대한 Kind 하나뿐임을 단정하고 돌려준다.
func grfSoleTransition(t *testing.T, res graph.ApplyResult, kind graph.TransitionKind, what string) graph.Transition {
	t.Helper()
	if len(res.Transitions) != 1 {
		t.Fatalf("%s: Transitions %d개, 정확히 1개를 기대했다 (%v)",
			what, len(res.Transitions), grfTransitionKeys(res.Transitions))
	}
	if got := res.Transitions[0].Kind; got != kind {
		t.Fatalf("%s: Transitions[0].Kind = %s, want %s", what, got.String(), kind.String())
	}
	return res.Transitions[0]
}

// assertZeroState는 미구성 State의 계약(GRF-030·031)을 단정한다.
func assertZeroState(t *testing.T, s graph.State, what string) {
	t.Helper()

	if got := s.Partition(); got != model.PartitionKey("") {
		t.Errorf("%s: Partition() = %q, want zero 값", what, got.String())
	}

	snap := s.Snapshot()
	if snap == nil {
		t.Fatalf("%s: Snapshot()이 nil이다 — 항상 비-nil이어야 한다", what)
	}
	if got := snap.Partition(); got != model.PartitionKey("") {
		t.Errorf("%s: Snapshot().Partition() = %q, want zero 값", what, got.String())
	}
	assertEmptyTopology(t, snap, what+": 빈 snapshot")
	assertSequence(t, snap, 0, false, false, what+": 빈 snapshot")

	// 3.6: 빈 snapshot의 Window()는 zero Window다 — 조회 안전·조립 불가.
	assertZeroWindow(t, snap.Window(), grfNow, what+": 빈 snapshot의 Window()")
}
