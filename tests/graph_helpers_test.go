// graph_helpers_test.go — internal/graph 블랙박스 테스트의 공용 헬퍼·픽스처.
// specs/graph/spec.md (ratified v1.0) 3절의 공개 계약만 참조한다.
//
// GRF-001(표준 라이브러리와 pkg/model·internal/evidence 외 import 금지)과
// GRF-002의 **부재 조항**(현재 시각·난수·환경 변수·I/O 비접근, Reducer의 시간
// 경로가 주입된 Clock뿐이라는 것)은 블랙박스 테스트의 사정 범위 밖이다 —
// 스펙 5절이 `make arch-check`와 verifier의 소스 검사(`go list -deps`,
// `time.Now`·`rand`·`os.Getenv` 부재 확인)에 배정한다. EVD-001·IDN-001·MDL-001과
// 같은 취급이다. GRF-002의 **무-panic 조항**은 이 스위트 소관이다
// (graph_contract_test.go).
package tests

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// --- 시각 픽스처 ---
//
// GRF-002(시계 비의존)·GRF-004(결정론)를 위해 모든 시각은 고정 리터럴에서
// 파생한다. 기준은 evidence 스위트와 공유하는 evdT0다 (evdAt 참조).

// grfNow는 window 판정(freshness·Level)에 넘기는 고정 now다. evdConfig()의
// MaxAge가 5분이므로 evdAt(0)의 관측은 이 시각에 신선하다.
var grfNow = evdAt(time.Minute)

// grfTickAt은 가짜 Ticker가 흘려보내는 시각 값이다. reducer는 tick의 **도착**만
// 보고 값은 보지 않는다(3.7 Run 규칙 6) — 결정론을 위해 고정한다.
var grfTickAt = evdAt(2 * time.Minute)

// --- 비동기 대기 (Reducer 테스트 전용) ---

const (
	// grfSnapshotEvery는 ReducerConfig.SnapshotEvery의 기본값이다. 실제 경과
	// 시간은 아무 역할도 하지 않는다 — tick은 가짜 Ticker가 전달한다.
	grfSnapshotEvery = 50 * time.Millisecond

	// grfWaitTimeout은 결정적 조건이 성립하기를 기다리는 상한이다. 고정 sleep이
	// 아니라 조건 폴링의 안전망이다.
	grfWaitTimeout = 10 * time.Second

	grfPollInterval = 200 * time.Microsecond
)

// grfWaitFor는 cond가 참이 될 때까지 폴링한다. 시간 기반 sleep으로 "충분히
// 기다렸겠지"를 가정하지 않기 위한 결정적 동기화 수단이다.
func grfWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(grfWaitTimeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s 안에 조건이 성립하지 않았다", what, grfWaitTimeout)
		}
		time.Sleep(grfPollInterval)
	}
}

// --- asset 픽스처 ---
//
// Key()는 "<Kind>/<Canonical>"이다 (MDL-014). 바이트 오름차순은
//
//	EthernetSwitch/… < KubernetesNode/… < NICPort/…0 < NICPort/…1 <
//	PCIeRootPort/… < Pod/… < SwitchPort/…1 < SwitchPort/…2 < Transceiver/…
//
// 순이다 (GRF-007 정렬 계약의 기대값 계산은 이 순서를 코드로 재현하지 않고
// grfSortedAssetKeys가 계산한다).

// grfNodeAnchor는 Kind가 KindKubernetesNode인 파티션 anchor다 (3.1).
func grfNodeAnchor(t *testing.T) model.AssetRef {
	t.Helper()
	return mustAssetRef(t, model.KindKubernetesNode,
		mustTypedID(t, string(model.NamespaceKubernetesNodeUID), "node-a"))
}

// grfSwitchAnchor는 Kind가 KindEthernetSwitch인 파티션 anchor다 (3.1).
func grfSwitchAnchor(t *testing.T) model.AssetRef {
	t.Helper()
	return mustAssetRef(t, model.KindEthernetSwitch,
		mustTypedID(t, string(model.NamespaceLLDPChassisID), "tor-1"))
}

// grfNIC0은 evidence 스위트의 기본 subject와 같은 asset이다 — topology와 window가
// 같은 asset을 가리키는 픽스처가 GRF-053의 검증을 자연스럽게 만든다.
func grfNIC0(t *testing.T) model.AssetRef {
	t.Helper()
	return evdSubjectA(t)
}

// grfNIC0Aliased는 grfNIC0과 Key()가 같고 Aliases만 다른 AssetRef다
// (GRF-013·034 (b)·052).
func grfNIC0Aliased(t *testing.T, aliases ...model.TypedID) model.AssetRef {
	t.Helper()
	return evdSubjectAWithAliases(t, aliases...)
}

func grfNIC1(t *testing.T) model.AssetRef {
	t.Helper()
	return mustAssetRef(t, model.KindNICPort, mustTypedID(t, string(model.NamespacePCIBDF), "0000:af:00.1"))
}

func grfRootPort(t *testing.T) model.AssetRef {
	t.Helper()
	return mustAssetRef(t, model.KindPCIeRootPort, mustTypedID(t, string(model.NamespacePCIBDF), "0000:00:01.0"))
}

