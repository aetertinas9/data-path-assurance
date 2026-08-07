// evidence_add_test.go — 조립 (specs/evidence/spec.md 3.4, EVD-020~028).
package tests

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// EVD-020: 유효한 관측을 Add하면 nil과 새 Window를 반환하고, 새 Window의 조회에
// 그 관측이 나타나며, 수신자 Window는 변하지 않는다.
func TestEVD020_AddStoresObservation(t *testing.T) {
	now := evdAt(time.Minute)

	base := mustWindow(t, evdConfig())
	beforeBase := evdSnapshot(t, base, now)

	o := evdFloat(t, evdAt(0), 100)
	o.Dimensions = map[string]string{"lane": "0"}

	next, err := base.Add(o)
	requireNoErr(t, err, "Add(유효 관측)")

	// 수신자는 변하지 않는다 (함수형 갱신).
	assertSnapshotUnchanged(t, beforeBase, evdSnapshot(t, base, now), "Add 후의 수신자")

	subjects := next.Subjects()
	if len(subjects) != 1 || subjects[0].Key() != evdSubjectA(t).Key() {
		t.Fatalf("Subjects() = %v, want [%s]", subjects, evdSubjectA(t).Key())
	}

	signals, err := next.Signals(evdSubjectA(t))
	requireNoErr(t, err, "Signals")
	if len(signals) != 1 || signals[0] != evdSigA {
		t.Fatalf("Signals() = %v, want [%s]", signals, evdSigA.String())
	}

	sources := next.Sources()
	if len(sources) != 1 || sources[0] != evdSrcProm {
		t.Fatalf("Sources() = %v, want [%+v]", sources, evdSrcProm)
	}

	s := evdDefaultSeries(t, next)
	if got := s.Source(); got != evdSrcProm {
		t.Errorf("Series.Source() = %+v, want %+v", got, evdSrcProm)
	}
	if got := s.Signal(); got != evdSigA {
		t.Errorf("Series.Signal() = %q, want %q", got.String(), evdSigA.String())
	}
	if got := s.Subject().Key(); got != evdSubjectA(t).Key() {
		t.Errorf("Series.Subject().Key() = %q, want %q", got, evdSubjectA(t).Key())
	}
	if got := s.Dimensions(); len(got) != 1 || got["lane"] != "0" {
		t.Errorf("Series.Dimensions() = %v, want {lane:0}", got)
	}

	obs := s.Observations()
	if len(obs) != 1 {
		t.Fatalf("Observations() 길이 %d, want 1", len(obs))
	}
	if obs[0].ID != o.ID {
		t.Errorf("보존된 관측의 ID = %q, want %q", obs[0].ID, o.ID)
	}
	if !obs[0].ObservedAt.Equal(o.ObservedAt) {
		t.Errorf("보존된 관측의 ObservedAt = %s, want %s",
			evdTimeString(obs[0].ObservedAt), evdTimeString(o.ObservedAt))
	}

	latest, ok := s.Latest()
	if !ok {
		t.Fatalf("SeriesFor가 반환한 시리즈의 Latest() ok = false, want true")
	}
	if latest.ID != o.ID {
		t.Errorf("Latest().ID = %q, want %q", latest.ID, o.ID)
	}

	last, err := next.SourceLastObserved(evdSrcProm)
	requireNoErr(t, err, "SourceLastObserved")
	if !last.Equal(o.ObservedAt) {
		t.Errorf("SourceLastObserved() = %s, want %s", evdTimeString(last), evdTimeString(o.ObservedAt))
	}
}

