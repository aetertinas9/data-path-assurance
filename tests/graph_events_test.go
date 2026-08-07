// graph_events_test.go — typed event 6종 (specs/graph/spec.md 3.4,
// GRF-020~024).
package tests

import (
	"math"
	"strconv"
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// grfMaxSeq는 sequence의 상한 2^64−1이다 — 3.1상 정당한 값이며 sentinel이 아니다.
const grfMaxSeq uint64 = math.MaxUint64

// GRF-020: 유효한 인자면 생성자는 오류 없이 이벤트를 만들고, Partition()·
// Sequence()·접근자는 전달값을 그대로 돌려준다.
func TestGRF020_ConstructorsPreservePartitionSequenceAndPayload(t *testing.T) {
	p := grfPartitionA(t)
	asset := grfNIC0(t)
	edge := grfConnected(t, grfNIC0(t), grfSwitchPort1(t))
	obs := evdFloat(t, evdAt(0), 42)

	seqs := []uint64{0, 1, 7, grfMaxSeq - 1, grfMaxSeq}
	for _, seq := range seqs {
		t.Run("seq="+strconv.FormatUint(seq, 10), func(t *testing.T) {
			resync := grfResync(t, p, seq, []model.AssetRef{asset}, []graph.Edge{edge})
			assertEventHeader(t, resync, p, seq, "Resync")
			assertStringsEqual(t, grfAssetKeys(resync.Assets()), []string{asset.Key()}, "Resync.Assets()")
			assertStringsEqual(t, grfEdgeIdentities(resync.Edges()), []string{grfEdgeIdentity(edge)}, "Resync.Edges()")

			au := grfAssetUpsert(t, p, seq, asset)
			assertEventHeader(t, au, p, seq, "AssetUpsert")
			if got := grfAssetString(au.Asset()); got != grfAssetString(asset) {
				t.Errorf("AssetUpsert.Asset()\n got=[%s]\nwant=[%s]", got, grfAssetString(asset))
			}

			ar := grfAssetRemove(t, p, seq, asset)
			assertEventHeader(t, ar, p, seq, "AssetRemove")
			if got := grfAssetString(ar.Asset()); got != grfAssetString(asset) {
				t.Errorf("AssetRemove.Asset()\n got=[%s]\nwant=[%s]", got, grfAssetString(asset))
			}

			eu := grfEdgeUpsert(t, p, seq, edge)
			assertEventHeader(t, eu, p, seq, "EdgeUpsert")
			if got := grfEdgeString(eu.Edge()); got != grfEdgeString(edge) {
				t.Errorf("EdgeUpsert.Edge()\n got=[%s]\nwant=[%s]", got, grfEdgeString(edge))
			}

			er := grfEdgeRemove(t, p, seq, edge)
			assertEventHeader(t, er, p, seq, "EdgeRemove")
			if got := grfEdgeString(er.Edge()); got != grfEdgeString(edge) {
				t.Errorf("EdgeRemove.Edge()\n got=[%s]\nwant=[%s]", got, grfEdgeString(edge))
			}

			oa := grfObsAppend(t, p, seq, obs)
			assertEventHeader(t, oa, p, seq, "ObservationAppend")
			if got := evdObsString(oa.Observation()); got != evdObsString(obs) {
				t.Errorf("ObservationAppend.Observation()\n got=%s\nwant=%s", got, evdObsString(obs))
			}
		})
	}
}

// GRF-020 (Resync 정규화): assets는 Key() 오름차순으로, edges는 edge 전순서로
// 정규화되어 노출된다 — 전달 순서와 무관하다.
func TestGRF020_ResyncAccessorsAreNormalized(t *testing.T) {
	p := grfPartitionA(t)

	assets := []model.AssetRef{
		grfSwitchPort2(t), grfNIC0(t), grfSwitchAnchor(t), grfRootPort(t), grfNIC1(t),
	}
	edges := []graph.Edge{
		grfNewEdge(t, grfSwitchPort1(t), grfNIC0(t), model.RelConnectedTo, model.OriginObserved),
		grfNewEdge(t, grfNIC0(t), grfSwitchPort1(t), model.RelUpstreamOf, model.OriginObserved),
		grfNewEdge(t, grfNIC0(t), grfSwitchPort1(t), model.RelConnectedTo, model.OriginIntended),
		grfNewEdge(t, grfNIC0(t), grfSwitchPort1(t), model.RelConnectedTo, model.OriginObserved),
		grfNewEdge(t, grfNIC0(t), grfSwitchPort2(t), model.RelConnectedTo, model.OriginObserved),
	}

	ev := grfResync(t, p, 1, assets, edges)
	assertStringsEqual(t, grfAssetKeys(ev.Assets()), grfSortedAssetKeys(assets), "Resync.Assets() 정렬")
	assertStringsEqual(t, grfEdgeIdentities(ev.Edges()), grfSortedEdgeIdentities(edges), "Resync.Edges() 정렬")

	// 전달 순서를 뒤집어도 같은 결과다 (결정론).
	reversedAssets := grfReversedAssets(assets)
	reversedEdges := grfReversedEdges(edges)
	other := grfResync(t, p, 1, reversedAssets, reversedEdges)
	assertStringsEqual(t, grfAssetKeys(other.Assets()), grfAssetKeys(ev.Assets()), "역순 전달의 Assets()")
	assertStringsEqual(t, grfEdgeIdentities(other.Edges()), grfEdgeIdentities(ev.Edges()), "역순 전달의 Edges()")
}

// GRF-021 (edge): 무효 파티션 키·payload는 zero value와 model.ErrInvalid 계열
// 오류다.
func TestGRF021_ConstructorsRejectInvalidPartitionAndPayload(t *testing.T) {
	valid := grfPartitionA(t)
	asset := grfNIC0(t)
	edge := grfConnected(t, grfNIC0(t), grfSwitchPort1(t))
	obs := evdFloat(t, evdAt(0), 1)

	invalidKeys := []model.PartitionKey{
		model.PartitionKey(""),
	}

	t.Run("무효 파티션 키", func(t *testing.T) {
		for _, key := range invalidKeys {
			if _, err := graph.NewResync(key, 1, []model.AssetRef{asset}, nil); !isInvalid(err) {
				t.Errorf("NewResync(무효 키)의 오류 = %v, want model.ErrInvalid 계열", err)
			}
			if _, err := graph.NewAssetUpsert(key, 1, asset); !isInvalid(err) {
				t.Errorf("NewAssetUpsert(무효 키)의 오류 = %v, want model.ErrInvalid 계열", err)
			}
			if _, err := graph.NewAssetRemove(key, 1, asset); !isInvalid(err) {
				t.Errorf("NewAssetRemove(무효 키)의 오류 = %v, want model.ErrInvalid 계열", err)
			}
			if _, err := graph.NewEdgeUpsert(key, 1, edge); !isInvalid(err) {
				t.Errorf("NewEdgeUpsert(무효 키)의 오류 = %v, want model.ErrInvalid 계열", err)
			}
			if _, err := graph.NewEdgeRemove(key, 1, edge); !isInvalid(err) {
				t.Errorf("NewEdgeRemove(무효 키)의 오류 = %v, want model.ErrInvalid 계열", err)
			}
			if _, err := graph.NewObservationAppend(key, 1, obs); !isInvalid(err) {
				t.Errorf("NewObservationAppend(무효 키)의 오류 = %v, want model.ErrInvalid 계열", err)
			}
		}
	})

	t.Run("무효 asset", func(t *testing.T) {
		bad := []model.AssetRef{
			{},
			{Kind: model.KindNICPort},
			{Kind: model.AssetKind(0), Canonical: canonicalString},
			{Kind: model.KindNICPort, Canonical: "형식아님"},
			{Kind: model.KindNICPort, Canonical: canonicalString, Aliases: []model.TypedID{{Namespace: "ns"}}},
		}
		for i, ref := range bad {
			got, err := graph.NewAssetUpsert(valid, uint64(i), ref)
			requireErrInvalid(t, err, "NewAssetUpsert(무효 asset)")
			assertZeroEvent(t, got, "NewAssetUpsert(무효 asset)")

			gotRemove, err := graph.NewAssetRemove(valid, uint64(i), ref)
			requireErrInvalid(t, err, "NewAssetRemove(무효 asset)")
			assertZeroEvent(t, gotRemove, "NewAssetRemove(무효 asset)")

			if _, err := graph.NewResync(valid, uint64(i), []model.AssetRef{ref}, nil); !isInvalid(err) {
				t.Errorf("NewResync(무효 asset 포함)의 오류 = %v, want model.ErrInvalid 계열", err)
			}
		}
	})

	t.Run("무효 edge", func(t *testing.T) {
		bad := []graph.Edge{
			{},
			{From: grfNIC0(t), To: grfNIC0(t), Relation: model.RelConnectedTo, Origin: model.OriginObserved},
			{From: grfNIC0(t), To: grfSwitchPort1(t), Relation: model.EdgeRelation(0), Origin: model.OriginObserved},
			{From: grfNIC0(t), To: grfSwitchPort1(t), Relation: model.RelConnectedTo, Origin: model.EdgeOrigin(0)},
			{From: model.AssetRef{}, To: grfSwitchPort1(t), Relation: model.RelConnectedTo, Origin: model.OriginObserved},
			{From: grfNIC0(t), To: grfSwitchPort1(t), Relation: model.RelIntendedToConnect, Origin: model.OriginObserved},
			{From: grfNIC0(t), To: grfSwitchPort1(t), Relation: model.RelSharesFailureDomainWith, Origin: model.OriginObserved},
		}
		for i, e := range bad {
			got, err := graph.NewEdgeUpsert(valid, uint64(i), e)
			requireErrInvalid(t, err, "NewEdgeUpsert(무효 edge)")
			assertZeroEvent(t, got, "NewEdgeUpsert(무효 edge)")

			gotRemove, err := graph.NewEdgeRemove(valid, uint64(i), e)
			requireErrInvalid(t, err, "NewEdgeRemove(무효 edge)")
			assertZeroEvent(t, gotRemove, "NewEdgeRemove(무효 edge)")

			if _, err := graph.NewResync(valid, uint64(i), nil, []graph.Edge{e}); !isInvalid(err) {
				t.Errorf("NewResync(무효 edge 포함)의 오류 = %v, want model.ErrInvalid 계열", err)
			}
		}
	})

	t.Run("무효 관측", func(t *testing.T) {
		bad := []model.Observation{
			{},
			grfWithoutID(evdFloat(t, evdAt(0), 1)),
			grfWithZeroObservedAt(evdFloat(t, evdAt(0), 1)),
			grfWithZeroValue(evdFloat(t, evdAt(0), 1)),
		}
		for i, o := range bad {
			got, err := graph.NewObservationAppend(valid, uint64(i), o)
			requireErrInvalid(t, err, "NewObservationAppend(무효 관측)")
			assertZeroEvent(t, got, "NewObservationAppend(무효 관측)")
		}
	})
}

// GRF-022 (Resync 원소 — edge): 중복 Key/정체성은 거부, nil·길이 0은 수용,
// 저장 asset 집합에 없는 endpoint를 참조하는 edge는 수용된다.
func TestGRF022_ResyncElementRules(t *testing.T) {
	p := grfPartitionA(t)

	t.Run("assets에 같은 Key가 두 번이면 거부한다", func(t *testing.T) {
		dup := []model.AssetRef{grfNIC0(t), grfSwitchPort1(t), grfNIC0(t)}
		got, err := graph.NewResync(p, 1, dup, nil)
		requireErrInvalid(t, err, "NewResync(중복 Key)")
		assertZeroEvent(t, got, "NewResync(중복 Key)")

		// Aliases가 달라도 Key()가 같으면 중복이다 (3.1 asset 동일성).
		dupAliased := []model.AssetRef{grfNIC0(t), grfNIC0Aliased(t, grfAliasA(t))}
		got, err = graph.NewResync(p, 1, dupAliased, nil)
		requireErrInvalid(t, err, "NewResync(Aliases만 다른 중복 Key)")
		assertZeroEvent(t, got, "NewResync(Aliases만 다른 중복 Key)")
	})

	t.Run("edges에 같은 정체성이 두 번이면 거부한다", func(t *testing.T) {
		e := grfConnected(t, grfNIC0(t), grfSwitchPort1(t))
		aliased := grfConnected(t, grfNIC0Aliased(t, grfAliasB(t)), grfSwitchPort1(t))

		got, err := graph.NewResync(p, 1, nil, []graph.Edge{e, e})
		requireErrInvalid(t, err, "NewResync(중복 정체성)")
		assertZeroEvent(t, got, "NewResync(중복 정체성)")

		got, err = graph.NewResync(p, 1, nil, []graph.Edge{e, aliased})
		requireErrInvalid(t, err, "NewResync(Aliases만 다른 중복 정체성)")
		assertZeroEvent(t, got, "NewResync(Aliases만 다른 중복 정체성)")
	})

	t.Run("nil·길이 0은 빈 스냅샷으로 수용된다", func(t *testing.T) {
		cases := []struct {
			name   string
			assets []model.AssetRef
			edges  []graph.Edge
		}{
			{"둘 다 nil", nil, nil},
			{"둘 다 길이 0", []model.AssetRef{}, []graph.Edge{}},
			{"assets만 nil", nil, []graph.Edge{grfConnected(t, grfNIC0(t), grfSwitchPort1(t))}},
			{"edges만 nil", []model.AssetRef{grfNIC0(t)}, nil},
		}
		for _, tc := range cases {
			ev, err := graph.NewResync(p, 1, tc.assets, tc.edges)
			requireNoErr(t, err, "NewResync("+tc.name+")")
			if got, want := len(ev.Assets()), len(tc.assets); got != want {
				t.Errorf("%s: Assets() 길이 %d, want %d", tc.name, got, want)
			}
			if got, want := len(ev.Edges()), len(tc.edges); got != want {
				t.Errorf("%s: Edges() 길이 %d, want %d", tc.name, got, want)
			}
		}
	})

	t.Run("저장 asset 집합에 없는 endpoint를 참조하는 edge를 수용한다", func(t *testing.T) {
		// assets에는 NIC만 나열하고 edge는 원격 SwitchPort를 가리킨다.
		ev, err := graph.NewResync(p, 1,
			[]model.AssetRef{grfNIC0(t)},
			[]graph.Edge{grfConnected(t, grfNIC0(t), grfSwitchPort1(t))})
		requireNoErr(t, err, "NewResync(endpoint 비강제)")
		assertStringsEqual(t, grfAssetKeys(ev.Assets()), []string{grfNIC0(t).Key()}, "Assets()")
		if len(ev.Edges()) != 1 {
			t.Fatalf("Edges() 길이 %d, want 1", len(ev.Edges()))
		}

		// 양쪽 endpoint가 모두 assets 밖이어도 수용된다.
		ev, err = graph.NewResync(p, 2, nil,
			[]graph.Edge{grfConnected(t, grfSwitchPort1(t), grfSwitchPort2(t))})
		requireNoErr(t, err, "NewResync(양쪽 endpoint가 원격)")
		if len(ev.Edges()) != 1 {
			t.Fatalf("Edges() 길이 %d, want 1", len(ev.Edges()))
		}
	})
}

// GRF-023 (edge): Validate()는 nil이지만 Float 값이 NaN·±Inf인 관측을 생성자는
// 수용한다 — 거부는 적용 시점 window의 몫이다 (GRF-047).
func TestGRF023_ObservationAppendAcceptsNonFiniteFloatValues(t *testing.T) {
	p := grfPartitionA(t)

	cases := []struct {
		name string
		v    float64
	}{
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := evdFloat(t, evdAt(0), tc.v)
			requireNoErr(t, o.Validate(), "픽스처 관측의 Validate()")

			ev, err := graph.NewObservationAppend(p, 1, o)
			requireNoErr(t, err, "NewObservationAppend("+tc.name+")")
			if ev.Partition() != p || ev.Sequence() != 1 {
				t.Errorf("헤더가 전달값과 다르다: partition=%q seq=%d", ev.Partition().String(), ev.Sequence())
			}
			got, ok := ev.Observation().Value.Float()
			if !ok {
				t.Fatalf("Observation().Value가 Float kind가 아니다")
			}
			switch tc.name {
			case "NaN":
				if !math.IsNaN(got) {
					t.Errorf("Observation().Value = %v, want NaN", got)
				}
			case "+Inf":
				if !math.IsInf(got, 1) {
					t.Errorf("Observation().Value = %v, want +Inf", got)
				}
			case "-Inf":
				if !math.IsInf(got, -1) {
					t.Errorf("Observation().Value = %v, want -Inf", got)
				}
			}
		})
	}
}