// grfSwitchPort1은 evidence 스위트의 evdSubjectB와 같은 asset이다.
func grfSwitchPort1(t *testing.T) model.AssetRef {
	t.Helper()
	return evdSubjectB(t)
}

func grfSwitchPort2(t *testing.T) model.AssetRef {
	t.Helper()
	return mustAssetRef(t, model.KindSwitchPort, mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/2"))
}

// grfUnknownAsset은 어떤 픽스처도 저장하지 않는 유효한 AssetRef다 (GRF-052).
func grfUnknownAsset(t *testing.T) model.AssetRef {
	t.Helper()
	return mustAssetRef(t, model.KindTransceiver, mustTypedID(t, "graph-test", "never-stored"))
}

// grfAliasA·grfAliasB는 Key()에 관여하지 않는 alias다 (3.1 asset 동일성).
func grfAliasA(t *testing.T) model.TypedID {
	t.Helper()
	return mustTypedID(t, "graph-test", "alias-a")
}

func grfAliasB(t *testing.T) model.TypedID {
	t.Helper()
	return mustTypedID(t, "graph-test", "alias-b")
}

// grfAliasWithRaw는 Raw·Source를 가진 alias다 (GRF-005 방어적 복사 검증용).
// 손으로 조립하지만 Namespace·Value가 비어 있지 않으므로 유효하다 (MDL-005).
func grfAliasWithRaw(raw []byte) model.TypedID {
	return model.TypedID{Namespace: "graph-test", Value: "alias-raw", Raw: raw, Source: "graph-test-source"}
}

// grfProbeAssets는 snapshot 렌더링이 매번 조회하는 고정 probe 목록이다.
func grfProbeAssets(t *testing.T) []model.AssetRef {
	t.Helper()
	return []model.AssetRef{
		grfNodeAnchor(t),
		grfSwitchAnchor(t),
		grfNIC0(t),
		grfNIC1(t),
		grfRootPort(t),
		grfSwitchPort1(t),
		grfSwitchPort2(t),
		grfUnknownAsset(t),
	}
}

// --- 파티션 픽스처 (3.3 PartitionFor) ---

func grfPartitionA(t *testing.T) model.PartitionKey {
	t.Helper()
	p, err := graph.PartitionFor(grfNodeAnchor(t))
	requireNoErr(t, err, "PartitionFor(node anchor)")
	return p
}

func grfPartitionB(t *testing.T) model.PartitionKey {
	t.Helper()
	p, err := graph.PartitionFor(grfSwitchAnchor(t))
	requireNoErr(t, err, "PartitionFor(switch anchor)")
	return p
}

// --- Edge 픽스처 (3.3) ---

func grfNewEdge(t *testing.T, from, to model.AssetRef, rel model.EdgeRelation, origin model.EdgeOrigin) graph.Edge {
	t.Helper()
	e, err := graph.NewEdge(from, to, rel, origin)
	requireNoErr(t, err, "NewEdge")
	return e
}

// grfConnected는 CONNECTED_TO(Observed) edge의 축약이다 — 관계-기원 불변식(3.3
// 규칙 4)이 제한하지 않는 조합이다.
func grfConnected(t *testing.T, from, to model.AssetRef) graph.Edge {
	t.Helper()
	return grfNewEdge(t, from, to, model.RelConnectedTo, model.OriginObserved)
}

// grfIntended는 INTENDED_TO_CONNECT(Intended) edge다 — 불변식이 요구하는 유일한
// 조합이다 (GRF-012).
func grfIntended(t *testing.T, from, to model.AssetRef) graph.Edge {
	t.Helper()
	return grfNewEdge(t, from, to, model.RelIntendedToConnect, model.OriginIntended)
}

// --- 이벤트 픽스처 (3.4) ---

func grfResync(t *testing.T, p model.PartitionKey, seq uint64, assets []model.AssetRef, edges []graph.Edge) graph.Resync {
	t.Helper()
	ev, err := graph.NewResync(p, seq, assets, edges)
	requireNoErr(t, err, "NewResync")
	return ev
}

func grfAssetUpsert(t *testing.T, p model.PartitionKey, seq uint64, a model.AssetRef) graph.AssetUpsert {
	t.Helper()
	ev, err := graph.NewAssetUpsert(p, seq, a)
	requireNoErr(t, err, "NewAssetUpsert")
	return ev
}

func grfAssetRemove(t *testing.T, p model.PartitionKey, seq uint64, a model.AssetRef) graph.AssetRemove {
	t.Helper()
	ev, err := graph.NewAssetRemove(p, seq, a)
	requireNoErr(t, err, "NewAssetRemove")
	return ev
}

func grfEdgeUpsert(t *testing.T, p model.PartitionKey, seq uint64, e graph.Edge) graph.EdgeUpsert {
	t.Helper()
	ev, err := graph.NewEdgeUpsert(p, seq, e)
	requireNoErr(t, err, "NewEdgeUpsert")
	return ev
}

func grfEdgeRemove(t *testing.T, p model.PartitionKey, seq uint64, e graph.Edge) graph.EdgeRemove {
	t.Helper()
	ev, err := graph.NewEdgeRemove(p, seq, e)
	requireNoErr(t, err, "NewEdgeRemove")
	return ev
}

func grfObsAppend(t *testing.T, p model.PartitionKey, seq uint64, o model.Observation) graph.ObservationAppend {
	t.Helper()
	ev, err := graph.NewObservationAppend(p, seq, o)
	requireNoErr(t, err, "NewObservationAppend")
	return ev
}

// grfZeroEvents는 생성자를 거치지 않은 zero value 이벤트 6종이다 (3.4 마지막
// 항목·GRF-024). Event 인터페이스 값으로 유통된다.
func grfZeroEvents() []graph.Event {
	return []graph.Event{
		graph.Resync{},
		graph.AssetUpsert{},
		graph.AssetRemove{},
		graph.EdgeUpsert{},
		graph.EdgeRemove{},
		graph.ObservationAppend{},
	}
}

// grfForeignEvent는 **이 패키지 밖에서 정의된** Event 구현이다 (3.4: 이벤트
// 집합은 닫혀 있다 — Apply·Offer가 거부한다, GRF-024·061).
type grfForeignEvent struct {
	partition model.PartitionKey
	seq       uint64
}

func (e grfForeignEvent) Partition() model.PartitionKey { return e.partition }

func (e grfForeignEvent) Sequence() uint64 { return e.seq }

// --- State 픽스처 (3.5) ---

func grfNewState(t *testing.T, p model.PartitionKey) graph.State {
	t.Helper()
	return grfNewStateWith(t, p, evdConfig())
}

func grfNewStateWith(t *testing.T, p model.PartitionKey, cfg evidence.Config) graph.State {
	t.Helper()
	s, err := graph.NewState(p, cfg)
	requireNoErr(t, err, "NewState")
	return s
}

// grfSyncedState는 Resync 하나를 적용해 baseline이 선 State를 만든다.
func grfSyncedState(t *testing.T, p model.PartitionKey, seq uint64, assets []model.AssetRef, edges []graph.Edge) graph.State {
	t.Helper()
	s := grfNewState(t, p)
	s, _ = grfApply(t, s, grfResync(t, p, seq, assets, edges), graph.OutcomeResynced, "baseline Resync")
	return s
}

// grfApply는 오류 없는 Apply를 단정하고 Outcome과 transition 모양(GRF-036)을
// 함께 검사한다.
func grfApply(t *testing.T, s graph.State, ev graph.Event, want graph.ApplyOutcome, what string) (graph.State, graph.ApplyResult) {
	t.Helper()
	next, res, err := s.Apply(ev)
	requireNoErr(t, err, what+": Apply")
	if res.Outcome != want {
		t.Fatalf("%s: Outcome = %s, want %s", what, res.Outcome.String(), want.String())
	}
	assertTransitionShape(t, res.Transitions, what)
	return next, res
}

// grfApplyRejected는 Apply가 model.ErrInvalid 계열 오류를 내고 수신자·반환
// State가 관측상 동일함을 단정한다 (3.2 공통 규약·GRF-003·031·032).
func grfApplyRejected(t *testing.T, s graph.State, ev graph.Event, what string) {
	t.Helper()
	before := grfStateString(t, s, grfNow)

	next, res, err := s.Apply(ev)
	requireErrInvalid(t, err, what+": Apply")
	assertZeroApplyResult(t, res, what)
	assertSnapshotUnchanged(t, before, grfStateString(t, next, grfNow), what+": Apply가 반환한 State")
	assertSnapshotUnchanged(t, before, grfStateString(t, s, grfNow), what+": 수신자 State")
}

// --- 결정론적 렌더링 ---

func grfAssetString(ref model.AssetRef) string {
	return "key=" + ref.Key() + " canonical=" + ref.Canonical + " aliases=" + evdAliasesString(ref)
}

// grfEdgeString은 Edge의 관측 가능한 전부를 렌더링한다 (endpoint의 Aliases 포함
// — GRF-005 방어적 복사 위반이 스냅샷에 드러나야 한다).
func grfEdgeString(e graph.Edge) string {
	return strings.Join([]string{
		"from=" + e.From.Key(),
		"fromAliases=" + evdAliasesString(e.From),
		"to=" + e.To.Key(),
		"toAliases=" + evdAliasesString(e.To),
		"rel=" + e.Relation.String(),
		"origin=" + e.Origin.String(),
	}, " ")
}

// grfEdgeIdentity는 3.1이 정의한 edge 정체성 4튜플만 렌더링한다 — endpoint의
// Aliases는 정체성에 관여하지 않는다.
func grfEdgeIdentity(e graph.Edge) string {
	return e.From.Key() + " -[" + e.Relation.String() + "/" + e.Origin.String() + "]-> " + e.To.Key()
}

func grfEdgeIdentities(edges []graph.Edge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, grfEdgeIdentity(e))
	}
	return out
}