// EVD-021 (edge): Validate()가 nil이 아닌 관측, 또는 Float kind 값이 NaN·±Inf인
// 관측은 model.ErrInvalid 계열 오류이고 window는 변하지 않는다.
func TestEVD021_AddRejectsInvalidObservation(t *testing.T) {
	now := evdAt(time.Minute)

	// 전제 확인: 열거 밖 Quality 값은 무효여야 한다 (MDL-070(d)·(e)).
	const outOfEnumQuality = 42
	if model.EvidenceQuality(outOfEnumQuality).IsValid() {
		t.Fatalf("전제 불일치: EvidenceQuality(%d)가 유효하다", outOfEnumQuality)
	}

	broken := func(f func(o *model.Observation)) model.Observation {
		o := evdFloat(t, evdAt(30*time.Second), 42)
		f(&o)
		return o
	}

	cases := []struct {
		name string
		obs  model.Observation
	}{
		{"zero value Observation", model.Observation{}},
		{"ID가 빔", broken(func(o *model.Observation) { o.ID = "" })},
		{"Source가 zero value", broken(func(o *model.Observation) { o.Source = model.SourceRef{} })},
		{"Source.Type이 빔", broken(func(o *model.Observation) { o.Source.Type = "" })},
		{"Source.Name이 빔", broken(func(o *model.Observation) { o.Source.Name = "" })},
		{"Subject가 zero value", broken(func(o *model.Observation) { o.Subject = model.AssetRef{} })},
		{"Signal이 빔", broken(func(o *model.Observation) { o.Signal = model.SignalRef("") })},
		{"Signal 형식 위반(세그먼트 1개)", broken(func(o *model.Observation) { o.Signal = model.SignalRef("fabric") })},
		{"Value가 zero value", broken(func(o *model.Observation) { o.Value = model.Value{} })},
		{"ObservedAt이 zero time", broken(func(o *model.Observation) { o.ObservedAt = zeroTime })},
		{"ReceivedAt이 zero time", broken(func(o *model.Observation) { o.ReceivedAt = zeroTime })},
		{"Quality가 열거 밖", broken(func(o *model.Observation) { o.Quality = model.EvidenceQuality(outOfEnumQuality) })},
		{"ExpiresAt이 ObservedAt과 같음", broken(func(o *model.Observation) { o.ExpiresAt = o.ObservedAt })},
		{"ExpiresAt이 ObservedAt보다 이름", broken(func(o *model.Observation) { o.ExpiresAt = o.ObservedAt.Add(-time.Nanosecond) })},

		// 3.4 규칙 1의 이 패키지 자체 불변식 — model 계약으로는 유효한 값들이다.
		{"Float 값이 NaN", broken(func(o *model.Observation) { o.Value = model.NewFloatValue(math.NaN()) })},
		{"Float 값이 +Inf", broken(func(o *model.Observation) { o.Value = model.NewFloatValue(math.Inf(1)) })},
		{"Float 값이 -Inf", broken(func(o *model.Observation) { o.Value = model.NewFloatValue(math.Inf(-1)) })},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := mustAdd(t, mustWindow(t, evdConfig()), evdFloat(t, evdAt(0), 10))
			before := evdSnapshot(t, base, now)

			got, err := base.Add(tc.obs)
			requireErrInvalid(t, err, "Add("+tc.name+")")
			assertSnapshotUnchanged(t, before, evdSnapshot(t, got, now), "반환 Window")
			assertSnapshotUnchanged(t, before, evdSnapshot(t, base, now), "수신자 Window")
		})
	}
}

// 3.4 규칙 1의 대우: NaN·±Inf가 아닌 유한한 Float과 모든 Int·Bool·String 값은
// 수용된다 (도달한 유한 수치는 전부 정당한 관측값이다 — 1절·EVD-056).
func TestEVD021_AddAcceptsEveryFiniteValueKind(t *testing.T) {
	cases := []struct {
		name string
		v    model.Value
	}{
		{"Float 0", model.NewFloatValue(0)},
		{"Float 음수", model.NewFloatValue(-3.5)},
		{"Float 매우 작은 양수", model.NewFloatValue(math.SmallestNonzeroFloat64)},
		{"Float 매우 큰 값", model.NewFloatValue(math.MaxFloat64)},
		{"Float 2^64-1", model.NewFloatValue(float64(math.MaxUint64))},
		{"Int 0", model.NewIntValue(0)},
		{"Int MaxInt64", model.NewIntValue(math.MaxInt64)},
		{"Int MinInt64", model.NewIntValue(math.MinInt64)},
		{"Bool", model.NewBoolValue(true)},
		{"String", model.NewStringValue("Ethernet1/1")},
		{"빈 String", model.NewStringValue("")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := mustAdd(t, mustWindow(t, evdConfig()), evdObs(t, evdAt(0), tc.v))
			obs := evdDefaultSeries(t, w).Observations()
			if len(obs) != 1 {
				t.Fatalf("Observations() 길이 %d, want 1", len(obs))
			}
			if got, want := evdValueString(obs[0].Value), evdValueString(tc.v); got != want {
				t.Errorf("보존된 값 = %s, want %s", got, want)
			}
		})
	}
}

