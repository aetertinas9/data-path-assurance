// identity_resolve_test.go — 해석과 조회 (specs/identity/spec.md 3.4,
// IDN-040~044·050~054). 스펙이 "edge"로 표시한 기준을 최우선으로 다룬다.
package tests

import (
	"errors"
	"sort"
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// idnSampleResolver는 여러 테스트가 공유하는 등록 상태를 만든다.
//
//	NICPort/cA        aliases: lldp-chassis-id…, lldp-port-id…
//	SwitchPort/cB     aliases: kubernetes-pod-uid…
//	KubernetesNode/cC aliases: 없음
func idnSampleResolver(t *testing.T) *identity.Resolver {
	t.Helper()
	r := identity.NewResolver()
	mustRegisterAsset(t, r, model.KindNICPort, idnCanonicalA(t))
	mustRegisterAsset(t, r, model.KindSwitchPort, idnCanonicalB(t))
	mustRegisterAsset(t, r, model.KindKubernetesNode, idnCanonicalC(t))
	mustRegisterAlias(t, r, idnCanonicalA(t), idnAliasPort(t))
	mustRegisterAlias(t, r, idnCanonicalA(t), idnAliasChassis(t))
	mustRegisterAlias(t, r, idnCanonicalB(t), idnAliasPod(t))
	return r
}

// idnSampleProbes는 idnSampleResolver의 모든 identity와 미등록 identity다.
func idnSampleProbes(t *testing.T) []model.TypedID {
	t.Helper()
	return []model.TypedID{
		idnCanonicalA(t), idnCanonicalB(t), idnCanonicalC(t),
		idnAliasPort(t), idnAliasChassis(t), idnAliasPod(t),
		idnUnregistered(t),
	}
}

// IDN-040: canonical로 해석한 결과와 alias로 해석한 결과는 동일하다.
func TestIDN040_ResolveByCanonicalAndAliasAreIdentical(t *testing.T) {
	r := idnSampleResolver(t)
	cA := idnCanonicalA(t)
	wantAliases := []string{idnAliasChassis(t).String(), idnAliasPort(t).String()}

	viaCanonical := mustResolve(t, r, cA)
	assertRefShape(t, viaCanonical, model.KindNICPort, cA, wantAliases, "Resolve(canonical)")

	for _, alias := range []model.TypedID{idnAliasPort(t), idnAliasChassis(t)} {
		t.Run(alias.String(), func(t *testing.T) {
			viaAlias := mustResolve(t, r, alias)
			assertRefShape(t, viaAlias, model.KindNICPort, cA, wantAliases, "Resolve(alias)")
			assertSameRef(t, viaAlias, viaCanonical, "alias 해석 결과")
			if viaAlias.Key() != viaCanonical.Key() {
				t.Errorf("Key() = %q, want %q", viaAlias.Key(), viaCanonical.Key())
			}
		})
	}

	// alias가 없는 asset은 길이 0인 Aliases를 낸다.
	assertRefShape(t, mustResolve(t, r, idnCanonicalC(t)), model.KindKubernetesNode, idnCanonicalC(t), nil,
		"Resolve(alias 없는 asset)")
}

// IDN-041 (edge): 유효하지만 등록되지 않은 identity는 zero AssetRef와
// ErrNotRegistered다.
func TestIDN041_ResolveUnregisteredIdentity(t *testing.T) {
	r := idnSampleResolver(t)

	cases := []idnCase{
		{"어디에도 없는 identity", idnUnregistered(t)},
		{"등록된 값과 namespace만 같음", mustTypedID(t, string(model.NamespacePCIBDF), "0000:ff:00.9")},
		{"등록된 값과 value만 같음", mustTypedID(t, "other-ns", canonicalValue)},
		{"공백만인 identity", mustTypedID(t, " ", " ")},
		{"대소문자만 다른 identity", mustTypedID(t, "PCI-BDF", canonicalValue)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := resolverSnapshot(t, r, idnSampleProbes(t)...)

			got, err := r.Resolve(tc.id)
			requireErrNotRegistered(t, err, "Resolve(미등록)")
			assertZeroRef(t, got, "Resolve(미등록)")

			assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnSampleProbes(t)...), "Resolve(미등록)")
		})
	}

	// 빈 Resolver에서는 어떤 identity도 해석되지 않는다.
	empty := identity.NewResolver()
	_, err := empty.Resolve(idnCanonicalA(t))
	requireErrNotRegistered(t, err, "빈 Resolver의 Resolve")
}

