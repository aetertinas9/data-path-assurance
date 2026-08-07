// evidence_rate_test.go — reset-aware rate (specs/evidence/spec.md 3.6,
// EVD-050~059).
package tests

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// EVD-050 (edge): 보존 관측이 0개 또는 1개면 ErrInsufficientSamples이고 zero
// RateResult다 — 단일 counter 값은 정보량이 없다.
func TestEVD050_InsufficientSamples(t *testing.T) {
	t.Run("관측 0개 (zero Series)", func(t *testing.T) {
		var s evidence.Series
		got, err := s.Rate()
		requireEvdInsufficientSamples(t, err, "zero Series의 Rate")
		assertZeroRateResult(t, got, "zero Series의 Rate")
	})

	t.Run("관측 1개", func(t *testing.T) {
		w := evdFloatSeriesWindow(t, evdConfig(), evdPoint{0, 100})
		got, err := evdDefaultSeries(t, w).Rate()
		requireEvdInsufficientSamples(t, err, "관측 1개의 Rate")
		assertZeroRateResult(t, got, "관측 1개의 Rate")
	})

	t.Run("MaxSamples=1이면 항상 1개뿐이다", func(t *testing.T) {
		cfg := evidence.Config{MaxAge: evdMaxAge, Horizon: time.Hour, MaxSamples: 1}
		w := evdFloatSeriesWindow(t, cfg, evdPoint{0, 100}, evdPoint{10 * time.Second, 200})
		got, err := evdDefaultSeries(t, w).Rate()
		requireEvdInsufficientSamples(t, err, "MaxSamples=1 시리즈의 Rate")
		assertZeroRateResult(t, got, "MaxSamples=1 시리즈의 Rate")
	})

	t.Run("horizon sliding으로 1개만 남아도 마찬가지다", func(t *testing.T) {
		cfg := evidence.Config{MaxAge: evdMaxAge, Horizon: 10 * time.Second, MaxSamples: 8}
		w := evdFloatSeriesWindow(t, cfg, evdPoint{0, 100})
		w = mustAdd(t, w, evdFloat(t, evdAt(time.Minute), 200))
		got, err := evdDefaultSeries(t, w).Rate()
		requireEvdInsufficientSamples(t, err, "sliding 후 1개인 시리즈의 Rate")
		assertZeroRateResult(t, got, "sliding 후 1개인 시리즈의 Rate")
	})
}

// EVD-051: 단조 비감소 시리즈의 rate는 (v_N − v₁) ÷ (t_N − t₁)이고,
// 간격이 불균일해도 실제 timestamp 간격만 쓴다 (외삽·보간 없음).
func TestEVD051_MonotonicNonDecreasingRate(t *testing.T) {
	cases := []struct {
		name      string
		pts       []evdPoint
		want      float64
		wantN     int
		startEnd  [2]time.Duration
		wantDiscs int
	}{
		{
			name: "균일 간격 2점", pts: []evdPoint{{0, 100}, {10 * time.Second, 200}},
			want: 10, wantN: 2, startEnd: [2]time.Duration{0, 10 * time.Second},
		},
		{
			name: "불균일 간격 3점 (scrape gap)", pts: []evdPoint{{0, 100}, {10 * time.Second, 150}, {40 * time.Second, 400}},
			want: 7.5, wantN: 3, startEnd: [2]time.Duration{0, 40 * time.Second},
		},
		{
			name: "중간이 평평함", pts: []evdPoint{{0, 0}, {10 * time.Second, 0}, {20 * time.Second, 40}},
			want: 2, wantN: 3, startEnd: [2]time.Duration{0, 20 * time.Second},
		},
		{
			name: "1초 간격 1 증가", pts: []evdPoint{{0, 0}, {time.Second, 1}},
			want: 1, wantN: 2, startEnd: [2]time.Duration{0, time.Second},
		},
		{
			name: "소수 값", pts: []evdPoint{{0, 0.5}, {2 * time.Second, 1.5}},
			want: 0.5, wantN: 2, startEnd: [2]time.Duration{0, 2 * time.Second},
		},
		{
			name: "8개 샘플 (MaxSamples 한계)",
			pts: []evdPoint{
				{0, 0}, {time.Second, 1}, {2 * time.Second, 2}, {3 * time.Second, 3},
				{4 * time.Second, 4}, {5 * time.Second, 5}, {6 * time.Second, 6}, {7 * time.Second, 7},
			},
			want: 1, wantN: 8, startEnd: [2]time.Duration{0, 7 * time.Second},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := evdFloatSeriesWindow(t, evdConfig(), tc.pts...)
			got, err := evdDefaultSeries(t, w).Rate()
			requireNoErr(t, err, "Rate")
			assertRateResult(t, got, tc.want, tc.wantN, tc.wantDiscs,
				evdAt(tc.startEnd[0]), evdAt(tc.startEnd[1]), tc.name)
		})
	}
}