// EVD-022 (edge): 동등한 관측의 재전송은 멱등이다 — nil을 반환하고 window
// 내용은 변하지 않으며, 보존되는 것은 먼저 추가된 관측이다.
func TestEVD022_EqualResendIsIdempotent(t *testing.T) {
	now := evdAt(time.Minute)

	alias := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
	otherAlias := mustTypedID(t, string(model.NamespaceLLDPChassisID), "aa:bb:cc:dd:ee:ff")

	first := evdFloat(t, evdAt(0), 100)
	first.ID = "obs-먼저"
	first.Sequence = 1
	first.RawDigest = "sha256:aaaa"
	first.Dimensions = map[string]string{"lane": "0"}
	first.Subject = evdSubjectAWithAliases(t, alias)
	first.ExpiresAt = evdAt(2 * time.Minute)

	// 동등성 판정에 쓰지 않는 필드만 다른 재전송 (3.1).
	resend := first
	resend.ID = "obs-나중"
	resend.ReceivedAt = first.ReceivedAt.Add(37 * time.Second)
	resend.Sequence = 999
	resend.RawDigest = "sha256:bbbb"
	resend.Subject = evdSubjectAWithAliases(t, otherAlias)
	resend.Dimensions = map[string]string{"lane": "0"} // 같은 키-값 집합, 다른 맵 인스턴스

	w := mustAdd(t, mustWindow(t, evdConfig()), first)
	before := evdSnapshot(t, w, now)

	after, err := w.Add(resend)
	requireNoErr(t, err, "Add(동등 재전송)")
	assertSnapshotUnchanged(t, before, evdSnapshot(t, after, now), "동등 재전송 후")

	obs := evdDefaultSeries(t, after).Observations()
	if len(obs) != 1 {
		t.Fatalf("Observations() 길이 %d, want 1 (재전송이 새 관측을 만들었다)", len(obs))
	}
	if obs[0].ID != first.ID {
		t.Errorf("보존된 관측의 ID = %q, want %q (먼저 추가된 관측이 남는다)", obs[0].ID, first.ID)
	}
	if !obs[0].ReceivedAt.Equal(first.ReceivedAt) {
		t.Errorf("보존된 관측의 ReceivedAt = %s, want %s",
			evdTimeString(obs[0].ReceivedAt), evdTimeString(first.ReceivedAt))
	}
	if obs[0].Sequence != first.Sequence {
		t.Errorf("보존된 관측의 Sequence = %d, want %d", obs[0].Sequence, first.Sequence)
	}
	if obs[0].RawDigest != first.RawDigest {
		t.Errorf("보존된 관측의 RawDigest = %q, want %q", obs[0].RawDigest, first.RawDigest)
	}

	// 여러 번 재전송해도 멱등이다.
	for i := 0; i < 3; i++ {
		after, err = after.Add(resend)
		requireNoErr(t, err, "Add(반복 재전송)")
	}
	assertSnapshotUnchanged(t, before, evdSnapshot(t, after, now), "재전송 4회 후")
}

// EVD-022 (edge): 동등성 필드별로 하나씩만 다르게 해도 재전송으로 판정되는
// 필드들 — ID·ReceivedAt·Sequence·RawDigest·Subject.Aliases.
func TestEVD022_FieldsExcludedFromEquality(t *testing.T) {
	now := evdAt(time.Minute)

	base := evdFloat(t, evdAt(0), 100)
	base.ID = "obs-기준"
	base.Sequence = 7
	base.RawDigest = "sha256:base"
	base.Subject = evdSubjectAWithAliases(t,
		mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1"))

	aliasWithRaw := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
	aliasWithRaw.Raw = []byte{0x01}
	aliasWithRaw.Source = "sysfs"

	cases := []struct {
		name string
		mut  func(o *model.Observation)
	}{
		{"ID만 다름", func(o *model.Observation) { o.ID = "obs-다름" }},
		{"ReceivedAt만 다름", func(o *model.Observation) { o.ReceivedAt = o.ReceivedAt.Add(time.Hour) }},
		{"Sequence만 다름", func(o *model.Observation) { o.Sequence = 12345 }},
		{"RawDigest만 다름", func(o *model.Observation) { o.RawDigest = "sha256:다름" }},
		{"Subject.Aliases가 빔", func(o *model.Observation) { o.Subject = evdSubjectA(t) }},
		{"Subject.Aliases가 더 많음", func(o *model.Observation) {
			o.Subject = evdSubjectAWithAliases(t,
				mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1"),
				mustTypedID(t, string(model.NamespaceLLDPChassisID), "aa:bb"))
		}},
		{"Subject alias의 Raw·Source만 다름", func(o *model.Observation) {
			o.Subject = evdSubjectAWithAliases(t, aliasWithRaw)
		}},
		{"nil Dimensions vs 빈 맵", func(o *model.Observation) { o.Dimensions = map[string]string{} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := mustAdd(t, mustWindow(t, evdConfig()), base)
			before := evdSnapshot(t, w, now)

			variant := base
			tc.mut(&variant)

			after, err := w.Add(variant)
			requireNoErr(t, err, "Add("+tc.name+")")
			assertSnapshotUnchanged(t, before, evdSnapshot(t, after, now), tc.name)
		})
	}
}

// EVD-023 (edge): RawDigest는 동등성·중복 제거의 키가 아니다.
func TestEVD023_RawDigestIsNotAnEqualityKey(t *testing.T) {
	now := evdAt(time.Minute)

	t.Run("동등성 필드가 같고 RawDigest만 다르면 재전송 no-op이다", func(t *testing.T) {
		first := evdFloat(t, evdAt(0), 100)
		first.RawDigest = "sha256:aaaa"

		variant := first
		variant.RawDigest = "sha256:bbbb"

		w := mustAdd(t, mustWindow(t, evdConfig()), first)
		before := evdSnapshot(t, w, now)

		after, err := w.Add(variant)
		requireNoErr(t, err, "Add(RawDigest만 다름)")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, after, now), "RawDigest만 다른 재전송")
	})

	t.Run("RawDigest가 같고 Value가 다르면 충돌이다", func(t *testing.T) {
		first := evdFloat(t, evdAt(0), 100)
		first.RawDigest = "sha256:같음"

		variant := evdFloat(t, evdAt(0), 200)
		variant.RawDigest = "sha256:같음"
		variant.ID = "obs-다름"

		w := mustAdd(t, mustWindow(t, evdConfig()), first)
		before := evdSnapshot(t, w, now)

		after, err := w.Add(variant)
		requireEvdConflict(t, err, "Add(RawDigest 일치 + Value 상이)")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, after, now), "충돌 시 반환 Window")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, w, now), "충돌 시 수신자 Window")
	})
}