// IDN-042 (edge): zero TypedID나 3.1 위반 TypedID는 model.ErrInvalid 계열이며
// ErrNotRegistered가 아니다.
func TestIDN042_ResolveInvalidIdentityIsNotNotRegistered(t *testing.T) {
	r := idnSampleResolver(t)

	for _, tc := range invalidIdentities() {
		t.Run(tc.name, func(t *testing.T) {
			before := resolverSnapshot(t, r, idnSampleProbes(t)...)

			got, err := r.Resolve(tc.id)
			requireErrInvalid(t, err, "Resolve(무효 identity)")
			assertZeroRef(t, got, "Resolve(무효 identity)")
			if errors.Is(err, identity.ErrNotRegistered) {
				t.Errorf("무효 identity의 오류가 ErrNotRegistered로도 판정되었다 (err=%v)", err)
			}

			assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnSampleProbes(t)...), "Resolve(무효 identity)")
		})
	}

	// 빈 Resolver에서도 판정 순서는 같다 — 등록 여부보다 유효성이 먼저다(IDN-005).
	empty := identity.NewResolver()
	_, err := empty.Resolve(model.TypedID{})
	requireErrInvalid(t, err, "빈 Resolver의 Resolve(zero value)")
	if errors.Is(err, identity.ErrNotRegistered) {
		t.Errorf("빈 Resolver에서도 무효 identity는 ErrNotRegistered가 아니어야 한다 (err=%v)", err)
	}
}

// IDN-043 (edge): Namespace·Value가 같고 Raw·Source만 다른 TypedID는 같은
// asset으로 해석된다.
func TestIDN043_ResolveIgnoresRawAndSource(t *testing.T) {
	t.Run("정규화된 값으로 등록하고 Raw·Source가 있는 값으로 해석", func(t *testing.T) {
		r := idnSampleResolver(t)
		cA := idnCanonicalA(t)
		want := mustResolve(t, r, cA)

		variants := []model.TypedID{
			{Namespace: cA.Namespace, Value: cA.Value, Raw: []byte{0x01, 0x02}},
			{Namespace: cA.Namespace, Value: cA.Value, Source: "sysfs"},
			{Namespace: cA.Namespace, Value: cA.Value, Raw: []byte("raw"), Source: "gnmi"},
		}
		for _, v := range variants {
			got, err := r.Resolve(v)
			requireNoErr(t, err, "Resolve(Raw·Source 변형)")
			assertSameRef(t, got, want, "Raw·Source 변형의 해석 결과")
		}

		// alias도 마찬가지다.
		alias := idnAliasPort(t)
		got, err := r.Resolve(model.TypedID{
			Namespace: alias.Namespace,
			Value:     alias.Value,
			Raw:       []byte{0xaa},
			Source:    "lldp",
		})
		requireNoErr(t, err, "Resolve(alias의 Raw·Source 변형)")
		assertSameRef(t, got, want, "alias 변형의 해석 결과")
	})

	t.Run("Raw·Source가 있는 값으로 등록하고 정규화된 값으로 해석", func(t *testing.T) {
		r := identity.NewResolver()
		cA := idnCanonicalA(t)
		alias := idnAliasPort(t)

		mustRegisterAsset(t, r, model.KindNICPort, model.TypedID{
			Namespace: cA.Namespace, Value: cA.Value, Raw: []byte{0x01}, Source: "sysfs",
		})
		mustRegisterAlias(t, r,
			model.TypedID{Namespace: cA.Namespace, Value: cA.Value, Source: "sysfs"},
			model.TypedID{Namespace: alias.Namespace, Value: alias.Value, Raw: []byte{0x02}, Source: "lldp"})

		// 3.1: 저장된 identity는 정규화되어 있다.
		ref := mustResolve(t, r, cA)
		assertRefShape(t, ref, model.KindNICPort, cA, []string{alias.String()}, "정규화 저장 확인")

		assertSameRef(t, mustResolve(t, r, alias), ref, "alias 해석 결과")
	})
}

