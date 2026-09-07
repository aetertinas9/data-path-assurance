// identity_conflict_test.go — IDENTITY_CONFLICT finding과 Conflict 오류
// (specs/identity/spec.md 3.3·3.5, IDN-060~063·070).
package tests

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

const idnFindingID = "fnd-idn-0001"

// IDN-060: 유효한 인자에 대해 3.5 표의 필드 매핑을 그대로 가진 Finding을 낸다.
func TestIDN060_NewConflictFindingFieldMapping(t *testing.T) {
	c := idnValidConflict(t)
	evidence := idnConflictEvidence()

	got, err := identity.NewConflictFinding(c, idnFindingID, evidence, tFirstSeen)
	requireNoErr(t, err, "NewConflictFinding")

	if got.ID != idnFindingID {
		t.Errorf("ID = %q, want %q", got.ID, idnFindingID)
	}
	if got.Type != model.FindingIdentityConflict {
		t.Errorf("Type = %q, want %q", got.Type.String(), model.FindingIdentityConflict.String())
	}
	if got.Type.String() != "IDENTITY_CONFLICT" {
		t.Errorf("Type.String() = %q, want %q", got.Type.String(), "IDENTITY_CONFLICT")
	}

	// Scope는 [Existing, Claimed] 순서 그대로 2개다.
	if len(got.Scope) != 2 {
		t.Fatalf("len(Scope) = %d, want 2", len(got.Scope))
	}
	assertSameRef(t, got.Scope[0], c.Existing, "Scope[0] (Existing)")
	assertSameRef(t, got.Scope[1], c.Claimed, "Scope[1] (Claimed)")

	if got.Severity != model.SeverityWarning {
		t.Errorf("Severity = %q, want %q", got.Severity.String(), model.SeverityWarning.String())
	}
	if got.Confidence != model.ConfidenceHigh {
		t.Errorf("Confidence = %q, want %q", got.Confidence.String(), model.ConfidenceHigh.String())
	}
	if got.State != model.StateActive {
		t.Errorf("State = %q, want %q", got.State.String(), model.StateActive.String())
	}

	if !reflect.DeepEqual(got.Evidence, evidence) {
		t.Errorf("Evidence = %#v, want %#v", got.Evidence, evidence)
	}
	if len(got.MissingInputs) != 0 {
		t.Errorf("len(MissingInputs) = %d, want 0 (%#v)", len(got.MissingInputs), got.MissingInputs)
	}
	if len(got.Affected) != 0 {
		t.Errorf("len(Affected) = %d, want 0 (%#v)", len(got.Affected), got.Affected)
	}

	if !got.FirstSeen.Equal(tFirstSeen) {
		t.Errorf("FirstSeen = %v, want %v", got.FirstSeen, tFirstSeen)
	}
	if !got.LastSeen.Equal(tFirstSeen) {
		t.Errorf("LastSeen = %v, want %v", got.LastSeen, tFirstSeen)
	}

	if got.Explanation == "" {
		t.Errorf("Explanation은 비어 있지 않아야 한다")
	}
	for _, want := range []string{c.ID.String(), c.Existing.Canonical, c.Claimed.Canonical} {
		if !strings.Contains(got.Explanation, want) {
			t.Errorf("Explanation = %q, %q를 포함해야 한다", got.Explanation, want)
		}
	}
	if got.SuggestedStep == "" {
		t.Errorf("SuggestedStep은 비어 있지 않아야 한다")
	}

	// 3.5: model.NewFinding 계약을 그대로 따른다 — Validate()는 nil이다.
	if err := got.Validate(); err != nil {
		t.Errorf("반환된 Finding의 Validate() = %v, want nil", err)
	}
}