// EVD-024 (edge): 같은 시리즈·같은 instant에 동등하지 않은 관측을 Add하면
// ErrConflict이고 자동 대체는 없다.
func TestEVD024_SameInstantConflict(t *testing.T) {
	now := evdAt(time.Minute)

	newBase := func(t *testing.T) model.Observation {
		t.Helper()
		o := evdFloat(t, evdAt(0), 100)
		o.ID = "obs-기준"
		o.Unit = "1"
		o.Quality = model.QualityGood
		o.ExpiresAt = evdAt(2 * time.Minute)
		return o
	}

	cases := []struct {
		name string
		mut  func(o *model.Observation)
	}{
		{"Value가 다름", func(o *model.Observation) { o.Value = model.NewFloatValue(200) }},
		{"Value의 kind가 다름 (같은 수치)", func(o *model.Observation) { o.Value = model.NewIntValue(100) }},
		{"Unit이 다름", func(o *model.Observation) { o.Unit = "bytes" }},
		{"Unit이 빔", func(o *model.Observation) { o.Unit = "" }},
		{"Quality가 다름", func(o *model.Observation) { o.Quality = model.QualityDegraded }},
		{"Quality가 Unknown(zero value)", func(o *model.Observation) { o.Quality = model.QualityUnknown }},
		{"ExpiresAt이 다름", func(o *model.Observation) { o.ExpiresAt = evdAt(3 * time.Minute) }},
		{"ExpiresAt이 zero time", func(o *model.Observation) { o.ExpiresAt = zeroTime }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := newBase(t)
			w := mustAdd(t, mustWindow(t, evdConfig()), base)
			before := evdSnapshot(t, w, now)

			variant := base
			variant.ID = "obs-충돌"
			tc.mut(&variant)

			after, err := w.Add(variant)
			requireEvdConflict(t, err, "Add("+tc.name+")")
			assertSnapshotUnchanged(t, before, evdSnapshot(t, after, now), "충돌 시 반환 Window")
			assertSnapshotUnchanged(t, before, evdSnapshot(t, w, now), "충돌 시 수신자 Window")

			// 자동 대체가 없다 — 보존된 것은 원래 관측이다.
			obs := evdDefaultSeries(t, after).Observations()
			if len(obs) != 1 {
				t.Fatalf("Observations() 길이 %d, want 1", len(obs))
			}
			if obs[0].ID != base.ID {
				t.Errorf("보존된 관측의 ID = %q, want %q", obs[0].ID, base.ID)
			}
		})
	}
}

// EVD-024 (edge): 다른 시리즈의 같은 instant는 충돌이 아니다.
func TestEVD024_SameInstantInDifferentSeriesIsNotAConflict(t *testing.T) {
	at := evdAt(0)

	w := mustWindow(t, evdConfig())
	w = mustAdd(t, w,
		evdRetag(evdFloat(t, at, 1), evdSrcProm, evdSubjectA(t), evdSigA, nil),
		evdRetag(evdFloat(t, at, 2), evdSrcGNMI, evdSubjectA(t), evdSigA, nil),
		evdRetag(evdFloat(t, at, 3), evdSrcProm, evdSubjectA(t), evdSigA, map[string]string{"lane": "0"}),
		evdRetag(evdFloat(t, at, 4), evdSrcProm, evdSubjectA(t), evdSigB, nil),
		evdRetag(evdFloat(t, at, 5), evdSrcProm, evdSubjectB(t), evdSigA, nil),
	)

	seriesA, err := w.SeriesFor(evdSubjectA(t), evdSigA)
	requireNoErr(t, err, "SeriesFor(A, sigA)")
	if len(seriesA) != 3 {
		t.Errorf("(A, sigA)의 시리즈 %d개, want 3", len(seriesA))
	}
	seriesB, err := w.SeriesFor(evdSubjectB(t), evdSigA)
	requireNoErr(t, err, "SeriesFor(B, sigA)")
	if len(seriesB) != 1 {
		t.Errorf("(B, sigA)의 시리즈 %d개, want 1", len(seriesB))
	}
}

