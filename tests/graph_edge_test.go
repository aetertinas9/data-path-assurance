// graph_edge_test.go — Edge와 PartitionFor (specs/graph/spec.md 3.1·3.3,
// GRF-010~014).
package tests

import (
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// GRF-010: 유효한 조합은 수용되고, 생성자를 거치지 않은 조립값에도 같은 규칙이
// 적용된다. zero value Edge{}의 Validate()는 model.ErrInvalid 계열이다.
func TestGRF010_NewEdgeAcceptsValidCombinationsAndValidateAgrees(t *testing.T) {
	from := grfNIC0(t)
	to := grfSwitchPort1(t)

	cases := []struct {
		name   string
		rel    model.EdgeRelation
		origin model.EdgeOrigin
	}{
		{"CONNECTED_TO + Observed", model.RelConnectedTo, model.OriginObserved},
		{"CONNECTED_TO + Intended", model.RelConnectedTo, model.OriginIntended},
		{"CONNECTED_TO + Derived", model.RelConnectedTo, model.OriginDerived},
		{"LOCATED_IN + Intended", model.RelLocatedIn, model.OriginIntended},
		{"UPSTREAM_OF + Observed", model.RelUpstreamOf, model.OriginObserved},
		{"DOWNSTREAM_OF + Observed", model.RelDownstreamOf, model.OriginObserved},
		{"MEMBER_OF + Derived", model.RelMemberOf, model.OriginDerived},
		{"ALLOCATED_TO + Observed", model.RelAllocatedTo, model.OriginObserved},
		{"OBSERVED_BY + Observed", model.RelObservedBy, model.OriginObserved},
		{"INTENDED_TO_CONNECT + Intended", model.RelIntendedToConnect, model.OriginIntended},
		{"SHARES_FAILURE_DOMAIN_WITH + Derived", model.RelSharesFailureDomainWith, model.OriginDerived},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := graph.NewEdge(from, to, tc.rel, tc.origin)
			requireNoErr(t, err, "NewEdge("+tc.name+")")

			// 생성자가 반환한 값의 Validate()는 nil이다.
			requireNoErr(t, got.Validate(), "생성자가 반환한 Edge의 Validate()")

			if got.From.Key() != from.Key() || got.To.Key() != to.Key() {
				t.Errorf("endpoint가 전달값과 다르다: from=%s to=%s", got.From.Key(), got.To.Key())
			}
			if got.Relation != tc.rel || got.Origin != tc.origin {
				t.Errorf("Relation/Origin이 전달값과 다르다: rel=%s origin=%s",
					got.Relation.String(), got.Origin.String())
			}

			// 생성자를 거치지 않고 같은 필드로 조립한 Edge의 Validate()도 nil이다.
			assembled := graph.Edge{From: from, To: to, Relation: tc.rel, Origin: tc.origin}
			requireNoErr(t, assembled.Validate(), "손으로 조립한 Edge의 Validate()")
		})
	}
}

// GRF-010 (edge): zero value Edge{}의 Validate()는 model.ErrInvalid 계열이다.
func TestGRF010_ZeroEdgeIsInvalid(t *testing.T) {
	var e graph.Edge
	requireErrInvalid(t, e.Validate(), "zero value Edge{}의 Validate()")
	requireErrInvalid(t, (graph.Edge{}).Validate(), "graph.Edge{}.Validate()")
}

