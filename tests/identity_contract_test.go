// identity_contract_test.go — internal/identity의 공통 규약
// (specs/identity/spec.md 3.1·3.3, IDN-002~005).
//
// IDN-001(표준 라이브러리와 pkg/model 외 import 금지)은 블랙박스 테스트의 사정
// 범위 밖이다 — 스펙 5절이 `make arch-check`와 verifier의 `go list -deps`에
// 배정한다. pkg/model의 MDL-001·110·111과 같은 취급이다.
package tests

import (
	"errors"
	"strings"
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// IDN-002: 같은 Resolver 상태와 같은 인자로 같은 함수·메서드를 두 번 호출하면
// 두 결과는 동일하다 (시계·난수·환경·I/O 비의존).
func TestIDN002_OperationsAreDeterministic(t *testing.T) {
	build := func(t *testing.T) *identity.Resolver {
		t.Helper()
		r := identity.NewResolver()
		mustRegisterAsset(t, r, model.KindNICPort, idnCanonicalA(t))
		mustRegisterAsset(t, r, model.KindSwitchPort, idnCanonicalB(t))
		mustRegisterAlias(t, r, idnCanonicalA(t), idnAliasPort(t))
		mustRegisterAlias(t, r, idnCanonicalA(t), idnAliasChassis(t))
		return r
	}

	// 같은 호출 열로 만든 두 Resolver는 구별되지 않는다.
	first, second := build(t), build(t)
	probes := idnSampleProbes(t)
	if a, b := resolverSnapshot(t, first, probes...), resolverSnapshot(t, second, probes...); a != b {
		t.Errorf("같은 호출 열의 두 Resolver가 달랐다\n--- 1 ---\n%s--- 2 ---\n%s", a, b)
	}

	t.Run("Resolve", func(t *testing.T) {
		a := mustResolve(t, first, idnCanonicalA(t))
		b := mustResolve(t, first, idnCanonicalA(t))
		assertSameRef(t, b, a, "Resolve 2회차")
	})

	t.Run("ResolveAll", func(t *testing.T) {
		ids := []model.TypedID{idnCanonicalA(t), idnAliasPort(t)}
		a, errA := first.ResolveAll(ids)
		b, errB := first.ResolveAll(ids)
		requireNoErr(t, errA, "ResolveAll(1회차)")
		requireNoErr(t, errB, "ResolveAll(2회차)")
		assertSameRef(t, b, a, "ResolveAll 2회차")
	})

	t.Run("Assets", func(t *testing.T) {
		a, b := first.Assets(), first.Assets()
		if len(a) != len(b) {
			t.Fatalf("len(Assets()) = %d vs %d", len(a), len(b))
		}
		for i := range a {
			assertSameRef(t, b[i], a[i], "Assets() 2회차")
		}
	})

	t.Run("ParseCanonical", func(t *testing.T) {
		a, errA := identity.ParseCanonical(canonicalString)
		b, errB := identity.ParseCanonical(canonicalString)
		requireNoErr(t, errA, "ParseCanonical(1회차)")
		requireNoErr(t, errB, "ParseCanonical(2회차)")
		if !a.Equal(b) || a.String() != b.String() {
			t.Errorf("두 호출의 결과가 달랐다: %#v vs %#v", a, b)
		}
	})

	t.Run("NewConflictFinding", func(t *testing.T) {
		c := idnValidConflict(t)
		a, errA := identity.NewConflictFinding(c, idnFindingID, idnConflictEvidence(), tFirstSeen)
		b, errB := identity.NewConflictFinding(c, idnFindingID, idnConflictEvidence(), tFirstSeen)
		requireNoErr(t, errA, "NewConflictFinding(1회차)")
		requireNoErr(t, errB, "NewConflictFinding(2회차)")
		if a.Explanation != b.Explanation || a.SuggestedStep != b.SuggestedStep {
			t.Errorf("문면이 호출마다 달랐다:\n%q / %q\n%q / %q",
				a.Explanation, a.SuggestedStep, b.Explanation, b.SuggestedStep)
		}
	})

	t.Run("오류 경로도 결정론적", func(t *testing.T) {
		_, errA := first.Resolve(idnUnregistered(t))
		_, errB := first.Resolve(idnUnregistered(t))
		requireErrNotRegistered(t, errA, "Resolve(1회차)")
		requireErrNotRegistered(t, errB, "Resolve(2회차)")
		if errA.Error() != errB.Error() {
			t.Errorf("오류 메시지가 호출마다 달랐다: %q vs %q", errA.Error(), errB.Error())
		}

		_, errC := identity.ParseCanonical("콜론없음")
		_, errD := identity.ParseCanonical("콜론없음")
		requireErrInvalid(t, errC, "ParseCanonical(1회차)")
		requireErrInvalid(t, errD, "ParseCanonical(2회차)")
		if errC.Error() != errD.Error() {
			t.Errorf("오류 메시지가 호출마다 달랐다: %q vs %q", errC.Error(), errD.Error())
		}
	})
}

// IDN-002: 어떤 공개 함수·메서드도 panic하지 않는다 —
// zero value·무효 입력·nil 슬라이스·극단적인 문자열에서도 마찬가지다.
// (zero value Resolver{}의 동작은 계약이 아니므로 NewResolver만 쓴다.)
func TestIDN002_PublicAPIDoesNotPanic(t *testing.T) {
	var zeroID model.TypedID
	var zeroKind model.AssetKind

	mustNotPanic(t, "ParseCanonical(극단 입력)", func() {
		inputs := []string{
			"", ":", "::", ":::", "a:", ":b", "콜론없음", canonicalString,
			"\x00:\x00", "\t:\n", " : ", strings.Repeat("a", 4096),
			strings.Repeat("a:", 2048), strings.Repeat(":", 512),
		}
		for _, s := range inputs {
			_, _ = identity.ParseCanonical(s)
		}
	})

	mustNotPanic(t, "빈 Resolver의 조회", func() {
		r := identity.NewResolver()
		_ = r.Assets()
		_, _ = r.Resolve(zeroID)
		_, _ = r.Resolve(idnCanonicalA(t))
		_, _ = r.ResolveAll(nil)
		_, _ = r.ResolveAll([]model.TypedID{})
		_, _ = r.ResolveAll([]model.TypedID{zeroID})
		_, _ = r.ResolveAll([]model.TypedID{{Namespace: "a:b", Value: "c"}})
	})

	mustNotPanic(t, "무효 인자를 받은 등록", func() {
		r := identity.NewResolver()
		_ = r.RegisterAsset(zeroKind, zeroID)
		_ = r.RegisterAsset(model.KindNICPort, zeroID)
		_ = r.RegisterAsset(model.AssetKind(outOfEnumA), idnCanonicalA(t))
		_ = r.RegisterAlias(zeroID, zeroID)
		_ = r.RegisterAlias(idnCanonicalA(t), zeroID)
		_ = r.RegisterAlias(zeroID, idnCanonicalA(t))
	})

	mustNotPanic(t, "충돌을 겪은 Resolver의 조회", func() {
		r := identity.NewResolver()
		_ = r.RegisterAsset(model.KindNICPort, idnCanonicalA(t))
		_ = r.RegisterAsset(model.KindPod, idnCanonicalA(t))
		_ = r.Assets()
		_, _ = r.Resolve(idnCanonicalA(t))
	})

	mustNotPanic(t, "NewConflictFinding(무효 인자)", func() {
		_, _ = identity.NewConflictFinding(identity.Conflict{}, "", nil, zeroTime)
		_, _ = identity.NewConflictFinding(identity.Conflict{
			ID:       zeroID,
			Existing: model.AssetRef{},
			Claimed:  model.AssetRef{},
		}, idnFindingID, []model.EvidenceRef{{}}, tFirstSeen)
	})

	mustNotPanic(t, "Conflict의 메서드", func() {
		var zeroConflict identity.Conflict
		_ = zeroConflict.Error()
		_ = zeroConflict.Unwrap()

		c := idnValidConflict(t)
		_ = c.Error()
		_ = c.Unwrap()
	})
}

// IDN-003: 3.1의 유효성을 위반한 TypedID 인자는 어떤 연산에서도 상태를 바꾸지
// 않고 zero value와 model.ErrInvalid 계열 오류를 낸다.
func TestIDN003_AllOperationsRejectInvalidIdentity(t *testing.T) {
	for _, tc := range invalidIdentities() {
		t.Run(tc.name, func(t *testing.T) {
			r := idnSampleResolver(t)
			probes := idnSampleProbes(t)
			before := resolverSnapshot(t, r, probes...)

			requireErrInvalid(t, r.RegisterAsset(model.KindPod, tc.id), "RegisterAsset")
			requireErrInvalid(t, r.RegisterAlias(idnCanonicalA(t), tc.id), "RegisterAlias(alias 자리)")
			requireErrInvalid(t, r.RegisterAlias(tc.id, idnUnregistered(t)), "RegisterAlias(canonical 자리)")

			gotRef, err := r.Resolve(tc.id)
			requireErrInvalid(t, err, "Resolve")
			assertZeroRef(t, gotRef, "Resolve")

			gotAll, err := r.ResolveAll([]model.TypedID{tc.id})
			requireErrInvalid(t, err, "ResolveAll(원소 1개)")
			assertZeroRef(t, gotAll, "ResolveAll(원소 1개)")

			gotMixed, err := r.ResolveAll([]model.TypedID{idnCanonicalA(t), tc.id})
			requireErrInvalid(t, err, "ResolveAll(유효 원소와 혼합)")
			assertZeroRef(t, gotMixed, "ResolveAll(유효 원소와 혼합)")

			assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "무효 인자 연산")
		})
	}
}

