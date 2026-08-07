// evidence_window_test.go — 생성과 zero value (specs/evidence/spec.md 3.3,
// EVD-010~012) 그리고 3.6의 zero value Series.
package tests

import (
	"math"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// EVD-010: 유효한 Config면 오류 없이 빈 Window를 반환한다.
func TestEVD010_NewWindowWithValidConfig(t *testing.T) {
	now := evdAt(time.Minute)

	cases := []struct {
		name string
		cfg  evidence.Config
	}{
		{"기본 구성", evdConfig()},
		{"MaxSamples 최소값 1", evidence.Config{MaxAge: time.Minute, Horizon: time.Minute, MaxSamples: 1}},
		{"최소 단위(1ns) MaxAge·Horizon", evidence.Config{MaxAge: time.Nanosecond, Horizon: time.Nanosecond, MaxSamples: 1}},
		{"Horizon < MaxAge (대소 제약 없음)", evidence.Config{MaxAge: time.Hour, Horizon: time.Second, MaxSamples: 4}},
		{"Horizon > MaxAge", evidence.Config{MaxAge: time.Second, Horizon: time.Hour, MaxSamples: 4}},
		{"Horizon == MaxAge", evidence.Config{MaxAge: time.Minute, Horizon: time.Minute, MaxSamples: 4}},
		{"극단적으로 큰 값", evidence.Config{MaxAge: math.MaxInt64, Horizon: math.MaxInt64, MaxSamples: math.MaxInt32}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, err := evidence.NewWindow(tc.cfg)
			requireNoErr(t, err, "NewWindow")
			assertEmptyWindow(t, w, now, "NewWindow의 빈 Window")

			// 조립 가능한 Window다 (zero Window와 구별되는 지점).
			next := mustAdd(t, w, evdFloat(t, evdAt(0), 1))
			if got := len(next.Subjects()); got != 1 {
				t.Errorf("Add 후 Subjects() 길이 %d, want 1", got)
			}
			// EVD-020: 수신자는 변하지 않는다.
			assertEmptyWindow(t, w, now, "Add 후의 수신자")
		})
	}
}

// EVD-011 (edge): 무효한 Config는 zero Window와 model.ErrInvalid 계열 오류다.
func TestEVD011_NewWindowRejectsInvalidConfig(t *testing.T) {
	now := evdAt(time.Minute)

	cases := []struct {
		name string
		cfg  evidence.Config
	}{
		{"zero value Config{}", evidence.Config{}},
		{"MaxAge가 0", evidence.Config{MaxAge: 0, Horizon: time.Hour, MaxSamples: 4}},
		{"MaxAge가 음수", evidence.Config{MaxAge: -time.Nanosecond, Horizon: time.Hour, MaxSamples: 4}},
		{"MaxAge가 크게 음수", evidence.Config{MaxAge: math.MinInt64, Horizon: time.Hour, MaxSamples: 4}},
		{"Horizon이 0", evidence.Config{MaxAge: time.Minute, Horizon: 0, MaxSamples: 4}},
		{"Horizon이 음수", evidence.Config{MaxAge: time.Minute, Horizon: -time.Second, MaxSamples: 4}},
		{"MaxSamples가 0", evidence.Config{MaxAge: time.Minute, Horizon: time.Hour, MaxSamples: 0}},
		{"MaxSamples가 음수", evidence.Config{MaxAge: time.Minute, Horizon: time.Hour, MaxSamples: -1}},
		{"MaxSamples가 크게 음수", evidence.Config{MaxAge: time.Minute, Horizon: time.Hour, MaxSamples: math.MinInt32}},
		{"MaxAge·Horizon 둘 다 0", evidence.Config{MaxSamples: 4}},
		{"셋 다 위반", evidence.Config{MaxAge: -1, Horizon: -1, MaxSamples: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, err := evidence.NewWindow(tc.cfg)
			requireErrInvalid(t, err, "NewWindow("+tc.name+")")
			assertZeroWindow(t, w, now, "무효 Config의 반환 Window")
		})
	}
}

// EVD-012 (edge): zero value Window는 조회·판정에 안전하고 조립은 불가능하다.
func TestEVD012_ZeroWindowIsReadableButNotAssemblable(t *testing.T) {
	now := evdAt(time.Minute)

	var w evidence.Window
	assertZeroWindow(t, w, now, "var w evidence.Window")

	t.Run("조회는 빈 window와 같은 결과다", func(t *testing.T) {
		empty := mustWindow(t, evdConfig())
		assertSnapshotUnchanged(t, evdSnapshot(t, empty, now), evdSnapshot(t, w, now),
			"zero Window와 NewWindow의 빈 Window")
	})

	t.Run("Add·Prune 후에도 zero Window는 변하지 않는다", func(t *testing.T) {
		before := evdSnapshot(t, w, now)
		got, err := w.Add(evdFloat(t, evdAt(0), 1))
		requireErrInvalid(t, err, "zero Window의 Add")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, got, now), "Add가 반환한 Window")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, w, now), "수신자")

		got, err = w.Prune(now)
		requireErrInvalid(t, err, "zero Window의 Prune")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, got, now), "Prune이 반환한 Window")
	})

	t.Run("값 복사·전달도 안전하다", func(t *testing.T) {
		copyOfW := w
		assertEmptyWindow(t, copyOfW, now, "zero Window의 복사본")
		pass := func(inner evidence.Window) { assertEmptyWindow(t, inner, now, "인자로 전달된 zero Window") }
		pass(w)
	})
}