func grfAssetKeys(assets []model.AssetRef) []string {
	out := make([]string, 0, len(assets))
	for _, a := range assets {
		out = append(out, a.Key())
	}
	return out
}

// grfEdgeLess는 3.1의 edge 전순서를 스펙 문언 그대로 재현한다 —
// (1) From.Key(), (2) To.Key(), (3) Relation.String(), (4) Origin.String()의
// 바이트 오름차순 사전식 튜플 비교.
func grfEdgeLess(a, b graph.Edge) bool {
	if a.From.Key() != b.From.Key() {
		return a.From.Key() < b.From.Key()
	}
	if a.To.Key() != b.To.Key() {
		return a.To.Key() < b.To.Key()
	}
	if a.Relation.String() != b.Relation.String() {
		return a.Relation.String() < b.Relation.String()
	}
	return a.Origin.String() < b.Origin.String()
}

// grfSortedEdgeIdentities는 기대 edge 집합을 edge 전순서로 정렬해 렌더링한다.
func grfSortedEdgeIdentities(edges []graph.Edge) []string {
	sorted := append([]graph.Edge(nil), edges...)
	sort.SliceStable(sorted, func(i, j int) bool { return grfEdgeLess(sorted[i], sorted[j]) })
	return grfEdgeIdentities(sorted)
}

