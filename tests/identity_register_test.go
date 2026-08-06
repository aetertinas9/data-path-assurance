// identity_register_test.go — asset·alias 등록 (specs/identity/spec.md 3.4,
// IDN-020~024·030~035). 스펙이 "edge"로 표시한 기준을 최우선으로 다룬다.
package tests

import (
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// IDN-020: 등록된 적 없는 canonical을 등록하면 nil을 반환하고, 이후
// Resolve(canonical)이 Kind·Canonical이 맞고 Aliases가 빈 AssetRef를 낸다.
func TestIDN020_RegisterAssetThenResolveCanonical(t *testing.T) {
	kinds := []model.AssetKind{
		model.KindNICPort, model.KindSwitchPort, model.KindPod,
		model.KindKubernetesNode, model.KindPCIeFunction, model.KindTransceiver,
	}
	for _, kind := range kinds {
		t.Run(kind.String(), func(t *testing.T) {
			r := identity.NewResolver()
			c := idnCanonicalA(t)

			if err := r.RegisterAsset(kind, c); err != nil {
				t.Fatalf("RegisterAsset(%s) = %v, want nil", kind.String(), err)
			}

			ref := mustResolve(t, r, c)
			assertRefShape(t, ref, kind, c, nil, "Resolve(canonical)")

			// 등록된 asset은 Assets()에도 나타난다.
			all := r.Assets()
			if len(all) != 1 {
				t.Fatalf("len(Assets()) = %d, want 1", len(all))
			}
			assertRefShape(t, all[0], kind, c, nil, "Assets()[0]")
		})
	}
}

// IDN-020: 서로 다른 canonical은 독립적으로 등록된다.
func TestIDN020_RegisterAssetAcceptsDistinctCanonicals(t *testing.T) {
	r := identity.NewResolver()
	cA, cB, cC := idnCanonicalA(t), idnCanonicalB(t), idnCanonicalC(t)

	mustRegisterAsset(t, r, model.KindNICPort, cA)
	mustRegisterAsset(t, r, model.KindNICPort, cB)
	mustRegisterAsset(t, r, model.KindKubernetesNode, cC)

	assertRefShape(t, mustResolve(t, r, cA), model.KindNICPort, cA, nil, "Resolve(cA)")
	assertRefShape(t, mustResolve(t, r, cB), model.KindNICPort, cB, nil, "Resolve(cB)")
	assertRefShape(t, mustResolve(t, r, cC), model.KindKubernetesNode, cC, nil, "Resolve(cC)")

	if len(r.Assets()) != 3 {
		t.Errorf("len(Assets()) = %d, want 3", len(r.Assets()))
	}
}

// IDN-021 (edge): 무효한 kind(zero value 포함) 또는 3.1을 위반한 canonical은
// 상태를 바꾸지 않고 model.ErrInvalid 계열 오류다.
func TestIDN021_RegisterAssetRejectsInvalidKindOrCanonical(t *testing.T) {
	var zeroKind model.AssetKind

	t.Run("무효한 kind", func(t *testing.T) {
		kinds := []struct {
			name string
			kind model.AssetKind
		}{
			{"zero value", zeroKind},
			{"열거 밖 양수", model.AssetKind(outOfEnumA)},
			// MDL-070(e): 열거형 기반 타입은 부호 있는 정수 계열이다.
			{"열거 밖 음수", model.AssetKind(-1)},
		}
		for _, tc := range kinds {
			t.Run(tc.name, func(t *testing.T) {
				r := identity.NewResolver()
				c := idnCanonicalA(t)
				before := resolverSnapshot(t, r, c)

				requireErrInvalid(t, r.RegisterAsset(tc.kind, c), "RegisterAsset(무효 kind)")

				assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, c), "RegisterAsset(무효 kind)")
				_, err := r.Resolve(c)
				requireErrNotRegistered(t, err, "무효 등록 후 Resolve")
			})
		}
	})

	t.Run("3.1을 위반한 canonical", func(t *testing.T) {
		for _, tc := range invalidIdentities() {
			t.Run(tc.name, func(t *testing.T) {
				r := identity.NewResolver()
				other := idnCanonicalB(t)
				mustRegisterAsset(t, r, model.KindSwitchPort, other)
				before := resolverSnapshot(t, r, other)

				requireErrInvalid(t, r.RegisterAsset(model.KindNICPort, tc.id), "RegisterAsset(무효 canonical)")

				assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, other), "RegisterAsset(무효 canonical)")
			})
		}
	})

	// IDN-005: kind와 canonical이 둘 다 무효여도 판정은 유효성 오류다.
	t.Run("kind와 canonical 둘 다 무효", func(t *testing.T) {
		r := identity.NewResolver()
		before := resolverSnapshot(t, r)
		requireErrInvalid(t, r.RegisterAsset(zeroKind, model.TypedID{}), "RegisterAsset(둘 다 무효)")
		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r), "RegisterAsset(둘 다 무효)")
	})
}

