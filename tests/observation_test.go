package tests

import (
	"reflect"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// MDL-030: 필수 불변식을 하나라도 위반하면 ErrInvalid.
func TestMDL030_NewObservationRejectsInvalidInput(t *testing.T) {
	var zeroValue model.Value

	cases := []struct {
		name   string
		mutate func(o *model.Observation)
	}{
		{"ID가 빔", func(o *model.Observation) { o.ID = "" }},
		{"Source가 zero value", func(o *model.Observation) { o.Source = model.SourceRef{} }},
		{"Source.Type이 빔", func(o *model.Observation) { o.Source.Type = "" }},
		{"Source.Name이 빔", func(o *model.Observation) { o.Source.Name = "" }},
		{"Subject가 zero value", func(o *model.Observation) { o.Subject = model.AssetRef{} }},
		{"Subject.Kind가 무효", func(o *model.Observation) {
			var zeroKind model.AssetKind
			o.Subject.Kind = zeroKind
		}},
		{"Subject.Canonical이 빔", func(o *model.Observation) { o.Subject.Canonical = "" }},
		{"Subject.Canonical이 TypedID 문자열 형식이 아님", func(o *model.Observation) {
			o.Subject.Canonical = "구분자없음"
		}},
		{"Subject의 alias가 무효", func(o *model.Observation) {
			o.Subject.Aliases = []model.TypedID{{Namespace: "lldp-port-id", Value: ""}}
		}},
		{"Signal이 빔", func(o *model.Observation) { o.Signal = model.SignalRef("") }},
		{"Signal 세그먼트가 하나뿐", func(o *model.Observation) { o.Signal = model.SignalRef("fabric") }},
		{"Value가 zero value", func(o *model.Observation) { o.Value = zeroValue }},
		{"ObservedAt이 zero time", func(o *model.Observation) { o.ObservedAt = zeroTime }},
		{"ReceivedAt이 zero time", func(o *model.Observation) { o.ReceivedAt = zeroTime }},
		{"ObservedAt·ReceivedAt 둘 다 zero time", func(o *model.Observation) {
			o.ObservedAt = zeroTime
			o.ReceivedAt = zeroTime
		}},
		// v1.2 추가: Quality 무효 (QualityUnknown은 유효하므로 열거 밖 값을 쓴다).
		{"Quality가 열거 밖 값", func(o *model.Observation) {
			o.Quality = model.EvidenceQuality(outOfEnumA)
		}},
		{"Quality가 다른 열거 밖 값", func(o *model.Observation) {
			o.Quality = model.EvidenceQuality(outOfEnumB)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validObservationInput(t)
			tc.mutate(&in)

			got, err := model.NewObservation(in)
			requireErrInvalid(t, err, "NewObservation")
			// MDL-003: 오류 시 zero value를 반환한다.
			if !reflect.DeepEqual(got, model.Observation{}) {
				t.Errorf("오류 시 zero Observation을 기대했으나 %#v를 받았다", got)
			}
			// MDL-005: 손으로 조립한 불변식 위반 값은 Validate()도 ErrInvalid.
			requireErrInvalid(t, in.Validate(), "Observation.Validate()")
		})
	}
}

// MDL-030(역): 유효한 입력은 통과하고 모든 필드가 보존된다.
func TestMDL030_NewObservationAcceptsValidInput(t *testing.T) {
	in := validObservationInput(t)
	got, err := model.NewObservation(in)
	requireNoErr(t, err, "NewObservation")

	if got.ID != in.ID {
		t.Errorf("ID = %q, want %q", got.ID, in.ID)
	}
	if got.Source != in.Source {
		t.Errorf("Source = %#v, want %#v", got.Source, in.Source)
	}
	if got.Subject.Key() != in.Subject.Key() {
		t.Errorf("Subject.Key() = %q, want %q", got.Subject.Key(), in.Subject.Key())
	}
	if got.Signal != in.Signal {
		t.Errorf("Signal = %q, want %q", string(got.Signal), string(in.Signal))
	}
	if !reflect.DeepEqual(got.Value, in.Value) {
		t.Errorf("Value = %#v, want %#v", got.Value, in.Value)
	}
	if got.Unit != in.Unit {
		t.Errorf("Unit = %q, want %q", got.Unit, in.Unit)
	}
	if !reflect.DeepEqual(got.Dimensions, in.Dimensions) {
		t.Errorf("Dimensions = %#v, want %#v", got.Dimensions, in.Dimensions)
	}
	if !got.ObservedAt.Equal(in.ObservedAt) {
		t.Errorf("ObservedAt = %v, want %v", got.ObservedAt, in.ObservedAt)
	}
	if !got.ReceivedAt.Equal(in.ReceivedAt) {
		t.Errorf("ReceivedAt = %v, want %v", got.ReceivedAt, in.ReceivedAt)
	}
	if got.Sequence != in.Sequence {
		t.Errorf("Sequence = %d, want %d", got.Sequence, in.Sequence)
	}
	if got.Quality != in.Quality {
		t.Errorf("Quality = %q, want %q", got.Quality.String(), in.Quality.String())
	}
	if got.RawDigest != in.RawDigest {
		t.Errorf("RawDigest = %q, want %q", got.RawDigest, in.RawDigest)
	}
	// 3.2: 반환값의 Validate()는 nil이다.
	if err := got.Validate(); err != nil {
		t.Errorf("생성자 반환값의 Validate() = %v, want nil", err)
	}
}

// MDL-030: 선택 필드(Unit·Dimensions·Sequence·RawDigest·ExpiresAt)는 비어 있어도 유효하다.
// Quality의 zero value(QualityUnknown)도 유효하다 (MDL-070 예외).
func TestMDL030_NewObservationAcceptsEmptyOptionalFields(t *testing.T) {
	var zeroQuality model.EvidenceQuality

	in := validObservationInput(t)
	in.Unit = ""
	in.Dimensions = nil
	in.Sequence = 0
	in.RawDigest = ""
	in.ExpiresAt = zeroTime
	in.Quality = zeroQuality

	got, err := model.NewObservation(in)
	requireNoErr(t, err, "NewObservation(선택 필드 비움)")
	if err := got.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	if got.Quality != zeroQuality {
		t.Errorf("Quality = %q, want zero value", got.Quality.String())
	}
}

// MDL-030: 4종 Value kind 모두 Observation의 Value로 유효하다 (zero Value만 무효).
func TestMDL030_NewObservationAcceptsEveryValueKind(t *testing.T) {
	cases := []struct {
		name string
		v    model.Value
	}{
		{"Float", model.NewFloatValue(0)},
		{"Int", model.NewIntValue(0)},
		{"Bool(false)", model.NewBoolValue(false)},
		{"String(빈 문자열)", model.NewStringValue("")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validObservationInput(t)
			in.Value = tc.v
			got, err := model.NewObservation(in)
			requireNoErr(t, err, "NewObservation("+tc.name+")")
			if got.Value.Kind() != tc.v.Kind() {
				t.Errorf("Kind() = %q, want %q", got.Value.Kind().String(), tc.v.Kind().String())
			}
		})
	}
}