// EVD-051 (edge): 초 미만 간격에서도 실제 timestamp 간격만 쓴다 — 외삽·보간으로
// "초당"을 만들어 내지 않는다. 분모가 이진수로 정확하지 않으므로 PerSecond는
// 상대 오차로 단정한다 (EVD-008).
func TestEVD051_SubSecondIntervalsUseActualSpan(t *testing.T) {
	cases := []struct {
		name  string
		span  time.Duration
		delta float64
		want  float64
	}{
		{"1ns 간격", time.Nanosecond, 1, 1e9},
		{"1µs 간격", time.Microsecond, 1, 1e6},
		{"1ms 간격", time.Millisecond, 2, 2000},
		{"100ms 간격", 100 * time.Millisecond, 1, 10},
		{"1.5s 간격", 1500 * time.Millisecond, 3, 2},
		{"333ns 간격", 333 * time.Nanosecond, 999, 3e9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := evdFloatSeriesWindow(t, evdConfig(), evdPoint{0, 0}, evdPoint{tc.span, tc.delta})
			got, err := evdDefaultSeries(t, w).Rate()
			requireNoErr(t, err, "Rate")
			assertRateInvariants(t, got, tc.name)
			if got.Samples != 2 || got.Discontinuities != 0 {
				t.Errorf("Samples=%d Discontinuities=%d, want 2·0", got.Samples, got.Discontinuities)
			}
			if !got.Start.Equal(evdAt(0)) || !got.End.Equal(evdAt(tc.span)) {
				t.Errorf("Start·End = %s..%s, want %s..%s",
					evdTimeString(got.Start), evdTimeString(got.End),
					evdTimeString(evdAt(0)), evdTimeString(evdAt(tc.span)))
			}
			assertRateNear(t, got.PerSecond, tc.want, 1e-12, tc.name)
		})
	}
}

// EVD-052 (edge): 변화가 없는 시리즈의 PerSecond는 정확히 0이고
// Discontinuities도 0이다.
func TestEVD052_UnchangedSeriesHasZeroRate(t *testing.T) {
	cases := []struct {
		name  string
		pts   []evdPoint
		wantN int
		span  time.Duration
	}{
		{"모든 값이 42", []evdPoint{{0, 42}, {10 * time.Second, 42}, {25 * time.Second, 42}}, 3, 25 * time.Second},
		{"모든 값이 0", []evdPoint{{0, 0}, {time.Second, 0}}, 2, time.Second},
		{"모든 값이 2^64-1", []evdPoint{{0, float64(math.MaxUint64)}, {time.Second, float64(math.MaxUint64)}}, 2, time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := evdFloatSeriesWindow(t, evdConfig(), tc.pts...)
			got, err := evdDefaultSeries(t, w).Rate()
			requireNoErr(t, err, "Rate")
			// 3.6 규칙 6: "정확히 0" — 근사 비교가 아니라 등호로 단정한다.
			// (−0.0과 +0.0의 구별은 스펙이 규정하지 않으므로 단정하지 않는다.)
			assertRateResult(t, got, 0, tc.wantN, 0, evdAt(0), evdAt(tc.span), tc.name)
		})
	}
}

// EVD-053 (edge): vᵢ₊₁ < vᵢ인 인접 쌍마다 discontinuity 하나를 세고, 감소 직후의
// 재상승은 정상 증가로 집계된다.
func TestEVD053_DiscontinuityDetection(t *testing.T) {
	cases := []struct {
		name      string
		pts       []evdPoint
		wantRate  float64
		wantN     int
		wantDiscs int
		span      time.Duration
	}{
		{
			// 스펙 예시: 100, 250, 30, 90 → 기여 150 + 30 + 60 = 240, 30초.
			name: "감소 1회 + 재상승",
			pts: []evdPoint{
				{0, 100}, {10 * time.Second, 250}, {20 * time.Second, 30}, {30 * time.Second, 90},
			},
			wantRate: 8, wantN: 4, wantDiscs: 1, span: 30 * time.Second,
		},
		{
			// 100, 50, 200, 10 → 기여 50 + 150 + 10 = 210, 30초.
			name: "감소 2회",
			pts: []evdPoint{
				{0, 100}, {10 * time.Second, 50}, {20 * time.Second, 200}, {30 * time.Second, 10},
			},
			wantRate: 7, wantN: 4, wantDiscs: 2, span: 30 * time.Second,
		},
		{
			// 매 구간 감소: 300, 200, 100 → 기여 200 + 100 = 300, 20초.
			name: "모든 인접 쌍이 감소",
			pts: []evdPoint{
				{0, 300}, {10 * time.Second, 200}, {20 * time.Second, 100},
			},
			wantRate: 15, wantN: 3, wantDiscs: 2, span: 20 * time.Second,
		},
		{
			// 1만큼의 미세한 감소도 discontinuity다.
			name: "1만큼의 감소",
			pts: []evdPoint{
				{0, 100}, {10 * time.Second, 99},
			},
			wantRate: 9.9, wantN: 2, wantDiscs: 1, span: 10 * time.Second,
		},
		{
			// 0으로의 감소 — 기여는 0이다.
			name: "0으로 reset",
			pts: []evdPoint{
				{0, 500}, {10 * time.Second, 0},
			},
			wantRate: 0, wantN: 2, wantDiscs: 1, span: 10 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := evdFloatSeriesWindow(t, evdConfig(), tc.pts...)
			got, err := evdDefaultSeries(t, w).Rate()
			requireNoErr(t, err, "Rate")
			assertRateResult(t, got, tc.wantRate, tc.wantN, tc.wantDiscs,
				evdAt(0), evdAt(tc.span), tc.name)
		})
	}
}

