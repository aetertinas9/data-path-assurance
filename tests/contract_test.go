package tests

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// MDL-002: 같은 인자로 두 번 호출하면 같은 결과다 (시계·난수·환경 비의존).
func TestMDL002_ConstructorsAreDeterministic(t *testing.T) {
	t.Run("NewTypedID", func(t *testing.T) {
		a, errA := model.NewTypedID(string(model.NamespacePCIBDF), canonicalValue)
		b, errB := model.NewTypedID(string(model.NamespacePCIBDF), canonicalValue)
		requireNoErr(t, errA, "NewTypedID(1회차)")
		requireNoErr(t, errB, "NewTypedID(2회차)")
		if !reflect.DeepEqual(a, b) {
			t.Errorf("두 호출의 결과가 달랐다: %#v vs %#v", a, b)
		}
		if a.String() != b.String() {
			t.Errorf("String()이 달랐다: %q vs %q", a.String(), b.String())
		}
	})

	t.Run("NewAssetRef", func(t *testing.T) {
		canonical := canonicalTypedID(t)
		alias := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")

		a, errA := model.NewAssetRef(model.KindNICPort, canonical, alias)
		b, errB := model.NewAssetRef(model.KindNICPort, canonical, alias)
		requireNoErr(t, errA, "NewAssetRef(1회차)")
		requireNoErr(t, errB, "NewAssetRef(2회차)")
		if !reflect.DeepEqual(a, b) {
			t.Errorf("두 호출의 결과가 달랐다: %#v vs %#v", a, b)
		}
		if a.Key() != b.Key() {
			t.Errorf("Key()가 달랐다: %q vs %q", a.Key(), b.Key())
		}
		// 같은 값에 대해 Key()를 반복 호출해도 같다.
		firstCall := a.Key()
		secondCall := a.Key()
		if firstCall != secondCall {
			t.Errorf("같은 값의 Key()가 호출마다 달랐다: %q vs %q", firstCall, secondCall)
		}
	})

	t.Run("NewObservation", func(t *testing.T) {
		in := validObservationInput(t)
		a, errA := model.NewObservation(in)
		b, errB := model.NewObservation(in)
		requireNoErr(t, errA, "NewObservation(1회차)")
		requireNoErr(t, errB, "NewObservation(2회차)")
		if !reflect.DeepEqual(a, b) {
			t.Errorf("두 호출의 결과가 달랐다:\n%#v\n%#v", a, b)
		}
	})

	t.Run("NewFinding", func(t *testing.T) {
		in := validFindingInput(t)
		a, errA := model.NewFinding(in)
		b, errB := model.NewFinding(in)
		requireNoErr(t, errA, "NewFinding(1회차)")
		requireNoErr(t, errB, "NewFinding(2회차)")
		if !reflect.DeepEqual(a, b) {
			t.Errorf("두 호출의 결과가 달랐다:\n%#v\n%#v", a, b)
		}
	})

	t.Run("Value 생성자", func(t *testing.T) {
		if !reflect.DeepEqual(model.NewFloatValue(1.5), model.NewFloatValue(1.5)) {
			t.Errorf("NewFloatValue가 결정론적이지 않다")
		}
		if !reflect.DeepEqual(model.NewIntValue(-7), model.NewIntValue(-7)) {
			t.Errorf("NewIntValue가 결정론적이지 않다")
		}
		if !reflect.DeepEqual(model.NewBoolValue(true), model.NewBoolValue(true)) {
			t.Errorf("NewBoolValue가 결정론적이지 않다")
		}
		if !reflect.DeepEqual(model.NewStringValue("up"), model.NewStringValue("up")) {
			t.Errorf("NewStringValue가 결정론적이지 않다")
		}
	})

	t.Run("오류 경로도 결정론적", func(t *testing.T) {
		_, errA := model.NewTypedID("", "")
		_, errB := model.NewTypedID("", "")
		requireErrInvalid(t, errA, "NewTypedID(1회차)")
		requireErrInvalid(t, errB, "NewTypedID(2회차)")
		if errA.Error() != errB.Error() {
			t.Errorf("오류 메시지가 호출마다 달랐다: %q vs %q", errA.Error(), errB.Error())
		}
	})

	t.Run("String과 IsValid는 반복 호출에 안정적", func(t *testing.T) {
		id := canonicalTypedID(t)
		for i := 0; i < 3; i++ {
			if id.String() != canonicalString {
				t.Fatalf("String()이 %d번째 호출에서 달라졌다: %q", i, id.String())
			}
			if !model.KindNICPort.IsValid() || model.KindNICPort.String() != "NICPort" {
				t.Fatalf("열거형 메서드가 %d번째 호출에서 달라졌다", i)
			}
		}
	})
}