// IDN-044 (edge 집합): ResolveAll의 다섯 갈래.
func TestIDN044_ResolveAll(t *testing.T) {
	cA, cB := idnCanonicalA(t), idnCanonicalB(t)
	aliasPort, aliasChassis := idnAliasPort(t), idnAliasChassis(t)
	unregistered := idnUnregistered(t)

	t.Run("(a) 길이 0이면 ErrInvalid", func(t *testing.T) {
		r := idnSampleResolver(t)
		before := resolverSnapshot(t, r, idnSampleProbes(t)...)

		for _, ids := range [][]model.TypedID{nil, {}} {
			got, err := r.ResolveAll(ids)
			requireErrInvalid(t, err, "ResolveAll(길이 0)")
			assertZeroRef(t, got, "ResolveAll(길이 0)")
		}
		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnSampleProbes(t)...), "ResolveAll(길이 0)")
	})

	t.Run("(b) 원소 하나라도 3.1 위반이면 ErrInvalid", func(t *testing.T) {
		r := idnSampleResolver(t)
		for _, tc := range invalidIdentities() {
			t.Run(tc.name, func(t *testing.T) {
				before := resolverSnapshot(t, r, idnSampleProbes(t)...)

				lists := [][]model.TypedID{
					{tc.id},
					{tc.id, cA},
					{cA, tc.id},
					{cA, aliasPort, tc.id, cB},
				}
				for _, ids := range lists {
					got, err := r.ResolveAll(ids)
					requireErrInvalid(t, err, "ResolveAll(무효 원소 포함)")
					assertZeroRef(t, got, "ResolveAll(무효 원소 포함)")
				}
				assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnSampleProbes(t)...),
					"ResolveAll(무효 원소 포함)")
			})
		}
	})

	t.Run("(b) 유효성이 등록 여부·모호성보다 먼저 판정된다", func(t *testing.T) {
		r := idnSampleResolver(t)

		// 무효 원소 + 미등록 원소
		_, err := r.ResolveAll([]model.TypedID{{}, unregistered})
		requireErrInvalid(t, err, "ResolveAll(무효 + 미등록)")

		// 무효 원소 + 서로 다른 두 asset
		_, err = r.ResolveAll([]model.TypedID{cA, cB, {Namespace: "ns:콜론", Value: "v"}})
		requireErrInvalid(t, err, "ResolveAll(무효 + 모호)")
	})

	t.Run("(c) 모두 같은 asset이면 그 AssetRef와 nil", func(t *testing.T) {
		r := idnSampleResolver(t)
		want := mustResolve(t, r, cA)

		lists := []struct {
			name string
			ids  []model.TypedID
		}{
			{"canonical 하나", []model.TypedID{cA}},
			{"alias 하나", []model.TypedID{aliasPort}},
			{"canonical + alias", []model.TypedID{cA, aliasPort}},
			{"alias 두 개", []model.TypedID{aliasPort, aliasChassis}},
			{"중복 포함", []model.TypedID{aliasPort, aliasPort, cA, aliasChassis, cA}},
			{"Raw·Source 변형 혼용", []model.TypedID{
				{Namespace: cA.Namespace, Value: cA.Value, Raw: []byte{0x01}},
				{Namespace: aliasPort.Namespace, Value: aliasPort.Value, Source: "lldp"},
			}},
		}
		for _, tc := range lists {
			t.Run(tc.name, func(t *testing.T) {
				before := resolverSnapshot(t, r, idnSampleProbes(t)...)

				got, err := r.ResolveAll(tc.ids)
				requireNoErr(t, err, "ResolveAll(같은 asset)")
				assertSameRef(t, got, want, "ResolveAll 결과")
				assertRefShape(t, got, model.KindNICPort, cA,
					[]string{aliasChassis.String(), aliasPort.String()}, "ResolveAll 결과")

				assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnSampleProbes(t)...),
					"ResolveAll(같은 asset)")
			})
		}

		// 원소 1개면 Resolve와 같다.
		for _, id := range []model.TypedID{cA, cB, idnCanonicalC(t), aliasPort, idnAliasPod(t)} {
			single, err := r.ResolveAll([]model.TypedID{id})
			requireNoErr(t, err, "ResolveAll(원소 1개)")
			assertSameRef(t, single, mustResolve(t, r, id), "ResolveAll(원소 1개)와 Resolve")
		}
	})

	t.Run("(d) 해석되지 않는 원소가 있으면 ErrNotRegistered", func(t *testing.T) {
		r := idnSampleResolver(t)
		before := resolverSnapshot(t, r, idnSampleProbes(t)...)

		lists := []struct {
			name string
			ids  []model.TypedID
		}{
			{"미등록 하나뿐", []model.TypedID{unregistered}},
			{"앞에 미등록", []model.TypedID{unregistered, cA}},
			{"뒤에 미등록", []model.TypedID{cA, aliasPort, unregistered}},
			{"모두 미등록", []model.TypedID{unregistered, mustTypedID(t, "other-ns", "other-value")}},
		}
		for _, tc := range lists {
			t.Run(tc.name, func(t *testing.T) {
				got, err := r.ResolveAll(tc.ids)
				requireErrNotRegistered(t, err, "ResolveAll(미등록 포함)")
				assertZeroRef(t, got, "ResolveAll(미등록 포함)")
			})
		}
		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnSampleProbes(t)...), "ResolveAll(미등록 포함)")
	})

	t.Run("(e) 서로 다른 두 개 이상의 asset이면 ErrAmbiguous", func(t *testing.T) {
		r := idnSampleResolver(t)
		before := resolverSnapshot(t, r, idnSampleProbes(t)...)

		lists := []struct {
			name string
			ids  []model.TypedID
		}{
			{"canonical 둘", []model.TypedID{cA, cB}},
			{"canonical과 남의 alias", []model.TypedID{cA, idnAliasPod(t)}},
			{"alias 둘", []model.TypedID{aliasPort, idnAliasPod(t)}},
			{"세 asset", []model.TypedID{cA, cB, idnCanonicalC(t)}},
			{"같은 asset 여럿 + 다른 asset 하나", []model.TypedID{cA, aliasPort, aliasChassis, cB}},
		}
		for _, tc := range lists {
			t.Run(tc.name, func(t *testing.T) {
				got, err := r.ResolveAll(tc.ids)
				requireErrAmbiguous(t, err, "ResolveAll(모호)")
				assertZeroRef(t, got, "ResolveAll(모호)")
			})
		}
		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnSampleProbes(t)...), "ResolveAll(모호)")
	})

	// 미등록 원소와 모호성이 겹치는 경우의 우선순위는 스펙이 규정하지 않는다
	// (artifacts/discrepancy-notes-tests.md I-9). 확실한 부분만 단정한다:
	// 오류가 나고, zero value이며, 상태가 변하지 않는다.
	t.Run("미등록 + 모호가 겹치면 둘 중 한 계열의 오류", func(t *testing.T) {
		r := idnSampleResolver(t)
		before := resolverSnapshot(t, r, idnSampleProbes(t)...)

		got, err := r.ResolveAll([]model.TypedID{cA, cB, unregistered})
		if err == nil {
			t.Fatalf("오류를 기대했으나 nil을 받았다 (got %#v)", got)
		}
		if !errors.Is(err, identity.ErrNotRegistered) && !errors.Is(err, identity.ErrAmbiguous) {
			t.Errorf("ErrNotRegistered 또는 ErrAmbiguous를 기대했다 (err=%v)", err)
		}
		assertZeroRef(t, got, "ResolveAll(미등록 + 모호)")
		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnSampleProbes(t)...), "ResolveAll(미등록 + 모호)")
	})
}