// EVD-025 (edge): 도착 순서가 아니라 ObservedAt이 관측의 위치를 정한다.
func TestEVD025_OutOfOrderInsertion(t *testing.T) {
	t1, t2, t3 := evdAt(0), evdAt(10*time.Second), evdAt(20*time.Second)

	w := mustAdd(t, mustWindow(t, evdConfig()),
		evdFloat(t, t1, 100),
		evdFloat(t, t3, 300),
	)
	w = mustAdd(t, w, evdFloat(t, t2, 200))

	assertObservedAts(t, evdDefaultSeries(t, w), []time.Time{t1, t2, t3}, "늦게 도착한 t2")

	// 가장 이른 instant가 마지막에 도착해도 앞에 놓인다.
	t0 := evdAt(-10 * time.Second)
	w = mustAdd(t, w, evdFloat(t, t0, 50))
	assertObservedAts(t, evdDefaultSeries(t, w), []time.Time{t0, t1, t2, t3}, "가장 이른 관측이 마지막 도착")

	// Latest는 여전히 t3다.
	latest, ok := evdDefaultSeries(t, w).Latest()
	if !ok {
		t.Fatalf("Latest() ok = false")
	}
	if !latest.ObservedAt.Equal(t3) {
		t.Errorf("Latest().ObservedAt = %s, want %s", evdTimeString(latest.ObservedAt), evdTimeString(t3))
	}
}

// EVD-026 (edge) (a)(b): horizon 경계는 폐구간이다.
func TestEVD026_HorizonBoundaryIsClosed(t *testing.T) {
	now := evdAt(time.Minute)
	cfg := evidence.Config{MaxAge: evdMaxAge, Horizon: 100 * time.Second, MaxSamples: 8}

	tMax := evdAt(0)
	base := mustAdd(t, mustWindow(t, cfg), evdFloat(t, tMax, 100))
	before := evdSnapshot(t, base, now)

	t.Run("(a) T_max-Horizon보다 이전은 ErrOutOfWindow", func(t *testing.T) {
		for _, d := range []time.Duration{time.Nanosecond, time.Second, time.Hour} {
			at := tMax.Add(-100*time.Second - d)
			got, err := base.Add(evdFloat(t, at, 1))
			requireEvdOutOfWindow(t, err, "Add("+evdTimeString(at)+")")
			assertSnapshotUnchanged(t, before, evdSnapshot(t, got, now), "반환 Window")
			assertSnapshotUnchanged(t, before, evdSnapshot(t, base, now), "수신자 Window")
		}
	})

	t.Run("(b) 정확히 T_max-Horizon은 수용된다", func(t *testing.T) {
		at := tMax.Add(-100 * time.Second)
		next := mustAdd(t, base, evdFloat(t, at, 1))
		assertObservedAts(t, evdDefaultSeries(t, next), []time.Time{at, tMax}, "폐구간 경계")
	})

	t.Run("(b) 경계보다 1ns 뒤는 당연히 수용된다", func(t *testing.T) {
		at := tMax.Add(-100*time.Second + time.Nanosecond)
		next := mustAdd(t, base, evdFloat(t, at, 1))
		assertObservedAts(t, evdDefaultSeries(t, next), []time.Time{at, tMax}, "경계 + 1ns")
	})
}