// IDN-022 (edge): 같은 (kind, canonical) 재등록은 no-op nil이고 상태가 변하지
// 않는다 — canonical은 Equal이면 Raw·Source가 달라도 같은 identity다.
func TestIDN022_RegisterAssetIsIdempotent(t *testing.T) {
	r := identity.NewResolver()
	c := idnCanonicalA(t)
	aliasPort, aliasChassis := idnAliasPort(t), idnAliasChassis(t)

	mustRegisterAsset(t, r, model.KindNICPort, c)
	mustRegisterAlias(t, r, c, aliasPort)
	mustRegisterAlias(t, r, c, aliasChassis)

	probes := []model.TypedID{c, aliasPort, aliasChassis}
	before := resolverSnapshot(t, r, probes...)

	cases := []struct {
		name      string
		canonical model.TypedID
	}{
		{"같은 값", c},
		{"Raw만 다름", model.TypedID{Namespace: c.Namespace, Value: c.Value, Raw: []byte{0x01, 0x02}}},
		{"Source만 다름", model.TypedID{Namespace: c.Namespace, Value: c.Value, Source: "sysfs"}},
		{"Raw·Source 둘 다 다름", model.TypedID{
			Namespace: c.Namespace,
			Value:     c.Value,
			Raw:       []byte("다른 raw"),
			Source:    "gnmi",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.RegisterAsset(model.KindNICPort, tc.canonical); err != nil {
				t.Fatalf("멱등 재등록이 실패했다: %v", err)
			}
			assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "멱등 재등록")
		})
	}

	// 재등록을 여러 번 반복해도 alias 목록이 늘거나 줄지 않는다.
	for i := 0; i < 3; i++ {
		mustRegisterAsset(t, r, model.KindNICPort, c)
	}
	assertRefShape(t, mustResolve(t, r, c), model.KindNICPort, c,
		[]string{aliasChassis.String(), aliasPort.String()}, "반복 재등록 후 Resolve(canonical)")
	assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "반복 재등록")
}