// GRF-011 (edge): 무효 endpoint·자기 edge·무효 Relation/Origin은 zero Edge와
// model.ErrInvalid 계열 오류다. Validate()도 같은 판정을 한다 (규칙은 양쪽 동일).
func TestGRF011_NewEdgeRejectsInvalidInput(t *testing.T) {
	valid := grfNIC0(t)
	other := grfSwitchPort1(t)

	// Key()는 같고 Aliases만 다른 endpoint — 3.1의 asset 동일성상 같은 asset이므로
	// 자기 edge다.
	sameKeyOtherAliases := grfNIC0Aliased(t, grfAliasA(t))

	var zeroRef model.AssetRef

	cases := []struct {
		name   string
		from   model.AssetRef
		to     model.AssetRef
		rel    model.EdgeRelation
		origin model.EdgeOrigin
	}{
		{"From이 zero AssetRef", zeroRef, other, model.RelConnectedTo, model.OriginObserved},
		{"To가 zero AssetRef", valid, zeroRef, model.RelConnectedTo, model.OriginObserved},
		{"양쪽 endpoint가 zero", zeroRef, zeroRef, model.RelConnectedTo, model.OriginObserved},
		{"From의 Kind가 무효", model.AssetRef{Kind: model.AssetKind(0), Canonical: canonicalString}, other, model.RelConnectedTo, model.OriginObserved},
		{"To의 Canonical이 비어 있음", valid, model.AssetRef{Kind: model.KindNICPort}, model.RelConnectedTo, model.OriginObserved},
		{"자기 edge (같은 값)", valid, valid, model.RelConnectedTo, model.OriginObserved},
		{"자기 edge (Aliases만 다름)", valid, sameKeyOtherAliases, model.RelConnectedTo, model.OriginObserved},
		{"자기 edge (Aliases만 다름, 반대 방향)", sameKeyOtherAliases, valid, model.RelConnectedTo, model.OriginObserved},
		{"Relation이 zero value", valid, other, model.EdgeRelation(0), model.OriginObserved},
		{"Relation이 열거 밖 양수", valid, other, model.EdgeRelation(9999), model.OriginObserved},
		{"Relation이 열거 밖 음수", valid, other, model.EdgeRelation(-1), model.OriginObserved},
		{"Origin이 zero value", valid, other, model.RelConnectedTo, model.EdgeOrigin(0)},
		{"Origin이 열거 밖 양수", valid, other, model.RelConnectedTo, model.EdgeOrigin(9999)},
		{"Origin이 열거 밖 음수", valid, other, model.RelConnectedTo, model.EdgeOrigin(-1)},
		{"Relation·Origin 둘 다 zero", valid, other, model.EdgeRelation(0), model.EdgeOrigin(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := graph.NewEdge(tc.from, tc.to, tc.rel, tc.origin)
			requireErrInvalid(t, err, "NewEdge("+tc.name+")")
			assertZeroEdge(t, got, "무효 입력의 NewEdge 반환값")

			// 같은 필드로 조립한 Edge의 Validate()도 같은 계열 오류다.
			assembled := graph.Edge{From: tc.from, To: tc.to, Relation: tc.rel, Origin: tc.origin}
			requireErrInvalid(t, assembled.Validate(), "손으로 조립한 Edge의 Validate()("+tc.name+")")
		})
	}
}

// GRF-012 (관계-기원 불변식): INTENDED_TO_CONNECT는 OriginIntended만,
// SHARES_FAILURE_DOMAIN_WITH는 OriginDerived만 허용한다. 그 외 조합은 제한하지
// 않는다.
func TestGRF012_RelationOriginInvariant(t *testing.T) {
	from := grfNIC0(t)
	to := grfSwitchPort1(t)

	cases := []struct {
		name      string
		rel       model.EdgeRelation
		origin    model.EdgeOrigin
		wantValid bool
	}{
		{"INTENDED_TO_CONNECT + Intended", model.RelIntendedToConnect, model.OriginIntended, true},
		{"INTENDED_TO_CONNECT + Observed", model.RelIntendedToConnect, model.OriginObserved, false},
		{"INTENDED_TO_CONNECT + Derived", model.RelIntendedToConnect, model.OriginDerived, false},
		{"SHARES_FAILURE_DOMAIN_WITH + Derived", model.RelSharesFailureDomainWith, model.OriginDerived, true},
		{"SHARES_FAILURE_DOMAIN_WITH + Observed", model.RelSharesFailureDomainWith, model.OriginObserved, false},
		{"SHARES_FAILURE_DOMAIN_WITH + Intended", model.RelSharesFailureDomainWith, model.OriginIntended, false},
		{"CONNECTED_TO + Observed (제한 없음)", model.RelConnectedTo, model.OriginObserved, true},
		{"CONNECTED_TO + Intended (제한 없음)", model.RelConnectedTo, model.OriginIntended, true},
		{"CONNECTED_TO + Derived (제한 없음)", model.RelConnectedTo, model.OriginDerived, true},
		{"LOCATED_IN + Intended (제한 없음)", model.RelLocatedIn, model.OriginIntended, true},
		{"LOCATED_IN + Derived (제한 없음)", model.RelLocatedIn, model.OriginDerived, true},
		{"UPSTREAM_OF + Derived (제한 없음)", model.RelUpstreamOf, model.OriginDerived, true},
		{"MEMBER_OF + Intended (제한 없음)", model.RelMemberOf, model.OriginIntended, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := graph.NewEdge(from, to, tc.rel, tc.origin)
			requireValidity(t, err, tc.wantValid, "NewEdge("+tc.name+")")
			if !tc.wantValid {
				assertZeroEdge(t, got, "불변식 위반의 NewEdge 반환값")
			}

			assembled := graph.Edge{From: from, To: to, Relation: tc.rel, Origin: tc.origin}
			requireValidity(t, assembled.Validate(), tc.wantValid, "손으로 조립한 Edge의 Validate()("+tc.name+")")
		})
	}
}

