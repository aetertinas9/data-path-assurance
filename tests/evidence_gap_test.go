// evidence_gap_test.go — scrape gap과 source 기초 사실
// (specs/evidence/spec.md 1절·3.5, EVD-080~082).
package tests

import (
	"reflect"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// EVD-080 (edge): 부재는 0이 아니다 — gap 구간에 값이 합성되지 않는다.
func TestEVD080_MissingScrapesSynthesizeNothing(t *testing.T) {
	t.Run("gap이 있어도 discontinuity가 생기지 않는다", func(t *testing.T) {
		// 10초 간격이어야 할 곳에 30초 gap이 있다. 부재를 0으로 읽으면
		// 감소가 두 번 생겨 Discontinuities가 0이 아니게 된다.
		w := evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, 0}, evdPoint{10 * time.Second, 10}, evdPoint{40 * time.Second, 40})

		s := evdDefaultSeries(t, w)
		assertObservedAts(t, s, []time.Time{evdAt(0), evdAt(10 * time.Second), evdAt(40 * time.Second)},
			"gap 구간에 관측이 합성되지 않는다")

		got, err := s.Rate()
		requireNoErr(t, err, "Rate(gap 있음)")
		assertRateResult(t, got, 1, 3, 0, evdAt(0), evdAt(40*time.Second), "gap 시리즈")
	})

	t.Run("gap의 유무는 rate 값을 바꾸지 않는다 (timestamp가 같은 한)", func(t *testing.T) {
		gapped := evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, 0}, evdPoint{10 * time.Second, 10}, evdPoint{40 * time.Second, 40})
		dense := evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, 0}, evdPoint{10 * time.Second, 10}, evdPoint{20 * time.Second, 20},
			evdPoint{30 * time.Second, 30}, evdPoint{40 * time.Second, 40})

		rGap, err := evdDefaultSeries(t, gapped).Rate()
		requireNoErr(t, err, "Rate(gap)")
		rDense, err := evdDefaultSeries(t, dense).Rate()
		requireNoErr(t, err, "Rate(dense)")

		if rGap.PerSecond != rDense.PerSecond {
			t.Errorf("gap 유무로 PerSecond가 달라졌다: %s vs %s",
				evdFloatString(rGap.PerSecond), evdFloatString(rDense.PerSecond))
		}
		if rGap.Discontinuities != rDense.Discontinuities {
			t.Errorf("gap 유무로 Discontinuities가 달라졌다: %d vs %d",
				rGap.Discontinuities, rDense.Discontinuities)
		}
		// 보존된 샘플 수 자체는 다르다 — gap 구간에 값을 만들어 넣지 않기 때문이다.
		if rGap.Samples != 3 || rDense.Samples != 5 {
			t.Errorf("Samples = %d(gap) / %d(dense), want 3 / 5", rGap.Samples, rDense.Samples)
		}
	})

	t.Run("값 0의 관측은 데이터다 (집계에 참여한다)", func(t *testing.T) {
		w := evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, 0}, evdPoint{10 * time.Second, 0}, evdPoint{20 * time.Second, 100})

		s := evdDefaultSeries(t, w)
		if got := len(s.Observations()); got != 3 {
			t.Errorf("Observations() 길이 %d, want 3 (값 0도 보존된다)", got)
		}
		got, err := s.Rate()
		requireNoErr(t, err, "Rate")
		assertRateResult(t, got, 5, 3, 0, evdAt(0), evdAt(20*time.Second), "값 0 포함")
	})

	t.Run("긴 gap 뒤의 관측도 그대로 쓰인다", func(t *testing.T) {
		cfg := evidence.Config{MaxAge: evdMaxAge, Horizon: 24 * time.Hour, MaxSamples: 8}
		w := evdFloatSeriesWindow(t, cfg, evdPoint{0, 0}, evdPoint{10 * time.Hour, 36000})
		got, err := evdDefaultSeries(t, w).Rate()
		requireNoErr(t, err, "Rate(10시간 gap)")
		assertRateResult(t, got, 1, 2, 0, evdAt(0), evdAt(10*time.Hour), "10시간 gap")
	})
}