// IDN-050: Assets()는 모든 asset의 AssetRef를 Key() 바이트 단위 오름차순으로
// 반환하고, 빈 Resolver에서는 길이 0이다.
func TestIDN050_AssetsAreSortedByKey(t *testing.T) {
	t.Run("빈 Resolver", func(t *testing.T) {
		r := identity.NewResolver()
		if got := r.Assets(); len(got) != 0 {
			t.Errorf("빈 Resolver의 Assets() 길이 = %d, want 0 (%#v)", len(got), got)
		}
	})

	t.Run("여러 asset", func(t *testing.T) {
		r := identity.NewResolver()
		// Key()가 뒤섞이도록 일부러 역순에 가깝게 등록한다.
		type reg struct {
			kind      model.AssetKind
			canonical model.TypedID
		}
		regs := []reg{
			{model.KindSwitchPort, idnAliasPort(t)},
			{model.KindPod, idnAliasPod(t)},
			{model.KindNICPort, idnCanonicalB(t)},
			{model.KindKubernetesNode, idnCanonicalC(t)},
			{model.KindNICPort, idnCanonicalA(t)},
			{model.KindPCIeFunction, idnUnregistered(t)},
		}
		wantKeys := make([]string, 0, len(regs))
		for _, g := range regs {
			mustRegisterAsset(t, r, g.kind, g.canonical)
			wantKeys = append(wantKeys, mustAssetRef(t, g.kind, g.canonical).Key())
		}
		sort.Strings(wantKeys)

		got := r.Assets()
		if len(got) != len(regs) {
			t.Fatalf("len(Assets()) = %d, want %d", len(got), len(regs))
		}
		gotKeys := make([]string, 0, len(got))
		for _, ref := range got {
			gotKeys = append(gotKeys, ref.Key())
		}
		if !sort.StringsAreSorted(gotKeys) {
			t.Errorf("Assets()가 Key() 오름차순이 아니다: %v", gotKeys)
		}
		for i := range wantKeys {
			if gotKeys[i] != wantKeys[i] {
				t.Errorf("Assets()[%d].Key() = %q, want %q (전체 %v)", i, gotKeys[i], wantKeys[i], gotKeys)
			}
		}
		// 각 원소는 3.4 형태를 만족한다.
		for _, ref := range got {
			if err := ref.Validate(); err != nil {
				t.Errorf("Assets() 원소의 Validate() = %v, want nil (%#v)", err, ref)
			}
		}
	})

	t.Run("alias가 있어도 Assets()에는 asset만 나온다", func(t *testing.T) {
		r := idnSampleResolver(t)
		got := r.Assets()
		if len(got) != 3 {
			t.Fatalf("len(Assets()) = %d, want 3", len(got))
		}
		wantAliases := map[string][]string{
			mustAssetRef(t, model.KindNICPort, idnCanonicalA(t)).Key(): {
				idnAliasChassis(t).String(), idnAliasPort(t).String(),
			},
			mustAssetRef(t, model.KindSwitchPort, idnCanonicalB(t)).Key():     {idnAliasPod(t).String()},
			mustAssetRef(t, model.KindKubernetesNode, idnCanonicalC(t)).Key(): {},
		}
		for _, ref := range got {
			want, ok := wantAliases[ref.Key()]
			if !ok {
				t.Errorf("예상치 못한 asset이 나왔다: %q", ref.Key())
				continue
			}
			gotAliases := aliasStrings(ref)
			if len(gotAliases) != len(want) {
				t.Errorf("%s의 Aliases = %v, want %v", ref.Key(), gotAliases, want)
				continue
			}
			for i := range want {
				if gotAliases[i] != want[i] {
					t.Errorf("%s의 Aliases[%d] = %q, want %q", ref.Key(), i, gotAliases[i], want[i])
				}
			}
		}
	})
}