// EVD-026 (edge) (c): 새 최신 관측의 Add로 기존 관측이 horizon 밖이 되면
// 그 관측은 조회에서 사라진다 (정상 sliding, nil 반환).
func TestEVD026_NewLatestSlidesHorizon(t *testing.T) {
	cfg := evidence.Config{MaxAge: evdMaxAge, Horizon: 100 * time.Second, MaxSamples: 8}

	w := mustAdd(t, mustWindow(t, cfg),
		evdFloat(t, evdAt(0), 100),
		evdFloat(t, evdAt(10*time.Second), 200),
		evdFloat(t, evdAt(60*time.Second), 300),
	)
	assertObservedAts(t, evdDefaultSeries(t, w),
		[]time.Time{evdAt(0), evdAt(10 * time.Second), evdAt(60 * time.Second)}, "sliding 전")

	// T_max = t+150s → cutoff = t+50s → t+0, t+10s가 밀려난다.
	w = mustAdd(t, w, evdFloat(t, evdAt(150*time.Second), 400))
	assertObservedAts(t, evdDefaultSeries(t, w),
		[]time.Time{evdAt(60 * time.Second), evdAt(150 * time.Second)}, "sliding 후")

	// 전부 밀려나는 경우 — 새 관측 하나만 남는다.
	w = mustAdd(t, w, evdFloat(t, evdAt(1000*time.Second), 500))
	assertObservedAts(t, evdDefaultSeries(t, w), []time.Time{evdAt(1000 * time.Second)}, "전부 sliding")
}

// EVD-026 (edge) (d): capacity 경계 — MaxSamples를 넘으면 늦은 쪽부터 남는다.
func TestEVD026_CapacityBound(t *testing.T) {
	now := evdAt(time.Minute)
	cfg := evidence.Config{MaxAge: evdMaxAge, Horizon: time.Hour, MaxSamples: 3}

	full := mustAdd(t, mustWindow(t, cfg),
		evdFloat(t, evdAt(0), 100),
		evdFloat(t, evdAt(10*time.Second), 200),
		evdFloat(t, evdAt(20*time.Second), 300),
	)
	before := evdSnapshot(t, full, now)

	t.Run("가장 오래된 것보다 이전은 ErrOutOfWindow", func(t *testing.T) {
		got, err := full.Add(evdFloat(t, evdAt(-time.Second), 50))
		requireEvdOutOfWindow(t, err, "Add(가장 오래된 것보다 이전)")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, got, now), "반환 Window")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, full, now), "수신자 Window")
	})

	t.Run("더 늦은 instant는 수용되고 가장 오래된 것이 밀려난다", func(t *testing.T) {
		next := mustAdd(t, full, evdFloat(t, evdAt(30*time.Second), 400))
		assertObservedAts(t, evdDefaultSeries(t, next),
			[]time.Time{evdAt(10 * time.Second), evdAt(20 * time.Second), evdAt(30 * time.Second)},
			"capacity 유지")
	})

	t.Run("가운데 instant도 수용되고 가장 오래된 것이 밀려난다", func(t *testing.T) {
		next := mustAdd(t, full, evdFloat(t, evdAt(15*time.Second), 250))
		assertObservedAts(t, evdDefaultSeries(t, next),
			[]time.Time{evdAt(10 * time.Second), evdAt(15 * time.Second), evdAt(20 * time.Second)},
			"가운데 삽입")
	})

	t.Run("MaxSamples가 1이면 항상 최신 하나만 남는다", func(t *testing.T) {
		one := evidence.Config{MaxAge: evdMaxAge, Horizon: time.Hour, MaxSamples: 1}
		w := mustAdd(t, mustWindow(t, one), evdFloat(t, evdAt(0), 100))
		w = mustAdd(t, w, evdFloat(t, evdAt(10*time.Second), 200))
		assertObservedAts(t, evdDefaultSeries(t, w), []time.Time{evdAt(10 * time.Second)}, "MaxSamples=1")

		got, err := w.Add(evdFloat(t, evdAt(5*time.Second), 150))
		requireEvdOutOfWindow(t, err, "MaxSamples=1에서 더 이른 관측")
		assertObservedAts(t, evdDefaultSeries(t, got), []time.Time{evdAt(10 * time.Second)}, "변화 없음")
	})
}