// grfSortedAssetKeys는 기대 asset 집합을 Key() 바이트 오름차순으로 정렬해
// 렌더링한다.
func grfSortedAssetKeys(assets []model.AssetRef) []string {
	keys := grfAssetKeys(assets)
	sort.Strings(keys)
	return keys
}

// grfTransitionKey는 transition을 **계약된 부분만**으로 렌더링한다 — asset
// 계열은 Kind + Asset.Key(), edge 계열은 Kind + edge 정체성이다. edge 계열
// transition의 Edge 값이 "이벤트의 값"인지 "저장값"인지는 스펙이 규정하지
// 않으므로(artifacts/discrepancy-notes-tests.md 참조) 정체성만 단정한다.
func grfTransitionKey(tr graph.Transition) string {
	switch tr.Kind {
	case graph.TransitionAssetAdded, graph.TransitionAssetRemoved:
		return tr.Kind.String() + " " + tr.Asset.Key()
	case graph.TransitionEdgeAdded, graph.TransitionEdgeRemoved:
		return tr.Kind.String() + " " + grfEdgeIdentity(tr.Edge)
	default:
		return tr.Kind.String() + " <unknown-kind>"
	}
}

func grfTransitionKeys(trs []graph.Transition) []string {
	out := make([]string, 0, len(trs))
	for _, tr := range trs {
		out = append(out, grfTransitionKey(tr))
	}
	return out
}

// grfTransitionString은 transition의 관측 가능한 전부를 렌더링한다 — 결정론
// 비교(GRF-004)처럼 양쪽이 모두 구현 산출물일 때만 쓴다.
func grfTransitionString(tr graph.Transition) string {
	return "kind=" + tr.Kind.String() + " asset=[" + grfAssetString(tr.Asset) + "] edge=[" + grfEdgeString(tr.Edge) + "]"
}

func grfTransitionStrings(trs []graph.Transition) []string {
	out := make([]string, 0, len(trs))
	for _, tr := range trs {
		out = append(out, grfTransitionString(tr))
	}
	return out
}

// grfAssetTransition·grfEdgeTransition은 기대 transition 키를 조립한다.
func grfAssetTransition(kind graph.TransitionKind, ref model.AssetRef) string {
	return kind.String() + " " + ref.Key()
}

func grfEdgeTransition(kind graph.TransitionKind, e graph.Edge) string {
	return kind.String() + " " + grfEdgeIdentity(e)
}

// grfEdgeTransitionKeys는 edge 계열 기대 transition 키를 edge 전순서로 만든다.
func grfEdgeTransitionKeys(kind graph.TransitionKind, edges []graph.Edge) []string {
	ids := grfSortedEdgeIdentities(edges)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, kind.String()+" "+id)
	}
	return out
}

// grfAssetTransitionKeys는 asset 계열 기대 transition 키를 Key() 오름차순으로 만든다.
func grfAssetTransitionKeys(kind graph.TransitionKind, assets []model.AssetRef) []string {
	keys := grfSortedAssetKeys(assets)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, kind.String()+" "+k)
	}
	return out
}

// grfApplyResultString은 ApplyResult 전체를 렌더링한다 (GRF-004의 "반환된
// ApplyResult 열이 정확히 일치한다").
func grfApplyResultString(res graph.ApplyResult, err error) string {
	return "outcome=" + res.Outcome.String() + " err=" + grfErrClass(err) +
		" transitions=[" + strings.Join(grfTransitionStrings(res.Transitions), " | ") + "]"
}