// MDL-031: ExpiresAt 경계. zero time은 "만료 없음"으로 유효하고,
// ObservedAt보다 이르거나 *같으면* 무효다.
func TestMDL031_ExpiresAtBoundary(t *testing.T) {
	cases := []struct {
		name      string
		wantValid bool
		mutate    func(o *model.Observation)
	}{
		{"ExpiresAt이 zero time이면 만료 없음(유효)", true, func(o *model.Observation) {
			o.ExpiresAt = zeroTime
		}},
		{"ExpiresAt이 ObservedAt 이후면 유효", true, func(o *model.Observation) {
			o.ExpiresAt = tExpires
		}},
		{"ExpiresAt이 ObservedAt보다 1ns 뒤면 유효", true, func(o *model.Observation) {
			o.ExpiresAt = o.ObservedAt.Add(time.Nanosecond)
		}},
		{"ExpiresAt == ObservedAt이면 무효", false, func(o *model.Observation) {
			o.ExpiresAt = o.ObservedAt
		}},
		{"ExpiresAt이 ObservedAt보다 1ns 이르면 무효", false, func(o *model.Observation) {
			o.ExpiresAt = o.ObservedAt.Add(-time.Nanosecond)
		}},
		{"ExpiresAt이 ObservedAt보다 한참 이르면 무효", false, func(o *model.Observation) {
			o.ExpiresAt = tBefore
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validObservationInput(t)
			tc.mutate(&in)

			// MDL-031(v1.2): 같은 규칙이 NewObservation과 Validate() 양쪽에 적용된다.
			requireValidity(t, in.Validate(), tc.wantValid, "Observation.Validate()")

			got, err := model.NewObservation(in)
			if tc.wantValid {
				requireNoErr(t, err, "NewObservation")
				if err := got.Validate(); err != nil {
					t.Errorf("생성자 반환값의 Validate() = %v, want nil", err)
				}
				return
			}
			requireErrInvalid(t, err, "NewObservation")
			if !reflect.DeepEqual(got, model.Observation{}) {
				t.Errorf("오류 시 zero Observation을 기대했으나 %#v를 받았다", got)
			}
		})
	}
}