// EVD-026 (edge) (e): 보존은 시리즈별로 독립이다.
func TestEVD026_RetentionIsPerSeries(t *testing.T) {
	now := evdAt(time.Minute)
	cfg := evidence.Config{MaxAge: evdMaxAge, Horizon: 100 * time.Second, MaxSamples: 2}

	other := func(t *testing.T, at time.Time, v float64) model.Observation {
		t.Helper()
		return evdRetag(evdFloat(t, at, v), evdSrcGNMI, evdSubjectA(t), evdSigA, nil)
	}

	w := mustAdd(t, mustWindow(t, cfg),
		evdFloat(t, evdAt(0), 100),
		evdFloat(t, evdAt(10*time.Second), 200),
		other(t, evdAt(0), 1),
		other(t, evdAt(10*time.Second), 2),
	)

	// 두 시리즈는 좌표 전체로 지목한다 — source만으로 거르면 픽스처에 같은
	// source·다른 dimensions의 시리즈가 생기는 순간 지목이 모호해진다 (3.1).
	otherSeries := func(w evidence.Window) evidence.Series {
		t.Helper()
		return evdDefaultSeriesOf(t, w, evdSrcGNMI, nil)
	}

	beforeOther := evdSeriesString(otherSeries(w), now, "other")

	// prometheus 시리즈에서 축출이 일어나도 gnmi 시리즈는 그대로다.
	w2 := mustAdd(t, w, evdFloat(t, evdAt(20*time.Second), 300))
	assertObservedAts(t, evdDefaultSeriesOf(t, w2, evdSrcProm, nil),
		[]time.Time{evdAt(10 * time.Second), evdAt(20 * time.Second)}, "prometheus 시리즈")
	assertSnapshotUnchanged(t, beforeOther, evdSeriesString(otherSeries(w2), now, "other"), "gnmi 시리즈")

	// horizon sliding도 마찬가지다.
	w3 := mustAdd(t, w, evdFloat(t, evdAt(500*time.Second), 999))
	assertObservedAts(t, evdDefaultSeriesOf(t, w3, evdSrcProm, nil),
		[]time.Time{evdAt(500 * time.Second)}, "prometheus 시리즈(sliding)")
	assertSnapshotUnchanged(t, beforeOther, evdSeriesString(otherSeries(w3), now, "other"), "gnmi 시리즈(sliding)")

	// 축출로 ErrOutOfWindow가 나도 다른 시리즈는 영향받지 않는다.
	beforeAll := evdSnapshot(t, w, now)
	got, err := w.Add(evdFloat(t, evdAt(-time.Hour), 0))
	requireEvdOutOfWindow(t, err, "horizon 밖 Add")
	assertSnapshotUnchanged(t, beforeAll, evdSnapshot(t, got, now), "다른 시리즈 포함 전체")
}

// EVD-027: Prune은 cutoff보다 이전(strictly before)인 관측만 제거한다.
func TestEVD027_Prune(t *testing.T) {
	now := evdAt(time.Minute)

	build := func(t *testing.T) evidence.Window {
		t.Helper()
		return mustAdd(t, mustWindow(t, evdConfig()),
			evdFloat(t, evdAt(0), 100),
			evdFloat(t, evdAt(10*time.Second), 200),
			evdFloat(t, evdAt(20*time.Second), 300),
			evdRetag(evdFloat(t, evdAt(5*time.Second), 1), evdSrcGNMI, evdSubjectB(t), evdSigB, nil),
		)
	}

	t.Run("cutoff와 같은 instant는 남는다", func(t *testing.T) {
		w := mustPrune(t, build(t), evdAt(10*time.Second))
		assertObservedAts(t, evdDefaultSeries(t, w),
			[]time.Time{evdAt(10 * time.Second), evdAt(20 * time.Second)}, "Prune(t+10s)")
	})

	t.Run("아무것도 제거하지 않는 cutoff", func(t *testing.T) {
		original := build(t)
		before := evdSnapshot(t, original, now)
		w := mustPrune(t, original, evdAt(-time.Hour))
		assertSnapshotUnchanged(t, before, evdSnapshot(t, w, now), "모든 관측보다 이른 cutoff")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, original, now), "수신자")
	})

	t.Run("전부 제거된 시리즈·subject·source는 조회에 나타나지 않는다", func(t *testing.T) {
		w := mustPrune(t, build(t), evdAt(6*time.Second))

		if got := len(w.Subjects()); got != 1 {
			t.Errorf("Subjects() 길이 %d, want 1 (B는 사라져야 한다)", got)
		}
		for _, subj := range w.Subjects() {
			if subj.Key() == evdSubjectB(t).Key() {
				t.Errorf("전부 제거된 subject %s가 남아 있다", subj.Key())
			}
		}
		sources := w.Sources()
		if len(sources) != 1 || sources[0] != evdSrcProm {
			t.Errorf("Sources() = %v, want [prometheus/prom-main]", sources)
		}
		series, err := w.SeriesFor(evdSubjectB(t), evdSigB)
		requireNoErr(t, err, "SeriesFor(제거된 subject)")
		if len(series) != 0 {
			t.Errorf("제거된 subject의 SeriesFor 길이 %d, want 0", len(series))
		}
		last, err := w.SourceLastObserved(evdSrcGNMI)
		requireNoErr(t, err, "SourceLastObserved(제거된 source)")
		if !last.IsZero() {
			t.Errorf("제거된 source의 SourceLastObserved = %s, want zero time", evdTimeString(last))
		}
	})

	t.Run("전부 제거하면 빈 window가 되고 구성은 유지된다", func(t *testing.T) {
		w := mustPrune(t, build(t), evdAt(time.Hour))
		assertEmptyWindow(t, w, now, "전부 Prune된 Window")

		// 구성이 유지되므로 계속 Add할 수 있다.
		w = mustAdd(t, w, evdFloat(t, evdAt(2*time.Hour), 1), evdFloat(t, evdAt(2*time.Hour+10*time.Second), 2))
		assertObservedAts(t, evdDefaultSeries(t, w),
			[]time.Time{evdAt(2 * time.Hour), evdAt(2*time.Hour + 10*time.Second)}, "Prune 후 재조립")
	})

	t.Run("반복 Prune은 멱등이다", func(t *testing.T) {
		w := mustPrune(t, build(t), evdAt(10*time.Second))
		before := evdSnapshot(t, w, now)
		w2 := mustPrune(t, w, evdAt(10*time.Second))
		assertSnapshotUnchanged(t, before, evdSnapshot(t, w2, now), "같은 cutoff로 두 번 Prune")
	})

	t.Run("cutoff가 zero time이면 ErrInvalid이고 window는 변하지 않는다", func(t *testing.T) {
		original := build(t)
		before := evdSnapshot(t, original, now)
		got, err := original.Prune(zeroTime)
		requireErrInvalid(t, err, "Prune(zero time)")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, got, now), "반환 Window")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, original, now), "수신자 Window")
	})
}

