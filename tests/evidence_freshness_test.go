// evidence_freshness_test.go — freshness (specs/evidence/spec.md 3.1, EVD-040~045).
package tests

import (
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// evdFreshnessWindow는 관측 하나짜리 기본 시리즈를 만든다 (ExpiresAt 지정 가능 —
// zeroTime이면 만료 없음).
func evdFreshnessWindow(t *testing.T, at, expires time.Time) evidence.Window {
	t.Helper()
	o := evdFloat(t, at, 1)
	o.ExpiresAt = expires
	return mustAdd(t, mustWindow(t, evdConfig()), o)
}

// EVD-040: deadline은 min(비-zero ExpiresAt, ObservedAt + MaxAge)이고, now가
// deadline보다 이전이면 신선하다. 둘 중 이른 쪽이 항상 이긴다.
func TestEVD040_FreshnessDeadlineIsTheEarlierOfExpiresAtAndMaxAge(t *testing.T) {
	observedAt := evdAt(0)

	cases := []struct {
		name    string
		expires time.Time
		now     time.Time
		want    bool
	}{
		// ExpiresAt 없음 → ObservedAt + MaxAge(5분)만 본다.
		{"만료 없음 · 관측 직후", zeroTime, observedAt.Add(time.Nanosecond), true},
		{"만료 없음 · MaxAge 안쪽", zeroTime, observedAt.Add(time.Minute), true},
		{"만료 없음 · MaxAge 직전", zeroTime, observedAt.Add(evdMaxAge - time.Nanosecond), true},
		{"만료 없음 · MaxAge 이후", zeroTime, observedAt.Add(6 * time.Minute), false},

		// ExpiresAt이 MaxAge 상한보다 이르다 → ExpiresAt이 판정을 정한다.
		{"ExpiresAt이 이름 · 그 전", observedAt.Add(2 * time.Minute), observedAt.Add(time.Minute), true},
		{"ExpiresAt이 이름 · 그 후 (MaxAge 안쪽인데도 stale)", observedAt.Add(2 * time.Minute), observedAt.Add(3 * time.Minute), false},
		{"ExpiresAt이 이름 · MaxAge 직전에도 stale", observedAt.Add(2 * time.Minute), observedAt.Add(evdMaxAge - time.Nanosecond), false},

		// ExpiresAt이 MaxAge 상한보다 늦다 → MaxAge 상한이 판정을 정한다.
		{"ExpiresAt이 늦음 · MaxAge 안쪽", observedAt.Add(10 * time.Minute), observedAt.Add(4 * time.Minute), true},
		{"ExpiresAt이 늦음 · MaxAge 이후 (ExpiresAt 전인데도 stale)", observedAt.Add(10 * time.Minute), observedAt.Add(6 * time.Minute), false},
		{"ExpiresAt이 늦음 · ExpiresAt 직전에도 stale", observedAt.Add(10 * time.Minute), observedAt.Add(10*time.Minute - time.Nanosecond), false},

		// ExpiresAt과 MaxAge 상한이 같으면 결과는 하나뿐이다.
		{"ExpiresAt == ObservedAt+MaxAge · 그 전", observedAt.Add(evdMaxAge), observedAt.Add(evdMaxAge - time.Nanosecond), true},
		{"ExpiresAt == ObservedAt+MaxAge · 그 시점", observedAt.Add(evdMaxAge), observedAt.Add(evdMaxAge), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := evdFreshnessWindow(t, observedAt, tc.expires)
			assertFresh(t, evdDefaultSeries(t, w), tc.now, tc.want, tc.name)

			// 시리즈가 하나뿐이므로 Level은 신선도를 그대로 따른다.
			wantLevel := model.LevelUnknown
			if tc.want {
				wantLevel = model.LevelObserved
			}
			assertLevel(t, w, evdSubjectA(t), evdSigA, tc.now, wantLevel, tc.name)
		})
	}
}

// EVD-041 (edge): 유효 구간은 반개구간 [ObservedAt, deadline)이다 — 경계
// instant는 stale 쪽이다. 표로는 경계가 흐려지므로 개별 단정으로 쓴다.
func TestEVD041_DeadlineInstantIsStale_MaxAge(t *testing.T) {
	observedAt := evdAt(0)
	deadline := observedAt.Add(evdMaxAge)

	w := evdFreshnessWindow(t, observedAt, zeroTime)
	s := evdDefaultSeries(t, w)

	assertFresh(t, s, deadline.Add(-time.Nanosecond), true, "deadline − 1ns")
	assertFresh(t, s, deadline, false, "deadline 정확히")
	assertFresh(t, s, deadline.Add(time.Nanosecond), false, "deadline + 1ns")

	assertLevel(t, w, evdSubjectA(t), evdSigA, deadline.Add(-time.Nanosecond), model.LevelObserved, "deadline − 1ns")
	assertLevel(t, w, evdSubjectA(t), evdSigA, deadline, model.LevelUnknown, "deadline 정확히")
}