// GRF-024 (zero·외부 구현 — edge): nil Event, zero value 이벤트, 이 패키지 밖의
// Event 구현은 Apply·Offer 모두가 상태·큐를 바꾸지 않고 거부한다.
func TestGRF024_ApplyAndOfferRejectNilZeroAndForeignEvents(t *testing.T) {
	p := grfPartitionA(t)
	base := grfSyncedState(t, p, 10, []model.AssetRef{grfNIC0(t)}, nil)

	rejected := []struct {
		name string
		ev   graph.Event
	}{
		{"nil Event", nil},
		{"zero Resync{}", graph.Resync{}},
		{"zero AssetUpsert{}", graph.AssetUpsert{}},
		{"zero AssetRemove{}", graph.AssetRemove{}},
		{"zero EdgeUpsert{}", graph.EdgeUpsert{}},
		{"zero EdgeRemove{}", graph.EdgeRemove{}},
		{"zero ObservationAppend{}", graph.ObservationAppend{}},
		{"외부 구현 (유효한 파티션)", grfForeignEvent{partition: p, seq: 11}},
		{"외부 구현 (zero 파티션)", grfForeignEvent{}},
		{"외부 구현 (seq 상한)", grfForeignEvent{partition: p, seq: grfMaxSeq}},
	}

	t.Run("Apply", func(t *testing.T) {
		for _, tc := range rejected {
			grfApplyRejected(t, base, tc.ev, "Apply("+tc.name+")")
		}
	})

	t.Run("Offer", func(t *testing.T) {
		h := grfNewHarness(t, base, grfDefaultOpts())
		before := h.reducer.Stats()

		for _, tc := range rejected {
			if err := h.reducer.Offer(tc.ev); !isInvalid(err) {
				t.Errorf("Offer(%s)의 오류 = %v, want model.ErrInvalid 계열", tc.name, err)
			}
		}
		assertStats(t, h.reducer.Stats(), before, "무효 Offer 뒤의 Stats")

		// 큐도 바뀌지 않았다 — Run을 돌려도 처리된 이벤트가 없다.
		h.Start(t)
		h.MustOffer(t, grfAssetUpsert(t, p, 11, grfNIC1(t)))
		h.WaitProcessed(t, 1)
		got := h.Stop(t)
		if got == nil {
			t.Errorf("취소된 Run이 nil을 반환했다")
		}

		stats := h.reducer.Stats()
		if grfProcessed(stats) != 1 {
			t.Errorf("처리된 이벤트 %d건, want 1 (무효 Offer가 큐에 남지 않아야 한다): %s",
				grfProcessed(stats), grfStatsString(stats))
		}
		if stats.Enqueued != 1 {
			t.Errorf("Enqueued = %d, want 1 (무효 Offer는 계수를 바꾸지 않는다)", stats.Enqueued)
		}
	})
}

