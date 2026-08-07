// evidence_contract_test.go — internal/evidence의 공통 규약
// (specs/evidence/spec.md 3.1·3.2, EVD-002~008).
package tests

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// EVD-002 (edge): 어떤 공개 함수·메서드도 어떤 입력(zero value 수신자·인자 포함)
// 에서도 panic하지 않는다.
func TestEVD002_PublicAPIDoesNotPanic(t *testing.T) {
	now := evdAt(time.Minute)

	mustNotPanic(t, "NewWindow(극단 Config)", func() {
		configs := []evidence.Config{
			{},
			{MaxAge: -1, Horizon: -1, MaxSamples: -1},
			{MaxAge: math.MaxInt64, Horizon: math.MaxInt64, MaxSamples: math.MaxInt},
			{MaxAge: math.MinInt64, Horizon: math.MinInt64, MaxSamples: math.MinInt},
			{MaxAge: 1, Horizon: 1, MaxSamples: 1},
		}
		for _, cfg := range configs {
			_, _ = evidence.NewWindow(cfg)
		}
	})

	mustNotPanic(t, "zero value Window의 모든 연산", func() {
		var w evidence.Window
		_ = w.Subjects()
		_ = w.Sources()
		_, _ = w.Signals(model.AssetRef{})
		_, _ = w.Signals(evdSubjectA(t))
		_, _ = w.SeriesFor(model.AssetRef{}, model.SignalRef(""))
		_, _ = w.SeriesFor(evdSubjectA(t), evdSigA)
		_, _ = w.SourceLastObserved(model.SourceRef{})
		_, _ = w.SourceLastObserved(evdSrcProm)
		_, _ = w.Level(model.AssetRef{}, model.SignalRef(""), zeroTime)
		_, _ = w.Level(evdSubjectA(t), evdSigA, now)
		_, _ = w.Add(model.Observation{})
		_, _ = w.Add(evdFloat(t, evdAt(0), 1))
		_, _ = w.Prune(zeroTime)
		_, _ = w.Prune(now)
	})

	mustNotPanic(t, "zero value Series의 모든 연산", func() {
		var s evidence.Series
		_ = s.Source()
		_ = s.Subject()
		_ = s.Signal()
		_ = s.Dimensions()
		_ = s.Observations()
		_, _ = s.Latest()
		_, _ = s.Fresh(zeroTime)
		_, _ = s.Fresh(now)
		_, _ = s.Rate()
	})

	mustNotPanic(t, "구성된 Window의 무효 인자 연산", func() {
		w := mustWindow(t, evdConfig())
		w = mustAdd(t, w, evdFloat(t, evdAt(0), 1))
		_, _ = w.Add(model.Observation{})
		_, _ = w.Add(evdObs(t, evdAt(time.Second), model.NewFloatValue(math.NaN())))
		_, _ = w.Add(evdObs(t, evdAt(time.Second), model.Value{}))
		_, _ = w.Prune(zeroTime)
		_, _ = w.Signals(model.AssetRef{})
		_, _ = w.SeriesFor(evdSubjectA(t), model.SignalRef("점없음"))
		_, _ = w.SourceLastObserved(model.SourceRef{Type: "", Name: ""})
		_, _ = w.Level(evdSubjectA(t), evdSigA, zeroTime)
	})

	mustNotPanic(t, "극단 값·시각의 Rate", func() {
		w := evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, 0},
			evdPoint{time.Nanosecond, float64(math.MaxUint64)},
			evdPoint{2 * time.Nanosecond, 0},
		)
		s := evdDefaultSeries(t, w)
		_, _ = s.Rate()
		_, _ = s.Fresh(now)
	})
}

