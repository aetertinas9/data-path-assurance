// Package tests는 pkg/model의 블랙박스 인수 테스트다.
// specs/model/spec.md (ratified v1.1) 3절의 공개 계약만 참조한다.
package tests

import (
	"errors"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// 결정론(MDL-002)을 위해 모든 시각은 고정 리터럴이다. time.Now()는 쓰지 않는다.
var (
	tObserved   = time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	tReceived   = time.Date(2026, time.August, 5, 12, 0, 1, 0, time.UTC)
	tBefore     = time.Date(2026, time.August, 5, 11, 59, 59, 0, time.UTC)
	tExpires    = time.Date(2026, time.August, 5, 12, 5, 0, 0, time.UTC)
	tFirstSeen  = time.Date(2026, time.August, 5, 10, 0, 0, 0, time.UTC)
	tLastSeen   = time.Date(2026, time.August, 5, 10, 30, 0, 0, time.UTC)
	tTransition = time.Date(2026, time.August, 5, 9, 0, 0, 0, time.UTC)

	// zeroTime은 명시적인 zero time.Time이다.
	zeroTime time.Time
)

// 3.1 예시(MDL-011)의 canonical identity.
const (
	canonicalValue  = "0000:af:00.0"
	canonicalString = "pci-bdf:0000:af:00.0"
)

func requireErrInvalid(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: ErrInvalid 오류를 기대했으나 nil을 받았다", what)
	}
	if !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("%s: errors.Is(err, model.ErrInvalid)가 참이어야 한다 (err=%v)", what, err)
	}
}

func requireNoErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: 예상치 못한 오류: %v", what, err)
	}
}

// requireValidity는 wantValid에 따라 nil 또는 ErrInvalid를 단정한다.
func requireValidity(t *testing.T, err error, wantValid bool, what string) {
	t.Helper()
	if wantValid {
		requireNoErr(t, err, what)
		return
	}
	requireErrInvalid(t, err, what)
}

func mustNotPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: panic이 발생했다: %v", what, r)
		}
	}()
	fn()
}

func mustTypedID(t *testing.T, namespace, value string) model.TypedID {
	t.Helper()
	id, err := model.NewTypedID(namespace, value)
	requireNoErr(t, err, "NewTypedID("+namespace+", "+value+")")
	return id
}

// canonicalTypedID는 MDL-011의 예시와 같은 pci-bdf identity를 만든다.
func canonicalTypedID(t *testing.T) model.TypedID {
	t.Helper()
	return mustTypedID(t, string(model.NamespacePCIBDF), canonicalValue)
}

func mustAssetRef(t *testing.T, kind model.AssetKind, canonical model.TypedID, aliases ...model.TypedID) model.AssetRef {
	t.Helper()
	ref, err := model.NewAssetRef(kind, canonical, aliases...)
	requireNoErr(t, err, "NewAssetRef")
	return ref
}

// validObservationInput은 MDL-030·031을 모두 만족하는 *손으로 조립한* 입력이다.
// 각 테스트는 필요한 필드만 무너뜨려 쓴다.
func validObservationInput(t *testing.T) model.Observation {
	t.Helper()
	return model.Observation{
		ID:         "obs-0001",
		Source:     model.SourceRef{Type: string(model.SourceTypePrometheus), Name: "prom-main"},
		Subject:    mustAssetRef(t, model.KindNICPort, canonicalTypedID(t)),
		Signal:     model.SignalRef("fabric.port.fec.corrected_rate"),
		Value:      model.NewFloatValue(1.5),
		Unit:       "1/s",
		Dimensions: map[string]string{"lane": "0"},
		ObservedAt: tObserved,
		ReceivedAt: tReceived,
		Sequence:   42,
		Quality:    model.QualityGood,
		RawDigest:  "sha256:0123456789abcdef",
	}
}

// validFindingInput은 MDL-060·061을 모두 만족하는 *손으로 조립한* 입력이다.
func validFindingInput(t *testing.T) model.Finding {
	t.Helper()
	return model.Finding{
		ID:   "fnd-0001",
		Type: model.FindingPhysicalPeerMismatch,
		Scope: []model.AssetRef{
			mustAssetRef(t, model.KindSwitchPort, mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")),
		},
		Severity:   model.SeverityWarning,
		Confidence: model.ConfidenceMedium,
		State:      model.StateActive,
		Evidence: []model.EvidenceRef{
			{ObservationID: "obs-0001", Summary: "LLDP peer가 intent와 다르다"},
		},
		MissingInputs: nil,
		Affected: []model.ImpactRef{
			{
				Asset:    mustAssetRef(t, model.KindPod, mustTypedID(t, string(model.NamespaceKubernetesPodUID), "9f1c-pod-uid")),
				Accuracy: model.ImpactMapped,
			},
		},
		FirstSeen:     tFirstSeen,
		LastSeen:      tLastSeen,
		Explanation:   "LLDP로 관측한 peer가 intent와 불일치한다",
		SuggestedStep: "케이블 배선을 확인하라",
	}
}