// EVD-054 (edge): 감소 쌍의 기여는 감소 후 값이고, 어떤 입력 경로로도 PerSecond가
// 음수로 반환되지 않는다 (결정 지점 ② — 일괄 reset).
func TestEVD054_ResetCorrectionIsNonNegative(t *testing.T) {
	t.Run("스펙 예시 (t0,100)(t0+10s,40) → 4.0", func(t *testing.T) {
		w := evdFloatSeriesWindow(t, evdConfig(), evdPoint{0, 100}, evdPoint{10 * time.Second, 40})
		got, err := evdDefaultSeries(t, w).Rate()
		requireNoErr(t, err, "Rate")
		assertRateResult(t, got, 4, 2, 1, evdAt(0), evdAt(10*time.Second), "reset 보정")
	})

	t.Run("감소가 아무리 커도 음수가 되지 않는다", func(t *testing.T) {
		cases := []struct {
			name string
			pts  []evdPoint
		}{
			{"2^64-1 → 0", []evdPoint{{0, float64(math.MaxUint64)}, {time.Second, 0}}},
			{"1e300 → 1", []evdPoint{{0, 1e300}, {time.Second, 1}}},
			{"1e300 → 0 → 0", []evdPoint{{0, 1e300}, {time.Second, 0}, {2 * time.Second, 0}}},
			{"100 → 0 → 0 → 0", []evdPoint{{0, 100}, {time.Second, 0}, {2 * time.Second, 0}, {3 * time.Second, 0}}},
			{"계단식 감소", []evdPoint{{0, 5}, {time.Second, 4}, {2 * time.Second, 3}, {3 * time.Second, 2}, {4 * time.Second, 1}}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				w := evdFloatSeriesWindow(t, evdConfig(), tc.pts...)
				got, err := evdDefaultSeries(t, w).Rate()
				requireNoErr(t, err, "Rate")
				assertRateInvariants(t, got, tc.name)
			})
		}
	})
}

// EVD-055 (edge): 간격 0은 나누기 이전에 구조적으로 차단된다 — 같은 시리즈·같은
// instant의 두 관측이 공존할 수 없으므로 Rate가 성공하면 항상 t_N − t₁ > 0이다.
func TestEVD055_ZeroIntervalIsBlockedBeforeDivision(t *testing.T) {
	at := evdAt(0)

	t.Run("동등하지 않은 같은 instant는 Add에서 거부된다", func(t *testing.T) {
		w := mustAdd(t, mustWindow(t, evdConfig()), evdFloat(t, at, 100))
		conflicting := evdFloat(t, at, 200)
		conflicting.ID = "obs-충돌"
		_, err := w.Add(conflicting)
		requireEvdConflict(t, err, "같은 instant·다른 값")

		// 관측이 하나뿐이므로 Rate는 샘플 부족이다 (0으로 나누지 않는다).
		got, err := evdDefaultSeries(t, w).Rate()
		requireEvdInsufficientSamples(t, err, "충돌 후 Rate")
		assertZeroRateResult(t, got, "충돌 후 Rate")
	})

	t.Run("동등한 같은 instant는 재전송이라 샘플이 늘지 않는다", func(t *testing.T) {
		w := mustAdd(t, mustWindow(t, evdConfig()), evdFloat(t, at, 100))
		resend := evdFloat(t, at, 100)
		resend.ID = "obs-재전송"
		w = mustAdd(t, w, resend)

		got, err := evdDefaultSeries(t, w).Rate()
		requireEvdInsufficientSamples(t, err, "재전송 후 Rate")
		assertZeroRateResult(t, got, "재전송 후 Rate")
	})

	t.Run("성공한 Rate는 항상 End > Start이고 NaN·±Inf가 없다", func(t *testing.T) {
		// 표현 가능한 가장 짧은 간격(1ns)에서도 성립한다.
		w := evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, 0}, evdPoint{time.Nanosecond, 1}, evdPoint{2 * time.Nanosecond, 0})
		got, err := evdDefaultSeries(t, w).Rate()
		requireNoErr(t, err, "Rate(1ns 간격)")
		assertRateInvariants(t, got, "1ns 간격")
	})
}