// EVD-003: 무효 인자를 받은 연산은 model.ErrInvalid 계열 오류를 반환하고,
// Window 반환 위치에는 수신자와 관측상 동일한 Window를, 그 외에는 zero value를
// 반환한다.
func TestEVD003_InvalidArgumentsReturnErrInvalid(t *testing.T) {
	now := evdAt(time.Minute)
	base := mustAdd(t, mustWindow(t, evdConfig()),
		evdFloat(t, evdAt(0), 10),
		evdFloat(t, evdAt(10*time.Second), 20),
	)
	before := evdSnapshot(t, base, now)

	invalidSubjects := []struct {
		name string
		ref  model.AssetRef
	}{
		{"zero value AssetRef", model.AssetRef{}},
		{"Kind만 유효하고 Canonical이 빔", model.AssetRef{Kind: model.KindNICPort}},
		{"Canonical만 있고 Kind가 zero", model.AssetRef{Canonical: canonicalString}},
	}
	invalidSignals := []struct {
		name string
		sig  model.SignalRef
	}{
		{"빈 문자열", model.SignalRef("")},
		{"세그먼트 1개", model.SignalRef("fabric")},
		{"대문자 포함", model.SignalRef("Fabric.Port")},
		{"빈 세그먼트", model.SignalRef("fabric..port")},
	}
	invalidSources := []struct {
		name string
		src  model.SourceRef
	}{
		{"zero value SourceRef", model.SourceRef{}},
		{"Type이 빔", model.SourceRef{Name: "prom-main"}},
		{"Name이 빔", model.SourceRef{Type: string(model.SourceTypePrometheus)}},
	}

	t.Run("Signals(무효 subject)", func(t *testing.T) {
		for _, tc := range invalidSubjects {
			got, err := base.Signals(tc.ref)
			requireErrInvalid(t, err, "Signals("+tc.name+")")
			if len(got) != 0 {
				t.Errorf("%s: 오류 시 zero value(길이 0)를 기대했으나 %v를 받았다", tc.name, got)
			}
		}
	})

	t.Run("SeriesFor(무효 인자)", func(t *testing.T) {
		for _, tc := range invalidSubjects {
			got, err := base.SeriesFor(tc.ref, evdSigA)
			requireErrInvalid(t, err, "SeriesFor(subject="+tc.name+")")
			if len(got) != 0 {
				t.Errorf("subject=%s: 오류 시 길이 0을 기대했으나 %d개를 받았다", tc.name, len(got))
			}
		}
		for _, tc := range invalidSignals {
			got, err := base.SeriesFor(evdSubjectA(t), tc.sig)
			requireErrInvalid(t, err, "SeriesFor(signal="+tc.name+")")
			if len(got) != 0 {
				t.Errorf("signal=%s: 오류 시 길이 0을 기대했으나 %d개를 받았다", tc.name, len(got))
			}
		}
	})

	t.Run("SourceLastObserved(무효 source)", func(t *testing.T) {
		for _, tc := range invalidSources {
			got, err := base.SourceLastObserved(tc.src)
			requireErrInvalid(t, err, "SourceLastObserved("+tc.name+")")
			if !got.IsZero() {
				t.Errorf("%s: 오류 시 zero time을 기대했으나 %s를 받았다", tc.name, evdTimeString(got))
			}
		}
	})

	t.Run("Level(무효 인자)", func(t *testing.T) {
		var zeroLevel model.EvidenceLevel
		check := func(what string, got model.EvidenceLevel, err error) {
			t.Helper()
			requireErrInvalid(t, err, what)
			if got != zeroLevel {
				t.Errorf("%s: 오류 시 zero EvidenceLevel을 기대했으나 %s를 받았다", what, got.String())
			}
		}
		for _, tc := range invalidSubjects {
			got, err := base.Level(tc.ref, evdSigA, now)
			check("Level(subject="+tc.name+")", got, err)
		}
		for _, tc := range invalidSignals {
			got, err := base.Level(evdSubjectA(t), tc.sig, now)
			check("Level(signal="+tc.name+")", got, err)
		}
		got, err := base.Level(evdSubjectA(t), evdSigA, zeroTime)
		check("Level(now=zero time)", got, err)
	})

	t.Run("Fresh(zero now)", func(t *testing.T) {
		s := evdDefaultSeries(t, base)
		got, err := s.Fresh(zeroTime)
		requireErrInvalid(t, err, "Fresh(zero time)")
		if got {
			t.Errorf("오류 시 false를 기대했으나 true를 받았다")
		}
	})

	t.Run("Add·Prune의 오류는 window를 파괴하지 않는다", func(t *testing.T) {
		afterAdd, err := base.Add(model.Observation{})
		requireErrInvalid(t, err, "Add(무효 관측)")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, afterAdd, now), "Add 오류 시 반환 Window")

		afterPrune, err := base.Prune(zeroTime)
		requireErrInvalid(t, err, "Prune(zero cutoff)")
		assertSnapshotUnchanged(t, before, evdSnapshot(t, afterPrune, now), "Prune 오류 시 반환 Window")

		// 3.2: `w, err = w.Add(o)` 패턴에서 window를 잃지 않는다 — 반환된
		// Window로 계속 조립할 수 있어야 한다.
		next := mustAdd(t, afterAdd, evdFloat(t, evdAt(20*time.Second), 30))
		if got := len(evdDefaultSeries(t, next).Observations()); got != 3 {
			t.Errorf("오류 후 반환된 Window로 조립한 결과 관측 %d개, want 3", got)
		}
		next = mustAdd(t, afterPrune, evdFloat(t, evdAt(20*time.Second), 30))
		if got := len(evdDefaultSeries(t, next).Observations()); got != 3 {
			t.Errorf("Prune 오류 후 반환된 Window로 조립한 결과 관측 %d개, want 3", got)
		}
	})

	t.Run("수신자 window는 변하지 않는다", func(t *testing.T) {
		assertSnapshotUnchanged(t, before, evdSnapshot(t, base, now), "무효 인자 연산 전체")
	})
}