// IDN-060 + IDN-070: 실제 등록 충돌이 낸 Conflict로도 같은 계약이 성립한다.
func TestIDN060_NewConflictFindingFromRegistrationConflict(t *testing.T) {
	r := identity.NewResolver()
	c := idnCanonicalA(t)
	alias := idnAliasPort(t)
	mustRegisterAsset(t, r, model.KindNICPort, c)
	mustRegisterAlias(t, r, c, alias)

	conflict := requireConflict(t, r.RegisterAsset(model.KindPCIeFunction, c), "RegisterAsset(kind 충돌)")

	got, err := identity.NewConflictFinding(*conflict, idnFindingID, idnConflictEvidence(), tFirstSeen)
	requireNoErr(t, err, "NewConflictFinding(등록 충돌)")

	if len(got.Scope) != 2 {
		t.Fatalf("len(Scope) = %d, want 2", len(got.Scope))
	}
	if got.Scope[0].Key() != conflict.Existing.Key() {
		t.Errorf("Scope[0].Key() = %q, want %q", got.Scope[0].Key(), conflict.Existing.Key())
	}
	if got.Scope[1].Key() != conflict.Claimed.Key() {
		t.Errorf("Scope[1].Key() = %q, want %q", got.Scope[1].Key(), conflict.Claimed.Key())
	}
	if !strings.Contains(got.Explanation, conflict.ID.String()) {
		t.Errorf("Explanation = %q, 충돌 identity %q를 포함해야 한다", got.Explanation, conflict.ID.String())
	}
	requireNoErr(t, got.Validate(), "반환된 Finding의 Validate()")
}

// IDN-061 (edge): 무효한 인자는 zero model.Finding과 model.ErrInvalid 계열 오류다.
func TestIDN061_NewConflictFindingRejectsInvalidInput(t *testing.T) {
	valid := idnValidConflict(t)

	cases := []struct {
		name     string
		conflict identity.Conflict
		id       string
		evidence []model.EvidenceRef
	}{
		{"zero value Conflict", identity.Conflict{}, idnFindingID, idnConflictEvidence()},
		{"Existing이 zero AssetRef", identity.Conflict{
			ID: valid.ID, Existing: model.AssetRef{}, Claimed: valid.Claimed,
		}, idnFindingID, idnConflictEvidence()},
		{"Claimed가 zero AssetRef", identity.Conflict{
			ID: valid.ID, Existing: valid.Existing, Claimed: model.AssetRef{},
		}, idnFindingID, idnConflictEvidence()},
		{"Existing의 Kind가 무효", identity.Conflict{
			ID:       valid.ID,
			Existing: model.AssetRef{Canonical: canonicalString},
			Claimed:  valid.Claimed,
		}, idnFindingID, idnConflictEvidence()},
		{"Claimed의 Canonical이 TypedID 형식이 아님", identity.Conflict{
			ID:       valid.ID,
			Existing: valid.Existing,
			Claimed:  model.AssetRef{Kind: model.KindPod, Canonical: "구분자없음"},
		}, idnFindingID, idnConflictEvidence()},
		{"ID가 zero TypedID", identity.Conflict{
			ID: model.TypedID{}, Existing: valid.Existing, Claimed: valid.Claimed,
		}, idnFindingID, idnConflictEvidence()},
		{"ID의 Value가 빔", identity.Conflict{
			ID:       model.TypedID{Namespace: string(model.NamespacePCIBDF)},
			Existing: valid.Existing,
			Claimed:  valid.Claimed,
		}, idnFindingID, idnConflictEvidence()},
		// 3.1 (b): 이 패키지의 모든 연산은 Namespace에 콜론이 없기를 요구한다.
		{"ID의 Namespace에 콜론", identity.Conflict{
			ID:       model.TypedID{Namespace: "pci:bdf", Value: canonicalValue},
			Existing: valid.Existing,
			Claimed:  valid.Claimed,
		}, idnFindingID, idnConflictEvidence()},
		{"id가 빈 문자열", valid, "", idnConflictEvidence()},
		{"evidence가 nil", valid, idnFindingID, nil},
		{"evidence가 길이 0", valid, idnFindingID, []model.EvidenceRef{}},
		{"evidence 원소의 ObservationID가 빔", valid, idnFindingID, []model.EvidenceRef{
			{ObservationID: "", Summary: "요약만 있다"},
		}},
		{"evidence 뒤쪽 원소가 무효", valid, idnFindingID, []model.EvidenceRef{
			{ObservationID: "obs-idn-0001"},
			{ObservationID: ""},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := identity.NewConflictFinding(tc.conflict, tc.id, tc.evidence, tFirstSeen)
			requireErrInvalid(t, err, "NewConflictFinding("+tc.name+")")
			if !reflect.DeepEqual(got, model.Finding{}) {
				t.Errorf("오류 시 zero Finding을 기대했으나 %#v를 받았다", got)
			}
		})
	}

	t.Run("seenAt이 zero time", func(t *testing.T) {
		got, err := identity.NewConflictFinding(valid, idnFindingID, idnConflictEvidence(), zeroTime)
		requireErrInvalid(t, err, "NewConflictFinding(zero seenAt)")
		if !reflect.DeepEqual(got, model.Finding{}) {
			t.Errorf("오류 시 zero Finding을 기대했으나 %#v를 받았다", got)
		}
	})

	t.Run("여러 위반이 겹쳐도 ErrInvalid", func(t *testing.T) {
		got, err := identity.NewConflictFinding(identity.Conflict{}, "", nil, zeroTime)
		requireErrInvalid(t, err, "NewConflictFinding(모두 무효)")
		if !reflect.DeepEqual(got, model.Finding{}) {
			t.Errorf("오류 시 zero Finding을 기대했으나 %#v를 받았다", got)
		}
	})
}