// EVD-056 (edge): 32-bit wrap 경계는 감소로 감지되고, 2^64−1·MaxInt64는 정당한
// counter 값으로 다뤄진다 (sentinel로 해석해 거부·강등하지 않는다).
func TestEVD056_WrapBoundaryAndMaxCounterValues(t *testing.T) {
	now := evdAt(time.Minute)

	t.Run("32-bit 경계 근방의 감소는 discontinuity다", func(t *testing.T) {
		w := evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, 4294967290}, evdPoint{10 * time.Second, 5})
		got, err := evdDefaultSeries(t, w).Rate()
		requireNoErr(t, err, "Rate(wrap)")
		// 결정 지점 ②: 일괄 reset — 기여는 감소 후 값 5, 10초 → 0.5.
		assertRateResult(t, got, 0.5, 2, 1, evdAt(0), evdAt(10*time.Second), "32-bit wrap")
	})

	t.Run("32-bit 최대값 자체도 정당한 값이다", func(t *testing.T) {
		w := evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, 4294967295}, evdPoint{10 * time.Second, 4294967295})
		got, err := evdDefaultSeries(t, w).Rate()
		requireNoErr(t, err, "Rate(2^32-1 유지)")
		assertRateResult(t, got, 0, 2, 0, evdAt(0), evdAt(10*time.Second), "2^32-1")
	})

	t.Run("float64(2^64-1)은 sentinel이 아니라 counter 값이다", func(t *testing.T) {
		maxU := float64(math.MaxUint64)

		w := evdFloatSeriesWindow(t, evdConfig(), evdPoint{0, 0}, evdPoint{10 * time.Second, maxU})

		// Add·조회: 값이 그대로 보존된다.
		obs := evdDefaultSeries(t, w).Observations()
		if len(obs) != 2 {
			t.Fatalf("Observations() 길이 %d, want 2", len(obs))
		}
		f, ok := obs[1].Value.Float()
		if !ok || f != maxU {
			t.Errorf("보존된 값 = %v (ok=%t), want %s", f, ok, evdFloatString(maxU))
		}

		// Rate: 유한·비음수로 계산된다.
		got, err := evdDefaultSeries(t, w).Rate()
		requireNoErr(t, err, "Rate(2^64-1)")
		assertRateInvariants(t, got, "2^64-1")
		if got.Samples != 2 || got.Discontinuities != 0 {
			t.Errorf("Samples=%d Discontinuities=%d, want 2·0", got.Samples, got.Discontinuities)
		}
		assertRateNear(t, got.PerSecond, maxU/10, 1e-12, "2^64-1의 PerSecond")

		// Level: 강등되지 않는다.
		assertLevel(t, w, evdSubjectA(t), evdSigA, now, model.LevelObserved, "2^64-1 관측의 Level")
	})

	t.Run("math.MaxInt64 Int 값도 counter 값이다", func(t *testing.T) {
		w := mustAdd(t, mustWindow(t, evdConfig()),
			evdInt(t, evdAt(0), 0),
			evdInt(t, evdAt(10*time.Second), math.MaxInt64),
		)

		obs := evdDefaultSeries(t, w).Observations()
		if len(obs) != 2 {
			t.Fatalf("Observations() 길이 %d, want 2", len(obs))
		}
		i, ok := obs[1].Value.Int()
		if !ok || i != math.MaxInt64 {
			t.Errorf("보존된 값 = %d (ok=%t), want %d", i, ok, int64(math.MaxInt64))
		}

		got, err := evdDefaultSeries(t, w).Rate()
		requireNoErr(t, err, "Rate(MaxInt64)")
		assertRateInvariants(t, got, "MaxInt64")
		assertRateNear(t, got.PerSecond, float64(math.MaxInt64)/10, 1e-12, "MaxInt64의 PerSecond")

		assertLevel(t, w, evdSubjectA(t), evdSigA, now, model.LevelObserved, "MaxInt64 관측의 Level")
	})

	t.Run("2^64-1에서 0으로의 감소는 reset이다", func(t *testing.T) {
		w := evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, float64(math.MaxUint64)}, evdPoint{10 * time.Second, 0})
		got, err := evdDefaultSeries(t, w).Rate()
		requireNoErr(t, err, "Rate")
		assertRateResult(t, got, 0, 2, 1, evdAt(0), evdAt(10*time.Second), "2^64-1 → 0")
	})
}

