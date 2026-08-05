package tests

import (
	"reflect"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// MDL-060: 필수 불변식을 하나라도 위반하면 ErrInvalid.
func TestMDL060_NewFindingRejectsInvalidInput(t *testing.T) {
	// 클로저 안에서 부모 t를 쓰지 않도록 픽스처를 미리 만들어 둔다.
	podAsset := mustAssetRef(t, model.KindPod,
		mustTypedID(t, string(model.NamespaceKubernetesPodUID), "9f1c-pod-uid"))

	cases := []struct {
		name   string
		mutate func(f *model.Finding)
	}{
		{"ID가 빔", func(f *model.Finding) { f.ID = "" }},
		{"Type이 zero value(무효)", func(f *model.Finding) {
			var zeroType model.FindingType
			f.Type = zeroType
		}},
		{"Scope가 nil", func(f *model.Finding) { f.Scope = nil }},
		{"Scope가 빈 슬라이스", func(f *model.Finding) { f.Scope = []model.AssetRef{} }},
		{"Scope에 zero AssetRef 포함", func(f *model.Finding) {
			f.Scope = []model.AssetRef{{}}
		}},
		{"Scope의 뒤쪽 항목이 무효", func(f *model.Finding) {
			f.Scope = append(f.Scope, model.AssetRef{Kind: model.KindNICPort, Canonical: ""})
		}},
		{"Scope 항목의 Canonical이 TypedID 형식이 아님", func(f *model.Finding) {
			f.Scope = []model.AssetRef{{Kind: model.KindNICPort, Canonical: "구분자없음"}}
		}},
		{"Severity가 zero value(무효)", func(f *model.Finding) {
			var zeroSeverity model.Severity
			f.Severity = zeroSeverity
		}},
		{"Confidence가 zero value(무효)", func(f *model.Finding) {
			var zeroConfidence model.Confidence
			f.Confidence = zeroConfidence
		}},
		{"State가 zero value(무효)", func(f *model.Finding) {
			var zeroState model.FindingState
			f.State = zeroState
		}},
		{"FirstSeen이 zero time", func(f *model.Finding) { f.FirstSeen = zeroTime }},
		{"LastSeen이 zero time", func(f *model.Finding) { f.LastSeen = zeroTime }},
		{"FirstSeen·LastSeen 둘 다 zero time", func(f *model.Finding) {
			f.FirstSeen = zeroTime
			f.LastSeen = zeroTime
		}},
		{"LastSeen이 FirstSeen보다 이름", func(f *model.Finding) {
			f.FirstSeen = tLastSeen
			f.LastSeen = tFirstSeen
		}},
		{"LastSeen이 FirstSeen보다 1ns 이름", func(f *model.Finding) {
			f.LastSeen = f.FirstSeen.Add(-time.Nanosecond)
		}},
		{"Explanation이 빔", func(f *model.Finding) { f.Explanation = "" }},
		// v1.2 추가: 컬렉션 원소의 유효성 (MDL-063·033·064 기준).
		{"Evidence에 ObservationID가 빈 항목", func(f *model.Finding) {
			f.Evidence = []model.EvidenceRef{{ObservationID: ""}}
		}},
		{"Evidence 뒤쪽 항목이 무효", func(f *model.Finding) {
			f.Evidence = []model.EvidenceRef{
				{ObservationID: "obs-0001"},
				{ObservationID: "", Summary: "요약만 있다"},
			}
		}},
		{"MissingInputs에 빈 SignalRef", func(f *model.Finding) {
			f.MissingInputs = []model.SignalRef{model.SignalRef("")}
		}},
		{"MissingInputs에 형식 위반 SignalRef", func(f *model.Finding) {
			f.MissingInputs = []model.SignalRef{model.SignalRef("Fabric.Port")}
		}},
		{"MissingInputs 뒤쪽 항목이 세그먼트 1개", func(f *model.Finding) {
			f.MissingInputs = []model.SignalRef{
				model.SignalRef("pcie.link.width_current"),
				model.SignalRef("fabric"),
			}
		}},
		{"Affected에 zero ImpactRef", func(f *model.Finding) {
			f.Affected = []model.ImpactRef{{}}
		}},
		{"Affected 항목의 Asset이 무효", func(f *model.Finding) {
			f.Affected = []model.ImpactRef{{Asset: model.AssetRef{}, Accuracy: model.ImpactMapped}}
		}},
		{"Affected 항목의 Accuracy가 무효(zero value)", func(f *model.Finding) {
			var zeroAccuracy model.ImpactAccuracy
			f.Affected = []model.ImpactRef{{Asset: podAsset, Accuracy: zeroAccuracy}}
		}},
		{"Affected 항목의 Accuracy가 열거 밖 값", func(f *model.Finding) {
			f.Affected = []model.ImpactRef{{
				Asset:    podAsset,
				Accuracy: model.ImpactAccuracy(outOfEnumA),
			}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validFindingInput(t)
			tc.mutate(&in)

			got, err := model.NewFinding(in)
			requireErrInvalid(t, err, "NewFinding")
			// MDL-003: 오류 시 zero value를 반환한다.
			if !reflect.DeepEqual(got, model.Finding{}) {
				t.Errorf("오류 시 zero Finding을 기대했으나 %#v를 받았다", got)
			}
			// MDL-005: 손으로 조립한 불변식 위반 값은 Validate()도 ErrInvalid.
			requireErrInvalid(t, in.Validate(), "Finding.Validate()")
		})
	}
}

// MDL-060(역): 유효한 입력은 통과하고 필드가 보존된다.
func TestMDL060_NewFindingAcceptsValidInput(t *testing.T) {
	in := validFindingInput(t)
	got, err := model.NewFinding(in)
	requireNoErr(t, err, "NewFinding")

	if got.ID != in.ID {
		t.Errorf("ID = %q, want %q", got.ID, in.ID)
	}
	if got.Type != in.Type {
		t.Errorf("Type = %q, want %q", got.Type.String(), in.Type.String())
	}
	if len(got.Scope) != len(in.Scope) {
		t.Fatalf("len(Scope) = %d, want %d", len(got.Scope), len(in.Scope))
	}
	if got.Scope[0].Key() != in.Scope[0].Key() {
		t.Errorf("Scope[0].Key() = %q, want %q", got.Scope[0].Key(), in.Scope[0].Key())
	}
	if got.Severity != in.Severity {
		t.Errorf("Severity = %q, want %q", got.Severity.String(), in.Severity.String())
	}
	if got.Confidence != in.Confidence {
		t.Errorf("Confidence = %q, want %q", got.Confidence.String(), in.Confidence.String())
	}
	if got.State != in.State {
		t.Errorf("State = %q, want %q", got.State.String(), in.State.String())
	}
	if !reflect.DeepEqual(got.Evidence, in.Evidence) {
		t.Errorf("Evidence = %#v, want %#v", got.Evidence, in.Evidence)
	}
	if len(got.Affected) != len(in.Affected) {
		t.Errorf("len(Affected) = %d, want %d", len(got.Affected), len(in.Affected))
	}
	if !got.FirstSeen.Equal(in.FirstSeen) {
		t.Errorf("FirstSeen = %v, want %v", got.FirstSeen, in.FirstSeen)
	}
	if !got.LastSeen.Equal(in.LastSeen) {
		t.Errorf("LastSeen = %v, want %v", got.LastSeen, in.LastSeen)
	}
	if got.Explanation != in.Explanation {
		t.Errorf("Explanation = %q, want %q", got.Explanation, in.Explanation)
	}
	if got.SuggestedStep != in.SuggestedStep {
		t.Errorf("SuggestedStep = %q, want %q", got.SuggestedStep, in.SuggestedStep)
	}
	// 3.3: 반환값의 Validate()는 nil이다.
	if err := got.Validate(); err != nil {
		t.Errorf("생성자 반환값의 Validate() = %v, want nil", err)
	}
}

// MDL-060 경계: LastSeen == FirstSeen은 "이르지 않으므로" 유효하다.
func TestMDL060_LastSeenEqualToFirstSeenIsValid(t *testing.T) {
	in := validFindingInput(t)
	in.FirstSeen = tFirstSeen
	in.LastSeen = tFirstSeen

	got, err := model.NewFinding(in)
	requireNoErr(t, err, "NewFinding(LastSeen == FirstSeen)")
	if err := got.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// MDL-060 경계: LastSeen이 FirstSeen보다 1ns 뒤면 유효하다.
func TestMDL060_LastSeenOneNanosecondAfterFirstSeenIsValid(t *testing.T) {
	in := validFindingInput(t)
	in.FirstSeen = tFirstSeen
	in.LastSeen = tFirstSeen.Add(time.Nanosecond)

	_, err := model.NewFinding(in)
	requireNoErr(t, err, "NewFinding(LastSeen = FirstSeen+1ns)")
}

// MDL-060: 선택 필드(SuggestedStep·Affected)는 비어 있어도 유효하다.
func TestMDL060_OptionalFieldsMayBeEmpty(t *testing.T) {
	in := validFindingInput(t)
	in.SuggestedStep = ""
	in.Affected = nil

	got, err := model.NewFinding(in)
	requireNoErr(t, err, "NewFinding(선택 필드 비움)")
	if err := got.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// MDL-060: Scope 항목이 여러 개여도 모두 유효하면 통과한다.
func TestMDL060_ScopeWithMultipleValidEntries(t *testing.T) {
	in := validFindingInput(t)
	in.Scope = []model.AssetRef{
		mustAssetRef(t, model.KindSwitchPort, mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")),
		mustAssetRef(t, model.KindNICPort, canonicalTypedID(t)),
		mustAssetRef(t, model.KindPhysicalLink, mustTypedID(t, "cable", "rack1-link-7")),
	}

	got, err := model.NewFinding(in)
	requireNoErr(t, err, "NewFinding(다중 Scope)")
	if len(got.Scope) != 3 {
		t.Errorf("len(Scope) = %d, want 3", len(got.Scope))
	}
}

// MDL-061: Evidence와 MissingInputs가 둘 다 비어 있으면 ErrInvalid.
func TestMDL061_EvidenceAndMissingInputsCannotBothBeEmpty(t *testing.T) {
	cases := []struct {
		name      string
		wantValid bool
		mutate    func(f *model.Finding)
	}{
		{"둘 다 nil", false, func(f *model.Finding) {
			f.Evidence = nil
			f.MissingInputs = nil
		}},
		{"둘 다 길이 0인 비-nil 슬라이스", false, func(f *model.Finding) {
			f.Evidence = []model.EvidenceRef{}
			f.MissingInputs = []model.SignalRef{}
		}},
		{"Evidence nil + MissingInputs 빈 슬라이스", false, func(f *model.Finding) {
			f.Evidence = nil
			f.MissingInputs = []model.SignalRef{}
		}},
		{"Evidence 빈 슬라이스 + MissingInputs nil", false, func(f *model.Finding) {
			f.Evidence = []model.EvidenceRef{}
			f.MissingInputs = nil
		}},
		{"Evidence만 있음", true, func(f *model.Finding) {
			f.Evidence = []model.EvidenceRef{{ObservationID: "obs-0001"}}
			f.MissingInputs = nil
		}},
		{"MissingInputs만 있음", true, func(f *model.Finding) {
			f.Evidence = nil
			f.MissingInputs = []model.SignalRef{model.SignalRef("fabric.port.fec.corrected_rate")}
		}},
		{"둘 다 있음", true, func(f *model.Finding) {
			f.Evidence = []model.EvidenceRef{{ObservationID: "obs-0001", Summary: "요약"}}
			f.MissingInputs = []model.SignalRef{model.SignalRef("pcie.link.width_current")}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validFindingInput(t)
			tc.mutate(&in)

			got, err := model.NewFinding(in)
			if tc.wantValid {
				requireNoErr(t, err, "NewFinding")
				if err := got.Validate(); err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}
			requireErrInvalid(t, err, "NewFinding")
			if !reflect.DeepEqual(got, model.Finding{}) {
				t.Errorf("오류 시 zero Finding을 기대했으나 %#v를 받았다", got)
			}
			// MDL-005: 손으로 조립한 위반 값은 Validate()도 ErrInvalid.
			requireErrInvalid(t, in.Validate(), "Finding.Validate()")
		})
	}
}

// MDL-062: FindingType 4종의 String()과 열거 외 값의 IsValid().
func TestMDL062_FindingTypeStringAndIsValid(t *testing.T) {
	cases := []struct {
		ft   model.FindingType
		want string
	}{
		{model.FindingPhysicalPeerMismatch, "PHYSICAL_PEER_MISMATCH"},
		{model.FindingOpticalPathDegrading, "OPTICAL_PATH_DEGRADING"},
		{model.FindingPCIeLinkWidthDegraded, "PCIE_LINK_WIDTH_DEGRADED"},
		{model.FindingIdentityConflict, "IDENTITY_CONFLICT"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.ft.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
			if !tc.ft.IsValid() {
				t.Errorf("열거된 FindingType의 IsValid()는 참이어야 한다 (%q)", tc.want)
			}
		})
	}

	// 열거 외 값(zero value)의 IsValid()는 거짓이다.
	var zeroType model.FindingType
	if zeroType.IsValid() {
		t.Errorf("zero value FindingType의 IsValid()는 거짓이어야 한다")
	}

	// 4종은 서로 구별된다.
	seen := map[string]bool{}
	for _, tc := range cases {
		if seen[tc.ft.String()] {
			t.Errorf("FindingType String()이 중복되었다: %q", tc.ft.String())
		}
		seen[tc.ft.String()] = true
	}
}

// MDL-063: EvidenceRef.ObservationID가 비면 ErrInvalid, Summary는 비어도 유효.
func TestMDL063_EvidenceRefValidate(t *testing.T) {
	cases := []struct {
		name      string
		ref       model.EvidenceRef
		wantValid bool
	}{
		{"zero value", model.EvidenceRef{}, false},
		{"ObservationID만 빔", model.EvidenceRef{ObservationID: "", Summary: "요약은 있다"}, false},
		{"ObservationID 있음, Summary 빔", model.EvidenceRef{ObservationID: "obs-0001"}, true},
		{"둘 다 있음", model.EvidenceRef{ObservationID: "obs-0001", Summary: "사람이 읽는 한 줄"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ref.Validate()
			if tc.wantValid {
				requireNoErr(t, err, "EvidenceRef.Validate()")
				return
			}
			requireErrInvalid(t, err, "EvidenceRef.Validate()")
		})
	}
}

// MDL-064: ImpactRef의 Asset 또는 Accuracy가 무효하면 ErrInvalid.
func TestMDL064_ImpactRefValidate(t *testing.T) {
	validAsset := mustAssetRef(t, model.KindPod,
		mustTypedID(t, string(model.NamespaceKubernetesPodUID), "9f1c-pod-uid"))
	var zeroAccuracy model.ImpactAccuracy
	var zeroKind model.AssetKind

	cases := []struct {
		name      string
		ref       model.ImpactRef
		wantValid bool
	}{
		{"zero value", model.ImpactRef{}, false},
		{"Asset 무효 + Accuracy 유효", model.ImpactRef{Asset: model.AssetRef{}, Accuracy: model.ImpactMapped}, false},
		{"Asset의 Kind만 무효", model.ImpactRef{
			Asset:    model.AssetRef{Kind: zeroKind, Canonical: canonicalString},
			Accuracy: model.ImpactConfirmed,
		}, false},
		{"Asset의 Canonical만 무효", model.ImpactRef{
			Asset:    model.AssetRef{Kind: model.KindPod, Canonical: ""},
			Accuracy: model.ImpactConfirmed,
		}, false},
		{"Asset 유효 + Accuracy 무효(zero value)", model.ImpactRef{Asset: validAsset, Accuracy: zeroAccuracy}, false},
		{"Asset 유효 + Accuracy 열거 밖 값", model.ImpactRef{
			Asset:    validAsset,
			Accuracy: model.ImpactAccuracy(outOfEnumA),
		}, false},
		{"Asset의 alias가 무효", model.ImpactRef{
			Asset: model.AssetRef{
				Kind:      model.KindPod,
				Canonical: "kubernetes-pod-uid:9f1c",
				Aliases:   []model.TypedID{{Namespace: "lldp-port-id", Value: ""}},
			},
			Accuracy: model.ImpactMapped,
		}, false},
		{"둘 다 유효 (Confirmed)", model.ImpactRef{Asset: validAsset, Accuracy: model.ImpactConfirmed}, true},
		{"둘 다 유효 (Mapped)", model.ImpactRef{Asset: validAsset, Accuracy: model.ImpactMapped}, true},
		{"둘 다 유효 (Inferred)", model.ImpactRef{Asset: validAsset, Accuracy: model.ImpactInferred}, true},
		{"둘 다 유효 (Unknown)", model.ImpactRef{Asset: validAsset, Accuracy: model.ImpactUnknown}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ref.Validate()
			if tc.wantValid {
				requireNoErr(t, err, "ImpactRef.Validate()")
				return
			}
			requireErrInvalid(t, err, "ImpactRef.Validate()")
		})
	}
}

// MDL-060 (v1.2): Evidence·MissingInputs·Affected 각 컬렉션의 **원소 유효성**이
// NewFinding과 Finding.Validate() 양쪽에서 동일하게 강제된다.
// 원소 판정 기준은 각각 MDL-063 / MDL-033 형식 / MDL-064다.
func TestMDL060_CollectionElementValidity(t *testing.T) {
	validAsset := mustAssetRef(t, model.KindPod,
		mustTypedID(t, string(model.NamespaceKubernetesPodUID), "9f1c-pod-uid"))

	t.Run("Evidence", func(t *testing.T) {
		cases := []struct {
			name      string
			evidence  []model.EvidenceRef
			wantValid bool
		}{
			{"유효 항목 1개", []model.EvidenceRef{{ObservationID: "obs-1"}}, true},
			{"유효 항목 2개", []model.EvidenceRef{
				{ObservationID: "obs-1"},
				{ObservationID: "obs-2", Summary: "요약"},
			}, true},
			{"Summary가 비어도 유효", []model.EvidenceRef{{ObservationID: "obs-1", Summary: ""}}, true},
			{"ObservationID가 빈 항목 1개", []model.EvidenceRef{{ObservationID: ""}}, false},
			{"앞 항목만 무효", []model.EvidenceRef{
				{ObservationID: ""},
				{ObservationID: "obs-2"},
			}, false},
			{"뒤 항목만 무효", []model.EvidenceRef{
				{ObservationID: "obs-1"},
				{ObservationID: "", Summary: "요약만"},
			}, false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				in := validFindingInput(t)
				in.Evidence = tc.evidence
				in.MissingInputs = nil

				// 전제: 각 원소의 Validate()가 기대와 맞는지 확인 (MDL-063).
				for i, e := range tc.evidence {
					if (e.Validate() == nil) != (e.ObservationID != "") {
						t.Fatalf("전제 불일치: Evidence[%d].Validate()", i)
					}
				}

				requireValidity(t, in.Validate(), tc.wantValid, "Finding.Validate()")
				_, err := model.NewFinding(in)
				requireValidity(t, err, tc.wantValid, "NewFinding")
			})
		}
	})

	t.Run("MissingInputs", func(t *testing.T) {
		cases := []struct {
			name      string
			missing   []model.SignalRef
			wantValid bool
		}{
			{"유효 항목 1개", []model.SignalRef{"fabric.port.fec.corrected_rate"}, true},
			{"유효 항목 2개", []model.SignalRef{"a.b", "pcie.link.width_current"}, true},
			{"빈 SignalRef 1개", []model.SignalRef{""}, false},
			{"대문자 포함", []model.SignalRef{"Fabric.Port"}, false},
			{"세그먼트 1개", []model.SignalRef{"fabric"}, false},
			{"하이픈 포함", []model.SignalRef{"fabric-port.rate"}, false},
			{"앞 항목만 무효", []model.SignalRef{"fabric", "a.b"}, false},
			{"뒤 항목만 무효", []model.SignalRef{"a.b", "fabric.."}, false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				in := validFindingInput(t)
				in.Evidence = nil
				in.MissingInputs = tc.missing

				// 전제: 각 원소의 IsValid()가 기대와 맞는지 확인 (MDL-033).
				allValid := true
				for _, s := range tc.missing {
					if !s.IsValid() {
						allValid = false
					}
				}
				if allValid != tc.wantValid {
					t.Fatalf("전제 불일치: SignalRef.IsValid() 집계 = %v, want %v", allValid, tc.wantValid)
				}

				requireValidity(t, in.Validate(), tc.wantValid, "Finding.Validate()")
				_, err := model.NewFinding(in)
				requireValidity(t, err, tc.wantValid, "NewFinding")
			})
		}
	})

	t.Run("Affected", func(t *testing.T) {
		var zeroAccuracy model.ImpactAccuracy

		cases := []struct {
			name      string
			affected  []model.ImpactRef
			wantValid bool
		}{
			{"nil (선택 필드)", nil, true},
			{"빈 슬라이스", []model.ImpactRef{}, true},
			{"유효 항목 1개", []model.ImpactRef{{Asset: validAsset, Accuracy: model.ImpactMapped}}, true},
			{"유효 항목 2개", []model.ImpactRef{
				{Asset: validAsset, Accuracy: model.ImpactConfirmed},
				{Asset: validAsset, Accuracy: model.ImpactUnknown},
			}, true},
			{"zero ImpactRef", []model.ImpactRef{{}}, false},
			{"Asset이 무효", []model.ImpactRef{{Asset: model.AssetRef{}, Accuracy: model.ImpactMapped}}, false},
			{"Accuracy가 zero value", []model.ImpactRef{{Asset: validAsset, Accuracy: zeroAccuracy}}, false},
			{"Accuracy가 열거 밖 값", []model.ImpactRef{{
				Asset:    validAsset,
				Accuracy: model.ImpactAccuracy(outOfEnumA),
			}}, false},
			{"뒤 항목만 무효", []model.ImpactRef{
				{Asset: validAsset, Accuracy: model.ImpactMapped},
				{Asset: validAsset, Accuracy: zeroAccuracy},
			}, false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				in := validFindingInput(t)
				in.Affected = tc.affected

				// 전제: 각 원소의 Validate()가 기대와 맞는지 확인 (MDL-064).
				allValid := true
				for _, ir := range tc.affected {
					if ir.Validate() != nil {
						allValid = false
					}
				}
				if allValid != tc.wantValid {
					t.Fatalf("전제 불일치: ImpactRef.Validate() 집계 = %v, want %v", allValid, tc.wantValid)
				}

				requireValidity(t, in.Validate(), tc.wantValid, "Finding.Validate()")
				_, err := model.NewFinding(in)
				requireValidity(t, err, tc.wantValid, "NewFinding")
			})
		}
	})
}