// EVD-041 (edge): ExpiresAt이 deadline을 정할 때도 경계 instant는 stale이다.
func TestEVD041_DeadlineInstantIsStale_ExpiresAt(t *testing.T) {
	observedAt := evdAt(0)
	expires := observedAt.Add(2 * time.Minute)

	w := evdFreshnessWindow(t, observedAt, expires)
	s := evdDefaultSeries(t, w)

	assertFresh(t, s, expires.Add(-time.Nanosecond), true, "ExpiresAt − 1ns")
	assertFresh(t, s, expires, false, "ExpiresAt 정확히")
	assertFresh(t, s, expires.Add(time.Nanosecond), false, "ExpiresAt + 1ns")

	assertLevel(t, w, evdSubjectA(t), evdSigA, expires.Add(-time.Nanosecond), model.LevelObserved, "ExpiresAt − 1ns")
	assertLevel(t, w, evdSubjectA(t), evdSigA, expires, model.LevelUnknown, "ExpiresAt 정확히")
}

// EVD-041 (edge): now가 ObservedAt과 정확히 같으면 신선하다 (반개구간의 왼쪽은
// 닫혀 있다).
func TestEVD041_ObservedAtInstantIsFresh(t *testing.T) {
	observedAt := evdAt(0)
	w := evdFreshnessWindow(t, observedAt, zeroTime)
	assertFresh(t, evdDefaultSeries(t, w), observedAt, true, "now == ObservedAt")
}

// EVD-042 (edge): zero ExpiresAt은 "만료 없음"이지 "즉시 만료"가 아니다.
func TestEVD042_ZeroExpiresAtMeansNoExpiry(t *testing.T) {
	observedAt := evdAt(0)
	w := evdFreshnessWindow(t, observedAt, zeroTime)
	s := evdDefaultSeries(t, w)

	// zero ExpiresAt이 "즉시 만료"로 읽히면 아래 두 단정이 깨진다.
	assertFresh(t, s, observedAt, true, "관측 시점")
	assertFresh(t, s, observedAt.Add(time.Nanosecond), true, "관측 직후")
	assertFresh(t, s, observedAt.Add(evdMaxAge-time.Nanosecond), true, "MaxAge 직전")
	assertFresh(t, s, observedAt.Add(evdMaxAge), false, "MaxAge 경계")

	assertLevel(t, w, evdSubjectA(t), evdSigA, observedAt.Add(time.Minute), model.LevelObserved, "관측 1분 후")
}

// EVD-043 (edge): ObservedAt이 now 이후인 관측(미래 시각)은 stale이 아니다.
func TestEVD043_FutureObservedAtIsFresh(t *testing.T) {
	now := evdAt(0)

	cases := []struct {
		name    string
		at      time.Time
		expires time.Time
	}{
		{"1ns 미래", now.Add(time.Nanosecond), zeroTime},
		{"10분 미래 (MaxAge보다 큼)", now.Add(10 * time.Minute), zeroTime},
		{"1시간 미래", now.Add(time.Hour), zeroTime},
		{"미래 관측 + 더 미래의 ExpiresAt", now.Add(10 * time.Minute), now.Add(20 * time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := evdFreshnessWindow(t, tc.at, tc.expires)
			assertFresh(t, evdDefaultSeries(t, w), now, true, tc.name)
			assertLevel(t, w, evdSubjectA(t), evdSigA, now, model.LevelObserved, tc.name)
		})
	}
}

// EVD-044 (edge): zero time now는 zero value와 model.ErrInvalid 계열 오류다.
func TestEVD044_ZeroNowIsInvalid(t *testing.T) {
	w := evdFreshnessWindow(t, evdAt(0), zeroTime)

	t.Run("Series.Fresh", func(t *testing.T) {
		got, err := evdDefaultSeries(t, w).Fresh(zeroTime)
		requireErrInvalid(t, err, "Fresh(zero time)")
		if got {
			t.Errorf("Fresh = true, want false (zero value)")
		}
	})

	t.Run("Window.Level", func(t *testing.T) {
		var zeroLevel model.EvidenceLevel
		got, err := w.Level(evdSubjectA(t), evdSigA, zeroTime)
		requireErrInvalid(t, err, "Level(zero time)")
		if got != zeroLevel {
			t.Errorf("Level = %s, want zero EvidenceLevel", got.String())
		}
	})

	t.Run("빈 Window·zero Series에서도 zero now는 오류다", func(t *testing.T) {
		empty := mustWindow(t, evdConfig())
		var zeroLevel model.EvidenceLevel
		got, err := empty.Level(evdSubjectA(t), evdSigA, zeroTime)
		requireErrInvalid(t, err, "빈 Window의 Level(zero time)")
		if got != zeroLevel {
			t.Errorf("Level = %s, want zero EvidenceLevel", got.String())
		}

		var s evidence.Series
		fresh, err := s.Fresh(zeroTime)
		requireErrInvalid(t, err, "zero Series의 Fresh(zero time)")
		if fresh {
			t.Errorf("Fresh = true, want false")
		}
	})
}