// IDN-023 (edge): 같은 canonical을 다른 kind로 등록하면 상태를 바꾸지 않고
// *Conflict를 반환한다.
func TestIDN023_RegisterAssetWithDifferentKindConflicts(t *testing.T) {
	r := identity.NewResolver()
	c := idnCanonicalA(t)
	alias := idnAliasPort(t)

	mustRegisterAsset(t, r, model.KindNICPort, c)
	mustRegisterAlias(t, r, c, alias)

	probes := []model.TypedID{c, alias}
	before := resolverSnapshot(t, r, probes...)

	cases := []struct {
		name      string
		kind      model.AssetKind
		canonical model.TypedID
	}{
		{"다른 kind", model.KindPCIeFunction, c},
		{"또 다른 kind", model.KindPod, c},
		// Equal이면 Raw·Source가 달라도 같은 identity이므로 역시 충돌이다.
		{"다른 kind + Raw·Source 다름", model.KindSwitchPort, model.TypedID{
			Namespace: c.Namespace,
			Value:     c.Value,
			Raw:       []byte{0xff},
			Source:    "sysfs",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := r.RegisterAsset(tc.kind, tc.canonical)
			conflict := requireConflict(t, err, "RegisterAsset(kind 충돌)")

			// ID는 정규화된 c다.
			if !conflict.ID.Equal(c) {
				t.Errorf("Conflict.ID = %#v, want %s와 Equal", conflict.ID, c.String())
			}
			// Existing은 기존 asset의 AssetRef(3.4 형태)다 — alias를 포함한다.
			assertRefShape(t, conflict.Existing, model.KindNICPort, c,
				[]string{alias.String()}, "Conflict.Existing")
			// Claimed는 Kind가 새 kind이고 Aliases가 빈 AssetRef다.
			assertRefShape(t, conflict.Claimed, tc.kind, c, nil, "Conflict.Claimed")

			assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "kind 충돌")
		})
	}

	// 같은 충돌을 반복해도 같은 Conflict가 반복된다 (IDN-002·052).
	first := requireConflict(t, r.RegisterAsset(model.KindPod, c), "1회차")
	second := requireConflict(t, r.RegisterAsset(model.KindPod, c), "2회차")
	if first.ID.String() != second.ID.String() {
		t.Errorf("반복 호출의 Conflict.ID가 달랐다: %q vs %q", first.ID.String(), second.ID.String())
	}
	assertSameRef(t, second.Existing, first.Existing, "반복 호출의 Conflict.Existing")
	assertSameRef(t, second.Claimed, first.Claimed, "반복 호출의 Conflict.Claimed")
}

// IDN-024 (edge): 다른 asset의 alias를 canonical로 등록하는 것은 암묵적 병합이므로
// 허용되지 않는다 — kind가 같아도 충돌이다.
func TestIDN024_RegisterAssetOnExistingAliasConflicts(t *testing.T) {
	alias := idnAliasPort(t)

	cases := []struct {
		name string
		kind model.AssetKind
	}{
		{"기존 asset과 같은 kind", model.KindNICPort},
		{"기존 asset과 다른 kind", model.KindSwitchPort},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := identity.NewResolver()
			c := idnCanonicalA(t)
			mustRegisterAsset(t, r, model.KindNICPort, c)
			mustRegisterAlias(t, r, c, alias)

			probes := []model.TypedID{c, alias}
			before := resolverSnapshot(t, r, probes...)

			conflict := requireConflict(t, r.RegisterAsset(tc.kind, alias), "RegisterAsset(alias를 canonical로)")

			if !conflict.ID.Equal(alias) {
				t.Errorf("Conflict.ID = %#v, want %s와 Equal", conflict.ID, alias.String())
			}
			assertRefShape(t, conflict.Existing, model.KindNICPort, c,
				[]string{alias.String()}, "Conflict.Existing")
			assertRefShape(t, conflict.Claimed, tc.kind, alias, nil, "Conflict.Claimed")

			assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "alias를 canonical로 등록")
			// 기존 결합이 유지된다.
			assertRefShape(t, mustResolve(t, r, alias), model.KindNICPort, c,
				[]string{alias.String()}, "충돌 후 Resolve(alias)")
		})
	}
}