// EVD-004: 같은 Config에서 출발해 같은 순서로 같은 연산 열을 적용한 두 Window의
// 모든 조회·판정 결과는 동일하다.
func TestEVD004_SameOperationSequenceYieldsIdenticalWindows(t *testing.T) {
	now := evdAt(time.Minute)

	build := func(t *testing.T) evidence.Window {
		t.Helper()
		w := mustWindow(t, evdConfig())
		w = mustAdd(t, w,
			evdFloat(t, evdAt(0), 10),
			evdRetag(evdFloat(t, evdAt(5*time.Second), 3), evdSrcGNMI, evdSubjectA(t), evdSigA, nil),
			evdFloat(t, evdAt(10*time.Second), 25),
			evdRetag(evdFloat(t, evdAt(3*time.Second), 1), evdSrcProm, evdSubjectB(t), evdSigB, map[string]string{"lane": "0"}),
		)
		return mustPrune(t, w, evdAt(time.Second))
	}

	first, second := build(t), build(t)
	assertSnapshotUnchanged(t, evdSnapshot(t, first, now), evdSnapshot(t, second, now),
		"같은 연산 열로 만든 두 Window")
}

// EVD-004 (edge): 쌍별로 (시리즈, ObservedAt instant)가 겹치지 않는 관측 집합은
// 어떤 순서로 Add해도 같은 Window가 된다 (삽입 순서 무관).
func TestEVD004_InsertionOrderDoesNotAffectResults(t *testing.T) {
	now := evdAt(time.Minute)

	obs := []model.Observation{
		evdFloat(t, evdAt(0), 10),
		evdFloat(t, evdAt(10*time.Second), 25),
		evdRetag(evdFloat(t, evdAt(5*time.Second), 3), evdSrcGNMI, evdSubjectA(t), evdSigA, nil),
		evdRetag(evdFloat(t, evdAt(3*time.Second), 1), evdSrcProm, evdSubjectB(t), evdSigB, map[string]string{"lane": "0"}),
	}

	perms := evdPermutations(obs)
	if len(perms) != 24 {
		t.Fatalf("순열 %d개, want 24", len(perms))
	}

	want := evdSnapshot(t, mustAdd(t, mustWindow(t, evdConfig()), perms[0]...), now)
	for i, p := range perms {
		got := evdSnapshot(t, mustAdd(t, mustWindow(t, evdConfig()), p...), now)
		if got != want {
			t.Fatalf("순열 %d의 결과가 달랐다\n--- want ---\n%s--- got ---\n%s", i, want, got)
		}
	}
}