// IDN-051 (edge): 같은 등록 호출 집합을 다른 순서로 적용한 두 Resolver의 모든
// 조회 결과는 Aliases 정렬 순서까지 동일하다.
func TestIDN051_RegistrationOrderDoesNotAffectQueries(t *testing.T) {
	cA, cB, cC := idnCanonicalA(t), idnCanonicalB(t), idnCanonicalC(t)
	aliasPort, aliasChassis, aliasPod := idnAliasPort(t), idnAliasChassis(t), idnAliasPod(t)

	// 각 순서는 모든 호출이 오류 없이 완료되도록 짜여 있다.
	orders := []func(t *testing.T, r *identity.Resolver){
		func(t *testing.T, r *identity.Resolver) {
			mustRegisterAsset(t, r, model.KindNICPort, cA)
			mustRegisterAsset(t, r, model.KindSwitchPort, cB)
			mustRegisterAsset(t, r, model.KindKubernetesNode, cC)
			mustRegisterAlias(t, r, cA, aliasPort)
			mustRegisterAlias(t, r, cA, aliasChassis)
			mustRegisterAlias(t, r, cB, aliasPod)
		},
		func(t *testing.T, r *identity.Resolver) {
			mustRegisterAsset(t, r, model.KindSwitchPort, cB)
			mustRegisterAlias(t, r, cB, aliasPod)
			mustRegisterAsset(t, r, model.KindNICPort, cA)
			mustRegisterAlias(t, r, cA, aliasChassis)
			mustRegisterAsset(t, r, model.KindKubernetesNode, cC)
			mustRegisterAlias(t, r, cA, aliasPort)
		},
		func(t *testing.T, r *identity.Resolver) {
			mustRegisterAsset(t, r, model.KindKubernetesNode, cC)
			mustRegisterAsset(t, r, model.KindNICPort, cA)
			mustRegisterAlias(t, r, cA, aliasPort)
			mustRegisterAsset(t, r, model.KindSwitchPort, cB)
			mustRegisterAlias(t, r, cB, aliasPod)
			mustRegisterAlias(t, r, cA, aliasChassis)
		},
	}

	probes := idnSampleProbes(t)
	reference := identity.NewResolver()
	orders[0](t, reference)
	want := resolverSnapshot(t, reference, probes...)

	for i, apply := range orders[1:] {
		r := identity.NewResolver()
		apply(t, r)
		got := resolverSnapshot(t, r, probes...)
		if got != want {
			t.Errorf("등록 순서 %d의 조회 결과가 달랐다\n--- 기준 ---\n%s--- 순서 %d ---\n%s", i+1, want, i+1, got)
		}

		// ResolveAll 결과도 같다.
		ids := []model.TypedID{cA, aliasPort, aliasChassis}
		refWant, errWant := reference.ResolveAll(ids)
		refGot, errGot := r.ResolveAll(ids)
		requireNoErr(t, errWant, "기준 Resolver의 ResolveAll")
		requireNoErr(t, errGot, "비교 Resolver의 ResolveAll")
		assertSameRef(t, refGot, refWant, "등록 순서가 다른 Resolver의 ResolveAll")
	}
}