// 3.4 (edge): zero value 이벤트의 Partition()은 zero 키이고, 접근자 호출은
// panic하지 않는다 (GRF-002).
func TestSpec34_ZeroValueEventsExposeZeroHeaders(t *testing.T) {
	for _, ev := range grfZeroEvents() {
		if got := ev.Partition(); got != model.PartitionKey("") {
			t.Errorf("zero value 이벤트의 Partition() = %q, want zero 키", got.String())
		}
		if got := ev.Sequence(); got != 0 {
			t.Errorf("zero value 이벤트의 Sequence() = %d, want 0", got)
		}
		if got := ev.Partition().IsValid(); got {
			t.Errorf("zero value 이벤트의 Partition().IsValid()가 참이다")
		}
	}

	mustNotPanic(t, "zero value 이벤트의 접근자", func() {
		if got := len((graph.Resync{}).Assets()); got != 0 {
			t.Errorf("Resync{}.Assets() 길이 %d, want 0", got)
		}
		if got := len((graph.Resync{}).Edges()); got != 0 {
			t.Errorf("Resync{}.Edges() 길이 %d, want 0", got)
		}
		if got := (graph.AssetUpsert{}).Asset(); !grfIsZeroAsset(got) {
			t.Errorf("AssetUpsert{}.Asset() = [%s], want zero", grfAssetString(got))
		}
		if got := (graph.AssetRemove{}).Asset(); !grfIsZeroAsset(got) {
			t.Errorf("AssetRemove{}.Asset() = [%s], want zero", grfAssetString(got))
		}
		if got := (graph.EdgeUpsert{}).Edge(); !grfIsZeroEdge(got) {
			t.Errorf("EdgeUpsert{}.Edge() = [%s], want zero", grfEdgeString(got))
		}
		if got := (graph.EdgeRemove{}).Edge(); !grfIsZeroEdge(got) {
			t.Errorf("EdgeRemove{}.Edge() = [%s], want zero", grfEdgeString(got))
		}
		if got := (graph.ObservationAppend{}).Observation(); got.ID != "" || !got.ObservedAt.IsZero() {
			t.Errorf("ObservationAppend{}.Observation() = %+v, want zero", got)
		}
	})
}