// EVD-081 (edge): gap은 이상 신호가 아니라 freshness 상실을 거쳐 판정 유보로
// 이어진다. 0 관측과 관측 부재는 구별된다.
func TestEVD081_GapLeadsToWithheldJudgementNotAnAnomaly(t *testing.T) {
	// 두 시리즈: 하나는 gap으로 최신 관측이 오래됐고, 다른 하나는 같은 시각에
	// 값 0을 정상 보고했다. 서로 다른 (subject, signal)에 둔다.
	now := evdAt(10 * time.Minute)

	gapped := mustAdd(t, mustWindow(t, evdConfig()),
		evdRetag(evdFloat(t, evdAt(0), 100), evdSrcProm, evdSubjectA(t), evdSigA, nil),
		evdRetag(evdFloat(t, evdAt(time.Minute), 110), evdSrcProm, evdSubjectA(t), evdSigA, nil),
	)
	reporting := mustAdd(t, gapped,
		evdRetag(evdFloat(t, evdAt(9*time.Minute), 0), evdSrcProm, evdSubjectB(t), evdSigA, nil),
		evdRetag(evdFloat(t, evdAt(10*time.Minute-time.Second), 0), evdSrcProm, evdSubjectB(t), evdSigA, nil),
	)

	t.Run("gap으로 deadline을 지난 시리즈는 stale·Unknown이다", func(t *testing.T) {
		s := evdSoleSeries(t, reporting, evdSubjectA(t), evdSigA)
		assertFresh(t, s, now, false, "gap 시리즈")
		assertLevel(t, reporting, evdSubjectA(t), evdSigA, now, model.LevelUnknown, "gap 시리즈")
	})

	t.Run("값 0을 정상 보고한 시리즈는 신선하고 Unknown이 아니다", func(t *testing.T) {
		s := evdSoleSeries(t, reporting, evdSubjectB(t), evdSigA)
		assertFresh(t, s, now, true, "값 0 보고 시리즈")

		got, err := reporting.Level(evdSubjectB(t), evdSigA, now)
		requireNoErr(t, err, "Level")
		if got == model.LevelUnknown {
			t.Errorf("값 0을 정상 보고한 시리즈가 Unknown으로 판정되었다")
		}
		assertLevel(t, reporting, evdSubjectB(t), evdSigA, now, model.LevelObserved, "값 0 보고 시리즈")
	})

	t.Run("gap 시리즈의 내용은 남아 있다 (판정만 유보된다)", func(t *testing.T) {
		s := evdSoleSeries(t, reporting, evdSubjectA(t), evdSigA)
		assertObservedAts(t, s, []time.Time{evdAt(0), evdAt(time.Minute)}, "gap 시리즈의 보존 내용")

		// rate도 그대로 계산된다 — freshness와 rate는 별개의 판정이다.
		got, err := s.Rate()
		requireNoErr(t, err, "Rate(stale 시리즈)")
		assertRateInvariants(t, got, "stale 시리즈의 Rate")
	})

	t.Run("gap이 길어지는 과정에서 판정은 경계에서 한 번만 바뀐다", func(t *testing.T) {
		s := evdSoleSeries(t, reporting, evdSubjectA(t), evdSigA)
		deadline := evdAt(time.Minute).Add(evdMaxAge)

		assertFresh(t, s, deadline.Add(-time.Nanosecond), true, "deadline 직전")
		assertFresh(t, s, deadline, false, "deadline")
		assertLevel(t, reporting, evdSubjectA(t), evdSigA, deadline.Add(-time.Nanosecond),
			model.LevelObserved, "deadline 직전")
		assertLevel(t, reporting, evdSubjectA(t), evdSigA, deadline,
			model.LevelUnknown, "deadline")
	})
}