// MDL-003: ErrInvalid는 공개 sentinel이다.
func TestMDL003_ErrInvalidIsPublicSentinel(t *testing.T) {
	if model.ErrInvalid == nil {
		t.Fatalf("model.ErrInvalid는 nil이 아니어야 한다")
	}
	if !errors.Is(model.ErrInvalid, model.ErrInvalid) {
		t.Errorf("errors.Is(ErrInvalid, ErrInvalid)가 참이어야 한다")
	}
	if errors.Is(errors.New("무관한 오류"), model.ErrInvalid) {
		t.Errorf("무관한 오류가 ErrInvalid로 판정되었다")
	}
}

// MDL-003: 모든 생성자는 무효 입력에 대해 zero value와 ErrInvalid를 반환한다.
func TestMDL003_ConstructorsReturnZeroValueWithErrInvalid(t *testing.T) {
	var zeroKind model.AssetKind

	t.Run("NewTypedID", func(t *testing.T) {
		got, err := model.NewTypedID("", "")
		requireErrInvalid(t, err, "NewTypedID")
		if !reflect.DeepEqual(got, model.TypedID{}) {
			t.Errorf("zero TypedID를 기대했으나 %#v", got)
		}
	})
	t.Run("NewAssetRef", func(t *testing.T) {
		got, err := model.NewAssetRef(zeroKind, model.TypedID{})
		requireErrInvalid(t, err, "NewAssetRef")
		if !reflect.DeepEqual(got, model.AssetRef{}) {
			t.Errorf("zero AssetRef를 기대했으나 %#v", got)
		}
	})
	t.Run("NewObservation", func(t *testing.T) {
		got, err := model.NewObservation(model.Observation{})
		requireErrInvalid(t, err, "NewObservation")
		if !reflect.DeepEqual(got, model.Observation{}) {
			t.Errorf("zero Observation을 기대했으나 %#v", got)
		}
	})
	t.Run("NewFinding", func(t *testing.T) {
		got, err := model.NewFinding(model.Finding{})
		requireErrInvalid(t, err, "NewFinding")
		if !reflect.DeepEqual(got, model.Finding{}) {
			t.Errorf("zero Finding을 기대했으나 %#v", got)
		}
	})
}