// EVD-028 (edge): Add의 오류 판정 우선순위는 유효성 → 충돌 → 보존이다.
func TestEVD028_AddErrorPriority(t *testing.T) {
	now := evdAt(time.Minute)

	t.Run("유효성이 충돌보다 먼저", func(t *testing.T) {
		base := evdFloat(t, evdAt(0), 100)
		w := mustAdd(t, mustWindow(t, evdConfig()), base)
		before := evdSnapshot(t, w, now)

		// 같은 시리즈·같은 instant이고 Value도 다르지만 관측 자체가 무효하다.
		cases := []struct {
			name string
			mut  func(o *model.Observation)
		}{
			{"ID가 빔", func(o *model.Observation) { o.ID = "" }},
			{"Value가 NaN", func(o *model.Observation) { o.Value = model.NewFloatValue(math.NaN()) }},
			{"ReceivedAt이 zero", func(o *model.Observation) { o.ReceivedAt = zeroTime }},
		}
		for _, tc := range cases {
			variant := evdFloat(t, evdAt(0), 200)
			tc.mut(&variant)

			got, err := w.Add(variant)
			requireErrInvalid(t, err, "Add(무효 + 충돌 소지: "+tc.name+")")
			if errors.Is(err, evidence.ErrConflict) {
				t.Errorf("%s: 유효성 위반이 ErrConflict로도 판정되었다 (err=%v)", tc.name, err)
			}
			assertSnapshotUnchanged(t, before, evdSnapshot(t, got, now), tc.name)
		}
	})

	t.Run("유효성이 보존보다 먼저", func(t *testing.T) {
		cfg := evidence.Config{MaxAge: evdMaxAge, Horizon: 100 * time.Second, MaxSamples: 2}
		w := mustAdd(t, mustWindow(t, cfg), evdFloat(t, evdAt(0), 100))
		before := evdSnapshot(t, w, now)

		// horizon 밖이면서 값이 NaN이다.
		variant := evdFloat(t, evdAt(-time.Hour), 0)
		variant.Value = model.NewFloatValue(math.NaN())

		got, err := w.Add(variant)
		requireErrInvalid(t, err, "Add(무효 + horizon 밖)")
		if errors.Is(err, evidence.ErrOutOfWindow) {
			t.Errorf("유효성 위반이 ErrOutOfWindow로도 판정되었다 (err=%v)", err)
		}
		assertSnapshotUnchanged(t, before, evdSnapshot(t, got, now), "무효 + horizon 밖")
	})

	t.Run("충돌이 보존보다 먼저", func(t *testing.T) {
		cfg := evidence.Config{MaxAge: evdMaxAge, Horizon: time.Hour, MaxSamples: 2}
		w := mustAdd(t, mustWindow(t, cfg),
			evdFloat(t, evdAt(0), 100),
			evdFloat(t, evdAt(10*time.Second), 200),
		)
		before := evdSnapshot(t, w, now)

		// capacity가 찬 시리즈에서 가장 오래된 관측과 같은 instant·다른 Value.
		variant := evdFloat(t, evdAt(0), 999)
		variant.ID = "obs-충돌"

		got, err := w.Add(variant)
		requireEvdConflict(t, err, "Add(충돌 + 보존 소지)")
		if errors.Is(err, evidence.ErrOutOfWindow) {
			t.Errorf("충돌이 ErrOutOfWindow로도 판정되었다 (err=%v)", err)
		}
		assertSnapshotUnchanged(t, before, evdSnapshot(t, got, now), "충돌 + 보존 소지")
	})
}