// IDN-052 (edge): 조회 연산을 같은 인자로 몇 번을 어떤 순서로 호출해도 결과는
// 매번 동일하고 상태는 변하지 않는다.
func TestIDN052_RepeatedQueriesAreIdenticalAndPure(t *testing.T) {
	r := idnSampleResolver(t)
	probes := idnSampleProbes(t)
	before := resolverSnapshot(t, r, probes...)

	cA := idnCanonicalA(t)
	alias := idnAliasPort(t)
	ids := []model.TypedID{cA, alias}

	firstResolve := mustResolve(t, r, cA)
	firstResolveAll, err := r.ResolveAll(ids)
	requireNoErr(t, err, "ResolveAll(1회차)")
	firstAssets := r.Assets()
	firstParsed, err := identity.ParseCanonical(firstResolve.Canonical)
	requireNoErr(t, err, "ParseCanonical(1회차)")

	for i := 0; i < 3; i++ {
		// 순서를 섞어 호출한다.
		assets := r.Assets()
		if len(assets) != len(firstAssets) {
			t.Fatalf("%d회차: len(Assets()) = %d, want %d", i, len(assets), len(firstAssets))
		}
		for j := range assets {
			assertSameRef(t, assets[j], firstAssets[j], "반복 호출의 Assets() 원소")
		}

		gotAll, err := r.ResolveAll(ids)
		requireNoErr(t, err, "ResolveAll(반복)")
		assertSameRef(t, gotAll, firstResolveAll, "반복 호출의 ResolveAll")

		gotResolve := mustResolve(t, r, alias)
		assertSameRef(t, gotResolve, firstResolve, "반복 호출의 Resolve")

		gotParsed, err := identity.ParseCanonical(firstResolve.Canonical)
		requireNoErr(t, err, "ParseCanonical(반복)")
		if !gotParsed.Equal(firstParsed) {
			t.Errorf("반복 호출의 ParseCanonical 결과가 달랐다: %#v vs %#v", gotParsed, firstParsed)
		}

		// 오류 경로도 반복에 안정적이다.
		if _, err := r.Resolve(idnUnregistered(t)); !errors.Is(err, identity.ErrNotRegistered) {
			t.Errorf("%d회차: 미등록 해석의 오류 계열이 달라졌다: %v", i, err)
		}
	}

	assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "반복 조회")
}