// MDL-003: 공개 함수는 panic하지 않는다 — zero value·무효 입력에서도.
func TestMDL003_PublicAPIDoesNotPanic(t *testing.T) {
	var (
		zeroID        model.TypedID
		zeroRef       model.AssetRef
		zeroSource    model.SourceRef
		zeroValue     model.Value
		zeroObs       model.Observation
		zeroFinding   model.Finding
		zeroEvidence  model.EvidenceRef
		zeroImpact    model.ImpactRef
		zeroSample    model.Sample
		zeroCondition model.Condition
		zeroSignal    model.SignalRef
		zeroPartition model.PartitionKey
	)

	mustNotPanic(t, "zero TypedID의 메서드", func() {
		_ = zeroID.String()
		_ = zeroID.Equal(zeroID)
		_ = zeroID.Validate()
	})
	mustNotPanic(t, "zero AssetRef의 메서드", func() {
		_ = zeroRef.Key()
		_ = zeroRef.Validate()
	})
	mustNotPanic(t, "zero Value의 접근자", func() {
		_ = zeroValue.Kind()
		_, _ = zeroValue.Float()
		_, _ = zeroValue.Int()
		_, _ = zeroValue.Bool()
		_, _ = zeroValue.Str()
	})
	mustNotPanic(t, "zero struct의 Validate()", func() {
		_ = zeroSource.Validate()
		_ = zeroObs.Validate()
		_ = zeroFinding.Validate()
		_ = zeroEvidence.Validate()
		_ = zeroImpact.Validate()
		_ = zeroSample.Validate()
		_ = zeroCondition.Validate()
	})
	mustNotPanic(t, "zero 문자열 기반 타입의 String()·IsValid()", func() {
		_, _ = zeroSignal.String(), zeroSignal.IsValid()
		_, _ = zeroPartition.String(), zeroPartition.IsValid()
		// 형식을 크게 벗어난 값에서도 panic하지 않는다.
		bad := model.SignalRef("...\x00...한글...")
		_, _ = bad.String(), bad.IsValid()
		longKey := model.PartitionKey(strings.Repeat("x", 4096))
		_, _ = longKey.String(), longKey.IsValid()
	})
	mustNotPanic(t, "zero 열거형의 String()·IsValid()", func() {
		var (
			assetKind       model.AssetKind
			valueKind       model.ValueKind
			evidenceQuality model.EvidenceQuality
			findingType     model.FindingType
			severity        model.Severity
			confidence      model.Confidence
			evidenceLevel   model.EvidenceLevel
			findingState    model.FindingState
			impactAccuracy  model.ImpactAccuracy
			edgeRelation    model.EdgeRelation
			edgeOrigin      model.EdgeOrigin
			conditionStatus model.ConditionStatus
		)
		_, _ = assetKind.String(), assetKind.IsValid()
		_, _ = valueKind.String(), valueKind.IsValid()
		_, _ = evidenceQuality.String(), evidenceQuality.IsValid()
		_, _ = findingType.String(), findingType.IsValid()
		_, _ = severity.String(), severity.IsValid()
		_, _ = confidence.String(), confidence.IsValid()
		_, _ = evidenceLevel.String(), evidenceLevel.IsValid()
		_, _ = findingState.String(), findingState.IsValid()
		_, _ = impactAccuracy.String(), impactAccuracy.IsValid()
		_, _ = edgeRelation.String(), edgeRelation.IsValid()
		_, _ = edgeOrigin.String(), edgeOrigin.IsValid()
		_, _ = conditionStatus.String(), conditionStatus.IsValid()
	})
	// MDL-070(d): 열거되지 않은 값의 String()은 계약이 아니지만 panic해서는 안 된다.
	mustNotPanic(t, "열거 밖 값의 String()·IsValid()", func() {
		for _, tc := range nonEnumeratedValues() {
			_, _ = tc.val.String(), tc.val.IsValid()
		}
	})
	mustNotPanic(t, "무효 입력을 받은 생성자", func() {
		var zeroKind model.AssetKind
		_, _ = model.NewTypedID("", "")
		_, _ = model.NewAssetRef(zeroKind, zeroID)
		_, _ = model.NewAssetRef(model.KindPod, zeroID, zeroID, zeroID)
		_, _ = model.NewObservation(zeroObs)
		_, _ = model.NewFinding(zeroFinding)
	})
	mustNotPanic(t, "부분적으로 nil인 입력을 받은 생성자", func() {
		obs := model.Observation{
			ID:         "obs",
			Dimensions: nil,
			Subject:    model.AssetRef{Aliases: nil},
		}
		_, _ = model.NewObservation(obs)

		fnd := model.Finding{ID: "fnd", Scope: nil, Evidence: nil, MissingInputs: nil, Affected: nil}
		_, _ = model.NewFinding(fnd)
	})
}

