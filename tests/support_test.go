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

// --- v1.3 신설: PartitionKey.String()의 항등성 (MDL-081) ---

// MDL-081 (v1.3 신설): 임의의 PartitionKey p(유효 여부와 무관, 빈 문자열 포함)에
// 대해 String()은 string(p)를 그대로 반환한다 — 정규화·트리밍이 없다.
// 회차 2의 해석 고지 I-7이 v1.3에서 계약으로 승격되었으므로 직접 단정한다.
func TestMDL081_PartitionKeyStringIsIdentity(t *testing.T) {
	corpus := []string{
		"",                // zero value (무효)
		" ",               // 공백만 (유효)
		"  rack-01  ",     // 앞뒤 공백 — 트리밍하지 않는다
		"\track-01\n",     // 탭·개행
		"RACK-01",         // 대문자 — 소문자화하지 않는다
		"rack-01",         //
		"node-a/pcie0",    // 경로 형태
		"site:row:rack",   // 콜론
		"랙-01",            // 비-ASCII
		"key\x00with-nul", // 제어 문자
		"a",               // 한 글자
	}
	for _, s := range corpus {
		t.Run(s, func(t *testing.T) {
			pk := model.PartitionKey(s)

			var got string
			mustNotPanic(t, "PartitionKey.String()", func() { got = pk.String() })
			if got != s {
				t.Errorf("PartitionKey(%q).String() = %q, want %q (항등이어야 한다)", s, got, s)
			}
			if got != string(pk) {
				t.Errorf("String() = %q, string(PartitionKey) = %q — 둘은 같아야 한다", got, string(pk))
			}
			// 반복 호출에 안정적이다 (MDL-002).
			if second := pk.String(); second != got {
				t.Errorf("String()이 호출마다 달랐다: %q vs %q", got, second)
			}
			// 유효성과 무관하다.
			_ = pk.IsValid()
			if third := pk.String(); third != got {
				t.Errorf("IsValid() 호출 후 String()이 달라졌다: %q vs %q", got, third)
			}
		})
	}
}

// MDL-081: zero value PartitionKey의 String()은 빈 문자열이다.
func TestMDL081_ZeroPartitionKeyStringIsEmpty(t *testing.T) {
	var zero model.PartitionKey
	var got string
	mustNotPanic(t, "zero PartitionKey.String()", func() { got = zero.String() })
	if got != "" {
		t.Errorf("zero PartitionKey.String() = %q, want 빈 문자열", got)
	}
}