// EVD-005 (edge): 발행된 Window·Series·RateResult 값은 이후의 파생 연산에
// 영향받지 않는다.
func TestEVD005_PublishedValuesAreImmutable(t *testing.T) {
	now := evdAt(time.Minute)

	w1 := evdFloatSeriesWindow(t, evdConfig(),
		evdPoint{0, 100},
		evdPoint{10 * time.Second, 200},
	)
	before := evdSnapshot(t, w1, now)

	s1 := evdDefaultSeries(t, w1)
	beforeSeries := evdSeriesString(s1, now, "s1")
	r1, err := s1.Rate()
	requireNoErr(t, err, "Rate()")

	// 파생 연산: Add·Prune·값 복사.
	w2 := mustAdd(t, w1, evdFloat(t, evdAt(20*time.Second), 500))
	w3 := mustPrune(t, w1, evdAt(5*time.Second))
	w4 := w1
	_ = mustAdd(t, w4, evdFloat(t, evdAt(30*time.Second), 900))
	_, _ = w2.Add(evdFloat(t, evdAt(40*time.Second), 1000))
	_ = w3

	assertSnapshotUnchanged(t, before, evdSnapshot(t, w1, now), "원래 Window")
	assertSnapshotUnchanged(t, beforeSeries, evdSeriesString(s1, now, "s1"), "먼저 얻은 Series")

	r2, err := s1.Rate()
	requireNoErr(t, err, "Rate()(2회차)")
	if r1 != r2 {
		t.Errorf("먼저 얻은 RateResult가 변했다: %+v vs %+v", r1, r2)
	}
	assertRateResult(t, r1, 10, 2, 0, evdAt(0), evdAt(10*time.Second), "먼저 얻은 RateResult")
}

// EVD-005 (edge): 같은 값에 같은 조회를 몇 번을 어떤 순서로 호출해도 결과는 같다.
func TestEVD005_RepeatedQueriesAreIdentical(t *testing.T) {
	now := evdAt(time.Minute)
	w := mustAdd(t, mustWindow(t, evdConfig()),
		evdFloat(t, evdAt(0), 100),
		evdFloat(t, evdAt(10*time.Second), 200),
		evdRetag(evdFloat(t, evdAt(5*time.Second), 7), evdSrcGNMI, evdSubjectA(t), evdSigA, nil),
	)

	first := evdSnapshot(t, w, now)
	for i := 0; i < 3; i++ {
		if got := evdSnapshot(t, w, now); got != first {
			t.Fatalf("%d회차 조회 결과가 달랐다\n--- 1회차 ---\n%s--- %d회차 ---\n%s", i+2, first, i+2, got)
		}
	}

	// 호출 순서를 바꿔도 같다. 이 픽스처는 (subjectA, sigA)에 시리즈가 둘이므로
	// (prometheus·gnmi, 3.1) 대상 시리즈는 좌표 전체로 지목한다 — 관측이 둘인
	// prometheus 시리즈라야 Rate()도 함께 검사할 수 있다.
	s := evdDefaultSeriesOf(t, w, evdSrcProm, nil)
	r1, err1 := s.Rate()
	f1, err2 := s.Fresh(now)
	lvl1, err3 := w.Level(evdSubjectA(t), evdSigA, now)
	requireNoErr(t, err1, "Rate")
	requireNoErr(t, err2, "Fresh")
	requireNoErr(t, err3, "Level")

	lvl2, err3 := w.Level(evdSubjectA(t), evdSigA, now)
	f2, err2 := s.Fresh(now)
	r2, err1 := s.Rate()
	requireNoErr(t, err1, "Rate(역순)")
	requireNoErr(t, err2, "Fresh(역순)")
	requireNoErr(t, err3, "Level(역순)")

	if r1 != r2 || f1 != f2 || lvl1 != lvl2 {
		t.Errorf("호출 순서에 따라 결과가 달라졌다: rate %+v vs %+v, fresh %t vs %t, level %s vs %s",
			r1, r2, f1, f2, lvl1.String(), lvl2.String())
	}
}