// MDL-004: NewAssetRef에 넘긴 aliases 슬라이스를 나중에 변경해도 반영되지 않는다.
func TestMDL004_NewAssetRefCopiesAliases(t *testing.T) {
	canonical := canonicalTypedID(t)
	alias0 := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
	alias1 := mustTypedID(t, string(model.NamespaceLLDPChassisID), "aa:bb:cc:dd:ee:ff")

	aliases := []model.TypedID{alias0, alias1}
	ref, err := model.NewAssetRef(model.KindNICPort, canonical, aliases...)
	requireNoErr(t, err, "NewAssetRef")

	// 호출자가 전달한 슬라이스를 생성 후에 변경한다.
	aliases[0] = model.TypedID{Namespace: "변조", Value: "변조"}
	aliases[1].Value = "변조된 값"

	if len(ref.Aliases) != 2 {
		t.Fatalf("len(Aliases) = %d, want 2", len(ref.Aliases))
	}
	if !ref.Aliases[0].Equal(alias0) {
		t.Errorf("Aliases[0]가 호출자의 변경에 영향받았다: %#v", ref.Aliases[0])
	}
	if !ref.Aliases[1].Equal(alias1) {
		t.Errorf("Aliases[1]이 호출자의 변경에 영향받았다: %#v", ref.Aliases[1])
	}
	if err := ref.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// MDL-004: 생성자가 돌려준 AssetRef의 Aliases를 호출자가 변경해도
// 다시 만든 동일 입력의 결과에는 영향이 없다 (내부 상태 공유 금지).
func TestMDL004_AssetRefAliasesAreNotSharedBetweenResults(t *testing.T) {
	canonical := canonicalTypedID(t)
	alias := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")

	first := mustAssetRef(t, model.KindNICPort, canonical, alias)
	second := mustAssetRef(t, model.KindNICPort, canonical, alias)

	if len(first.Aliases) == 0 || len(second.Aliases) == 0 {
		t.Fatalf("Aliases가 비어 있다")
	}
	first.Aliases[0] = model.TypedID{Namespace: "변조", Value: "변조"}

	if !second.Aliases[0].Equal(alias) {
		t.Errorf("두 결과가 같은 배열을 공유했다: %#v", second.Aliases[0])
	}
}

// MDL-004: NewObservation에 넘긴 Dimensions 맵을 나중에 변경해도 반영되지 않는다.
func TestMDL004_NewObservationCopiesDimensions(t *testing.T) {
	dims := map[string]string{"lane": "0", "fec": "rs544"}

	in := validObservationInput(t)
	in.Dimensions = dims

	got, err := model.NewObservation(in)
	requireNoErr(t, err, "NewObservation")

	// 호출자가 전달한 맵을 생성 후에 변경한다.
	dims["lane"] = "변조"
	delete(dims, "fec")
	dims["신규"] = "추가됨"

	if len(got.Dimensions) != 2 {
		t.Errorf("len(Dimensions) = %d, want 2 (호출자의 변경이 반영되었다): %#v", len(got.Dimensions), got.Dimensions)
	}
	if got.Dimensions["lane"] != "0" {
		t.Errorf("Dimensions[\"lane\"] = %q, want %q", got.Dimensions["lane"], "0")
	}
	if got.Dimensions["fec"] != "rs544" {
		t.Errorf("Dimensions[\"fec\"] = %q, want %q", got.Dimensions["fec"], "rs544")
	}
	if _, ok := got.Dimensions["신규"]; ok {
		t.Errorf("호출자가 나중에 추가한 키가 반영되었다: %#v", got.Dimensions)
	}
}

// MDL-004: NewFinding에 넘긴 Scope·Evidence·MissingInputs·Affected 슬라이스를
// 나중에 변경해도 반영되지 않는다.
func TestMDL004_NewFindingCopiesSlices(t *testing.T) {
	scope0 := mustAssetRef(t, model.KindSwitchPort, mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1"))
	scope1 := mustAssetRef(t, model.KindNICPort, canonicalTypedID(t))
	impactAsset := mustAssetRef(t, model.KindPod, mustTypedID(t, string(model.NamespaceKubernetesPodUID), "9f1c-pod-uid"))

	scope := []model.AssetRef{scope0, scope1}
	evidence := []model.EvidenceRef{{ObservationID: "obs-0001", Summary: "원래 요약"}}
	missing := []model.SignalRef{model.SignalRef("pcie.link.width_current")}
	affected := []model.ImpactRef{{Asset: impactAsset, Accuracy: model.ImpactMapped}}

	in := validFindingInput(t)
	in.Scope = scope
	in.Evidence = evidence
	in.MissingInputs = missing
	in.Affected = affected

	got, err := model.NewFinding(in)
	requireNoErr(t, err, "NewFinding")

	// 호출자가 전달한 슬라이스들을 생성 후에 변경한다.
	scope[0] = model.AssetRef{Kind: model.KindPod, Canonical: "변조:값"}
	scope[1].Canonical = "변조:값"
	evidence[0].ObservationID = "변조된-obs"
	evidence[0].Summary = "변조된 요약"
	missing[0] = model.SignalRef("변조.신호")
	affected[0].Accuracy = model.ImpactUnknown

	if len(got.Scope) != 2 {
		t.Fatalf("len(Scope) = %d, want 2", len(got.Scope))
	}
	if got.Scope[0].Key() != scope0.Key() {
		t.Errorf("Scope[0]이 호출자의 변경에 영향받았다: %q", got.Scope[0].Key())
	}
	if got.Scope[1].Key() != scope1.Key() {
		t.Errorf("Scope[1]이 호출자의 변경에 영향받았다: %q", got.Scope[1].Key())
	}
	if len(got.Evidence) != 1 || got.Evidence[0].ObservationID != "obs-0001" || got.Evidence[0].Summary != "원래 요약" {
		t.Errorf("Evidence가 호출자의 변경에 영향받았다: %#v", got.Evidence)
	}
	if len(got.MissingInputs) != 1 || string(got.MissingInputs[0]) != "pcie.link.width_current" {
		t.Errorf("MissingInputs가 호출자의 변경에 영향받았다: %#v", got.MissingInputs)
	}
	if len(got.Affected) != 1 || got.Affected[0].Accuracy != model.ImpactMapped {
		t.Errorf("Affected가 호출자의 변경에 영향받았다: %#v", got.Affected)
	}
	// 방어적 복사 후에도 반환값은 유효해야 한다.
	if err := got.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// MDL-004 (v1.2 확정 계약): 방어적 복사는 **중첩 참조까지 포함하는 깊은 복사**다.
// 스펙이 예시로 든 "aliases 원소의 Raw", "Scope 원소의 Aliases"를 포함해
// 생성자에 전달된 모든 중첩 slice가 호출자의 사후 변경에 노출되어서는 안 된다.
func TestMDL004_NestedSlicesAreAlsoCopied(t *testing.T) {
	t.Run("NewAssetRef가 alias의 Raw를 복사한다", func(t *testing.T) {
		raw := []byte{0x01, 0x02, 0x03}
		alias := model.TypedID{
			Namespace: string(model.NamespaceLLDPChassisID),
			Value:     "aa:bb:cc:dd:ee:ff",
			Raw:       raw,
			Source:    "lldp",
		}

		ref, err := model.NewAssetRef(model.KindSwitchPort, canonicalTypedID(t), alias)
		requireNoErr(t, err, "NewAssetRef")

		raw[0] = 0xff

		if len(ref.Aliases) != 1 {
			t.Fatalf("len(Aliases) = %d, want 1", len(ref.Aliases))
		}
		if len(ref.Aliases[0].Raw) != 3 {
			t.Fatalf("len(Aliases[0].Raw) = %d, want 3", len(ref.Aliases[0].Raw))
		}
		if ref.Aliases[0].Raw[0] != 0x01 {
			t.Errorf("Aliases[0].Raw[0] = %#x, want 0x01 (호출자의 변경이 반영되었다)", ref.Aliases[0].Raw[0])
		}
	})

	t.Run("NewObservation이 Subject.Aliases를 복사한다", func(t *testing.T) {
		alias := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
		subject := mustAssetRef(t, model.KindNICPort, canonicalTypedID(t), alias)

		in := validObservationInput(t)
		in.Subject = subject

		got, err := model.NewObservation(in)
		requireNoErr(t, err, "NewObservation")

		if len(subject.Aliases) != 1 {
			t.Fatalf("len(subject.Aliases) = %d, want 1", len(subject.Aliases))
		}
		subject.Aliases[0] = model.TypedID{Namespace: "변조", Value: "변조"}

		if len(got.Subject.Aliases) != 1 {
			t.Fatalf("len(got.Subject.Aliases) = %d, want 1", len(got.Subject.Aliases))
		}
		if !got.Subject.Aliases[0].Equal(alias) {
			t.Errorf("Subject.Aliases[0]이 호출자의 변경에 영향받았다: %#v", got.Subject.Aliases[0])
		}
	})

	t.Run("NewFinding이 Scope 항목의 Aliases를 복사한다", func(t *testing.T) {
		alias := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
		scoped := mustAssetRef(t, model.KindSwitchPort, canonicalTypedID(t), alias)

		in := validFindingInput(t)
		in.Scope = []model.AssetRef{scoped}

		got, err := model.NewFinding(in)
		requireNoErr(t, err, "NewFinding")

		if len(scoped.Aliases) != 1 {
			t.Fatalf("len(scoped.Aliases) = %d, want 1", len(scoped.Aliases))
		}
		scoped.Aliases[0] = model.TypedID{Namespace: "변조", Value: "변조"}

		if len(got.Scope) != 1 || len(got.Scope[0].Aliases) != 1 {
			t.Fatalf("Scope 구조가 보존되지 않았다: %#v", got.Scope)
		}
		if !got.Scope[0].Aliases[0].Equal(alias) {
			t.Errorf("Scope[0].Aliases[0]이 호출자의 변경에 영향받았다: %#v", got.Scope[0].Aliases[0])
		}
	})

	t.Run("NewFinding이 Affected 원소의 Asset.Aliases를 복사한다", func(t *testing.T) {
		alias := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
		asset := mustAssetRef(t, model.KindPod,
			mustTypedID(t, string(model.NamespaceKubernetesPodUID), "9f1c-pod-uid"), alias)

		in := validFindingInput(t)
		in.Affected = []model.ImpactRef{{Asset: asset, Accuracy: model.ImpactMapped}}

		got, err := model.NewFinding(in)
		requireNoErr(t, err, "NewFinding")

		if len(asset.Aliases) != 1 {
			t.Fatalf("len(asset.Aliases) = %d, want 1", len(asset.Aliases))
		}
		asset.Aliases[0] = model.TypedID{Namespace: "변조", Value: "변조"}

		if len(got.Affected) != 1 || len(got.Affected[0].Asset.Aliases) != 1 {
			t.Fatalf("Affected 구조가 보존되지 않았다: %#v", got.Affected)
		}
		if !got.Affected[0].Asset.Aliases[0].Equal(alias) {
			t.Errorf("Affected[0].Asset.Aliases[0]이 호출자의 변경에 영향받았다: %#v",
				got.Affected[0].Asset.Aliases[0])
		}
	})

	t.Run("NewObservation이 Subject.Aliases 원소의 Raw를 복사한다", func(t *testing.T) {
		raw := []byte{0x0a, 0x0b, 0x0c}
		alias := model.TypedID{
			Namespace: string(model.NamespaceLLDPChassisID),
			Value:     "aa:bb:cc:dd:ee:ff",
			Raw:       raw,
		}
		// NewAssetRef를 거치면 Raw가 이미 복사되어 이 테스트가 무의미해진다.
		// raw 배열을 실제로 공유하도록 AssetRef를 손으로 조립한다 (값 자체는 유효).
		subject := model.AssetRef{
			Kind:      model.KindNICPort,
			Canonical: canonicalString,
			Aliases:   []model.TypedID{alias},
		}
		requireNoErr(t, subject.Validate(), "손으로 조립한 Subject의 Validate()")

		in := validObservationInput(t)
		in.Subject = subject

		got, err := model.NewObservation(in)
		requireNoErr(t, err, "NewObservation")

		raw[0] = 0xff

		if len(got.Subject.Aliases) != 1 || len(got.Subject.Aliases[0].Raw) != 3 {
			t.Fatalf("Subject.Aliases 구조가 보존되지 않았다: %#v", got.Subject.Aliases)
		}
		if got.Subject.Aliases[0].Raw[0] != 0x0a {
			t.Errorf("Subject.Aliases[0].Raw[0] = %#x, want 0x0a (호출자의 변경이 반영되었다)",
				got.Subject.Aliases[0].Raw[0])
		}
	})

	t.Run("NewFinding이 Scope 원소의 Aliases 원소의 Raw를 복사한다", func(t *testing.T) {
		raw := []byte{0x11, 0x22}
		alias := model.TypedID{
			Namespace: string(model.NamespaceLLDPChassisID),
			Value:     "11:22",
			Raw:       raw,
		}
		// NewAssetRef를 거치면 Raw가 이미 복사된다 — raw 배열을 공유하도록 손으로 조립한다.
		scoped := model.AssetRef{
			Kind:      model.KindSwitchPort,
			Canonical: canonicalString,
			Aliases:   []model.TypedID{alias},
		}
		requireNoErr(t, scoped.Validate(), "손으로 조립한 Scope 항목의 Validate()")

		in := validFindingInput(t)
		in.Scope = []model.AssetRef{scoped}

		got, err := model.NewFinding(in)
		requireNoErr(t, err, "NewFinding")

		raw[1] = 0xff

		if len(got.Scope) != 1 || len(got.Scope[0].Aliases) != 1 || len(got.Scope[0].Aliases[0].Raw) != 2 {
			t.Fatalf("Scope 구조가 보존되지 않았다: %#v", got.Scope)
		}
		if got.Scope[0].Aliases[0].Raw[1] != 0x22 {
			t.Errorf("Scope[0].Aliases[0].Raw[1] = %#x, want 0x22 (호출자의 변경이 반영되었다)",
				got.Scope[0].Aliases[0].Raw[1])
		}
	})
}

// MDL-005: 생성자를 거치지 않고 조립한 TypedID의 불변식 위반을 Validate()가 잡는다.
func TestMDL005_TypedIDValidate(t *testing.T) {
	cases := []struct {
		name      string
		id        model.TypedID
		wantValid bool
	}{
		{"zero value", model.TypedID{}, false},
		{"Namespace만 있음", model.TypedID{Namespace: "pci-bdf"}, false},
		{"Value만 있음", model.TypedID{Value: canonicalValue}, false},
		{"Raw·Source만 있음", model.TypedID{Raw: []byte{1}, Source: "sysfs"}, false},
		{"Namespace·Value 있음", model.TypedID{Namespace: "pci-bdf", Value: canonicalValue}, true},
		{"Raw·Source가 채워져도 유효", model.TypedID{
			Namespace: "lldp-chassis-id",
			Value:     "aa:bb",
			Raw:       []byte{0xaa, 0xbb},
			Source:    "lldp",
		}, true},
		{"Raw가 nil이어도 유효", model.TypedID{Namespace: "pci-bdf", Value: canonicalValue, Raw: nil}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.id.Validate()
			if tc.wantValid {
				requireNoErr(t, err, "TypedID.Validate()")
				return
			}
			requireErrInvalid(t, err, "TypedID.Validate()")
		})
	}
}

// MDL-005: 조립한 AssetRef의 Canonical은 "유효한 TypedID의 문자열 형식"이어야 한다.
func TestMDL005_AssetRefValidate(t *testing.T) {
	var zeroKind model.AssetKind

	cases := []struct {
		name      string
		ref       model.AssetRef
		wantValid bool
	}{
		{"zero value", model.AssetRef{}, false},
		{"Kind가 무효", model.AssetRef{Kind: zeroKind, Canonical: canonicalString}, false},
		{"Canonical이 빔", model.AssetRef{Kind: model.KindNICPort, Canonical: ""}, false},
		{"Canonical에 구분자가 없음", model.AssetRef{Kind: model.KindNICPort, Canonical: "0000af000"}, false},
		{"Canonical의 Namespace 부분이 빔", model.AssetRef{Kind: model.KindNICPort, Canonical: ":x"}, false},
		{"Canonical의 Value 부분이 빔", model.AssetRef{Kind: model.KindNICPort, Canonical: "pci-bdf:"}, false},
		{"Canonical이 콜론뿐", model.AssetRef{Kind: model.KindNICPort, Canonical: ":"}, false},
		{"유효한 Canonical (콜론 1개)", model.AssetRef{Kind: model.KindPod, Canonical: "kubernetes-pod-uid:9f1c"}, true},
		{"유효한 Canonical (value에 콜론 포함)", model.AssetRef{Kind: model.KindNICPort, Canonical: canonicalString}, true},
		// Namespace ":0000", Value "af:00.0"의 유효한 model TypedID 렌더링이다.
		{"유효한 Canonical (namespace와 value에 콜론 포함)", model.AssetRef{Kind: model.KindNICPort, Canonical: ":0000:af:00.0"}, true},
		{"Aliases가 nil이어도 유효", model.AssetRef{Kind: model.KindPod, Canonical: "kubernetes-pod-uid:9f1c", Aliases: nil}, true},
		{"Aliases가 빈 슬라이스여도 유효", model.AssetRef{
			Kind:      model.KindPod,
			Canonical: "kubernetes-pod-uid:9f1c",
			Aliases:   []model.TypedID{},
		}, true},
		{"Alias가 무효", model.AssetRef{
			Kind:      model.KindPod,
			Canonical: "kubernetes-pod-uid:9f1c",
			Aliases:   []model.TypedID{{Namespace: "lldp-port-id"}},
		}, false},
		{"Alias가 유효", model.AssetRef{
			Kind:      model.KindPod,
			Canonical: "kubernetes-pod-uid:9f1c",
			Aliases:   []model.TypedID{{Namespace: "lldp-port-id", Value: "Ethernet1/1"}},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ref.Validate()
			if tc.wantValid {
				requireNoErr(t, err, "AssetRef.Validate()")
				return
			}
			requireErrInvalid(t, err, "AssetRef.Validate()")
		})
	}
}

// MDL-005: 생성자가 반환한 값의 Validate()는 항상 nil이다.
func TestMDL005_ConstructorOutputAlwaysValidates(t *testing.T) {
	id, err := model.NewTypedID(string(model.NamespacePCIBDF), canonicalValue)
	requireNoErr(t, err, "NewTypedID")
	if err := id.Validate(); err != nil {
		t.Errorf("NewTypedID 반환값의 Validate() = %v, want nil", err)
	}

	ref, err := model.NewAssetRef(model.KindNICPort, id,
		mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1"))
	requireNoErr(t, err, "NewAssetRef")
	if err := ref.Validate(); err != nil {
		t.Errorf("NewAssetRef 반환값의 Validate() = %v, want nil", err)
	}

	obs, err := model.NewObservation(validObservationInput(t))
	requireNoErr(t, err, "NewObservation")
	if err := obs.Validate(); err != nil {
		t.Errorf("NewObservation 반환값의 Validate() = %v, want nil", err)
	}

	fnd, err := model.NewFinding(validFindingInput(t))
	requireNoErr(t, err, "NewFinding")
	if err := fnd.Validate(); err != nil {
		t.Errorf("NewFinding 반환값의 Validate() = %v, want nil", err)
	}

	// 반환값을 다시 생성자에 넣어도 유효하다 (멱등).
	obs2, err := model.NewObservation(obs)
	requireNoErr(t, err, "NewObservation(반환값 재투입)")
	if err := obs2.Validate(); err != nil {
		t.Errorf("재투입 결과의 Validate() = %v, want nil", err)
	}
	if !reflect.DeepEqual(obs, obs2) {
		t.Errorf("생성자가 멱등하지 않다:\n%#v\n%#v", obs, obs2)
	}

	fnd2, err := model.NewFinding(fnd)
	requireNoErr(t, err, "NewFinding(반환값 재투입)")
	if !reflect.DeepEqual(fnd, fnd2) {
		t.Errorf("생성자가 멱등하지 않다:\n%#v\n%#v", fnd, fnd2)
	}
}

// MDL-005 (v1.2): Validate()를 제공해야 하는 9종 타입이 모두 그 메서드를 갖는다
// (v1.2에서 SourceRef가 추가되었다). 컴파일이 이 요구사항의 일부를 검증한다.
func TestMDL005_ValidateExistsOnAllRequiredTypes(t *testing.T) {
	var (
		id  model.TypedID
		ref model.AssetRef
		src model.SourceRef
		obs model.Observation
		fnd model.Finding
		ev  model.EvidenceRef
		im  model.ImpactRef
		sa  model.Sample
		co  model.Condition
	)

	validators := []struct {
		name string
		err  error
	}{
		{"TypedID", id.Validate()},
		{"AssetRef", ref.Validate()},
		{"SourceRef", src.Validate()},
		{"Observation", obs.Validate()},
		{"Finding", fnd.Validate()},
		{"EvidenceRef", ev.Validate()},
		{"ImpactRef", im.Validate()},
		{"Sample", sa.Validate()},
		{"Condition", co.Validate()},
	}
	if len(validators) != 9 {
		t.Fatalf("MDL-005는 9종을 요구한다 (got %d)", len(validators))
	}
	for _, v := range validators {
		t.Run(v.name, func(t *testing.T) {
			// 9종 모두 zero value는 불변식 위반이므로 ErrInvalid여야 한다.
			requireErrInvalid(t, v.err, v.name+".Validate()")
		})
	}
}