// EVD-057 (edge): 극단값·극단 간격의 조합에서도 결과는 항상 유한하고 0 이상이다.
//
// 스펙이 명시한 정의역 — 2⁶⁴ 근방의 float64, 1ns 간격, Horizon 한계의 긴 간격 —
// 을 조합해 훑는다 (artifacts/discrepancy-notes-tests.md의 E-1 참조).
func TestEVD057_ExtremeValuesAndIntervals(t *testing.T) {
	maxU := float64(math.MaxUint64)
	longCfg := evidence.Config{MaxAge: evdMaxAge, Horizon: 24 * time.Hour, MaxSamples: 8}

	cases := []struct {
		name string
		cfg  evidence.Config
		pts  []evdPoint
	}{
		{"2^64-1 · 1ns 간격", evdConfig(), []evdPoint{{0, 0}, {time.Nanosecond, maxU}}},
		{"2^64-1 · 1ns 간격 · 감소", evdConfig(), []evdPoint{{0, maxU}, {time.Nanosecond, 0}}},
		{"2^64 근방 왕복 · 1ns 간격", evdConfig(), []evdPoint{
			{0, maxU}, {time.Nanosecond, 0}, {2 * time.Nanosecond, maxU}, {3 * time.Nanosecond, 0},
		}},
		{"2^64-1 · 24시간 간격", longCfg, []evdPoint{{0, 0}, {24 * time.Hour, maxU}}},
		{"매우 작은 값 · 24시간 간격", longCfg, []evdPoint{{0, 0}, {24 * time.Hour, math.SmallestNonzeroFloat64}}},
		{"매우 작은 값 · 1ns 간격", evdConfig(), []evdPoint{
			{0, 0}, {time.Nanosecond, math.SmallestNonzeroFloat64},
		}},
		{"1ns 간격 8샘플 계단", evdConfig(), []evdPoint{
			{0, 0}, {time.Nanosecond, 1}, {2 * time.Nanosecond, 2}, {3 * time.Nanosecond, 3},
			{4 * time.Nanosecond, 4}, {5 * time.Nanosecond, 5}, {6 * time.Nanosecond, 6},
			{7 * time.Nanosecond, 7},
		}},
		{"2^53 경계 근방 (float64 정수 정밀도 한계)", evdConfig(), []evdPoint{
			{0, 9007199254740992}, {time.Second, 9007199254740993},
		}},
		{"Horizon 한계에 걸친 8샘플", longCfg, []evdPoint{
			{0, 0}, {3 * time.Hour, maxU / 8}, {6 * time.Hour, maxU / 4}, {9 * time.Hour, 0},
			{12 * time.Hour, maxU / 2}, {15 * time.Hour, maxU}, {18 * time.Hour, 1}, {24 * time.Hour, maxU},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := evdFloatSeriesWindow(t, tc.cfg, tc.pts...)
			got, err := evdDefaultSeries(t, w).Rate()
			requireNoErr(t, err, "Rate")
			assertRateInvariants(t, got, tc.name)
			if got.Samples != len(tc.pts) {
				t.Errorf("%s: Samples = %d, want %d", tc.name, got.Samples, len(tc.pts))
			}
		})
	}
}