// grfSnapshotString은 Snapshot의 관측 가능한 상태 전부(topology 조회 전부 +
// window 조회·판정 전부)를 결정론적 문자열로 만든다. "변하지 않는다"(GRF-006·
// 033·050)와 "두 값이 구별되지 않는다"(GRF-003·004·031)를 한 줄로 단정한다.
func grfSnapshotString(t *testing.T, snap *graph.Snapshot, now time.Time) string {
	t.Helper()

	var b strings.Builder
	seq, hasBaseline := snap.Sequence()
	fmt.Fprintf(&b, "partition\t%s\n", snap.Partition().String())
	fmt.Fprintf(&b, "sequence\t%s\tbaseline=%t\n", strconv.FormatUint(seq, 10), hasBaseline)
	fmt.Fprintf(&b, "synced\t%t\n", snap.Synced())

	assets := snap.Assets()
	fmt.Fprintf(&b, "assets\tn=%d\n", len(assets))
	for i, a := range assets {
		fmt.Fprintf(&b, "asset[%d]\t%s\n", i, grfAssetString(a))
	}

	edges := snap.Edges()
	fmt.Fprintf(&b, "edges\tn=%d\n", len(edges))
	for i, e := range edges {
		fmt.Fprintf(&b, "edge[%d]\t%s\n", i, grfEdgeString(e))
	}

	for _, probe := range grfProbeAssets(t) {
		got, err := snap.Asset(probe)
		fmt.Fprintf(&b, "probe\t%s\tasset=[%s]\terr=%s\n", probe.Key(), grfAssetString(got), grfErrClass(err))

		from, ferr := snap.EdgesFrom(probe)
		fmt.Fprintf(&b, "  from\tn=%d\terr=%s\n", len(from), grfErrClass(ferr))
		for i, e := range from {
			fmt.Fprintf(&b, "  from[%d]\t%s\n", i, grfEdgeString(e))
		}

		to, terr := snap.EdgesTo(probe)
		fmt.Fprintf(&b, "  to\tn=%d\terr=%s\n", len(to), grfErrClass(terr))
		for i, e := range to {
			fmt.Fprintf(&b, "  to[%d]\t%s\n", i, grfEdgeString(e))
		}
	}

	b.WriteString(evdSnapshot(t, snap.Window(), now))
	return b.String()
}

// grfTopologyString은 sequence 부기를 뺀 "내용"만 렌더링한다 — topology와
// window다. sequence는 전진하지만 내용은 불변이어야 하는 자리(GRF-042·047)에서
// 쓴다.
func grfTopologyString(t *testing.T, snap *graph.Snapshot, now time.Time) string {
	t.Helper()

	var b strings.Builder
	assets := snap.Assets()
	fmt.Fprintf(&b, "assets\tn=%d\n", len(assets))
	for i, a := range assets {
		fmt.Fprintf(&b, "asset[%d]\t%s\n", i, grfAssetString(a))
	}
	edges := snap.Edges()
	fmt.Fprintf(&b, "edges\tn=%d\n", len(edges))
	for i, e := range edges {
		fmt.Fprintf(&b, "edge[%d]\t%s\n", i, grfEdgeString(e))
	}
	b.WriteString(evdSnapshot(t, snap.Window(), now))
	return b.String()
}

// grfStateString은 State의 관측 가능한 전부다 — Partition()과 Snapshot() 전체.
func grfStateString(t *testing.T, s graph.State, now time.Time) string {
	t.Helper()
	return "state.partition\t" + s.Partition().String() + "\n" + grfSnapshotString(t, s.Snapshot(), now)
}

// --- 오류 계열 헬퍼 (3.2) ---

func requireErrStopped(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: ErrStopped 오류를 기대했으나 nil을 받았다", what)
	}
	if !errors.Is(err, graph.ErrStopped) {
		t.Fatalf("%s: errors.Is(err, graph.ErrStopped)가 참이어야 한다 (err=%v)", what, err)
	}
}

// grfErrClass는 오류를 스펙 3.2의 계열 이름으로 분류한다 (스냅샷 비교용).
func grfErrClass(err error) string {
	switch {
	case err == nil:
		return "nil"
	case errors.Is(err, model.ErrInvalid):
		return "invalid"
	case errors.Is(err, graph.ErrStopped):
		return "stopped"
	default:
		return "other"
	}
}

// --- 단정 헬퍼 ---

func grfIsZeroAsset(ref model.AssetRef) bool {
	return ref.Kind == model.AssetKind(0) && ref.Canonical == "" && len(ref.Aliases) == 0
}

func grfIsZeroEdge(e graph.Edge) bool {
	return grfIsZeroAsset(e.From) && grfIsZeroAsset(e.To) &&
		e.Relation == model.EdgeRelation(0) && e.Origin == model.EdgeOrigin(0)
}

func assertZeroEdge(t *testing.T, e graph.Edge, what string) {
	t.Helper()
	if !grfIsZeroEdge(e) {
		t.Errorf("%s: zero Edge를 기대했으나 %s를 받았다", what, grfEdgeString(e))
	}
}

// assertZeroApplyResult는 오류 시의 zero ApplyResult 반환(3.2·GRF-003)을 단정한다.
func assertZeroApplyResult(t *testing.T, res graph.ApplyResult, what string) {
	t.Helper()
	if res.Outcome != graph.ApplyOutcome(0) {
		t.Errorf("%s: 오류 시 Outcome = %s, want zero value", what, res.Outcome.String())
	}
	if len(res.Transitions) != 0 {
		t.Errorf("%s: 오류 시 Transitions 길이 %d, want 0 (%v)", what, len(res.Transitions), grfTransitionKeys(res.Transitions))
	}
}