// MDL-031: ExpiresAt는 ReceivedAt이 아니라 ObservedAt과 비교된다.
// ReceivedAt보다 이르더라도 ObservedAt 이후면 유효하다.
func TestMDL031_ExpiresAtComparedAgainstObservedAtNotReceivedAt(t *testing.T) {
	in := validObservationInput(t)
	in.ObservedAt = tObserved
	in.ReceivedAt = tExpires                       // 12:05:00
	in.ExpiresAt = tObserved.Add(time.Millisecond) // 12:00:00.001 — ObservedAt 이후, ReceivedAt 이전

	got, err := model.NewObservation(in)
	requireNoErr(t, err, "NewObservation")
	if err := got.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// MDL-032: ObservedAt과 ReceivedAt 사이에 순서를 강제하지 않는다.
func TestMDL032_NoOrderingBetweenObservedAtAndReceivedAt(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(o *model.Observation)
	}{
		{"ReceivedAt이 ObservedAt 이후 (정상)", func(o *model.Observation) {
			o.ObservedAt = tObserved
			o.ReceivedAt = tReceived
		}},
		{"ReceivedAt이 ObservedAt 이전 (clock skew)", func(o *model.Observation) {
			o.ObservedAt = tObserved
			o.ReceivedAt = tBefore
		}},
		{"ReceivedAt == ObservedAt", func(o *model.Observation) {
			o.ObservedAt = tObserved
			o.ReceivedAt = tObserved
		}},
		{"ReceivedAt이 ObservedAt보다 한참 이전", func(o *model.Observation) {
			o.ObservedAt = tExpires
			o.ReceivedAt = tFirstSeen
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validObservationInput(t)
			in.ExpiresAt = zeroTime
			tc.mutate(&in)

			got, err := model.NewObservation(in)
			requireNoErr(t, err, "NewObservation (ObservedAt/ReceivedAt 순서 강제 금지)")
			if err := got.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

// validSignalRefStrings / invalidSignalRefStrings — MDL-033의 정규식
// ^[a-z0-9_]+(\.[a-z0-9_]+)+$ 에 대한 공용 코퍼스. 직접 검증(IsValid)과
// 간접 검증(NewObservation), Finding.MissingInputs 검증이 함께 쓴다.
func validSignalRefStrings() []string {
	return []string{
		"fabric.port.fec.corrected_rate",
		"a.b",
		"pcie.link.width_current",
		"a1.b2_c3",
		"0.1",
		"_a._b",
		"fabric.port.fec.corrected_rate.extra.segment",
		"aaa_bbb.ccc_ddd",
		"__.__",
		"9.9.9",
	}
}

func invalidSignalRefStrings() []string {
	return []string{
		"",
		"fabric",
		"fabric.",
		".fabric",
		".",
		"..",
		"fabric..port",
		"Fabric.Port",
		"fabric.PORT",
		"fabric-port.rate",
		"fabric port.rate",
		"fabric.port rate",
		"fabric/port.rate",
		"fabric:port.rate",
		" fabric.port",
		"fabric.port ",
		"fabric.port.fec.",
		"한글.세그먼트",
	}
}

// MDL-033 (v1.2 직접 검증): SignalRef.IsValid()가 정규식대로 판정한다.
func TestMDL033_SignalRefIsValid(t *testing.T) {
	for _, s := range validSignalRefStrings() {
		t.Run("valid/"+s, func(t *testing.T) {
			if !model.SignalRef(s).IsValid() {
				t.Errorf("SignalRef(%q).IsValid() = false, want true", s)
			}
		})
	}
	for _, s := range invalidSignalRefStrings() {
		t.Run("invalid/"+s, func(t *testing.T) {
			if model.SignalRef(s).IsValid() {
				t.Errorf("SignalRef(%q).IsValid() = true, want false", s)
			}
		})
	}
}

// MDL-033 (v1.2): zero value SignalRef는 빈 문자열이므로 무효다.
func TestMDL033_ZeroSignalRefIsInvalid(t *testing.T) {
	var zero model.SignalRef
	if zero.IsValid() {
		t.Errorf("zero value SignalRef의 IsValid()는 거짓이어야 한다")
	}
	mustNotPanic(t, "zero SignalRef의 String()", func() { _ = zero.String() })
}

// MDL-033 (v1.2): SignalRef.String()은 기저 문자열을 그대로 돌려준다.
// (무효한 값에 대해서도 panic하지 않는다.)
func TestMDL033_SignalRefString(t *testing.T) {
	all := append(validSignalRefStrings(), invalidSignalRefStrings()...)
	for _, s := range all {
		t.Run(s, func(t *testing.T) {
			var got string
			mustNotPanic(t, "SignalRef.String()", func() { got = model.SignalRef(s).String() })
			if got != s {
				t.Errorf("SignalRef(%q).String() = %q, want %q", s, got, s)
			}
		})
	}
}

// MDL-033 + MDL-030: NewObservation은 유효 형식의 Signal을 받아들인다.
func TestMDL033_SignalRefFormatAcceptsValidForms(t *testing.T) {
	for _, s := range validSignalRefStrings() {
		t.Run(s, func(t *testing.T) {
			in := validObservationInput(t)
			in.Signal = model.SignalRef(s)
			got, err := model.NewObservation(in)
			requireNoErr(t, err, "NewObservation(Signal="+s+")")
			if string(got.Signal) != s {
				t.Errorf("Signal = %q, want %q", string(got.Signal), s)
			}
		})
	}
}

// MDL-033 + MDL-030: 형식을 벗어난 SignalRef는 Observation에서도 무효다.
func TestMDL033_SignalRefFormatRejectsInvalidForms(t *testing.T) {
	for _, s := range invalidSignalRefStrings() {
		t.Run(s, func(t *testing.T) {
			in := validObservationInput(t)
			in.Signal = model.SignalRef(s)
			_, err := model.NewObservation(in)
			requireErrInvalid(t, err, "NewObservation(Signal="+s+")")
			// MDL-005: 손으로 조립한 위반 값은 Validate()도 ErrInvalid.
			requireErrInvalid(t, in.Validate(), "Observation.Validate()")
		})
	}
}

// MDL-034: Value 생성자 4종과 접근자의 대칭 동작.
func TestMDL034_ValueConstructorsAndAccessors(t *testing.T) {
	type want struct {
		kind      model.ValueKind
		kindStr   string
		floatVal  float64
		floatOK   bool
		intVal    int64
		intOK     bool
		boolVal   bool
		boolOK    bool
		stringVal string
		stringOK  bool
	}

	cases := []struct {
		name string
		v    model.Value
		want want
	}{
		{
			name: "NewFloatValue(1.5)",
			v:    model.NewFloatValue(1.5),
			want: want{kind: model.ValueFloat, kindStr: "Float", floatVal: 1.5, floatOK: true},
		},
		{
			name: "NewFloatValue(0)",
			v:    model.NewFloatValue(0),
			want: want{kind: model.ValueFloat, kindStr: "Float", floatVal: 0, floatOK: true},
		},
		{
			name: "NewFloatValue(-2.25)",
			v:    model.NewFloatValue(-2.25),
			want: want{kind: model.ValueFloat, kindStr: "Float", floatVal: -2.25, floatOK: true},
		},
		{
			name: "NewIntValue(-7)",
			v:    model.NewIntValue(-7),
			want: want{kind: model.ValueInt, kindStr: "Int", intVal: -7, intOK: true},
		},
		{
			name: "NewIntValue(0)",
			v:    model.NewIntValue(0),
			want: want{kind: model.ValueInt, kindStr: "Int", intVal: 0, intOK: true},
		},
		{
			name: "NewBoolValue(true)",
			v:    model.NewBoolValue(true),
			want: want{kind: model.ValueBool, kindStr: "Bool", boolVal: true, boolOK: true},
		},
		{
			name: "NewBoolValue(false)",
			v:    model.NewBoolValue(false),
			want: want{kind: model.ValueBool, kindStr: "Bool", boolVal: false, boolOK: true},
		},
		{
			name: "NewStringValue(up)",
			v:    model.NewStringValue("up"),
			want: want{kind: model.ValueString, kindStr: "String", stringVal: "up", stringOK: true},
		},
		{
			name: "NewStringValue(빈 문자열)",
			v:    model.NewStringValue(""),
			want: want{kind: model.ValueString, kindStr: "String", stringVal: "", stringOK: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.Kind(); got != tc.want.kind {
				t.Errorf("Kind() = %q, want %q", got.String(), tc.want.kind.String())
			}
			if got := tc.v.Kind().String(); got != tc.want.kindStr {
				t.Errorf("Kind().String() = %q, want %q", got, tc.want.kindStr)
			}
			if !tc.v.Kind().IsValid() {
				t.Errorf("생성자가 만든 Value의 Kind().IsValid()는 참이어야 한다")
			}

			if f, ok := tc.v.Float(); f != tc.want.floatVal || ok != tc.want.floatOK {
				t.Errorf("Float() = (%v, %v), want (%v, %v)", f, ok, tc.want.floatVal, tc.want.floatOK)
			}
			if i, ok := tc.v.Int(); i != tc.want.intVal || ok != tc.want.intOK {
				t.Errorf("Int() = (%v, %v), want (%v, %v)", i, ok, tc.want.intVal, tc.want.intOK)
			}
			if b, ok := tc.v.Bool(); b != tc.want.boolVal || ok != tc.want.boolOK {
				t.Errorf("Bool() = (%v, %v), want (%v, %v)", b, ok, tc.want.boolVal, tc.want.boolOK)
			}
			if s, ok := tc.v.Str(); s != tc.want.stringVal || ok != tc.want.stringOK {
				t.Errorf("Str() = (%q, %v), want (%q, %v)", s, ok, tc.want.stringVal, tc.want.stringOK)
			}
		})
	}
}

// MDL-035: zero Value는 모든 접근자가 (zero, false)를 반환하고 Kind()는 무효한 kind다.
func TestMDL035_ZeroValueAccessorsAndKind(t *testing.T) {
	var v model.Value

	if f, ok := v.Float(); f != 0 || ok {
		t.Errorf("Float() = (%v, %v), want (0, false)", f, ok)
	}
	if i, ok := v.Int(); i != 0 || ok {
		t.Errorf("Int() = (%v, %v), want (0, false)", i, ok)
	}
	if b, ok := v.Bool(); b || ok {
		t.Errorf("Bool() = (%v, %v), want (false, false)", b, ok)
	}
	if s, ok := v.Str(); s != "" || ok {
		t.Errorf("Str() = (%q, %v), want (\"\", false)", s, ok)
	}
	if v.Kind().IsValid() {
		t.Errorf("zero Value의 Kind()는 유효하지 않아야 한다 (got %q)", v.Kind().String())
	}
}

// MDL-035: zero Value의 Kind()는 4종 유효 kind 중 어느 것과도 같지 않다.
func TestMDL035_ZeroValueKindIsNotAnyValidKind(t *testing.T) {
	var v model.Value
	got := v.Kind()
	for _, k := range []model.ValueKind{model.ValueFloat, model.ValueInt, model.ValueBool, model.ValueString} {
		if got == k {
			t.Errorf("zero Value의 Kind()가 유효 kind %q와 같았다", k.String())
		}
	}
}

// 3.2: SourceRef Type 상수 값 (열린 집합의 권장 값).
func TestSpec32_SourceTypeConstantValues(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"SourceTypePrometheus", string(model.SourceTypePrometheus), "prometheus"},
		{"SourceTypeGNMI", string(model.SourceTypeGNMI), "gnmi"},
		{"SourceTypeAgent", string(model.SourceTypeAgent), "agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

// MDL-030 (v1.2): Quality는 IsValid()가 참이어야 하며, QualityUnknown(zero
// value)은 유효하다. NewObservation과 Validate() 양쪽에서 동일하게 판정된다.
func TestMDL030_QualityValidity(t *testing.T) {
	var zeroQuality model.EvidenceQuality

	cases := []struct {
		name      string
		quality   model.EvidenceQuality
		wantValid bool
	}{
		{"zero value (QualityUnknown) 은 유효", zeroQuality, true},
		{"QualityUnknown", model.QualityUnknown, true},
		{"QualityGood", model.QualityGood, true},
		{"QualityDegraded", model.QualityDegraded, true},
		{"열거 밖 값 A", model.EvidenceQuality(outOfEnumA), false},
		{"열거 밖 값 B", model.EvidenceQuality(outOfEnumB), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 전제: 열거형 자체의 판정이 기대와 맞는지 먼저 확인한다.
			if tc.quality.IsValid() != tc.wantValid {
				t.Fatalf("전제 불일치: EvidenceQuality.IsValid() = %v, want %v",
					tc.quality.IsValid(), tc.wantValid)
			}

			in := validObservationInput(t)
			in.Quality = tc.quality

			requireValidity(t, in.Validate(), tc.wantValid, "Observation.Validate()")

			got, err := model.NewObservation(in)
			if tc.wantValid {
				requireNoErr(t, err, "NewObservation")
				if got.Quality != tc.quality {
					t.Errorf("Quality = %q, want %q", got.Quality.String(), tc.quality.String())
				}
				return
			}
			requireErrInvalid(t, err, "NewObservation")
			if !reflect.DeepEqual(got, model.Observation{}) {
				t.Errorf("오류 시 zero Observation을 기대했으나 %#v를 받았다", got)
			}
		})
	}
}

// MDL-031 (v1.2): ExpiresAt 규칙이 Observation.Validate()에도 동일하게 적용된다.
// 생성자를 거치지 않고 조립한 값이라도 ExpiresAt <= ObservedAt이면 무효다.
func TestMDL031_ValidateAppliesSameExpiresAtRule(t *testing.T) {
	cases := []struct {
		name      string
		expiresAt time.Time
		wantValid bool
	}{
		{"zero time (만료 없음)", zeroTime, true},
		{"ObservedAt 이후", tExpires, true},
		{"ObservedAt과 동일", tObserved, false},
		{"ObservedAt 이전", tBefore, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validObservationInput(t)
			in.ObservedAt = tObserved
			in.ExpiresAt = tc.expiresAt

			requireValidity(t, in.Validate(), tc.wantValid, "Observation.Validate()")
		})
	}
}

// MDL-005 (v1.2, 9종): SourceRef.Validate().
func TestMDL005_SourceRefValidate(t *testing.T) {
	cases := []struct {
		name      string
		ref       model.SourceRef
		wantValid bool
	}{
		{"zero value", model.SourceRef{}, false},
		{"Type만 빔", model.SourceRef{Type: "", Name: "prom-main"}, false},
		{"Name만 빔", model.SourceRef{Type: string(model.SourceTypePrometheus), Name: ""}, false},
		{"prometheus", model.SourceRef{Type: string(model.SourceTypePrometheus), Name: "prom-main"}, true},
		{"gnmi", model.SourceRef{Type: string(model.SourceTypeGNMI), Name: "leaf-01"}, true},
		{"agent", model.SourceRef{Type: string(model.SourceTypeAgent), Name: "node-agent-7"}, true},
		{"열린 집합 - 상수 밖의 Type", model.SourceRef{Type: "vendor-private", Name: "src"}, true},
		// 3절 서두(v1.2): "비어 있을 수 없다"는 길이 0 금지 — 공백만인 문자열은 유효.
		{"공백만인 Type·Name도 유효", model.SourceRef{Type: " ", Name: " "}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireValidity(t, tc.ref.Validate(), tc.wantValid, "SourceRef.Validate()")
		})
	}
}

// --- v1.3 신설: SignalRef.String()의 항등성 (MDL-036) ---

// MDL-036 (v1.3 신설): 임의의 SignalRef s(유효 여부와 무관, 빈 문자열 포함)에
// 대해 String()은 string(s)를 그대로 반환한다 — 정규화·트리밍이 없다.
// 회차 2의 해석 고지 I-7이 v1.3에서 계약으로 승격되었으므로 직접 단정한다.
func TestMDL036_SignalRefStringIsIdentity(t *testing.T) {
	corpus := append(validSignalRefStrings(), invalidSignalRefStrings()...)
	corpus = append(corpus,
		"",                  // zero value
		" ",                 // 공백만
		"   fabric.port   ", // 앞뒤 공백 — 트리밍하지 않는다
		"\tfabric.port\n",   // 탭·개행
		"FABRIC.PORT.RATE",  // 대문자 — 소문자화하지 않는다
		"Fabric.Port",       // 혼합 대소문자
		"fabric..port",      // 형식 위반
		"한글.신호",             // 비-ASCII
		"a.b\x00c",          // 제어 문자
		"signal.with:colon", // 콜론
		"signal.with/slash", // 슬래시
		"...",               // 점만
	)

	for _, s := range corpus {
		t.Run(s, func(t *testing.T) {
			sig := model.SignalRef(s)

			var got string
			mustNotPanic(t, "SignalRef.String()", func() { got = sig.String() })
			if got != s {
				t.Errorf("SignalRef(%q).String() = %q, want %q (항등이어야 한다)", s, got, s)
			}
			// 기저 문자열 변환과도 일치한다.
			if got != string(sig) {
				t.Errorf("String() = %q, string(SignalRef) = %q — 둘은 같아야 한다", got, string(sig))
			}
			// 반복 호출에 안정적이다 (MDL-002).
			if second := sig.String(); second != got {
				t.Errorf("String()이 호출마다 달랐다: %q vs %q", got, second)
			}
			// 유효성과 무관하다 — IsValid()의 결과가 String()을 바꾸지 않는다.
			_ = sig.IsValid()
			if third := sig.String(); third != got {
				t.Errorf("IsValid() 호출 후 String()이 달라졌다: %q vs %q", got, third)
			}
		})
	}
}

// MDL-036: zero value SignalRef의 String()은 빈 문자열이다.
func TestMDL036_ZeroSignalRefStringIsEmpty(t *testing.T) {
	var zero model.SignalRef
	var got string
	mustNotPanic(t, "zero SignalRef.String()", func() { got = zero.String() })
	if got != "" {
		t.Errorf("zero SignalRef.String() = %q, want 빈 문자열", got)
	}
}