// IDN-004 (edge): 대소문자·앞뒤 공백만 다른 identity는 서로 다른 identity다 —
// 각각 독립적으로 등록·해석되고, 서로 다른 asset에 결합해도 충돌이 아니다.
func TestIDN004_CaseAndWhitespaceMakeDistinctIdentities(t *testing.T) {
	variants := []idnCase{
		{"소문자 namespace", mustTypedID(t, "pci-bdf", canonicalValue)},
		{"대문자 namespace", mustTypedID(t, "PCI-BDF", canonicalValue)},
		{"대문자 value", mustTypedID(t, "pci-bdf", "0000:AF:00.0")},
		{"value 앞 공백", mustTypedID(t, "pci-bdf", " "+canonicalValue)},
		{"value 뒤 공백", mustTypedID(t, "pci-bdf", canonicalValue+" ")},
		{"namespace 앞 공백", mustTypedID(t, " pci-bdf", canonicalValue)},
	}

	t.Run("각각 독립적인 asset으로 등록된다", func(t *testing.T) {
		r := identity.NewResolver()
		for _, tc := range variants {
			if err := r.RegisterAsset(model.KindNICPort, tc.id); err != nil {
				t.Fatalf("%s: RegisterAsset = %v, want nil (서로 다른 identity여야 한다)", tc.name, err)
			}
		}
		if got := len(r.Assets()); got != len(variants) {
			t.Errorf("len(Assets()) = %d, want %d — 표기만 다른 identity가 병합되었다", got, len(variants))
		}
		for _, tc := range variants {
			ref := mustResolve(t, r, tc.id)
			if ref.Canonical != tc.id.String() {
				t.Errorf("%s: Canonical = %q, want %q", tc.name, ref.Canonical, tc.id.String())
			}
		}
	})

	t.Run("서로 다른 asset의 alias로 결합해도 충돌이 아니다", func(t *testing.T) {
		r := identity.NewResolver()
		cA, cB := idnCanonicalA(t), idnCanonicalB(t)
		mustRegisterAsset(t, r, model.KindNICPort, cA)
		mustRegisterAsset(t, r, model.KindSwitchPort, cB)

		lower := mustTypedID(t, string(model.NamespaceLLDPPortID), "ethernet1/1")
		upper := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
		padded := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1 ")

		mustRegisterAlias(t, r, cA, lower)
		mustRegisterAlias(t, r, cB, upper)
		mustRegisterAlias(t, r, cB, padded)

		if got := mustResolve(t, r, lower); got.Canonical != cA.String() {
			t.Errorf("소문자 alias가 %q로 해석되었다, want %q", got.Canonical, cA.String())
		}
		for _, id := range []model.TypedID{upper, padded} {
			if got := mustResolve(t, r, id); got.Canonical != cB.String() {
				t.Errorf("%q가 %q로 해석되었다, want %q", id.String(), got.Canonical, cB.String())
			}
		}
	})

	t.Run("공백만으로 된 Namespace·Value도 유효하다", func(t *testing.T) {
		r := identity.NewResolver()
		blanks := []idnCase{
			{"공백 한 칸", mustTypedID(t, " ", " ")},
			{"공백 두 칸", mustTypedID(t, "  ", "  ")},
			{"탭과 개행", mustTypedID(t, "\t", "\n")},
		}
		for _, tc := range blanks {
			if err := r.RegisterAsset(model.KindPod, tc.id); err != nil {
				t.Fatalf("%s: RegisterAsset = %v, want nil (길이 0만 금지)", tc.name, err)
			}
		}
		if got := len(r.Assets()); got != len(blanks) {
			t.Errorf("len(Assets()) = %d, want %d", got, len(blanks))
		}
		for _, tc := range blanks {
			ref := mustResolve(t, r, tc.id)
			if ref.Canonical != tc.id.String() {
				t.Errorf("%s: Canonical = %q, want %q", tc.name, ref.Canonical, tc.id.String())
			}
		}
	})

	t.Run("ResolveAll에서도 서로 다른 asset이면 모호하다", func(t *testing.T) {
		r := identity.NewResolver()
		lower := mustTypedID(t, "pci-bdf", canonicalValue)
		upper := mustTypedID(t, "PCI-BDF", canonicalValue)
		mustRegisterAsset(t, r, model.KindNICPort, lower)
		mustRegisterAsset(t, r, model.KindNICPort, upper)

		_, err := r.ResolveAll([]model.TypedID{lower, upper})
		requireErrAmbiguous(t, err, "ResolveAll(대소문자만 다른 두 asset)")
	})
}