// --- 이 파일 전용 보조 ---

func assertEventHeader(t *testing.T, ev graph.Event, wantPartition model.PartitionKey, wantSeq uint64, what string) {
	t.Helper()
	if got := ev.Partition(); got != wantPartition {
		t.Errorf("%s: Partition() = %q, want %q", what, got.String(), wantPartition.String())
	}
	if got := ev.Sequence(); got != wantSeq {
		t.Errorf("%s: Sequence() = %d, want %d", what, got, wantSeq)
	}
}

// assertZeroEvent는 생성자가 오류와 함께 반환한 값이 zero value임을 단정한다.
func assertZeroEvent(t *testing.T, ev graph.Event, what string) {
	t.Helper()
	if got := ev.Partition(); got != model.PartitionKey("") {
		t.Errorf("%s: 오류 시 Partition() = %q, want zero 키", what, got.String())
	}
	if got := ev.Sequence(); got != 0 {
		t.Errorf("%s: 오류 시 Sequence() = %d, want 0", what, got)
	}
}

func isInvalid(err error) bool { return grfErrClass(err) == "invalid" }

func grfReversedAssets(in []model.AssetRef) []model.AssetRef {
	out := make([]model.AssetRef, 0, len(in))
	for i := len(in) - 1; i >= 0; i-- {
		out = append(out, in[i])
	}
	return out
}

func grfReversedEdges(in []graph.Edge) []graph.Edge {
	out := make([]graph.Edge, 0, len(in))
	for i := len(in) - 1; i >= 0; i-- {
		out = append(out, in[i])
	}
	return out
}

func grfWithoutID(o model.Observation) model.Observation {
	o.ID = ""
	return o
}

func grfWithZeroObservedAt(o model.Observation) model.Observation {
	o.ObservedAt = zeroTime
	return o
}

func grfWithZeroValue(o model.Observation) model.Observation {
	o.Value = model.Value{}
	return o
}