// IDN-030: 등록된 asset에 새 alias를 결합하면 canonical과 alias가 같은 AssetRef로
// 해석되고, Aliases에 정규화된 alias가 포함된다.
func TestIDN030_RegisterAliasBindsAliasToAsset(t *testing.T) {
	r := identity.NewResolver()
	c := idnCanonicalA(t)
	alias := idnAliasPort(t)

	mustRegisterAsset(t, r, model.KindNICPort, c)
	if err := r.RegisterAlias(c, alias); err != nil {
		t.Fatalf("RegisterAlias = %v, want nil", err)
	}

	viaCanonical := mustResolve(t, r, c)
	viaAlias := mustResolve(t, r, alias)

	assertRefShape(t, viaCanonical, model.KindNICPort, c, []string{alias.String()}, "Resolve(canonical)")
	assertRefShape(t, viaAlias, model.KindNICPort, c, []string{alias.String()}, "Resolve(alias)")
	assertSameRef(t, viaAlias, viaCanonical, "alias 해석 결과와 canonical 해석 결과")
}

// IDN-030 + 3.4: alias가 여러 개면 String() 바이트 단위 오름차순으로 정렬된다.
// 등록 순서와 무관하다 (IDN-051).
func TestIDN030_AliasesAreSortedByString(t *testing.T) {
	c := idnCanonicalC(t)
	openConfig := mustTypedID(t, string(model.NamespaceOpenConfigComponent), "Ethernet1/1")

	// String() 바이트 단위 오름차순:
	// kubernetes-pod-uid… < lldp-chassis-id… < lldp-port-id… < openconfig-component…
	sorted := []string{
		idnAliasPod(t).String(),
		idnAliasChassis(t).String(),
		idnAliasPort(t).String(),
		openConfig.String(),
	}

	orders := [][]model.TypedID{
		{idnAliasPod(t), idnAliasChassis(t), idnAliasPort(t), openConfig},
		{openConfig, idnAliasPort(t), idnAliasChassis(t), idnAliasPod(t)},
		{idnAliasPort(t), idnAliasPod(t), openConfig, idnAliasChassis(t)},
	}
	for _, order := range orders {
		t.Run("등록 순서 "+order[0].String(), func(t *testing.T) {
			r := identity.NewResolver()
			mustRegisterAsset(t, r, model.KindKubernetesNode, c)
			for _, a := range order {
				mustRegisterAlias(t, r, c, a)
			}
			assertRefShape(t, mustResolve(t, r, c), model.KindKubernetesNode, c, sorted,
				"Resolve(canonical) — alias 등록 순서를 바꿔도 같은 정렬")
		})
	}
}

// IDN-031 (edge): canonical 또는 alias 인자가 3.1을 위반하면 상태를 바꾸지 않고
// model.ErrInvalid 계열 오류다. 이 판정은 등록 여부 확인보다 먼저다(IDN-005).
func TestIDN031_RegisterAliasRejectsInvalidArguments(t *testing.T) {
	t.Run("alias 인자가 무효", func(t *testing.T) {
		for _, tc := range invalidIdentities() {
			t.Run(tc.name, func(t *testing.T) {
				r := identity.NewResolver()
				c := idnCanonicalA(t)
				mustRegisterAsset(t, r, model.KindNICPort, c)
				before := resolverSnapshot(t, r, c)

				requireErrInvalid(t, r.RegisterAlias(c, tc.id), "RegisterAlias(무효 alias)")
				assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, c), "RegisterAlias(무효 alias)")
			})
		}
	})

	t.Run("canonical 인자가 무효", func(t *testing.T) {
		for _, tc := range invalidIdentities() {
			t.Run(tc.name, func(t *testing.T) {
				r := identity.NewResolver()
				c := idnCanonicalA(t)
				alias := idnAliasPort(t)
				mustRegisterAsset(t, r, model.KindNICPort, c)
				before := resolverSnapshot(t, r, c, alias)

				requireErrInvalid(t, r.RegisterAlias(tc.id, alias), "RegisterAlias(무효 canonical)")
				assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, c, alias), "RegisterAlias(무효 canonical)")

				// alias가 결합되지 않았다.
				_, err := r.Resolve(alias)
				requireErrNotRegistered(t, err, "무효 등록 후 Resolve(alias)")
			})
		}
	})

	// IDN-005: 유효성 위반이 "등록 여부" 위반보다 먼저 판정된다.
	t.Run("등록되지 않은 canonical + 무효한 alias는 ErrInvalid", func(t *testing.T) {
		r := identity.NewResolver()
		unregistered := idnUnregistered(t)
		before := resolverSnapshot(t, r)

		err := r.RegisterAlias(unregistered, model.TypedID{})
		requireErrInvalid(t, err, "RegisterAlias(미등록 canonical, 무효 alias)")
		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r), "우선순위 판정")
	})
}