// EVD-006 (edge): Add에 넘긴 관측의 참조 필드를 호출 후에 변형해도 window에
// 반영되지 않는다 (방어적 복사).
func TestEVD006_AddCopiesObservationReferenceFields(t *testing.T) {
	now := evdAt(time.Minute)

	alias := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
	alias.Raw = []byte("raw-bytes")
	alias.Source = "sysfs"
	subj := evdSubjectAWithAliases(t, alias)

	dims := map[string]string{"lane": "0"}

	o := evdFloat(t, evdAt(0), 100)
	o.Subject = subj
	o.Dimensions = dims

	w := mustAdd(t, mustWindow(t, evdConfig()), o)
	before := evdSnapshot(t, w, now)

	// 호출자가 원본을 변형한다.
	dims["lane"] = "99"
	dims["새키"] = "새값"
	delete(dims, "lane")
	subj.Aliases[0].Value = "변형된-값"
	subj.Aliases[0].Namespace = "변형된-ns"
	subj.Aliases[0].Source = "변형된-source"
	subj.Aliases[0].Raw[0] = 'X'
	o.Unit = "변형된-unit"
	o.ID = "변형된-id"

	assertSnapshotUnchanged(t, before, evdSnapshot(t, w, now), "Add 인자 변형 후")
}

// EVD-006 (edge): 반환된 컬렉션과 그 원소를 변형해도 이후 조회에 반영되지 않는다.
func TestEVD006_ReturnedCollectionsAreDefensivelyCopied(t *testing.T) {
	now := evdAt(time.Minute)

	alias := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
	alias.Raw = []byte("raw-bytes")
	o1 := evdFloat(t, evdAt(0), 100)
	o1.Subject = evdSubjectAWithAliases(t, alias)
	o1.Dimensions = map[string]string{"lane": "0"}
	o2 := evdFloat(t, evdAt(10*time.Second), 200)
	o2.Subject = evdSubjectAWithAliases(t, alias)
	o2.Dimensions = map[string]string{"lane": "0"}

	w := mustAdd(t, mustWindow(t, evdConfig()), o1, o2)
	before := evdSnapshot(t, w, now)

	t.Run("Subjects()", func(t *testing.T) {
		subs := w.Subjects()
		if len(subs) != 1 {
			t.Fatalf("Subjects() 길이 %d, want 1", len(subs))
		}
		subs[0].Canonical = "변형"
		subs[0].Kind = model.KindPod
		if len(subs[0].Aliases) > 0 {
			subs[0].Aliases[0].Value = "변형"
			if len(subs[0].Aliases[0].Raw) > 0 {
				subs[0].Aliases[0].Raw[0] = 'X'
			}
		}
		subs = append(subs[:0], model.AssetRef{})
		_ = subs
	})

	t.Run("Signals()·Sources()", func(t *testing.T) {
		signals, err := w.Signals(evdSubjectA(t))
		requireNoErr(t, err, "Signals")
		for i := range signals {
			signals[i] = model.SignalRef("변형.됨")
		}
		sources := w.Sources()
		for i := range sources {
			sources[i] = model.SourceRef{Type: "변형", Name: "변형"}
		}
	})

	t.Run("Observations()·Dimensions()", func(t *testing.T) {
		s := evdDefaultSeries(t, w)

		obs := s.Observations()
		if len(obs) != 2 {
			t.Fatalf("Observations() 길이 %d, want 2", len(obs))
		}
		obs[0].ID = "변형"
		obs[0].Unit = "변형"
		obs[0].ObservedAt = evdAt(time.Hour)
		if obs[0].Dimensions != nil {
			obs[0].Dimensions["lane"] = "99"
			obs[0].Dimensions["새키"] = "새값"
		}
		if len(obs[0].Subject.Aliases) > 0 {
			obs[0].Subject.Aliases[0].Value = "변형"
			if len(obs[0].Subject.Aliases[0].Raw) > 0 {
				obs[0].Subject.Aliases[0].Raw[0] = 'X'
			}
		}
		obs[1] = model.Observation{}

		dims := s.Dimensions()
		dims["lane"] = "99"
		dims["새키"] = "새값"

		latest, ok := s.Latest()
		if !ok {
			t.Fatalf("Latest()가 (zero, false)를 반환했다")
		}
		latest.ID = "변형"
		if latest.Dimensions != nil {
			latest.Dimensions["lane"] = "77"
		}
	})

	assertSnapshotUnchanged(t, before, evdSnapshot(t, w, now), "반환 컬렉션 변형 후")
}