// assertTransitionShape는 GRF-036의 populate 규칙을 단정한다 — asset 계열은
// Edge가 zero, edge 계열은 Asset이 zero다.
func assertTransitionShape(t *testing.T, trs []graph.Transition, what string) {
	t.Helper()
	for i, tr := range trs {
		if !tr.Kind.IsValid() {
			t.Errorf("%s: Transitions[%d].Kind의 IsValid()가 거짓이다 (%s)", what, i, tr.Kind.String())
			continue
		}
		switch tr.Kind {
		case graph.TransitionAssetAdded, graph.TransitionAssetRemoved:
			if !grfIsZeroEdge(tr.Edge) {
				t.Errorf("%s: Transitions[%d](%s)의 Edge가 zero가 아니다: %s",
					what, i, tr.Kind.String(), grfEdgeString(tr.Edge))
			}
			if grfIsZeroAsset(tr.Asset) {
				t.Errorf("%s: Transitions[%d](%s)의 Asset이 비어 있다", what, i, tr.Kind.String())
			}
		case graph.TransitionEdgeAdded, graph.TransitionEdgeRemoved:
			if !grfIsZeroAsset(tr.Asset) {
				t.Errorf("%s: Transitions[%d](%s)의 Asset이 zero가 아니다: %s",
					what, i, tr.Kind.String(), grfAssetString(tr.Asset))
			}
			if grfIsZeroEdge(tr.Edge) {
				t.Errorf("%s: Transitions[%d](%s)의 Edge가 비어 있다", what, i, tr.Kind.String())
			}
		}
	}
}

// assertTransitionKeys는 Transitions의 내용과 **순서**를 단정한다 (GRF-007).
func assertTransitionKeys(t *testing.T, got []graph.Transition, want []string, what string) {
	t.Helper()
	assertStringsEqual(t, grfTransitionKeys(got), want, what+": Transitions")
}

// assertSnapshotAssets는 Assets()의 내용과 Key() 오름차순 정렬을 단정한다.
func assertSnapshotAssets(t *testing.T, snap *graph.Snapshot, want []model.AssetRef, what string) {
	t.Helper()
	assertStringsEqual(t, grfAssetKeys(snap.Assets()), grfSortedAssetKeys(want), what+": Assets()")
}

// assertSnapshotEdges는 Edges()의 내용과 edge 전순서 정렬을 단정한다.
func assertSnapshotEdges(t *testing.T, snap *graph.Snapshot, want []graph.Edge, what string) {
	t.Helper()
	assertStringsEqual(t, grfEdgeIdentities(snap.Edges()), grfSortedEdgeIdentities(want), what+": Edges()")
}

func assertEdgesFrom(t *testing.T, snap *graph.Snapshot, ref model.AssetRef, want []graph.Edge, what string) {
	t.Helper()
	got, err := snap.EdgesFrom(ref)
	requireNoErr(t, err, what+": EdgesFrom("+ref.Key()+")")
	assertStringsEqual(t, grfEdgeIdentities(got), grfSortedEdgeIdentities(want), what+": EdgesFrom("+ref.Key()+")")
}

func assertEdgesTo(t *testing.T, snap *graph.Snapshot, ref model.AssetRef, want []graph.Edge, what string) {
	t.Helper()
	got, err := snap.EdgesTo(ref)
	requireNoErr(t, err, what+": EdgesTo("+ref.Key()+")")
	assertStringsEqual(t, grfEdgeIdentities(got), grfSortedEdgeIdentities(want), what+": EdgesTo("+ref.Key()+")")
}

// assertSequence는 Snapshot.Sequence()·Synced()를 함께 단정한다 (GRF-054).
func assertSequence(t *testing.T, snap *graph.Snapshot, wantSeq uint64, wantBaseline, wantSynced bool, what string) {
	t.Helper()
	seq, ok := snap.Sequence()
	if seq != wantSeq || ok != wantBaseline {
		t.Errorf("%s: Sequence() = (%d, %t), want (%d, %t)", what, seq, ok, wantSeq, wantBaseline)
	}
	if got := snap.Synced(); got != wantSynced {
		t.Errorf("%s: Synced() = %t, want %t", what, got, wantSynced)
	}
}

// assertEmptyTopology는 asset·edge가 하나도 없음을 단정한다.
func assertEmptyTopology(t *testing.T, snap *graph.Snapshot, what string) {
	t.Helper()
	if got := snap.Assets(); len(got) != 0 {
		t.Errorf("%s: Assets() 길이 %d, want 0 (%v)", what, len(got), grfAssetKeys(got))
	}
	if got := snap.Edges(); len(got) != 0 {
		t.Errorf("%s: Edges() 길이 %d, want 0 (%v)", what, len(got), grfEdgeIdentities(got))
	}
}

// assertAssetLookup은 Asset(ref)이 기대한 저장값(또는 부재)을 돌려주는지
// 단정한다. wantAliases가 nil이면 "저장되어 있지 않음"(zero, nil)을 기대한다.
func assertAssetLookup(t *testing.T, snap *graph.Snapshot, ref model.AssetRef, want *model.AssetRef, what string) {
	t.Helper()
	got, err := snap.Asset(ref)
	requireNoErr(t, err, what+": Asset("+ref.Key()+")")
	if want == nil {
		if !grfIsZeroAsset(got) {
			t.Errorf("%s: Asset(%s) = [%s], want zero (부재)", what, ref.Key(), grfAssetString(got))
		}
		return
	}
	if grfAssetString(got) != grfAssetString(*want) {
		t.Errorf("%s: Asset(%s)\n got=[%s]\nwant=[%s]", what, ref.Key(), grfAssetString(got), grfAssetString(*want))
	}
}