// IDN-032 (edge): canonical 인자가 등록된 asset의 canonical identity가 아니면
// ErrNotRegistered다 — alias를 경유한 간접 결합은 허용되지 않는다.
func TestIDN032_RegisterAliasRequiresRegisteredCanonical(t *testing.T) {
	t.Run("아예 등록되지 않은 canonical", func(t *testing.T) {
		r := identity.NewResolver()
		mustRegisterAsset(t, r, model.KindNICPort, idnCanonicalA(t))

		unregistered := idnUnregistered(t)
		newAlias := idnAliasPort(t)
		before := resolverSnapshot(t, r, idnCanonicalA(t), newAlias)

		err := r.RegisterAlias(unregistered, newAlias)
		requireErrNotRegistered(t, err, "RegisterAlias(미등록 canonical)")
		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, idnCanonicalA(t), newAlias),
			"RegisterAlias(미등록 canonical)")
	})

	t.Run("어떤 asset의 alias로만 등록된 identity", func(t *testing.T) {
		r := identity.NewResolver()
		c := idnCanonicalA(t)
		alias := idnAliasPort(t)
		mustRegisterAsset(t, r, model.KindNICPort, c)
		mustRegisterAlias(t, r, c, alias)

		newAlias := idnAliasChassis(t)
		probes := []model.TypedID{c, alias, newAlias}
		before := resolverSnapshot(t, r, probes...)

		// alias를 canonical 자리에 넘기면 간접 결합이 되므로 거부된다.
		err := r.RegisterAlias(alias, newAlias)
		requireErrNotRegistered(t, err, "RegisterAlias(alias를 canonical 자리에)")
		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "간접 결합 거부")

		_, err = r.Resolve(newAlias)
		requireErrNotRegistered(t, err, "거부 후 Resolve(newAlias)")
	})

	t.Run("빈 Resolver", func(t *testing.T) {
		r := identity.NewResolver()
		before := resolverSnapshot(t, r)
		err := r.RegisterAlias(idnCanonicalA(t), idnAliasPort(t))
		requireErrNotRegistered(t, err, "RegisterAlias(빈 Resolver)")
		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r), "빈 Resolver")
		if len(r.Assets()) != 0 {
			t.Errorf("len(Assets()) = %d, want 0", len(r.Assets()))
		}
	})
}

// IDN-033 (edge): 이미 그 asset으로 해석되는 alias의 재등록은 no-op nil이고,
// Aliases에 alias는 한 번만 나타난다.
func TestIDN033_RegisterAliasIsIdempotent(t *testing.T) {
	r := identity.NewResolver()
	c := idnCanonicalA(t)
	alias := idnAliasPort(t)

	mustRegisterAsset(t, r, model.KindNICPort, c)
	mustRegisterAlias(t, r, c, alias)

	probes := []model.TypedID{c, alias}
	before := resolverSnapshot(t, r, probes...)

	cases := []struct {
		name  string
		alias model.TypedID
	}{
		{"같은 값", alias},
		{"Raw만 다름", model.TypedID{Namespace: alias.Namespace, Value: alias.Value, Raw: []byte{0x0a}}},
		{"Source만 다름", model.TypedID{Namespace: alias.Namespace, Value: alias.Value, Source: "lldp"}},
		{"Raw·Source 둘 다 다름", model.TypedID{
			Namespace: alias.Namespace,
			Value:     alias.Value,
			Raw:       []byte("다른 raw"),
			Source:    "gnmi",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.RegisterAlias(c, tc.alias); err != nil {
				t.Fatalf("멱등 alias 재등록이 실패했다: %v", err)
			}
			assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "멱등 alias 재등록")

			ref := mustResolve(t, r, c)
			assertRefShape(t, ref, model.KindNICPort, c, []string{alias.String()}, "재등록 후 Resolve(canonical)")
		})
	}

	// canonical 인자로 Raw·Source가 다른 값을 써도 같은 asset을 가리킨다.
	canonicalCopy := model.TypedID{Namespace: c.Namespace, Value: c.Value, Source: "sysfs"}
	if err := r.RegisterAlias(canonicalCopy, alias); err != nil {
		t.Errorf("canonical 사본으로 멱등 재등록이 실패했다: %v", err)
	}
	assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "canonical 사본 재등록")
}