// EVD-057 (edge, v1.1 포화 단정): 참값이 float64로 표현 가능한 범위를 넘으면
// PerSecond는 **정확히** math.MaxFloat64로 포화한다 (3.6 규칙 6).
//
// v1.0은 "유한·0 이상"만 요구하고 그 값을 규정하지 않아 오류 반환·다른 클램프
// 값도 합격이었다(회차 1 E-1이 지적한 계약 공백). v1.1이 반환값을 규정했으므로
// 이제 등호로 단정한다. 기여 cᵢ가 전부 비음수이고 분모가 양수이므로(3.6 규칙
// 2·4·5) 오버플로 방향은 양(+)뿐이고 포화값은 math.MaxFloat64 하나다.
func TestEVD057_SaturationAtMaxFloat64(t *testing.T) {
	// (a) 나눗셈 오버플로 — 총 증가량 자체는 표현 가능하지만 1ns(=1e-9초)로
	// 나눈 참값(≈1.8e317)이 범위를 넘는다.
	t.Run("(a) 나눗셈 오버플로 — 1ns 간격의 math.MaxFloat64 증가", func(t *testing.T) {
		w := evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, 0}, evdPoint{time.Nanosecond, math.MaxFloat64})

		got, err := evdDefaultSeries(t, w).Rate()
		requireNoErr(t, err, "Rate(나눗셈 오버플로) — 포화는 오류가 아니다")
		assertRateResult(t, got, math.MaxFloat64, 2, 0,
			evdAt(0), evdAt(time.Nanosecond), "(a) 나눗셈 오버플로의 포화")
	})

	// (b) 누산 오버플로 — 기여 열은 math.MaxFloat64 + 0 + math.MaxFloat64다.
	// 감소 쌍의 기여가 "감소 후 값"(3.6 규칙 4)이라 reset이 누산을 비우지 않기
	// 때문이다. 요점은 **총 증가량의 참값(2 × math.MaxFloat64)만 범위를 넘고,
	// 경과 3초로 나눈 참 rate(≈1.198e308)는 표현 가능 범위 안**이라는 것이다 —
	// 그런데도 포화값이 반환되어야 한다(§6.1 A의 "의도된 귀결").
	//
	// 이 성질이 권고 의미론을 다음 두 부류의 구현과 구별한다. 둘 다 유한·비음수라
	// v1.0의 불변식만으로는 통과했다.
	//
	//   - **누산 단계 클램프 후 나누기**: 누산기를 math.MaxFloat64로 클램프한 뒤
	//     3으로 나눠 ≈5.99e307을 반환한다.
	//   - **확장 정밀도 누산**(또는 기여를 각각 나눈 뒤 합산 — Σ(cᵢ ÷ 경과초)):
	//     2 × math.MaxFloat64 ÷ 3 ≈ 1.198e308을 반환한다. 3.6 규칙 6의 경계
	//     판정 기준은 "규칙 4·5의 산술을 float64로 수행한 결과"이고 규칙 4의
	//     산술은 총 증가량 Σcᵢ이므로, 그 단계의 오버플로가 포화를 발동시킨다.
	t.Run("(b) 누산 오버플로 — 나눈 참 rate가 표현 가능해도 포화한다", func(t *testing.T) {
		w := evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, 0},
			evdPoint{time.Second, math.MaxFloat64},
			evdPoint{2 * time.Second, 0},
			evdPoint{3 * time.Second, math.MaxFloat64},
		)

		got, err := evdDefaultSeries(t, w).Rate()
		requireNoErr(t, err, "Rate(누산 오버플로) — 포화는 오류가 아니다")

		// 배제 대상 구현의 반환값을 이름 붙여 두고, 걸리면 어느 의미론인지
		// 진단으로 알린다 (본 단정은 아래 assertRateResult다).
		clampThenDivide := math.MaxFloat64 / 3
		extendedPrecision := math.MaxFloat64 * 2 / 3
		switch got.PerSecond {
		case clampThenDivide:
			t.Errorf("PerSecond = %s — 누산 단계에서 클램프한 뒤 나눈 값이다. "+
				"3.6 규칙 6은 총 증가량의 참값이 범위를 넘으면 경과 시간으로 나눈 결과와 "+
				"무관하게 math.MaxFloat64로 포화하도록 규정한다",
				evdFloatString(got.PerSecond))
		case extendedPrecision:
			t.Errorf("PerSecond = %s — 총 증가량을 float64 범위 밖에서 정확히 다룬 뒤 "+
				"나눈 값이다(확장 정밀도 누산 또는 Σ(cᵢ ÷ 경과초)). 3.6 규칙 6의 경계 "+
				"판정은 규칙 4의 산술(Σcᵢ)을 float64로 수행한 결과가 기준이므로 "+
				"이 입력은 포화한다", evdFloatString(got.PerSecond))
		}

		assertRateResult(t, got, math.MaxFloat64, 4, 1,
			evdAt(0), evdAt(3*time.Second), "(b) 누산 오버플로의 포화")
	})

	// (c) 포화는 참값이 범위를 넘을 때만이다 (3.6 규칙 5·6) — 표현 가능한
	// 참값은 그대로 반환되어야 하며, 값이 크다는 이유로 앞당겨 포화하면 안 된다.
	t.Run("(c) 표현 가능한 참값은 포화하지 않는다", func(t *testing.T) {
		// 증가량 math.MaxFloat64/2, 경과 1초 — 참값이 정확히 표현된다.
		w := evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, 0}, evdPoint{time.Second, math.MaxFloat64 / 2})

		got, err := evdDefaultSeries(t, w).Rate()
		requireNoErr(t, err, "Rate(포화 직전)")
		if got.PerSecond == math.MaxFloat64 {
			t.Errorf("PerSecond가 math.MaxFloat64로 포화했다 — 참값 %s는 표현 가능하다",
				evdFloatString(math.MaxFloat64/2))
		}
		assertRateResult(t, got, math.MaxFloat64/2, 2, 0,
			evdAt(0), evdAt(time.Second), "(c) 포화 직전의 정상 계산")
	})
}