// GRF-013 (정체성): Aliases·Raw·Source의 차이는 정체성에 관여하지 않는다 —
// upsert는 교체(transition 없음), remove는 매칭된다.
func TestGRF013_EdgeIdentityIgnoresEndpointAliases(t *testing.T) {
	p := grfPartitionA(t)
	base := grfConnected(t, grfNIC0(t), grfSwitchPort1(t))
	aliased := grfConnected(t, grfNIC0Aliased(t, grfAliasA(t), grfAliasWithRaw([]byte{1, 2, 3})), grfSwitchPort1(t))

	if grfEdgeIdentity(base) != grfEdgeIdentity(aliased) {
		t.Fatalf("픽스처 오류: 두 edge의 정체성이 달라졌다\n%s\n%s",
			grfEdgeIdentity(base), grfEdgeIdentity(aliased))
	}

	s := grfSyncedState(t, p, 10, nil, nil)

	s, res := grfApply(t, s, grfEdgeUpsert(t, p, 11, base), graph.OutcomeApplied, "새 edge upsert")
	assertTransitionKeys(t, res.Transitions, []string{grfEdgeTransition(graph.TransitionEdgeAdded, base)}, "새 edge upsert")

	// 같은 정체성의 재-upsert는 교체이며 transition을 내지 않는다.
	s, res = grfApply(t, s, grfEdgeUpsert(t, p, 12, aliased), graph.OutcomeApplied, "Aliases만 다른 재-upsert")
	assertTransitionKeys(t, res.Transitions, nil, "Aliases만 다른 재-upsert")
	assertSnapshotEdges(t, s.Snapshot(), []graph.Edge{base}, "재-upsert 후")

	// remove도 정체성으로 매칭된다 — Aliases가 달라도 지워진다.
	s, res = grfApply(t, s, grfEdgeRemove(t, p, 13, base), graph.OutcomeApplied, "Aliases 다른 값으로 remove")
	assertTransitionKeys(t, res.Transitions, []string{grfEdgeTransition(graph.TransitionEdgeRemoved, base)}, "remove")
	assertSnapshotEdges(t, s.Snapshot(), nil, "remove 후")
}

// GRF-013 (정체성 — edge): 방향·Relation·Origin이 다르면 다른 정체성이고 서로
// 독립으로 공존한다.
func TestGRF013_EdgeIdentityDistinguishesDirectionRelationAndOrigin(t *testing.T) {
	p := grfPartitionA(t)
	nic := grfNIC0(t)
	port := grfSwitchPort1(t)

	forward := grfConnected(t, nic, port)
	reversed := grfConnected(t, port, nic)
	otherRel := grfNewEdge(t, nic, port, model.RelUpstreamOf, model.OriginObserved)
	otherOrigin := grfNewEdge(t, nic, port, model.RelConnectedTo, model.OriginIntended)

	want := []graph.Edge{forward, reversed, otherRel, otherOrigin}

	s := grfSyncedState(t, p, 0, nil, nil)
	for i, e := range want {
		var res graph.ApplyResult
		s, res = grfApply(t, s, grfEdgeUpsert(t, p, uint64(i+1), e), graph.OutcomeApplied, "독립 edge upsert")
		assertTransitionKeys(t, res.Transitions,
			[]string{grfEdgeTransition(graph.TransitionEdgeAdded, e)}, "독립 edge upsert")
	}

	snap := s.Snapshot()
	assertSnapshotEdges(t, snap, want, "네 정체성이 공존한다")

	// 하나만 제거해도 나머지 셋은 남는다.
	s, _ = grfApply(t, s, grfEdgeRemove(t, p, 5, otherRel), graph.OutcomeApplied, "하나만 제거")
	assertSnapshotEdges(t, s.Snapshot(), []graph.Edge{forward, reversed, otherOrigin}, "하나 제거 후")

	// 3.3 방향과 비추론: 역방향 edge는 자동 생성되지 않는다.
	only := grfSyncedState(t, p, 100, nil, []graph.Edge{grfNewEdge(t, nic, port, model.RelUpstreamOf, model.OriginObserved)})
	assertSnapshotEdges(t, only.Snapshot(),
		[]graph.Edge{grfNewEdge(t, nic, port, model.RelUpstreamOf, model.OriginObserved)},
		"UPSTREAM_OF만 저장했을 때 DOWNSTREAM_OF는 생기지 않는다")
}