// IDN-062: 같은 인자로 두 번 호출하면 모든 필드가 동일하다
// (Explanation·SuggestedStep 문면 포함).
func TestIDN062_NewConflictFindingIsDeterministic(t *testing.T) {
	c := idnValidConflict(t)

	first, errFirst := identity.NewConflictFinding(c, idnFindingID, idnConflictEvidence(), tFirstSeen)
	second, errSecond := identity.NewConflictFinding(c, idnFindingID, idnConflictEvidence(), tFirstSeen)
	requireNoErr(t, errFirst, "NewConflictFinding(1회차)")
	requireNoErr(t, errSecond, "NewConflictFinding(2회차)")

	if !reflect.DeepEqual(first, second) {
		t.Errorf("두 호출의 Finding이 달랐다:\n%#v\n%#v", first, second)
	}
	if first.Explanation != second.Explanation {
		t.Errorf("Explanation이 달랐다: %q vs %q", first.Explanation, second.Explanation)
	}
	if first.SuggestedStep != second.SuggestedStep {
		t.Errorf("SuggestedStep이 달랐다: %q vs %q", first.SuggestedStep, second.SuggestedStep)
	}

	// 오류 경로도 결정론적이다.
	_, errA := identity.NewConflictFinding(identity.Conflict{}, idnFindingID, idnConflictEvidence(), tFirstSeen)
	_, errB := identity.NewConflictFinding(identity.Conflict{}, idnFindingID, idnConflictEvidence(), tFirstSeen)
	requireErrInvalid(t, errA, "NewConflictFinding(1회차, 무효)")
	requireErrInvalid(t, errB, "NewConflictFinding(2회차, 무효)")
	if errA.Error() != errB.Error() {
		t.Errorf("오류 메시지가 호출마다 달랐다: %q vs %q", errA.Error(), errB.Error())
	}
}