// EVD-007 (edge): Location만 다른 같은 instant는 모든 결과에 영향을 주지 않는다.
func TestEVD007_LocationDoesNotAffectResults(t *testing.T) {
	kst := time.FixedZone("KST", 9*60*60)
	west := time.FixedZone("UTC-7", -7*60*60)

	buildUTC := func(t *testing.T) evidence.Window {
		t.Helper()
		return evdFloatSeriesWindow(t, evdConfig(),
			evdPoint{0, 100},
			evdPoint{10 * time.Second, 250},
			evdPoint{20 * time.Second, 40},
		)
	}
	buildZoned := func(t *testing.T) evidence.Window {
		t.Helper()
		w := mustWindow(t, evdConfig())
		for i, p := range []evdPoint{{0, 100}, {10 * time.Second, 250}, {20 * time.Second, 40}} {
			zone := kst
			if i%2 == 1 {
				zone = west
			}
			o := evdFloat(t, evdAt(p.d).In(zone), p.v)
			o.ReceivedAt = o.ReceivedAt.In(west)
			o.ID = "obs-" + evdTimeString(evdAt(p.d))
			w = mustAdd(t, w, o)
		}
		return w
	}

	nowUTC := evdAt(time.Minute)
	nowZoned := nowUTC.In(kst)

	utcSnap := evdSnapshot(t, buildUTC(t), nowUTC)
	zonedSnap := evdSnapshot(t, buildZoned(t), nowZoned)
	assertSnapshotUnchanged(t, utcSnap, zonedSnap, "Location만 다른 window")

	// 같은 window에 대해 now의 Location만 바꿔도 판정이 같다.
	w := buildUTC(t)
	assertSnapshotUnchanged(t, evdSnapshot(t, w, nowUTC), evdSnapshot(t, w, nowZoned), "now의 Location만 다름")

	// 같은 instant를 다른 Location으로 표현한 관측은 재전송이지 충돌이 아니다.
	o := evdFloat(t, evdAt(0), 100)
	o.ObservedAt = evdAt(0).In(kst)
	o.ID = "다른-id"
	after, err := w.Add(o)
	requireNoErr(t, err, "Add(Location만 다른 동등 관측)")
	assertSnapshotUnchanged(t, evdSnapshot(t, w, nowUTC), evdSnapshot(t, after, nowUTC),
		"Location만 다른 동등 재전송")

	// 시각 결과 값의 동일성은 Equal(instant) 기준이다.
	last, err := w.SourceLastObserved(evdSrcProm)
	requireNoErr(t, err, "SourceLastObserved")
	if !last.Equal(evdAt(20 * time.Second)) {
		t.Errorf("SourceLastObserved() = %s, want %s (Equal 기준)",
			evdTimeString(last), evdTimeString(evdAt(20*time.Second)))
	}
}