// IDN-005: 여러 위반이 겹치면 인자 유효성 → 등록 여부 → 충돌 순으로 판정한다.
func TestIDN005_ErrorPriority(t *testing.T) {
	t.Run("유효성이 등록 여부보다 먼저", func(t *testing.T) {
		r := idnSampleResolver(t)

		// canonical이 등록되지 않았고 alias도 무효하다 → ErrInvalid.
		err := r.RegisterAlias(idnUnregistered(t), model.TypedID{})
		requireErrInvalid(t, err, "RegisterAlias(미등록 + 무효)")
		if errors.Is(err, identity.ErrNotRegistered) {
			t.Errorf("유효성 위반이 ErrNotRegistered로도 판정되었다 (err=%v)", err)
		}

		// 미등록 identity를 무효한 형태로 해석 → ErrInvalid.
		_, err = r.Resolve(model.TypedID{Namespace: "a:b", Value: "없는 값"})
		requireErrInvalid(t, err, "Resolve(무효 + 미등록)")
		if errors.Is(err, identity.ErrNotRegistered) {
			t.Errorf("유효성 위반이 ErrNotRegistered로도 판정되었다 (err=%v)", err)
		}
	})

	t.Run("유효성이 충돌보다 먼저", func(t *testing.T) {
		r := idnSampleResolver(t)
		probes := idnSampleProbes(t)
		before := resolverSnapshot(t, r, probes...)

		// 이미 다른 asset의 alias인 identity를 무효한 kind로 등록 → ErrInvalid.
		var zeroKind model.AssetKind
		err := r.RegisterAsset(zeroKind, idnAliasPort(t))
		requireErrInvalid(t, err, "RegisterAsset(무효 kind + alias 충돌 소지)")
		if errors.Is(err, identity.ErrConflict) {
			t.Errorf("유효성 위반이 ErrConflict로도 판정되었다 (err=%v)", err)
		}

		// 이미 X로 해석되는 alias를 무효한 canonical로 주장 → ErrInvalid.
		err = r.RegisterAlias(model.TypedID{Namespace: "ns:콜론", Value: "v"}, idnAliasPort(t))
		requireErrInvalid(t, err, "RegisterAlias(무효 canonical + 충돌 소지)")
		if errors.Is(err, identity.ErrConflict) {
			t.Errorf("유효성 위반이 ErrConflict로도 판정되었다 (err=%v)", err)
		}

		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "우선순위 판정")
	})

	t.Run("등록 여부가 충돌보다 먼저", func(t *testing.T) {
		r := idnSampleResolver(t)
		probes := idnSampleProbes(t)
		before := resolverSnapshot(t, r, probes...)

		// canonical 자리가 미등록이고, alias는 이미 다른 asset이 주장 중이다
		// → 충돌이 아니라 ErrNotRegistered.
		err := r.RegisterAlias(idnUnregistered(t), idnAliasPort(t))
		requireErrNotRegistered(t, err, "RegisterAlias(미등록 canonical + 충돌 소지)")
		if errors.Is(err, identity.ErrConflict) {
			t.Errorf("등록 여부 위반이 ErrConflict로도 판정되었다 (err=%v)", err)
		}
		var c *identity.Conflict
		if errors.As(err, &c) {
			t.Errorf("등록 여부 위반에서 *Conflict가 나왔다: %#v", c)
		}

		// canonical 자리가 어떤 asset의 alias이고, alias 자리는 또 다른 asset이
		// 주장 중이다 → 역시 ErrNotRegistered (IDN-032).
		err = r.RegisterAlias(idnAliasPort(t), idnAliasPod(t))
		requireErrNotRegistered(t, err, "RegisterAlias(alias를 canonical 자리에 + 충돌 소지)")

		assertSnapshotUnchanged(t, before, resolverSnapshot(t, r, probes...), "우선순위 판정")
	})
}