// IDN-053 (edge): 충돌로 기각된 등록의 흔적은 어떤 결과에도 남지 않는다 —
// 충돌이 없었던 Resolver와 구별할 수 없다.
func TestIDN053_RejectedRegistrationsLeaveNoTrace(t *testing.T) {
	cA, cB, cC := idnCanonicalA(t), idnCanonicalB(t), idnCanonicalC(t)
	aliasPort, aliasChassis, aliasPod := idnAliasPort(t), idnAliasChassis(t), idnAliasPod(t)
	probes := idnSampleProbes(t)

	// 성공하는 등록 4건 — 두 Resolver가 공유한다.
	succeed := func(t *testing.T, r *identity.Resolver) {
		t.Helper()
		mustRegisterAsset(t, r, model.KindNICPort, cA)
		mustRegisterAsset(t, r, model.KindSwitchPort, cB)
		mustRegisterAlias(t, r, cA, aliasPort)
		mustRegisterAlias(t, r, cB, aliasPod)
	}

	clean := identity.NewResolver()
	succeed(t, clean)

	scarred := identity.NewResolver()
	succeed(t, scarred)
	// 기각되는 등록들 — IDN-021·023·024·031·032·035.
	requireConflict(t, scarred.RegisterAsset(model.KindPod, cA), "kind 충돌 (IDN-023)")
	requireConflict(t, scarred.RegisterAsset(model.KindPod, aliasPod), "alias를 canonical로 (IDN-024)")
	requireConflict(t, scarred.RegisterAlias(cB, aliasPort), "alias 충돌 (IDN-035)")
	requireErrInvalid(t, scarred.RegisterAsset(model.KindPod, model.TypedID{}), "무효 canonical (IDN-021)")
	requireErrInvalid(t, scarred.RegisterAlias(cA, model.TypedID{Namespace: "ns:콜론", Value: "v"}), "무효 alias (IDN-031)")
	requireErrNotRegistered(t, scarred.RegisterAlias(idnUnregistered(t), aliasChassis), "미등록 canonical (IDN-032)")

	if got, want := resolverSnapshot(t, scarred, probes...), resolverSnapshot(t, clean, probes...); got != want {
		t.Errorf("충돌을 겪은 Resolver가 구별되었다\n--- clean ---\n%s--- scarred ---\n%s", want, got)
	}

	// 이후의 유효한 등록도 같은 결과를 낸다.
	for _, r := range []*identity.Resolver{clean, scarred} {
		mustRegisterAsset(t, r, model.KindKubernetesNode, cC)
		mustRegisterAlias(t, r, cA, aliasChassis)
	}
	if got, want := resolverSnapshot(t, scarred, probes...), resolverSnapshot(t, clean, probes...); got != want {
		t.Errorf("충돌 이후의 등록에서 두 Resolver가 갈라졌다\n--- clean ---\n%s--- scarred ---\n%s", want, got)
	}
}

