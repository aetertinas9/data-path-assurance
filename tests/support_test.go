package tests

import (
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// MDL-080: PartitionKey는 문자열 기반 타입이다.
func TestMDL080_PartitionKeyIsStringBased(t *testing.T) {
	var pk model.PartitionKey = "rack-01"
	if string(pk) != "rack-01" {
		t.Errorf("string(PartitionKey) = %q, want %q", string(pk), "rack-01")
	}

	var zero model.PartitionKey
	if string(zero) != "" {
		t.Errorf("zero PartitionKey = %q, want 빈 문자열", string(zero))
	}
	if converted := model.PartitionKey("node-a/pcie0"); string(converted) != "node-a/pcie0" {
		t.Errorf("PartitionKey 변환 결과 = %q, want %q", string(converted), "node-a/pcie0")
	}
}

// MDL-080 (v1.2 직접 검증): 빈 PartitionKey는 무효, 그 외는 유효.
// 3절 서두에 따라 "비어 있을 수 없다"는 길이 0 금지이므로 공백만인 값은 유효하다.
func TestMDL080_PartitionKeyIsValid(t *testing.T) {
	cases := []struct {
		name      string
		key       model.PartitionKey
		wantValid bool
	}{
		{"빈 문자열", model.PartitionKey(""), false},
		{"한 글자", model.PartitionKey("a"), true},
		{"rack 파티션", model.PartitionKey("rack-01"), true},
		{"경로 형태", model.PartitionKey("node-a/pcie0"), true},
		{"콜론 포함", model.PartitionKey("site:row:rack"), true},
		{"공백 한 칸도 비어 있지 않다", model.PartitionKey(" "), true},
		{"비-ASCII", model.PartitionKey("랙-01"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.key.IsValid(); got != tc.wantValid {
				t.Errorf("PartitionKey(%q).IsValid() = %v, want %v", string(tc.key), got, tc.wantValid)
			}
		})
	}
}

// MDL-080 (v1.2): zero value PartitionKey는 빈 문자열이므로 무효다.
func TestMDL080_ZeroPartitionKeyIsInvalid(t *testing.T) {
	var zero model.PartitionKey
	if zero.IsValid() {
		t.Errorf("zero value PartitionKey의 IsValid()는 거짓이어야 한다")
	}
	mustNotPanic(t, "zero PartitionKey의 String()", func() { _ = zero.String() })
}

// MDL-080 (v1.2): PartitionKey.String()은 기저 문자열을 그대로 돌려준다.
// (무효한 값에 대해서도 panic하지 않는다.)
func TestMDL080_PartitionKeyString(t *testing.T) {
	values := []string{"", "a", "rack-01", "node-a/pcie0", " ", "랙-01"}
	for _, s := range values {
		t.Run(s, func(t *testing.T) {
			var got string
			mustNotPanic(t, "PartitionKey.String()", func() { got = model.PartitionKey(s).String() })
			if got != s {
				t.Errorf("PartitionKey(%q).String() = %q, want %q", s, got, s)
			}
		})
	}
}

// MDL-090: Sample.Timestamp가 zero time이면 Validate()는 ErrInvalid.
func TestMDL090_SampleValidate(t *testing.T) {
	cases := []struct {
		name      string
		sample    model.Sample
		wantValid bool
	}{
		{"zero value", model.Sample{}, false},
		{"Timestamp만 zero", model.Sample{Timestamp: zeroTime, Value: 1.5, Labels: map[string]string{"lane": "0"}}, false},
		{"Timestamp 있음, Labels nil", model.Sample{Timestamp: tObserved, Value: 1.5}, true},
		{"Timestamp 있음, Labels 빈 맵", model.Sample{Timestamp: tObserved, Value: 0, Labels: map[string]string{}}, true},
		{"Timestamp 있음, Value 0", model.Sample{Timestamp: tObserved, Value: 0}, true},
		{"Timestamp 있음, Value 음수", model.Sample{Timestamp: tObserved, Value: -12.5}, true},
		{"Timestamp 있음, Labels 채움", model.Sample{
			Timestamp: tReceived,
			Value:     42,
			Labels:    map[string]string{"port": "Ethernet1/1", "lane": "3"},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.sample.Validate()
			if tc.wantValid {
				requireNoErr(t, err, "Sample.Validate()")
				return
			}
			requireErrInvalid(t, err, "Sample.Validate()")
		})
	}
}

// MDL-100: Condition의 Type·Reason이 비거나, Status가 무효하거나,
// LastTransitionTime이 zero time이면 Validate()는 ErrInvalid.
func TestMDL100_ConditionValidate(t *testing.T) {
	var zeroStatus model.ConditionStatus

	valid := model.Condition{
		Type:               "FabricPathHealthy",
		Status:             model.ConditionTrue,
		Reason:             "AllLinksUp",
		Message:            "모든 물리 링크가 정상이다",
		LastTransitionTime: tTransition,
	}

	cases := []struct {
		name      string
		mutate    func(c *model.Condition)
		wantValid bool
	}{
		{"모두 채워짐", func(c *model.Condition) {}, true},
		{"Message가 비어도 유효", func(c *model.Condition) { c.Message = "" }, true},
		{"Status가 False여도 유효", func(c *model.Condition) { c.Status = model.ConditionFalse }, true},
		{"Status가 Unknown이어도 유효", func(c *model.Condition) { c.Status = model.ConditionUnknown }, true},
		{"도메인 접두어 없는 Type도 유효 (모델은 강제하지 않는다)", func(c *model.Condition) {
			c.Type = "Ready"
		}, true},
		{"도메인 접두어 있는 Type도 유효", func(c *model.Condition) {
			c.Type = "assurance.dpa.io/FabricPathHealthy"
		}, true},
		{"Type이 빔", func(c *model.Condition) { c.Type = "" }, false},
		{"Reason이 빔", func(c *model.Condition) { c.Reason = "" }, false},
		{"Type·Reason 둘 다 빔", func(c *model.Condition) {
			c.Type = ""
			c.Reason = ""
		}, false},
		{"Status가 zero value(무효)", func(c *model.Condition) { c.Status = zeroStatus }, false},
		{"Status가 열거 밖 값", func(c *model.Condition) {
			c.Status = model.ConditionStatus(outOfEnumA)
		}, false},
		{"LastTransitionTime이 zero time", func(c *model.Condition) { c.LastTransitionTime = zeroTime }, false},
		// 3절 서두(v1.2): "비어 있을 수 없다"는 길이 0 금지 — 공백만인 문자열은 유효.
		{"공백만인 Type·Reason도 유효", func(c *model.Condition) {
			c.Type = " "
			c.Reason = " "
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.mutate(&c)

			err := c.Validate()
			if tc.wantValid {
				requireNoErr(t, err, "Condition.Validate()")
				return
			}
			requireErrInvalid(t, err, "Condition.Validate()")
		})
	}
}

// MDL-100: zero value Condition은 무효다.
func TestMDL100_ZeroConditionIsInvalid(t *testing.T) {
	var c model.Condition
	requireErrInvalid(t, c.Validate(), "zero Condition.Validate()")
}

// MDL-090/MDL-100: 시각 비교는 고정 리터럴만 쓰며, time.Time의 zero 판정이
// 위치(Location)에 좌우되지 않아야 한다 — UTC가 아닌 고정 지역시각도 유효하다.
func TestMDL090_SampleAcceptsNonUTCFixedTimestamp(t *testing.T) {
	seoul := time.FixedZone("KST", 9*60*60)
	s := model.Sample{
		Timestamp: time.Date(2026, time.August, 5, 21, 0, 0, 0, seoul),
		Value:     1,
	}
	requireNoErr(t, s.Validate(), "Sample.Validate()(비-UTC 고정 시각)")
}