// 3.6 (edge): zero value Series는 빈 시리즈로 동작한다 (조회 안전).
func TestSpec36_ZeroSeriesBehavesAsEmpty(t *testing.T) {
	now := evdAt(time.Minute)

	var s evidence.Series

	if got := s.Source(); got != (model.SourceRef{}) {
		t.Errorf("zero Series의 Source() = %+v, want zero value", got)
	}
	if got := s.Signal(); got != model.SignalRef("") {
		t.Errorf("zero Series의 Signal() = %q, want 빈 SignalRef", got.String())
	}
	if got := s.Subject(); got.Kind != model.AssetKind(0) || got.Canonical != "" || len(got.Aliases) != 0 {
		t.Errorf("zero Series의 Subject() = %+v, want zero value", got)
	}
	if got := s.Dimensions(); len(got) != 0 {
		t.Errorf("zero Series의 Dimensions() 길이 %d, want 0", len(got))
	}
	if got := s.Observations(); len(got) != 0 {
		t.Errorf("zero Series의 Observations() 길이 %d, want 0", len(got))
	}

	// 3.6: 빈(zero value) 시리즈의 Latest는 (zero, false)다.
	latest, ok := s.Latest()
	if ok {
		t.Errorf("zero Series의 Latest() ok = true, want false")
	}
	if latest.ID != "" || !latest.ObservedAt.IsZero() {
		t.Errorf("zero Series의 Latest() = %+v, want zero Observation", latest)
	}

	// 3.6 (결정 지점 ⑤): 빈 시리즈의 Fresh는 (false, nil)이다.
	fresh, err := s.Fresh(now)
	requireNoErr(t, err, "zero Series의 Fresh(유효 now)")
	if fresh {
		t.Errorf("zero Series의 Fresh() = true, want false")
	}

	// EVD-044: zero now는 오류다.
	fresh, err = s.Fresh(zeroTime)
	requireErrInvalid(t, err, "zero Series의 Fresh(zero now)")
	if fresh {
		t.Errorf("오류 시 false를 기대했으나 true를 받았다")
	}

	// EVD-050: 관측 0개면 ErrInsufficientSamples.
	r, err := s.Rate()
	requireEvdInsufficientSamples(t, err, "zero Series의 Rate")
	assertZeroRateResult(t, r, "zero Series의 Rate")

	// Dimensions()의 반환 맵을 변형해도 안전하다 (EVD-006).
	mustNotPanic(t, "zero Series의 Dimensions() 변형", func() {
		d := s.Dimensions()
		if d != nil {
			d["새키"] = "새값"
		}
	})
}

// 3.3: Window는 불변 값 타입이다 — 값 복사·인자 전달이 자유롭고 서로 영향을
// 주지 않는다 (룰 시그니처가 값으로 받는 전제).
func TestSpec33_WindowIsAnImmutableValueType(t *testing.T) {
	now := evdAt(time.Minute)

	original := evdFloatSeriesWindow(t, evdConfig(), evdPoint{0, 100}, evdPoint{10 * time.Second, 200})
	before := evdSnapshot(t, original, now)

	byValue := original
	derived := mustAdd(t, byValue, evdFloat(t, evdAt(20*time.Second), 300))

	// 인자로 전달한 window에 파생 연산을 해도 호출자의 값은 변하지 않는다.
	consume := func(w evidence.Window) evidence.Window {
		return mustAdd(t, w, evdFloat(t, evdAt(30*time.Second), 400))
	}
	consumed := consume(original)

	assertSnapshotUnchanged(t, before, evdSnapshot(t, original, now), "원본 Window")
	assertSnapshotUnchanged(t, before, evdSnapshot(t, byValue, now), "값 복사본")

	if got := len(evdDefaultSeries(t, derived).Observations()); got != 3 {
		t.Errorf("파생 Window의 관측 %d개, want 3", got)
	}
	if got := len(evdDefaultSeries(t, consumed).Observations()); got != 3 {
		t.Errorf("함수가 만든 Window의 관측 %d개, want 3", got)
	}
	if got := len(evdDefaultSeries(t, original).Observations()); got != 2 {
		t.Errorf("원본 Window의 관측 %d개, want 2", got)
	}
}