// EVD-058 (edge): 같은 instant에 함께 감소한 여러 시리즈는 각각 독립적으로
// discontinuity를 감지·보정하며 서로의 결과에 영향을 주지 않는다 (재부팅 시나리오).
func TestEVD058_SimultaneousResetsAreIndependent(t *testing.T) {
	resetAt := 20 * time.Second

	// 세 시리즈가 같은 instant에 함께 감소한다 (감소 폭은 서로 다르다).
	type spec struct {
		name string
		src  model.SourceRef
		subj func(*testing.T) model.AssetRef
		pts  []evdPoint
	}
	specs := []spec{
		{"prom/subjA", evdSrcProm, evdSubjectA, []evdPoint{{0, 100}, {10 * time.Second, 200}, {resetAt, 10}}},
		{"gnmi/subjA", evdSrcGNMI, evdSubjectA, []evdPoint{{0, 1000}, {10 * time.Second, 3000}, {resetAt, 500}}},
		{"prom/subjB", evdSrcProm, evdSubjectB, []evdPoint{{0, 7}, {10 * time.Second, 7}, {resetAt, 7}}},
	}

	// 시리즈 하나만 있는 window에서의 결과를 먼저 구한다.
	isolated := make(map[string]string, len(specs))
	for _, sp := range specs {
		w := mustWindow(t, evdConfig())
		for _, p := range sp.pts {
			w = mustAdd(t, w, evdRetag(evdFloat(t, evdAt(p.d), p.v), sp.src, sp.subj(t), evdSigA, nil))
		}
		r, err := evdSoleSeries(t, w, sp.subj(t), evdSigA).Rate()
		requireNoErr(t, err, sp.name+"의 단독 Rate")
		isolated[sp.name] = evdRateString(r)
	}

	// 셋을 한 window에 함께 담아도 각 결과가 같아야 한다.
	combined := mustWindow(t, evdConfig())
	for _, sp := range specs {
		for _, p := range sp.pts {
			combined = mustAdd(t, combined,
				evdRetag(evdFloat(t, evdAt(p.d), p.v), sp.src, sp.subj(t), evdSigA, nil))
		}
	}

	for _, sp := range specs {
		t.Run(sp.name, func(t *testing.T) {
			s := evdSeriesAt(t, combined, sp.subj(t), evdSigA, sp.src, nil)
			r, err := s.Rate()
			requireNoErr(t, err, "Rate")
			if got := evdRateString(r); got != isolated[sp.name] {
				t.Errorf("함께 담았을 때 결과가 달라졌다\n단독: %s\n합침: %s", isolated[sp.name], got)
			}
		})
	}

	// 각 시리즈의 discontinuity가 독립적으로 세어졌는지 직접 확인한다.
	promA, err := evdSeriesAt(t, combined, evdSubjectA(t), evdSigA, evdSrcProm, nil).Rate()
	requireNoErr(t, err, "Rate(prom/subjA)")
	// 기여 100 + 10 = 110, 20초 → 5.5, discontinuity 1.
	assertRateResult(t, promA, 5.5, 3, 1, evdAt(0), evdAt(resetAt), "prom/subjA")

	gnmiA, err := evdSeriesAt(t, combined, evdSubjectA(t), evdSigA, evdSrcGNMI, nil).Rate()
	requireNoErr(t, err, "Rate(gnmi/subjA)")
	// 기여 2000 + 500 = 2500, 20초 → 125, discontinuity 1.
	assertRateResult(t, gnmiA, 125, 3, 1, evdAt(0), evdAt(resetAt), "gnmi/subjA")

	promB, err := evdSeriesAt(t, combined, evdSubjectB(t), evdSigA, evdSrcProm, nil).Rate()
	requireNoErr(t, err, "Rate(prom/subjB)")
	// 변화 없음 — 이웃 시리즈의 reset에 물들지 않는다.
	assertRateResult(t, promB, 0, 3, 0, evdAt(0), evdAt(resetAt), "prom/subjB")
}

// EVD-059 (edge, v1.1): **전제 N ≥ 2** — N < 2이면 값 kind와 무관하게 3.6 규칙
// 1(샘플 요건)이 규칙 2(값 요건)보다 먼저 적용된다. 따라서 비수치·음수 샘플
// **1개뿐인** 시리즈는 model.ErrInvalid가 아니라 ErrInsufficientSamples다
// (EVD-050이 값 kind에 조건을 걸지 않는다).
//
// 아래 TestEVD059_RateRejectsNonNumericOrNegativeSamples의 모든 케이스는
// N ≥ 2이므로 이 전제와 충돌하지 않는다 — 이 테스트가 그 경계 반대편을 고정한다.
func TestEVD059_SingleSampleOutranksValueRequirement(t *testing.T) {
	cases := []struct {
		name string
		v    model.Value
	}{
		{"Bool 1개", model.NewBoolValue(true)},
		{"String 1개", model.NewStringValue("Ethernet1/1")},
		{"음수 Float 1개", model.NewFloatValue(-1)},
		{"음수 Int 1개", model.NewIntValue(math.MinInt64)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := mustAdd(t, mustWindow(t, evdConfig()), evdObs(t, evdAt(0), tc.v))

			got, err := evdDefaultSeries(t, w).Rate()
			requireEvdInsufficientSamples(t, err, "Rate("+tc.name+")")
			// v1.1이 명시한 우선 관계: 규칙 1이 규칙 2를 앞선다.
			if errors.Is(err, model.ErrInvalid) {
				t.Errorf("Rate(%s) = %v — N < 2에서는 값 요건(규칙 2)보다 샘플 요건(규칙 1)이 "+
					"먼저 적용되므로 model.ErrInvalid 계열이면 안 된다", tc.name, err)
			}
			assertZeroRateResult(t, got, "Rate("+tc.name+")")
		})
	}
}