// EVD-007 (edge): monotonic reading의 유무는 어떤 결과에도 영향을 주지 않는다.
//
// monotonic reading은 time.Now()로만 얻을 수 있다. 이 테스트의 판정은 "같은
// instant의 두 표현이 같은 결과를 낸다"이므로 벽시계 값에 의존하지 않는다 —
// 결정론(EVD-002)을 깨지 않는다.
func TestEVD007_MonotonicReadingDoesNotAffectResults(t *testing.T) {
	mono := time.Now()
	stripped := mono.Round(0) // monotonic reading 제거 (같은 instant)

	if !mono.Equal(stripped) {
		t.Fatalf("전제 불일치: Round(0)이 instant를 바꿨다 (%v vs %v)", mono, stripped)
	}

	build := func(t *testing.T, base time.Time) evidence.Window {
		t.Helper()
		w := mustWindow(t, evdConfig())
		for i, p := range []evdPoint{{0, 100}, {10 * time.Second, 250}, {20 * time.Second, 40}} {
			o := evdFloat(t, base.Add(p.d), p.v)
			o.ID = fmt.Sprintf("obs-mono-%d", i)
			o.ReceivedAt = base.Add(p.d + time.Second)
			w = mustAdd(t, w, o)
		}
		return w
	}

	monoWindow := build(t, mono)
	strippedWindow := build(t, stripped)

	// 스냅샷은 시각을 instant(UTC 포맷)로 렌더링하므로 두 표현이 같아야 한다.
	nowMono := mono.Add(time.Minute)
	nowStripped := stripped.Add(time.Minute)
	assertSnapshotUnchanged(t, evdSnapshot(t, monoWindow, nowMono), evdSnapshot(t, strippedWindow, nowStripped),
		"monotonic reading 유무만 다른 window")
	assertSnapshotUnchanged(t, evdSnapshot(t, monoWindow, nowMono), evdSnapshot(t, monoWindow, nowStripped),
		"now의 monotonic reading 유무만 다름")

	// monotonic reading이 제거된 같은 instant의 관측은 재전송(멱등)이다.
	o := evdFloat(t, stripped, 100)
	o.ID = "obs-mono-왕복"
	o.ReceivedAt = stripped.Add(time.Second)
	after, err := monoWindow.Add(o)
	requireNoErr(t, err, "Add(monotonic reading만 제거된 동등 관측)")
	assertSnapshotUnchanged(t, evdSnapshot(t, monoWindow, nowMono), evdSnapshot(t, after, nowMono),
		"monotonic reading만 다른 동등 재전송")

	// Rate의 Start·End는 Equal(instant) 기준으로 같다.
	r, err := evdDefaultSeries(t, monoWindow).Rate()
	requireNoErr(t, err, "Rate()")
	if !r.Start.Equal(mono) || !r.End.Equal(mono.Add(20*time.Second)) {
		t.Errorf("Start·End가 instant 기준으로 일치하지 않는다: %v..%v", r.Start, r.End)
	}
}