// IDN-063 (edge): 인자로 넘긴 evidence slice나 Conflict 내부의 slice를 호출 후에
// 변형해도 이미 반환된 Finding에는 반영되지 않는다.
func TestIDN063_NewConflictFindingCopiesSlices(t *testing.T) {
	t.Run("evidence 슬라이스", func(t *testing.T) {
		evidence := []model.EvidenceRef{
			{ObservationID: "obs-idn-0001", Summary: "원래 요약"},
			{ObservationID: "obs-idn-0002", Summary: "두 번째"},
		}
		got, err := identity.NewConflictFinding(idnValidConflict(t), idnFindingID, evidence, tFirstSeen)
		requireNoErr(t, err, "NewConflictFinding")

		evidence[0] = model.EvidenceRef{ObservationID: "변조된-obs", Summary: "변조"}
		evidence[1].Summary = "변조된 요약"

		if len(got.Evidence) != 2 {
			t.Fatalf("len(Evidence) = %d, want 2", len(got.Evidence))
		}
		if got.Evidence[0].ObservationID != "obs-idn-0001" || got.Evidence[0].Summary != "원래 요약" {
			t.Errorf("Evidence[0]이 호출자의 변경에 영향받았다: %#v", got.Evidence[0])
		}
		if got.Evidence[1].Summary != "두 번째" {
			t.Errorf("Evidence[1]이 호출자의 변경에 영향받았다: %#v", got.Evidence[1])
		}
		requireNoErr(t, got.Validate(), "변형 후 Finding.Validate()")
	})

	t.Run("Conflict 내부의 Aliases 슬라이스", func(t *testing.T) {
		alias := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
		aliases := []model.TypedID{alias}
		// NewAssetRef를 거치면 이 배열이 이미 복사되므로, 실제로 공유하도록
		// AssetRef를 손으로 조립한다 (값 자체는 유효하다).
		existing := model.AssetRef{
			Kind:      model.KindNICPort,
			Canonical: canonicalString,
			Aliases:   aliases,
		}
		requireNoErr(t, existing.Validate(), "손으로 조립한 Existing의 Validate()")

		c := identity.Conflict{
			ID:       idnCanonicalA(t),
			Existing: existing,
			Claimed:  mustAssetRef(t, model.KindPCIeFunction, idnCanonicalA(t)),
		}

		got, err := identity.NewConflictFinding(c, idnFindingID, idnConflictEvidence(), tFirstSeen)
		requireNoErr(t, err, "NewConflictFinding")

		aliases[0] = model.TypedID{Namespace: "변조", Value: "변조"}

		if len(got.Scope) != 2 || len(got.Scope[0].Aliases) != 1 {
			t.Fatalf("Scope 구조가 보존되지 않았다: %#v", got.Scope)
		}
		if !got.Scope[0].Aliases[0].Equal(alias) {
			t.Errorf("Scope[0].Aliases[0]이 호출자의 변경에 영향받았다: %#v", got.Scope[0].Aliases[0])
		}
		requireNoErr(t, got.Validate(), "변형 후 Finding.Validate()")
	})

	t.Run("Conflict 내부 alias의 Raw", func(t *testing.T) {
		raw := []byte{0x01, 0x02, 0x03}
		alias := model.TypedID{
			Namespace: string(model.NamespaceLLDPChassisID),
			Value:     "aa:bb:cc:dd:ee:ff",
			Raw:       raw,
		}
		existing := model.AssetRef{
			Kind:      model.KindNICPort,
			Canonical: canonicalString,
			Aliases:   []model.TypedID{alias},
		}
		requireNoErr(t, existing.Validate(), "손으로 조립한 Existing의 Validate()")

		c := identity.Conflict{
			ID:       idnCanonicalA(t),
			Existing: existing,
			Claimed:  mustAssetRef(t, model.KindPCIeFunction, idnCanonicalA(t)),
		}

		got, err := identity.NewConflictFinding(c, idnFindingID, idnConflictEvidence(), tFirstSeen)
		requireNoErr(t, err, "NewConflictFinding")

		raw[0] = 0xff

		if len(got.Scope) != 2 || len(got.Scope[0].Aliases) != 1 {
			t.Fatalf("Scope 구조가 보존되지 않았다: %#v", got.Scope)
		}
		if !got.Scope[0].Aliases[0].Equal(alias) {
			t.Errorf("Scope[0].Aliases[0]의 identity가 달라졌다: %#v", got.Scope[0].Aliases[0])
		}
		// v1.1 §3.5: Scope alias는 반드시 정규화된다.
		if gotRaw := got.Scope[0].Aliases[0].Raw; gotRaw != nil {
			t.Errorf("Scope alias Raw = %#v, want nil", gotRaw)
		}
	})

	// IDN-062 + 3.1: Conflict.ID의 Raw·Source는 identity의 일부가 아니므로
	// 결과에 새어 나오지 않는다 — 그 둘만 다른 두 Conflict는 같은 Finding을 낸다.
	t.Run("Conflict.ID의 Raw·Source는 결과에 영향을 주지 않는다", func(t *testing.T) {
		base := idnCanonicalA(t)
		existing := mustAssetRef(t, model.KindNICPort, base)
		claimed := mustAssetRef(t, model.KindPCIeFunction, base)

		plain := identity.Conflict{ID: base, Existing: existing, Claimed: claimed}
		decorated := identity.Conflict{
			ID: model.TypedID{
				Namespace: base.Namespace,
				Value:     base.Value,
				Raw:       []byte{0x0a, 0x0b},
				Source:    "sysfs",
			},
			Existing: existing,
			Claimed:  claimed,
		}

		a, err := identity.NewConflictFinding(plain, idnFindingID, idnConflictEvidence(), tFirstSeen)
		requireNoErr(t, err, "NewConflictFinding(정규화된 ID)")
		b, err := identity.NewConflictFinding(decorated, idnFindingID, idnConflictEvidence(), tFirstSeen)
		requireNoErr(t, err, "NewConflictFinding(Raw·Source가 있는 ID)")

		if !reflect.DeepEqual(a, b) {
			t.Errorf("ID의 Raw·Source만 다른데 Finding이 달랐다:\n%#v\n%#v", a, b)
		}
		requireNoErr(t, b.Validate(), "Finding.Validate()")
	})
}