// EVD-082 (edge): source의 생존·사망을 판정하는 API를 제공하지 않는다 —
// 제공하는 것은 Sources()와 SourceLastObserved()라는 기초 사실뿐이다.
//
// "제공하지 않는다"는 부재 주장이라 블랙박스 호출로는 확인할 수 없으므로,
// (1) 기초 사실 두 가지가 계약대로 동작하는지, (2) liveness 판정으로 읽힐
// 이름의 메서드가 공개 타입에 없는지를 reflect로 확인한다
// (artifacts/discrepancy-notes-tests.md의 E-2 참조).
func TestEVD082_NoSourceLivenessAPI(t *testing.T) {
	t.Run("기초 사실 두 가지는 계약대로 동작한다", func(t *testing.T) {
		w := mustAdd(t, mustWindow(t, evdConfig()),
			evdRetag(evdFloat(t, evdAt(0), 1), evdSrcProm, evdSubjectA(t), evdSigA, nil),
			evdRetag(evdFloat(t, evdAt(30*time.Second), 2), evdSrcGNMI, evdSubjectA(t), evdSigA, nil),
		)

		sources := w.Sources()
		if len(sources) != 2 {
			t.Fatalf("Sources() 길이 %d, want 2", len(sources))
		}
		for _, src := range sources {
			last, err := w.SourceLastObserved(src)
			requireNoErr(t, err, "SourceLastObserved")
			if last.IsZero() {
				t.Errorf("보존 관측이 있는 source %s/%s의 SourceLastObserved가 zero time이다", src.Type, src.Name)
			}
		}

		// 관측이 한 번도 없던 source는 (zero time, nil) — "죽었다"가 아니라
		// "이 window에는 없다"라는 기초 사실이다.
		last, err := w.SourceLastObserved(evdSrcAgent)
		requireNoErr(t, err, "SourceLastObserved(관측 없는 source)")
		if !last.IsZero() {
			t.Errorf("관측 없는 source의 SourceLastObserved = %s, want zero time", evdTimeString(last))
		}
	})

	t.Run("liveness 판정 API가 노출되어 있지 않다", func(t *testing.T) {
		forbidden := []string{
			"SourceAlive", "SourceIsAlive", "IsSourceAlive", "SourceLiveness",
			"SourceUp", "SourceIsUp", "IsSourceUp", "SourceDown", "SourceDead",
			"LiveSources", "DeadSources", "StaleSources", "MissingSources",
			"SourceHealth", "SourceHealthy", "SourceStatus", "SourceState",
			"SourceFresh", "SourceIsFresh",
		}

		var w evidence.Window
		var s evidence.Series
		for _, target := range []struct {
			name string
			typ  reflect.Type
		}{
			{"evidence.Window", reflect.TypeOf(w)},
			{"evidence.Series", reflect.TypeOf(s)},
		} {
			for _, name := range forbidden {
				if _, ok := target.typ.MethodByName(name); ok {
					t.Errorf("%s에 liveness 판정 API %s가 노출되어 있다 (EVD-082)", target.name, name)
				}
			}
		}
	})

	t.Run("up 같은 소스 생존 사실은 일반 시리즈로 소비된다", func(t *testing.T) {
		now := evdAt(time.Minute)
		upSignal := model.SignalRef("source.up")

		// 어댑터가 up == 0을 관측으로 주입한 상황.
		o := evdObs(t, evdAt(0), model.NewFloatValue(0))
		o = evdRetag(o, evdSrcProm, evdSubjectA(t), upSignal, nil)
		w := mustAdd(t, mustWindow(t, evdConfig()), o)

		signals, err := w.Signals(evdSubjectA(t))
		requireNoErr(t, err, "Signals")
		if len(signals) != 1 || signals[0] != upSignal {
			t.Fatalf("Signals() = %v, want [%s]", signals, upSignal.String())
		}

		s := evdSoleSeries(t, w, evdSubjectA(t), upSignal)
		latest, ok := s.Latest()
		if !ok {
			t.Fatalf("Latest() ok = false")
		}
		if f, fok := latest.Value.Float(); !fok || f != 0 {
			t.Errorf("보존된 값 = %v (ok=%t), want 0", f, fok)
		}

		// 특별 취급 없이 일반 시리즈로 판정된다 — up==0이라고 Unknown이 되지 않는다.
		assertFresh(t, s, now, true, "up 시리즈")
		assertLevel(t, w, evdSubjectA(t), upSignal, now, model.LevelObserved, "up 시리즈")
	})
}