// EVD-059 (edge): rate 대상 제약 — (N ≥ 2인) 시리즈에 비수치 kind나 음수 수치
// 샘플이 있으면 model.ErrInvalid 계열 오류다.
func TestEVD059_RateRejectsNonNumericOrNegativeSamples(t *testing.T) {
	cases := []struct {
		name   string
		values []model.Value
	}{
		{"Bool 샘플 하나", []model.Value{model.NewFloatValue(1), model.NewBoolValue(true)}},
		{"Bool 샘플이 처음", []model.Value{model.NewBoolValue(false), model.NewFloatValue(1)}},
		{"Bool 샘플이 가운데", []model.Value{model.NewFloatValue(1), model.NewBoolValue(true), model.NewFloatValue(3)}},
		{"String 샘플 하나", []model.Value{model.NewFloatValue(1), model.NewStringValue("Ethernet1/1")}},
		{"String 샘플만", []model.Value{model.NewStringValue("a"), model.NewStringValue("b")}},
		{"음수 Float", []model.Value{model.NewFloatValue(-1), model.NewFloatValue(1)}},
		{"음수 Float이 마지막", []model.Value{model.NewFloatValue(1), model.NewFloatValue(-0.5)}},
		{"음수 Int", []model.Value{model.NewIntValue(-1), model.NewIntValue(10)}},
		{"음수 Int이 가운데", []model.Value{model.NewIntValue(1), model.NewIntValue(-1), model.NewIntValue(5)}},
		{"MinInt64", []model.Value{model.NewIntValue(0), model.NewIntValue(math.MinInt64)}},
		{"Bool과 음수가 함께", []model.Value{model.NewBoolValue(true), model.NewFloatValue(-1)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := mustWindow(t, evdConfig())
			for i, v := range tc.values {
				w = mustAdd(t, w, evdObs(t, evdAt(time.Duration(i)*time.Second), v))
			}
			got, err := evdDefaultSeries(t, w).Rate()
			requireErrInvalid(t, err, "Rate("+tc.name+")")
			assertZeroRateResult(t, got, "Rate("+tc.name+")")
		})
	}
}

// EVD-059 (edge): Float과 Int가 섞인 비음수 수치 시리즈는 정상 계산된다.
func TestEVD059_MixedFloatAndIntIsComputed(t *testing.T) {
	w := mustAdd(t, mustWindow(t, evdConfig()),
		evdInt(t, evdAt(0), 100),
		evdFloat(t, evdAt(10*time.Second), 150),
		evdInt(t, evdAt(20*time.Second), 200),
	)
	got, err := evdDefaultSeries(t, w).Rate()
	requireNoErr(t, err, "Rate(혼합 kind)")
	assertRateResult(t, got, 5, 3, 0, evdAt(0), evdAt(20*time.Second), "Float·Int 혼합")

	// 0은 유효한 counter 값이다 (음수가 아니다).
	zeroStart := mustAdd(t, mustWindow(t, evdConfig()),
		evdInt(t, evdAt(0), 0),
		evdFloat(t, evdAt(10*time.Second), 0),
		evdInt(t, evdAt(20*time.Second), 20),
	)
	got, err = evdDefaultSeries(t, zeroStart).Rate()
	requireNoErr(t, err, "Rate(0 포함)")
	assertRateResult(t, got, 1, 3, 0, evdAt(0), evdAt(20*time.Second), "0 포함")
}

// EVD-059 (edge): Quality·Unit·Sequence는 rate에 관여하지 않는다 (3.6 규칙 7).
func TestEVD059_QualityUnitSequenceDoNotAffectRate(t *testing.T) {
	build := func(t *testing.T, mut func(i int, o *model.Observation)) evidence.Window {
		t.Helper()
		w := mustWindow(t, evdConfig())
		for i, p := range []evdPoint{{0, 100}, {10 * time.Second, 250}, {20 * time.Second, 30}} {
			o := evdFloat(t, evdAt(p.d), p.v)
			mut(i, &o)
			w = mustAdd(t, w, o)
		}
		return w
	}

	plain := build(t, func(int, *model.Observation) {})
	varied := build(t, func(i int, o *model.Observation) {
		switch i % 3 {
		case 0:
			o.Quality = model.QualityDegraded
		case 1:
			o.Quality = model.QualityUnknown
		default:
			o.Quality = model.QualityGood
		}
		o.Unit = "bytes"
		o.Sequence = uint64(i * 1000)
	})

	rPlain, err := evdDefaultSeries(t, plain).Rate()
	requireNoErr(t, err, "Rate(기본)")
	rVaried, err := evdDefaultSeries(t, varied).Rate()
	requireNoErr(t, err, "Rate(quality·unit·sequence 상이)")

	if evdRateString(rPlain) != evdRateString(rVaried) {
		t.Errorf("Quality·Unit·Sequence가 rate에 관여했다\n기본: %s\n변형: %s",
			evdRateString(rPlain), evdRateString(rVaried))
	}
}

// 3.6: Rate는 시리즈의 보존 관측 "전부"를 쓴다 — 축출로 샘플이 줄면 결과도
// 그 샘플들만 반영한다.
func TestSpec36_RateUsesAllRetainedSamplesOnly(t *testing.T) {
	cfg := evidence.Config{MaxAge: evdMaxAge, Horizon: time.Hour, MaxSamples: 3}

	w := evdFloatSeriesWindow(t, cfg,
		evdPoint{0, 0},
		evdPoint{10 * time.Second, 100},
		evdPoint{20 * time.Second, 200},
		evdPoint{30 * time.Second, 300},
	)
	// t0는 축출되어 남은 것은 t10·t20·t30이다.
	got, err := evdDefaultSeries(t, w).Rate()
	requireNoErr(t, err, "Rate")
	assertRateResult(t, got, 10, 3, 0, evdAt(10*time.Second), evdAt(30*time.Second), "축출 후 Rate")
}