// IDN-070: 등록 연산이 반환한 충돌 오류는 ErrConflict로 판정되고, *Conflict로
// 꺼낼 수 있으며, Error()는 비어 있지 않고 ID.String()을 포함한다.
func TestIDN070_ConflictErrorContract(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, r *identity.Resolver)
		invoke func(t *testing.T, r *identity.Resolver) error
		wantID func(t *testing.T) model.TypedID
	}{
		{
			name: "RegisterAsset kind 충돌 (IDN-023)",
			setup: func(t *testing.T, r *identity.Resolver) {
				mustRegisterAsset(t, r, model.KindNICPort, idnCanonicalA(t))
			},
			invoke: func(t *testing.T, r *identity.Resolver) error {
				return r.RegisterAsset(model.KindPod, idnCanonicalA(t))
			},
			wantID: idnCanonicalA,
		},
		{
			name: "RegisterAsset alias를 canonical로 (IDN-024)",
			setup: func(t *testing.T, r *identity.Resolver) {
				mustRegisterAsset(t, r, model.KindNICPort, idnCanonicalA(t))
				mustRegisterAlias(t, r, idnCanonicalA(t), idnAliasPort(t))
			},
			invoke: func(t *testing.T, r *identity.Resolver) error {
				return r.RegisterAsset(model.KindSwitchPort, idnAliasPort(t))
			},
			wantID: idnAliasPort,
		},
		{
			name: "RegisterAlias 한 alias를 두 asset이 주장 (IDN-035)",
			setup: func(t *testing.T, r *identity.Resolver) {
				mustRegisterAsset(t, r, model.KindNICPort, idnCanonicalA(t))
				mustRegisterAlias(t, r, idnCanonicalA(t), idnAliasPort(t))
				mustRegisterAsset(t, r, model.KindSwitchPort, idnCanonicalB(t))
			},
			invoke: func(t *testing.T, r *identity.Resolver) error {
				return r.RegisterAlias(idnCanonicalB(t), idnAliasPort(t))
			},
			wantID: idnAliasPort,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := identity.NewResolver()
			tc.setup(t, r)

			err := tc.invoke(t, r)
			if err == nil {
				t.Fatalf("충돌 오류를 기대했으나 nil을 받았다")
			}
			if !errors.Is(err, identity.ErrConflict) {
				t.Errorf("errors.Is(err, ErrConflict)가 참이어야 한다 (err=%v)", err)
			}

			var c *identity.Conflict
			if !errors.As(err, &c) {
				t.Fatalf("errors.As(err, &c)(c *identity.Conflict)가 참이어야 한다 (err=%v)", err)
			}
			if c == nil {
				t.Fatalf("errors.As가 nil *Conflict를 채웠다")
			}

			wantID := tc.wantID(t)
			if !c.ID.Equal(wantID) {
				t.Errorf("Conflict.ID = %#v, want %s와 Equal", c.ID, wantID.String())
			}
			assertNormalized(t, c.ID, "Conflict.ID")

			msg := err.Error()
			if msg == "" {
				t.Errorf("Error()는 비어 있지 않아야 한다")
			}
			if !strings.Contains(msg, c.ID.String()) {
				t.Errorf("Error() = %q, ID.String() %q를 포함해야 한다", msg, c.ID.String())
			}
			// (*Conflict).Error()도 같은 계약을 만족한다.
			direct := c.Error()
			if direct == "" || !strings.Contains(direct, c.ID.String()) {
				t.Errorf("(*Conflict).Error() = %q, 비어 있지 않고 %q를 포함해야 한다", direct, c.ID.String())
			}
			// Unwrap()은 ErrConflict를 반환한다.
			if unwrapped := c.Unwrap(); !errors.Is(unwrapped, identity.ErrConflict) {
				t.Errorf("(*Conflict).Unwrap() = %v, want ErrConflict", unwrapped)
			}
			// Existing·Claimed는 유효한 AssetRef다.
			requireNoErr(t, c.Existing.Validate(), "Conflict.Existing.Validate()")
			requireNoErr(t, c.Claimed.Validate(), "Conflict.Claimed.Validate()")
		})
	}
}