// GRF-014: anchor 2종만 파티션 키를 도출한다.
func TestGRF014_PartitionForAcceptsOnlyAnchorKinds(t *testing.T) {
	t.Run("anchor Kind 2종은 anchor.Key()를 키로 준다", func(t *testing.T) {
		anchors := []model.AssetRef{grfNodeAnchor(t), grfSwitchAnchor(t)}
		for _, anchor := range anchors {
			got, err := graph.PartitionFor(anchor)
			requireNoErr(t, err, "PartitionFor("+anchor.Key()+")")
			if got != model.PartitionKey(anchor.Key()) {
				t.Errorf("PartitionFor(%s) = %q, want %q", anchor.Key(), got.String(), anchor.Key())
			}
			if !got.IsValid() {
				t.Errorf("PartitionFor(%s)가 준 키의 IsValid()가 거짓이다", anchor.Key())
			}
			// 결정론: 같은 인자는 같은 결과다.
			again, err := graph.PartitionFor(anchor)
			requireNoErr(t, err, "PartitionFor 재호출")
			if again != got {
				t.Errorf("PartitionFor가 재호출에서 다른 값을 냈다: %q vs %q", got.String(), again.String())
			}
		}

		// 서로 다른 anchor는 서로 다른 파티션 키다.
		a, err := graph.PartitionFor(grfNodeAnchor(t))
		requireNoErr(t, err, "PartitionFor(node)")
		b, err := graph.PartitionFor(grfSwitchAnchor(t))
		requireNoErr(t, err, "PartitionFor(switch)")
		if a == b {
			t.Errorf("서로 다른 anchor가 같은 파티션 키를 냈다: %q", a.String())
		}

		// Aliases는 파티션 키에 관여하지 않는다 (Key() 기준).
		aliasedAnchor := mustAssetRef(t, model.KindKubernetesNode,
			mustTypedID(t, string(model.NamespaceKubernetesNodeUID), "node-a"), grfAliasA(t))
		aliased, err := graph.PartitionFor(aliasedAnchor)
		requireNoErr(t, err, "PartitionFor(Aliases 있는 anchor)")
		if aliased != a {
			t.Errorf("Aliases가 파티션 키를 바꿨다: %q vs %q", aliased.String(), a.String())
		}
	})

	t.Run("그 외 Kind와 무효 anchor는 zero 값과 ErrInvalid다", func(t *testing.T) {
		nonAnchors := []model.AssetRef{
			grfNIC0(t),
			grfNIC1(t),
			grfRootPort(t),
			grfSwitchPort1(t),
			grfUnknownAsset(t),
			evdSubjectC(t),
			mustAssetRef(t, model.KindSite, mustTypedID(t, "graph-test", "site-1")),
			mustAssetRef(t, model.KindRack, mustTypedID(t, "graph-test", "rack-1")),
			mustAssetRef(t, model.KindPhysicalLink, mustTypedID(t, "graph-test", "link-1")),
		}
		for _, ref := range nonAnchors {
			got, err := graph.PartitionFor(ref)
			requireErrInvalid(t, err, "PartitionFor("+ref.Key()+")")
			if got != model.PartitionKey("") {
				t.Errorf("PartitionFor(%s)가 zero 값이 아닌 %q를 반환했다", ref.Key(), got.String())
			}
		}

		invalid := []struct {
			name string
			ref  model.AssetRef
		}{
			{"zero AssetRef", model.AssetRef{}},
			{"Kind만 anchor이고 Canonical이 비어 있음", model.AssetRef{Kind: model.KindKubernetesNode}},
			{"Kind만 anchor이고 Canonical이 TypedID 형식이 아님", model.AssetRef{Kind: model.KindEthernetSwitch, Canonical: "형식아님"}},
			{"무효 alias를 가진 anchor", model.AssetRef{
				Kind:      model.KindKubernetesNode,
				Canonical: canonicalString,
				Aliases:   []model.TypedID{{Namespace: "", Value: ""}},
			}},
		}
		for _, tc := range invalid {
			got, err := graph.PartitionFor(tc.ref)
			requireErrInvalid(t, err, "PartitionFor("+tc.name+")")
			if got != model.PartitionKey("") {
				t.Errorf("PartitionFor(%s)가 zero 값이 아닌 %q를 반환했다", tc.name, got.String())
			}
		}
	})
}