// --- 주입 시계 (3.7 Clock 포트, GRF-064) ---

type grfFakeTicker struct {
	ch    chan time.Time
	stops atomic.Int64
}

func (tk *grfFakeTicker) C() <-chan time.Time { return tk.ch }

func (tk *grfFakeTicker) Stop() { tk.stops.Add(1) }

func (tk *grfFakeTicker) Stops() int { return int(tk.stops.Load()) }

// Tick은 tick 하나를 전달한다 — reducer가 받을 때까지(또는 버퍼에 들어갈 때까지)
// 기다린다.
func (tk *grfFakeTicker) Tick(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(grfWaitTimeout)
	defer timer.Stop()
	select {
	case tk.ch <- grfTickAt:
	case <-timer.C:
		t.Fatalf("tick 전달이 %s 안에 이루어지지 않았다", grfWaitTimeout)
	}
}

type grfFakeClock struct {
	mu       sync.Mutex
	requests []time.Duration
	tickers  []*grfFakeTicker
}

func newGrfFakeClock() *grfFakeClock { return &grfFakeClock{} }

func (c *grfFakeClock) Ticker(d time.Duration) graph.Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	tk := &grfFakeTicker{ch: make(chan time.Time, 1)}
	c.requests = append(c.requests, d)
	c.tickers = append(c.tickers, tk)
	return tk
}

func (c *grfFakeClock) Requests() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.requests...)
}

func (c *grfFakeClock) TickerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.tickers)
}

func (c *grfFakeClock) tickerAt(i int) *grfFakeTicker {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.tickers) {
		return nil
	}
	return c.tickers[i]
}

// --- Emit·RequestResync 기록기 (3.7) ---

type grfEmitRecorder struct {
	mu      sync.Mutex
	batches [][]graph.Transition
}

// Emit은 ReducerConfig.Emit으로 넘기는 함수다. reducer goroutine에서 동기
// 호출되므로 자체 뮤텍스로 보호한다.
func (r *grfEmitRecorder) Emit(trs []graph.Transition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	batch := make([]graph.Transition, len(trs))
	copy(batch, trs)
	r.batches = append(r.batches, batch)
}

func (r *grfEmitRecorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.batches)
}

// Batches는 batch별 transition 키 목록을 순서대로 돌려준다.
func (r *grfEmitRecorder) Batches() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, 0, len(r.batches))
	for _, b := range r.batches {
		out = append(out, grfTransitionKeys(b))
	}
	return out
}

// Rendered는 batch 경계를 포함한 전체를 한 문자열로 만든다 (GRF-004 비교용).
func (r *grfEmitRecorder) Rendered() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for i, batch := range r.batches {
		fmt.Fprintf(&b, "batch[%d] %s\n", i, strings.Join(grfTransitionStrings(batch), " | "))
	}
	return b.String()
}

type grfResyncRecorder struct {
	mu   sync.Mutex
	keys []model.PartitionKey
}

func (r *grfResyncRecorder) Request(p model.PartitionKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, p)
}

func (r *grfResyncRecorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.keys)
}

func (r *grfResyncRecorder) Keys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.keys))
	for _, k := range r.keys {
		out = append(out, k.String())
	}
	return out
}

// --- Reducer 하네스 (3.7) ---

// grfReducerOpts는 하네스가 만들 ReducerConfig를 기술한다.
type grfReducerOpts struct {
	capacity      int
	snapshotEvery time.Duration
	nilEmit       bool
	nilResync     bool
}

func grfDefaultOpts() grfReducerOpts {
	return grfReducerOpts{capacity: 64, snapshotEvery: grfSnapshotEvery}
}

type grfHarness struct {
	reducer *graph.Reducer
	clock   *grfFakeClock
	emit    *grfEmitRecorder
	resync  *grfResyncRecorder
	cancel  context.CancelFunc
	done    chan error
	started bool
	stopped bool
}

func grfNewHarness(t *testing.T, initial graph.State, opts grfReducerOpts) *grfHarness {
	t.Helper()

	h := &grfHarness{
		clock:  newGrfFakeClock(),
		emit:   &grfEmitRecorder{},
		resync: &grfResyncRecorder{},
		done:   make(chan error, 1),
	}
	cfg := graph.ReducerConfig{
		Capacity:      opts.capacity,
		SnapshotEvery: opts.snapshotEvery,
		Clock:         h.clock,
	}
	if !opts.nilEmit {
		cfg.Emit = h.emit.Emit
	}
	if !opts.nilResync {
		cfg.RequestResync = h.resync.Request
	}

	r, err := graph.NewReducer(initial, cfg)
	requireNoErr(t, err, "NewReducer")
	if r == nil {
		t.Fatalf("NewReducer가 nil reducer와 nil 오류를 함께 반환했다")
	}
	h.reducer = r
	return h
}

// Start는 Run을 별도 goroutine에서 시작한다.
func (h *grfHarness) Start(t *testing.T) {
	t.Helper()
	if h.started {
		t.Fatalf("하네스의 Start를 두 번 호출했다")
	}
	h.started = true
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- h.reducer.Run(ctx) }()
	h.WaitTicker(t) // Run이 실제로 시작했음을 확인한다 (3.7 Run 규칙 2).
}

