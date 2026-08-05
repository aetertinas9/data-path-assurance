package tests

import (
	"testing"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// enumCase는 각 열거형이 서로 다른 Go 타입이므로 IsValid()·String()의 결과를
// 표 작성 시점에 값으로 걷어 담는다.
type enumCase struct {
	enum    string // 열거형 이름
	label   string // 상수 이름
	isValid bool   // 상수의 IsValid()
	str     string // 상수의 String()
	wantStr string // 스펙 3절 표의 문자열
}

func enumeratedConstants() []enumCase {
	return []enumCase{
		// 3.1 AssetKind — String()은 접두어 Kind를 뗀 표기.
		{"AssetKind", "KindSite", model.KindSite.IsValid(), model.KindSite.String(), "Site"},
		{"AssetKind", "KindRow", model.KindRow.IsValid(), model.KindRow.String(), "Row"},
		{"AssetKind", "KindRack", model.KindRack.IsValid(), model.KindRack.String(), "Rack"},
		{"AssetKind", "KindKubernetesNode", model.KindKubernetesNode.IsValid(), model.KindKubernetesNode.String(), "KubernetesNode"},
		{"AssetKind", "KindPCIeRootPort", model.KindPCIeRootPort.IsValid(), model.KindPCIeRootPort.String(), "PCIeRootPort"},
		{"AssetKind", "KindPCIeSwitch", model.KindPCIeSwitch.IsValid(), model.KindPCIeSwitch.String(), "PCIeSwitch"},
		{"AssetKind", "KindPCIeFunction", model.KindPCIeFunction.IsValid(), model.KindPCIeFunction.String(), "PCIeFunction"},
		{"AssetKind", "KindNICPort", model.KindNICPort.IsValid(), model.KindNICPort.String(), "NICPort"},
		{"AssetKind", "KindVF", model.KindVF.IsValid(), model.KindVF.String(), "VF"},
		{"AssetKind", "KindEthernetSwitch", model.KindEthernetSwitch.IsValid(), model.KindEthernetSwitch.String(), "EthernetSwitch"},
		{"AssetKind", "KindSwitchPort", model.KindSwitchPort.IsValid(), model.KindSwitchPort.String(), "SwitchPort"},
		{"AssetKind", "KindTransceiver", model.KindTransceiver.IsValid(), model.KindTransceiver.String(), "Transceiver"},
		{"AssetKind", "KindPhysicalLink", model.KindPhysicalLink.IsValid(), model.KindPhysicalLink.String(), "PhysicalLink"},
		{"AssetKind", "KindPod", model.KindPod.IsValid(), model.KindPod.String(), "Pod"},
		{"AssetKind", "KindContainer", model.KindContainer.IsValid(), model.KindContainer.String(), "Container"},

		// 3.2 ValueKind
		{"ValueKind", "ValueFloat", model.ValueFloat.IsValid(), model.ValueFloat.String(), "Float"},
		{"ValueKind", "ValueInt", model.ValueInt.IsValid(), model.ValueInt.String(), "Int"},
		{"ValueKind", "ValueBool", model.ValueBool.IsValid(), model.ValueBool.String(), "Bool"},
		{"ValueKind", "ValueString", model.ValueString.IsValid(), model.ValueString.String(), "String"},

		// 3.2 EvidenceQuality — zero value(QualityUnknown)도 유효한 예외.
		{"EvidenceQuality", "QualityUnknown", model.QualityUnknown.IsValid(), model.QualityUnknown.String(), "Unknown"},
		{"EvidenceQuality", "QualityGood", model.QualityGood.IsValid(), model.QualityGood.String(), "Good"},
		{"EvidenceQuality", "QualityDegraded", model.QualityDegraded.IsValid(), model.QualityDegraded.String(), "Degraded"},

		// 3.3 FindingType
		{"FindingType", "FindingPhysicalPeerMismatch", model.FindingPhysicalPeerMismatch.IsValid(), model.FindingPhysicalPeerMismatch.String(), "PHYSICAL_PEER_MISMATCH"},
		{"FindingType", "FindingOpticalPathDegrading", model.FindingOpticalPathDegrading.IsValid(), model.FindingOpticalPathDegrading.String(), "OPTICAL_PATH_DEGRADING"},
		{"FindingType", "FindingPCIeLinkWidthDegraded", model.FindingPCIeLinkWidthDegraded.IsValid(), model.FindingPCIeLinkWidthDegraded.String(), "PCIE_LINK_WIDTH_DEGRADED"},
		{"FindingType", "FindingIdentityConflict", model.FindingIdentityConflict.IsValid(), model.FindingIdentityConflict.String(), "IDENTITY_CONFLICT"},

		// 3.3 Severity
		{"Severity", "SeverityInfo", model.SeverityInfo.IsValid(), model.SeverityInfo.String(), "Info"},
		{"Severity", "SeverityWarning", model.SeverityWarning.IsValid(), model.SeverityWarning.String(), "Warning"},
		{"Severity", "SeverityCritical", model.SeverityCritical.IsValid(), model.SeverityCritical.String(), "Critical"},

		// 3.3 Confidence
		{"Confidence", "ConfidenceLow", model.ConfidenceLow.IsValid(), model.ConfidenceLow.String(), "Low"},
		{"Confidence", "ConfidenceMedium", model.ConfidenceMedium.IsValid(), model.ConfidenceMedium.String(), "Medium"},
		{"Confidence", "ConfidenceHigh", model.ConfidenceHigh.IsValid(), model.ConfidenceHigh.String(), "High"},

		// 3.3 EvidenceLevel
		{"EvidenceLevel", "LevelObserved", model.LevelObserved.IsValid(), model.LevelObserved.String(), "Observed"},
		{"EvidenceLevel", "LevelCorroborated", model.LevelCorroborated.IsValid(), model.LevelCorroborated.String(), "Corroborated"},
		{"EvidenceLevel", "LevelInferred", model.LevelInferred.IsValid(), model.LevelInferred.String(), "Inferred"},
		{"EvidenceLevel", "LevelUnknown", model.LevelUnknown.IsValid(), model.LevelUnknown.String(), "Unknown"},
		{"EvidenceLevel", "LevelOutOfScope", model.LevelOutOfScope.IsValid(), model.LevelOutOfScope.String(), "OutOfScope"},

		// 3.3 FindingState
		{"FindingState", "StateActive", model.StateActive.IsValid(), model.StateActive.String(), "Active"},
		{"FindingState", "StateResolved", model.StateResolved.IsValid(), model.StateResolved.String(), "Resolved"},

		// 3.3 ImpactAccuracy
		{"ImpactAccuracy", "ImpactConfirmed", model.ImpactConfirmed.IsValid(), model.ImpactConfirmed.String(), "Confirmed"},
		{"ImpactAccuracy", "ImpactMapped", model.ImpactMapped.IsValid(), model.ImpactMapped.String(), "Mapped"},
		{"ImpactAccuracy", "ImpactInferred", model.ImpactInferred.IsValid(), model.ImpactInferred.String(), "Inferred"},
		{"ImpactAccuracy", "ImpactUnknown", model.ImpactUnknown.IsValid(), model.ImpactUnknown.String(), "Unknown"},

		// 3.4 EdgeRelation — 대문자 스네이크 표기.
		{"EdgeRelation", "RelLocatedIn", model.RelLocatedIn.IsValid(), model.RelLocatedIn.String(), "LOCATED_IN"},
		{"EdgeRelation", "RelConnectedTo", model.RelConnectedTo.IsValid(), model.RelConnectedTo.String(), "CONNECTED_TO"},
		{"EdgeRelation", "RelUpstreamOf", model.RelUpstreamOf.IsValid(), model.RelUpstreamOf.String(), "UPSTREAM_OF"},
		{"EdgeRelation", "RelDownstreamOf", model.RelDownstreamOf.IsValid(), model.RelDownstreamOf.String(), "DOWNSTREAM_OF"},
		{"EdgeRelation", "RelMemberOf", model.RelMemberOf.IsValid(), model.RelMemberOf.String(), "MEMBER_OF"},
		{"EdgeRelation", "RelAllocatedTo", model.RelAllocatedTo.IsValid(), model.RelAllocatedTo.String(), "ALLOCATED_TO"},
		{"EdgeRelation", "RelObservedBy", model.RelObservedBy.IsValid(), model.RelObservedBy.String(), "OBSERVED_BY"},
		{"EdgeRelation", "RelIntendedToConnect", model.RelIntendedToConnect.IsValid(), model.RelIntendedToConnect.String(), "INTENDED_TO_CONNECT"},
		{"EdgeRelation", "RelSharesFailureDomainWith", model.RelSharesFailureDomainWith.IsValid(), model.RelSharesFailureDomainWith.String(), "SHARES_FAILURE_DOMAIN_WITH"},

		// 3.4 EdgeOrigin
		{"EdgeOrigin", "OriginObserved", model.OriginObserved.IsValid(), model.OriginObserved.String(), "Observed"},
		{"EdgeOrigin", "OriginIntended", model.OriginIntended.IsValid(), model.OriginIntended.String(), "Intended"},
		{"EdgeOrigin", "OriginDerived", model.OriginDerived.IsValid(), model.OriginDerived.String(), "Derived"},

		// 3.4 ConditionStatus
		{"ConditionStatus", "ConditionTrue", model.ConditionTrue.IsValid(), model.ConditionTrue.String(), "True"},
		{"ConditionStatus", "ConditionFalse", model.ConditionFalse.IsValid(), model.ConditionFalse.String(), "False"},
		{"ConditionStatus", "ConditionUnknown", model.ConditionUnknown.IsValid(), model.ConditionUnknown.String(), "Unknown"},
	}
}

// MDL-070 (a)(c): 열거된 상수는 IsValid()가 참이고 String()이 3절 표와 일치한다.
func TestMDL070_EnumeratedConstantsAreValidAndStringifyPerSpec(t *testing.T) {
	for _, tc := range enumeratedConstants() {
		t.Run(tc.enum+"/"+tc.label, func(t *testing.T) {
			if !tc.isValid {
				t.Errorf("%s.IsValid() = false, want true", tc.label)
			}
			if tc.str != tc.wantStr {
				t.Errorf("%s.String() = %q, want %q", tc.label, tc.str, tc.wantStr)
			}
		})
	}
}

// MDL-070: 같은 열거형 안에서 String()은 상수마다 서로 다르다 (String()이 식별자 역할).
func TestMDL070_StringIsUniqueWithinEachEnum(t *testing.T) {
	seen := map[string]string{} // "enum\x00str" -> label
	for _, tc := range enumeratedConstants() {
		key := tc.enum + "\x00" + tc.str
		if prev, dup := seen[key]; dup {
			t.Errorf("%s: %s와 %s의 String()이 둘 다 %q였다", tc.enum, prev, tc.label, tc.str)
		}
		seen[key] = tc.label
	}
}

// MDL-070 (b): zero value의 IsValid()는 거짓이다 — EvidenceQuality만 예외.
func TestMDL070_ZeroValuesAreInvalidExceptEvidenceQuality(t *testing.T) {
	var (
		zeroAssetKind       model.AssetKind
		zeroValueKind       model.ValueKind
		zeroEvidenceQuality model.EvidenceQuality
		zeroFindingType     model.FindingType
		zeroSeverity        model.Severity
		zeroConfidence      model.Confidence
		zeroEvidenceLevel   model.EvidenceLevel
		zeroFindingState    model.FindingState
		zeroImpactAccuracy  model.ImpactAccuracy
		zeroEdgeRelation    model.EdgeRelation
		zeroEdgeOrigin      model.EdgeOrigin
		zeroConditionStatus model.ConditionStatus
	)

	cases := []struct {
		enum      string
		isValid   bool
		wantValid bool
	}{
		{"AssetKind", zeroAssetKind.IsValid(), false},
		{"ValueKind", zeroValueKind.IsValid(), false},
		{"EvidenceQuality", zeroEvidenceQuality.IsValid(), true}, // 명시된 예외
		{"FindingType", zeroFindingType.IsValid(), false},
		{"Severity", zeroSeverity.IsValid(), false},
		{"Confidence", zeroConfidence.IsValid(), false},
		{"EvidenceLevel", zeroEvidenceLevel.IsValid(), false},
		{"FindingState", zeroFindingState.IsValid(), false},
		{"ImpactAccuracy", zeroImpactAccuracy.IsValid(), false},
		{"EdgeRelation", zeroEdgeRelation.IsValid(), false},
		{"EdgeOrigin", zeroEdgeOrigin.IsValid(), false},
		{"ConditionStatus", zeroConditionStatus.IsValid(), false},
	}
	for _, tc := range cases {
		t.Run(tc.enum, func(t *testing.T) {
			if tc.isValid != tc.wantValid {
				t.Errorf("zero value %s의 IsValid() = %v, want %v", tc.enum, tc.isValid, tc.wantValid)
			}
		})
	}
}

// MDL-070 예외: EvidenceQuality의 zero value는 QualityUnknown이고 "Unknown"을 낸다.
func TestMDL070_EvidenceQualityZeroValueIsQualityUnknown(t *testing.T) {
	var zero model.EvidenceQuality

	if zero != model.QualityUnknown {
		t.Errorf("zero value EvidenceQuality가 QualityUnknown이어야 한다 (got %q)", zero.String())
	}
	if !zero.IsValid() {
		t.Errorf("zero value EvidenceQuality의 IsValid()는 참이어야 한다")
	}
	if got := zero.String(); got != "Unknown" {
		t.Errorf("zero value EvidenceQuality의 String() = %q, want %q", got, "Unknown")
	}
}

// MDL-070 (b): EvidenceQuality를 뺀 열거형은 zero value가 열거된 상수 중 어느
// 것과도 같지 않다.
func TestMDL070_ZeroValueIsNotAnyEnumeratedConstant(t *testing.T) {
	t.Run("AssetKind", func(t *testing.T) {
		var zero model.AssetKind
		for _, k := range []model.AssetKind{
			model.KindSite, model.KindRow, model.KindRack, model.KindKubernetesNode,
			model.KindPCIeRootPort, model.KindPCIeSwitch, model.KindPCIeFunction,
			model.KindNICPort, model.KindVF, model.KindEthernetSwitch, model.KindSwitchPort,
			model.KindTransceiver, model.KindPhysicalLink, model.KindPod, model.KindContainer,
		} {
			if zero == k {
				t.Errorf("zero value AssetKind가 열거 상수 %q와 같았다", k.String())
			}
		}
	})

	t.Run("Severity", func(t *testing.T) {
		var zero model.Severity
		for _, s := range []model.Severity{model.SeverityInfo, model.SeverityWarning, model.SeverityCritical} {
			if zero == s {
				t.Errorf("zero value Severity가 열거 상수 %q와 같았다", s.String())
			}
		}
	})

	t.Run("Confidence", func(t *testing.T) {
		var zero model.Confidence
		for _, c := range []model.Confidence{model.ConfidenceLow, model.ConfidenceMedium, model.ConfidenceHigh} {
			if zero == c {
				t.Errorf("zero value Confidence가 열거 상수 %q와 같았다", c.String())
			}
		}
	})

	t.Run("FindingState", func(t *testing.T) {
		var zero model.FindingState
		for _, s := range []model.FindingState{model.StateActive, model.StateResolved} {
			if zero == s {
				t.Errorf("zero value FindingState가 열거 상수 %q와 같았다", s.String())
			}
		}
	})

	t.Run("EvidenceLevel", func(t *testing.T) {
		var zero model.EvidenceLevel
		for _, l := range []model.EvidenceLevel{
			model.LevelObserved, model.LevelCorroborated, model.LevelInferred,
			model.LevelUnknown, model.LevelOutOfScope,
		} {
			if zero == l {
				t.Errorf("zero value EvidenceLevel이 열거 상수 %q와 같았다", l.String())
			}
		}
	})

	t.Run("ImpactAccuracy", func(t *testing.T) {
		var zero model.ImpactAccuracy
		for _, a := range []model.ImpactAccuracy{
			model.ImpactConfirmed, model.ImpactMapped, model.ImpactInferred, model.ImpactUnknown,
		} {
			if zero == a {
				t.Errorf("zero value ImpactAccuracy가 열거 상수 %q와 같았다", a.String())
			}
		}
	})

	t.Run("ConditionStatus", func(t *testing.T) {
		var zero model.ConditionStatus
		for _, s := range []model.ConditionStatus{
			model.ConditionTrue, model.ConditionFalse, model.ConditionUnknown,
		} {
			if zero == s {
				t.Errorf("zero value ConditionStatus가 열거 상수 %q와 같았다", s.String())
			}
		}
	})

	t.Run("EdgeRelation", func(t *testing.T) {
		var zero model.EdgeRelation
		for _, r := range []model.EdgeRelation{
			model.RelLocatedIn, model.RelConnectedTo, model.RelUpstreamOf, model.RelDownstreamOf,
			model.RelMemberOf, model.RelAllocatedTo, model.RelObservedBy,
			model.RelIntendedToConnect, model.RelSharesFailureDomainWith,
		} {
			if zero == r {
				t.Errorf("zero value EdgeRelation이 열거 상수 %q와 같았다", r.String())
			}
		}
	})

	t.Run("EdgeOrigin", func(t *testing.T) {
		var zero model.EdgeOrigin
		for _, o := range []model.EdgeOrigin{model.OriginObserved, model.OriginIntended, model.OriginDerived} {
			if zero == o {
				t.Errorf("zero value EdgeOrigin이 열거 상수 %q와 같았다", o.String())
			}
		}
	})

	t.Run("FindingType", func(t *testing.T) {
		var zero model.FindingType
		for _, f := range []model.FindingType{
			model.FindingPhysicalPeerMismatch, model.FindingOpticalPathDegrading,
			model.FindingPCIeLinkWidthDegraded, model.FindingIdentityConflict,
		} {
			if zero == f {
				t.Errorf("zero value FindingType이 열거 상수 %q와 같았다", f.String())
			}
		}
	})

	t.Run("ValueKind", func(t *testing.T) {
		var zero model.ValueKind
		for _, k := range []model.ValueKind{
			model.ValueFloat, model.ValueInt, model.ValueBool, model.ValueString,
		} {
			if zero == k {
				t.Errorf("zero value ValueKind가 열거 상수 %q와 같았다", k.String())
			}
		}
	})
}

// --- 열거 밖(non-enumerated) 값 ---
//
// MDL-070(b)에 따라 대부분의 열거형은 zero value가 곧 열거 밖 값이다. 그러나
// EvidenceQuality만은 zero value(QualityUnknown)가 유효하므로, MDL-030의
// "Quality 무효" 케이스를 만들려면 열거 밖 값을 직접 구성할 수밖에 없다.
// 이때 기반 타입이 정수라는 가정이 필요하다 — 3절은 기반 타입을 구현자 선택으로
// 두므로 이 가정은 artifacts/discrepancy-notes-tests.md I-6에 고지해 두었다.
// 좁은 기반 타입(int8)에서도 상수 overflow가 나지 않도록 128 미만만 쓴다.
const (
	outOfEnumA = 42
	outOfEnumB = 99
)

// enumValue는 3절이 모든 열거형에 요구하는 두 메서드다.
type enumValue interface {
	String() string
	IsValid() bool
}

type nonEnumCase struct {
	enum string
	val  enumValue
}

// nonEnumeratedValues는 12개 열거형 각각의 열거 밖 값을 돌려준다.
func nonEnumeratedValues() []nonEnumCase {
	return []nonEnumCase{
		{"AssetKind", model.AssetKind(outOfEnumA)},
		{"ValueKind", model.ValueKind(outOfEnumA)},
		{"EvidenceQuality", model.EvidenceQuality(outOfEnumA)},
		{"FindingType", model.FindingType(outOfEnumA)},
		{"Severity", model.Severity(outOfEnumA)},
		{"Confidence", model.Confidence(outOfEnumA)},
		{"EvidenceLevel", model.EvidenceLevel(outOfEnumA)},
		{"FindingState", model.FindingState(outOfEnumA)},
		{"ImpactAccuracy", model.ImpactAccuracy(outOfEnumA)},
		{"EdgeRelation", model.EdgeRelation(outOfEnumA)},
		{"EdgeOrigin", model.EdgeOrigin(outOfEnumA)},
		{"ConditionStatus", model.ConditionStatus(outOfEnumA)},

		{"AssetKind(2)", model.AssetKind(outOfEnumB)},
		{"EvidenceQuality(2)", model.EvidenceQuality(outOfEnumB)},
		{"EdgeRelation(2)", model.EdgeRelation(outOfEnumB)},
	}
}

// MDL-070(d) + 3절 서두: 열거되지 않은 값은 invalid이고, String()은 계약으로
// 규정되지 않지만 어떤 입력에서도 panic해서는 안 된다.
func TestMDL070_NonEnumeratedValuesAreInvalidAndDoNotPanic(t *testing.T) {
	for _, tc := range nonEnumeratedValues() {
		t.Run(tc.enum, func(t *testing.T) {
			if tc.val.IsValid() {
				t.Errorf("열거 밖 %s의 IsValid()는 거짓이어야 한다", tc.enum)
			}
			// String()의 반환값은 비계약이다 — panic 부재만 검증한다.
			mustNotPanic(t, "열거 밖 "+tc.enum+"의 String()", func() {
				_ = tc.val.String()
			})
		})
	}
}

// MDL-070(d): 열거 밖 값의 String()을 반복 호출해도 panic 없이 안정적이다.
func TestMDL070_NonEnumeratedStringIsStable(t *testing.T) {
	for _, tc := range nonEnumeratedValues() {
		t.Run(tc.enum, func(t *testing.T) {
			var first, second string
			mustNotPanic(t, "열거 밖 "+tc.enum+"의 String() 반복 호출", func() {
				first = tc.val.String()
				second = tc.val.String()
			})
			if first != second {
				t.Errorf("String()이 호출마다 달랐다: %q vs %q", first, second)
			}
		})
	}
}