// IDN-034 (edge): 자기 자신을 alias로 등록하면 nil이고 상태는 변하지 않는다 —
// canonical identity는 Aliases에 나타나지 않는다.
func TestIDN034_RegisterAliasWithCanonicalItselfIsNoOp(t *testing.T) {
	r := identity.NewResolver()
	c := idnCanonicalA(t)
	alias := idnAliasPort(t)

	mustRegisterAsset(t, r, model.KindNICPort, c)

	probes := []model.TypedID{c, alias}
	before := resolverSnapshot(t, r, probes...)

	if err := r.RegisterAlias(c, c); err != nil {
		t.Fatalf("RegisterAlias(c, c) = %v, want nil", err)
	}
	assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "자기 자신 alias 등록")
	assertRefShape(t, mustResolve(t, r, c), model.KindNICPort, c, nil, "자기 자신 alias 등록 후 Resolve")

	// Raw·Source만 다른 사본으로 자기 자신을 등록해도 같다.
	copyOfC := model.TypedID{Namespace: c.Namespace, Value: c.Value, Raw: []byte{0x01}, Source: "sysfs"}
	if err := r.RegisterAlias(c, copyOfC); err != nil {
		t.Errorf("RegisterAlias(c, c의 사본) = %v, want nil", err)
	}
	if err := r.RegisterAlias(copyOfC, c); err != nil {
		t.Errorf("RegisterAlias(c의 사본, c) = %v, want nil", err)
	}
	assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "사본으로 자기 자신 alias 등록")

	// alias가 있는 asset에서도 canonical 자신은 Aliases에 들어가지 않는다.
	mustRegisterAlias(t, r, c, alias)
	mustRegisterAlias(t, r, c, c)
	assertRefShape(t, mustResolve(t, r, c), model.KindNICPort, c, []string{alias.String()},
		"alias가 있는 상태에서 자기 자신 alias 등록 후 Resolve")
}