// Stop은 ctx를 취소하고 Run이 반환할 때까지 기다린 뒤 그 오류를 돌려준다.
func (h *grfHarness) Stop(t *testing.T) error {
	t.Helper()
	if !h.started {
		t.Fatalf("Start하지 않은 하네스를 Stop했다")
	}
	if h.stopped {
		t.Fatalf("하네스의 Stop을 두 번 호출했다")
	}
	h.stopped = true
	h.cancel()

	timer := time.NewTimer(grfWaitTimeout)
	defer timer.Stop()
	select {
	case err := <-h.done:
		return err
	case <-timer.C:
		t.Fatalf("Run이 취소 후 %s 안에 반환하지 않았다", grfWaitTimeout)
		return nil
	}
}

func (h *grfHarness) MustOffer(t *testing.T, evs ...graph.Event) {
	t.Helper()
	for i, ev := range evs {
		if err := h.reducer.Offer(ev); err != nil {
			t.Fatalf("Offer(#%d): 예상치 못한 오류: %v", i, err)
		}
	}
}

// WaitProcessed는 Apply가 처리한 이벤트 수가 want 이상이 될 때까지 기다린다.
func (h *grfHarness) WaitProcessed(t *testing.T, want uint64) {
	t.Helper()
	grfWaitFor(t, fmt.Sprintf("처리 완료 %d건 대기", want), func() bool {
		return grfProcessed(h.reducer.Stats()) >= want
	})
}

// WaitTicker는 Run이 Clock.Ticker를 요청할 때까지 기다린 뒤 그 Ticker를 준다.
func (h *grfHarness) WaitTicker(t *testing.T) *grfFakeTicker {
	t.Helper()
	grfWaitFor(t, "Run이 Clock.Ticker를 요청하기를 대기", func() bool {
		return h.clock.TickerCount() >= 1
	})
	return h.clock.tickerAt(0)
}

// Tick은 tick 하나를 전달한다 (snapshot 발행 유도 — 3.7 Run 규칙 6).
func (h *grfHarness) Tick(t *testing.T) {
	t.Helper()
	h.WaitTicker(t).Tick(t)
}

// TickAndWait는 tick을 보낸 뒤 발행된 snapshot이 cond를 만족할 때까지 기다린다.
func (h *grfHarness) TickAndWait(t *testing.T, what string, cond func(*graph.Snapshot) bool) {
	t.Helper()
	h.Tick(t)
	grfWaitFor(t, what, func() bool { return cond(h.reducer.Snapshot()) })
}

// --- Stats 헬퍼 (3.7) ---

// grfProcessed는 Apply가 처리한 이벤트 수 = Outcome 계수의 합 + Invalid다.
func grfProcessed(s graph.Stats) uint64 {
	return s.Applied + s.Resynced + s.Stale + s.Gaps + s.AwaitingResync + s.ObservationRejected + s.Invalid
}

// grfOutcomeTotal은 Outcome 계수만의 합이다 (GRF-067: 오류 없이 처리한 수).
func grfOutcomeTotal(s graph.Stats) uint64 {
	return s.Applied + s.Resynced + s.Stale + s.Gaps + s.AwaitingResync + s.ObservationRejected
}

func grfStatsString(s graph.Stats) string {
	return fmt.Sprintf(
		"enqueued=%d evicted=%d applied=%d resynced=%d stale=%d gaps=%d awaiting=%d rejected=%d invalid=%d resyncRequests=%d",
		s.Enqueued, s.Evicted, s.Applied, s.Resynced, s.Stale, s.Gaps,
		s.AwaitingResync, s.ObservationRejected, s.Invalid, s.ResyncRequests)
}

func assertStats(t *testing.T, got, want graph.Stats, what string) {
	t.Helper()
	if grfStatsString(got) != grfStatsString(want) {
		t.Errorf("%s: Stats 불일치\n got=%s\nwant=%s", what, grfStatsString(got), grfStatsString(want))
	}
}

// assertStatsMonotone은 각 필드가 감소하지 않았음을 단정한다 (GRF-067).
func assertStatsMonotone(t *testing.T, prev, next graph.Stats, what string) {
	t.Helper()
	fields := []struct {
		name       string
		prev, next uint64
	}{
		{"Enqueued", prev.Enqueued, next.Enqueued},
		{"Evicted", prev.Evicted, next.Evicted},
		{"Applied", prev.Applied, next.Applied},
		{"Resynced", prev.Resynced, next.Resynced},
		{"Stale", prev.Stale, next.Stale},
		{"Gaps", prev.Gaps, next.Gaps},
		{"AwaitingResync", prev.AwaitingResync, next.AwaitingResync},
		{"ObservationRejected", prev.ObservationRejected, next.ObservationRejected},
		{"Invalid", prev.Invalid, next.Invalid},
		{"ResyncRequests", prev.ResyncRequests, next.ResyncRequests},
	}
	for _, f := range fields {
		if f.next < f.prev {
			t.Errorf("%s: Stats.%s가 %d에서 %d로 감소했다", what, f.name, f.prev, f.next)
		}
	}
}