// MDL-060 (v1.2): Scope 원소의 유효성도 NewFinding·Validate() 양쪽에서 강제된다
// (v1.1부터 규정되어 있었으나 양쪽 경로를 명시적으로 대조한다).
func TestMDL060_ScopeElementValidityOnBothPaths(t *testing.T) {
	cases := []struct {
		name      string
		scope     []model.AssetRef
		wantValid bool
	}{
		{"유효 항목 1개", []model.AssetRef{
			mustAssetRef(t, model.KindNICPort, canonicalTypedID(t)),
		}, true},
		{"nil", nil, false},
		{"빈 슬라이스", []model.AssetRef{}, false},
		{"zero AssetRef", []model.AssetRef{{}}, false},
		{"Kind가 열거 밖 값", []model.AssetRef{
			{Kind: model.AssetKind(outOfEnumA), Canonical: canonicalString},
		}, false},
		{"뒤 항목만 무효", []model.AssetRef{
			mustAssetRef(t, model.KindNICPort, canonicalTypedID(t)),
			{Kind: model.KindPod, Canonical: "구분자없음"},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validFindingInput(t)
			in.Scope = tc.scope

			requireValidity(t, in.Validate(), tc.wantValid, "Finding.Validate()")
			_, err := model.NewFinding(in)
			requireValidity(t, err, tc.wantValid, "NewFinding")
		})
	}
}