// IDN-070 + 3.3: 손으로 조립한 *Conflict도 error로 쓰이며 ErrConflict로 판정된다.
func TestIDN070_HandBuiltConflictIsAnError(t *testing.T) {
	c := idnValidConflict(t)
	var err error = &c

	if !errors.Is(err, identity.ErrConflict) {
		t.Errorf("errors.Is(&Conflict{...}, ErrConflict)가 참이어야 한다 (err=%v)", err)
	}
	var target *identity.Conflict
	if !errors.As(err, &target) {
		t.Errorf("errors.As(&Conflict{...}, &target)이 참이어야 한다")
	}
	if err.Error() == "" {
		t.Errorf("Error()는 비어 있지 않아야 한다")
	}
	if !strings.Contains(err.Error(), c.ID.String()) {
		t.Errorf("Error() = %q, ID.String() %q를 포함해야 한다", err.Error(), c.ID.String())
	}
}

// IDN-070 + 3.3: 공개 sentinel 3종은 서로 구별되고 model.ErrInvalid와도 다르다.
func TestIDN070_SentinelsAreDistinct(t *testing.T) {
	sentinels := []struct {
		name string
		err  error
	}{
		{"ErrNotRegistered", identity.ErrNotRegistered},
		{"ErrAmbiguous", identity.ErrAmbiguous},
		{"ErrConflict", identity.ErrConflict},
	}
	for _, s := range sentinels {
		if s.err == nil {
			t.Fatalf("%s는 nil이 아니어야 한다", s.name)
		}
		if !errors.Is(s.err, s.err) {
			t.Errorf("errors.Is(%s, %s)가 참이어야 한다", s.name, s.name)
		}
		if errors.Is(s.err, model.ErrInvalid) {
			t.Errorf("%s가 model.ErrInvalid로 판정되었다", s.name)
		}
		if errors.Is(model.ErrInvalid, s.err) {
			t.Errorf("model.ErrInvalid가 %s로 판정되었다", s.name)
		}
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(a.err, b.err) {
				t.Errorf("%s가 %s로 판정되었다", a.name, b.name)
			}
		}
	}
}