// IDN-054 (edge): 반환 값은 Resolver 내부 상태와 공유되지 않는다.
func TestIDN054_ReturnedValuesAreDefensivelyCopied(t *testing.T) {
	cA := idnCanonicalA(t)
	aliasPort, aliasChassis := idnAliasPort(t), idnAliasChassis(t)
	wantAliases := []string{aliasChassis.String(), aliasPort.String()}

	t.Run("Resolve가 돌려준 Aliases 슬라이스 변형", func(t *testing.T) {
		r := idnSampleResolver(t)
		before := resolverSnapshot(t, r, idnSampleProbes(t)...)

		ref := mustResolve(t, r, cA)
		if len(ref.Aliases) != 2 {
			t.Fatalf("len(Aliases) = %d, want 2", len(ref.Aliases))
		}
		// 슬라이스 원소 교체
		ref.Aliases[0] = model.TypedID{Namespace: "변조", Value: "변조"}
		// 원소 내부 필드 변형
		ref.Aliases[1].Value = "변조된 값"
		ref.Aliases[1].Namespace = "변조된 namespace"

		after := mustResolve(t, r, cA)
		assertRefShape(t, after, model.KindNICPort, cA, wantAliases, "변형 후 Resolve(canonical)")
		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnSampleProbes(t)...), "반환 값 변형")
	})

	t.Run("Assets가 돌려준 슬라이스 변형", func(t *testing.T) {
		r := idnSampleResolver(t)
		before := resolverSnapshot(t, r, idnSampleProbes(t)...)

		assets := r.Assets()
		if len(assets) != 3 {
			t.Fatalf("len(Assets()) = %d, want 3", len(assets))
		}
		for i := range assets {
			assets[i].Canonical = "변조:값"
			assets[i].Kind = model.KindContainer
			for j := range assets[i].Aliases {
				assets[i].Aliases[j] = model.TypedID{Namespace: "변조", Value: "변조"}
			}
		}
		assets[0] = model.AssetRef{}

		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnSampleProbes(t)...), "Assets() 반환 값 변형")
		assertRefShape(t, mustResolve(t, r, cA), model.KindNICPort, cA, wantAliases, "Assets() 변형 후 Resolve")
	})

	t.Run("ResolveAll이 돌려준 Aliases 변형", func(t *testing.T) {
		r := idnSampleResolver(t)
		before := resolverSnapshot(t, r, idnSampleProbes(t)...)

		ref, err := r.ResolveAll([]model.TypedID{cA, aliasPort})
		requireNoErr(t, err, "ResolveAll")
		for i := range ref.Aliases {
			ref.Aliases[i].Value = "변조"
		}

		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnSampleProbes(t)...), "ResolveAll 반환 값 변형")
	})

	t.Run("인자로 넘긴 TypedID의 Raw 변형", func(t *testing.T) {
		r := identity.NewResolver()
		raw := []byte{0x01, 0x02, 0x03}
		canonicalArg := model.TypedID{Namespace: cA.Namespace, Value: cA.Value, Raw: raw, Source: "sysfs"}
		aliasRaw := []byte{0x0a, 0x0b}
		aliasArg := model.TypedID{Namespace: aliasPort.Namespace, Value: aliasPort.Value, Raw: aliasRaw, Source: "lldp"}

		mustRegisterAsset(t, r, model.KindNICPort, canonicalArg)
		mustRegisterAlias(t, r, canonicalArg, aliasArg)

		before := resolverSnapshot(t, r, cA, aliasPort)

		// 호출 후에 호출자가 인자의 Raw를 변형한다.
		raw[0] = 0xff
		aliasRaw[1] = 0xff

		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, cA, aliasPort), "인자 Raw 변형")
		assertRefShape(t, mustResolve(t, r, cA), model.KindNICPort, cA,
			[]string{aliasPort.String()}, "인자 Raw 변형 후 Resolve")
	})

	t.Run("ResolveAll에 넘긴 슬라이스 변형", func(t *testing.T) {
		r := idnSampleResolver(t)
		before := resolverSnapshot(t, r, idnSampleProbes(t)...)

		ids := []model.TypedID{cA, aliasPort}
		got, err := r.ResolveAll(ids)
		requireNoErr(t, err, "ResolveAll")

		ids[0] = model.TypedID{Namespace: "변조", Value: "변조"}
		ids[1].Value = "변조"

		assertRefShape(t, got, model.KindNICPort, cA, wantAliases, "인자 변형 후 이미 반환된 AssetRef")
		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnSampleProbes(t)...), "ResolveAll 인자 변형")
	})
}