// IDN-035 (edge): 한 alias가 두 canonical에 결합되려 하면 자동 병합하지 않고
// *Conflict를 반환하며, 기존 결합이 유지된다.
func TestIDN035_AliasClaimedByTwoAssetsConflicts(t *testing.T) {
	t.Run("a가 X의 alias인 경우", func(t *testing.T) {
		r := identity.NewResolver()
		cX, cY := idnCanonicalA(t), idnCanonicalB(t)
		a := idnAliasPort(t)

		mustRegisterAsset(t, r, model.KindNICPort, cX)
		mustRegisterAlias(t, r, cX, a)
		mustRegisterAsset(t, r, model.KindSwitchPort, cY)
		mustRegisterAlias(t, r, cY, idnAliasChassis(t))

		wantExisting := mustResolve(t, r, cX)
		wantClaimed := mustResolve(t, r, cY)

		probes := []model.TypedID{cX, cY, a, idnAliasChassis(t)}
		before := resolverSnapshot(t, r, probes...)

		conflict := requireConflict(t, r.RegisterAlias(cY, a), "RegisterAlias(다른 asset이 주장)")

		if !conflict.ID.Equal(a) {
			t.Errorf("Conflict.ID = %#v, want %s와 Equal", conflict.ID, a.String())
		}
		assertSameRef(t, conflict.Existing, wantExisting, "Conflict.Existing (X의 AssetRef)")
		assertSameRef(t, conflict.Claimed, wantClaimed, "Conflict.Claimed (Y의 AssetRef)")
		// 기각된 등록은 Claimed에 반영되지 않는다.
		for _, s := range aliasStrings(conflict.Claimed) {
			if s == a.String() {
				t.Errorf("기각된 alias %q가 Claimed.Aliases에 들어 있다", s)
			}
		}
		requireNoErr(t, conflict.Existing.Validate(), "Conflict.Existing.Validate()")
		requireNoErr(t, conflict.Claimed.Validate(), "Conflict.Claimed.Validate()")

		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "alias 충돌")

		// 기존 결합이 유지된다 — Resolve(a)는 여전히 X다.
		assertSameRef(t, mustResolve(t, r, a), wantExisting, "충돌 후 Resolve(a)")

		// 같은 호출을 반복하면 같은 Conflict가 반복된다.
		again := requireConflict(t, r.RegisterAlias(cY, a), "반복 호출")
		if again.ID.String() != conflict.ID.String() {
			t.Errorf("반복 호출의 Conflict.ID가 달랐다: %q vs %q", again.ID.String(), conflict.ID.String())
		}
		assertSameRef(t, again.Existing, conflict.Existing, "반복 호출의 Conflict.Existing")
		assertSameRef(t, again.Claimed, conflict.Claimed, "반복 호출의 Conflict.Claimed")
	})

	t.Run("a가 X의 canonical인 경우", func(t *testing.T) {
		r := identity.NewResolver()
		cX, cY := idnCanonicalA(t), idnCanonicalB(t)

		mustRegisterAsset(t, r, model.KindNICPort, cX)
		mustRegisterAsset(t, r, model.KindSwitchPort, cY)

		wantExisting := mustResolve(t, r, cX)
		wantClaimed := mustResolve(t, r, cY)

		probes := []model.TypedID{cX, cY}
		before := resolverSnapshot(t, r, probes...)

		conflict := requireConflict(t, r.RegisterAlias(cY, cX), "RegisterAlias(Y, X의 canonical)")

		if !conflict.ID.Equal(cX) {
			t.Errorf("Conflict.ID = %#v, want %s와 Equal", conflict.ID, cX.String())
		}
		assertSameRef(t, conflict.Existing, wantExisting, "Conflict.Existing")
		assertSameRef(t, conflict.Claimed, wantClaimed, "Conflict.Claimed")

		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "canonical을 남의 alias로")
		assertSameRef(t, mustResolve(t, r, cX), wantExisting, "충돌 후 Resolve(cX)")
	})

	t.Run("Raw·Source만 다른 사본으로 주장해도 충돌", func(t *testing.T) {
		r := identity.NewResolver()
		cX, cY := idnCanonicalA(t), idnCanonicalB(t)
		a := idnAliasPort(t)

		mustRegisterAsset(t, r, model.KindNICPort, cX)
		mustRegisterAlias(t, r, cX, a)
		mustRegisterAsset(t, r, model.KindSwitchPort, cY)

		probes := []model.TypedID{cX, cY, a}
		before := resolverSnapshot(t, r, probes...)

		claimCopy := model.TypedID{Namespace: a.Namespace, Value: a.Value, Raw: []byte{0x07}, Source: "lldp"}
		conflict := requireConflict(t, r.RegisterAlias(cY, claimCopy), "RegisterAlias(사본으로 주장)")

		if !conflict.ID.Equal(a) {
			t.Errorf("Conflict.ID = %#v, want %s와 Equal", conflict.ID, a.String())
		}
		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "사본 주장 충돌")
	})
}