// EVD-045 (edge): ReceivedAt은 신선도·level·rate 어디에도 관여하지 않는다.
func TestEVD045_ReceivedAtDoesNotAffectJudgements(t *testing.T) {
	now := evdAt(time.Minute)

	build := func(t *testing.T, delay time.Duration) evidence.Window {
		t.Helper()
		w := mustWindow(t, evdConfig())
		for _, p := range []evdPoint{{0, 100}, {10 * time.Second, 200}} {
			o := evdFloat(t, evdAt(p.d), p.v)
			o.ReceivedAt = evdAt(p.d).Add(delay)
			o.ID = "obs-recv-" + evdTimeString(evdAt(p.d))
			w = mustAdd(t, w, o)
		}
		return w
	}

	fast := build(t, time.Millisecond)
	slow := build(t, 3*time.Hour) // 수집이 아주 늦게 도착한 경우

	freshFast, err := evdDefaultSeries(t, fast).Fresh(now)
	requireNoErr(t, err, "Fresh(fast)")
	freshSlow, err := evdDefaultSeries(t, slow).Fresh(now)
	requireNoErr(t, err, "Fresh(slow)")
	if freshFast != freshSlow {
		t.Errorf("ReceivedAt에 따라 Fresh가 달라졌다: %t vs %t", freshFast, freshSlow)
	}

	lvlFast, err := fast.Level(evdSubjectA(t), evdSigA, now)
	requireNoErr(t, err, "Level(fast)")
	lvlSlow, err := slow.Level(evdSubjectA(t), evdSigA, now)
	requireNoErr(t, err, "Level(slow)")
	if lvlFast != lvlSlow {
		t.Errorf("ReceivedAt에 따라 Level이 달라졌다: %s vs %s", lvlFast.String(), lvlSlow.String())
	}

	rateFast, err := evdDefaultSeries(t, fast).Rate()
	requireNoErr(t, err, "Rate(fast)")
	rateSlow, err := evdDefaultSeries(t, slow).Rate()
	requireNoErr(t, err, "Rate(slow)")
	if evdRateString(rateFast) != evdRateString(rateSlow) {
		t.Errorf("ReceivedAt에 따라 Rate가 달라졌다: %s vs %s",
			evdRateString(rateFast), evdRateString(rateSlow))
	}

	// 지연 기록 자체는 보존·노출된다 (판정에만 들어가지 않는다).
	obs := evdDefaultSeries(t, slow).Observations()
	if len(obs) != 2 {
		t.Fatalf("Observations() 길이 %d, want 2", len(obs))
	}
	if !obs[0].ReceivedAt.Equal(evdAt(0).Add(3 * time.Hour)) {
		t.Errorf("보존된 ReceivedAt = %s, want %s",
			evdTimeString(obs[0].ReceivedAt), evdTimeString(evdAt(0).Add(3*time.Hour)))
	}
}

// 3.1: 시리즈의 신선도는 최신 관측(ObservedAt 최대)의 신선도와 같다 —
// 오래된 관측이 stale해도 최신 관측이 신선하면 시리즈는 신선하다.
func TestSpec31_SeriesFreshnessFollowsLatestObservation(t *testing.T) {
	w := mustAdd(t, mustWindow(t, evdConfig()),
		evdFloat(t, evdAt(0), 100),
		evdFloat(t, evdAt(4*time.Minute), 200),
	)
	s := evdDefaultSeries(t, w)

	// now = t+6분: 첫 관측은 이미 stale(deadline t+5분)이지만 최신 관측의
	// deadline은 t+9분이므로 시리즈는 신선하다.
	now := evdAt(6 * time.Minute)
	assertFresh(t, s, now, true, "최신 관측이 신선함")
	assertLevel(t, w, evdSubjectA(t), evdSigA, now, model.LevelObserved, "최신 관측이 신선함")

	// now = t+9분: 최신 관측의 deadline 경계 → stale.
	assertFresh(t, s, evdAt(9*time.Minute), false, "최신 관측의 deadline")
	assertLevel(t, w, evdSubjectA(t), evdSigA, evdAt(9*time.Minute), model.LevelUnknown, "최신 관측의 deadline")

	// 최신 관측의 ExpiresAt만 이르면 시리즈 전체가 stale이 된다.
	late := evdFloat(t, evdAt(8*time.Minute), 300)
	late.ExpiresAt = evdAt(8*time.Minute + time.Second)
	w2 := mustAdd(t, w, late)
	assertFresh(t, evdDefaultSeries(t, w2), evdAt(8*time.Minute+2*time.Second), false,
		"최신 관측의 ExpiresAt이 지남")
}