// EVD-008: 이산 결과와 PerSecond는 같은 바이너리 안에서 같은 입력에 대해 항상
// 동일하다 (아키텍처 간 보장은 5절이 CI에 배정한다).
func TestEVD008_ResultsAreStableWithinBinary(t *testing.T) {
	now := evdAt(time.Minute)

	pts := []evdPoint{
		{0, 0.1},
		{7 * time.Second, 3.3},
		{11 * time.Second, 2.2},
		{29 * time.Second, 1e18},
		{31 * time.Second, 1e18 + 1},
	}

	first := evdFloatSeriesWindow(t, evdConfig(), pts...)
	second := evdFloatSeriesWindow(t, evdConfig(), pts...)

	assertSnapshotUnchanged(t, evdSnapshot(t, first, now), evdSnapshot(t, second, now),
		"같은 입력으로 만든 두 window")

	r1, err := evdDefaultSeries(t, first).Rate()
	requireNoErr(t, err, "Rate(1)")
	r2, err := evdDefaultSeries(t, second).Rate()
	requireNoErr(t, err, "Rate(2)")

	if math.Float64bits(r1.PerSecond) != math.Float64bits(r2.PerSecond) {
		t.Errorf("같은 입력의 PerSecond가 비트 수준에서 달랐다: %s vs %s",
			evdFloatString(r1.PerSecond), evdFloatString(r2.PerSecond))
	}
	if r1.Samples != r2.Samples || r1.Discontinuities != r2.Discontinuities {
		t.Errorf("이산 결과가 달랐다: %+v vs %+v", r1, r2)
	}
	assertRateInvariants(t, r1, "EVD-008 표본")

	// 삽입 순서를 바꿔도 이산 결과와 PerSecond는 같다.
	reversed := mustWindow(t, evdConfig())
	for i := len(pts) - 1; i >= 0; i-- {
		reversed = mustAdd(t, reversed, evdFloat(t, evdAt(pts[i].d), pts[i].v))
	}
	r3, err := evdDefaultSeries(t, reversed).Rate()
	requireNoErr(t, err, "Rate(역순 삽입)")
	if math.Float64bits(r1.PerSecond) != math.Float64bits(r3.PerSecond) {
		t.Errorf("삽입 순서에 따라 PerSecond가 달라졌다: %s vs %s",
			evdFloatString(r1.PerSecond), evdFloatString(r3.PerSecond))
	}
}

// EVD-002·003: 오류 경로도 결정론적이다 — 같은 무효 입력은 항상 같은 계열의
// 오류를 낸다.
func TestEVD002_ErrorPathsAreDeterministic(t *testing.T) {
	w := mustAdd(t, mustWindow(t, evdConfig()), evdFloat(t, evdAt(0), 1))

	conflicting := evdFloat(t, evdAt(0), 2)
	conflicting.ID = "다른-id"

	for i := 0; i < 3; i++ {
		_, errA := w.Add(model.Observation{})
		_, errB := w.Add(conflicting)
		_, errC := w.Prune(zeroTime)
		if got := evdErrClass(errA); got != "invalid" {
			t.Errorf("%d회차 Add(무효) 오류 계열 = %q, want invalid", i+1, got)
		}
		if got := evdErrClass(errB); got != "conflict" {
			t.Errorf("%d회차 Add(충돌) 오류 계열 = %q, want conflict", i+1, got)
		}
		if got := evdErrClass(errC); got != "invalid" {
			t.Errorf("%d회차 Prune(zero) 오류 계열 = %q, want invalid", i+1, got)
		}
	}
}

// 3.2: 이 패키지의 세 sentinel은 서로 구별되고 model.ErrInvalid와도 구별된다.
func TestSpec32_SentinelsAreDistinct(t *testing.T) {
	sentinels := []struct {
		name string
		err  error
	}{
		{"ErrConflict", evidence.ErrConflict},
		{"ErrOutOfWindow", evidence.ErrOutOfWindow},
		{"ErrInsufficientSamples", evidence.ErrInsufficientSamples},
	}
	for i, a := range sentinels {
		if a.err == nil {
			t.Fatalf("%s는 nil이 아닌 공개 sentinel이어야 한다", a.name)
		}
		if a.err.Error() == "" {
			t.Errorf("%s의 Error()는 비어 있지 않아야 한다", a.name)
		}
		if errors.Is(a.err, model.ErrInvalid) {
			t.Errorf("%s가 model.ErrInvalid로도 판정된다", a.name)
		}
		if errors.Is(model.ErrInvalid, a.err) {
			t.Errorf("model.ErrInvalid가 %s로도 판정된다", a.name)
		}
		for j, b := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(a.err, b.err) {
				t.Errorf("%s가 %s로도 판정된다", a.name, b.name)
			}
		}
	}
}
