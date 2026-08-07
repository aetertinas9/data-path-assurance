// evidence_query_test.go — Window 조회 (specs/evidence/spec.md 3.5, EVD-030~034)
// 와 3.1의 시리즈 정의·전순서.
package tests

import (
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// EVD-030: Subjects()는 Key() 바이트 오름차순이다 (삽입 순서 무관).
func TestEVD030_SubjectsAreSortedByKey(t *testing.T) {
	subjects := []model.AssetRef{evdSubjectB(t), evdSubjectC(t), evdSubjectA(t)}
	want := []string{evdSubjectA(t).Key(), evdSubjectC(t).Key(), evdSubjectB(t).Key()}

	forward := mustWindow(t, evdConfig())
	for i, subj := range subjects {
		forward = mustAdd(t, forward,
			evdRetag(evdFloat(t, evdAt(time.Duration(i)*time.Second), 1), evdSrcProm, subj, evdSigA, nil))
	}
	reverse := mustWindow(t, evdConfig())
	for i := len(subjects) - 1; i >= 0; i-- {
		reverse = mustAdd(t, reverse,
			evdRetag(evdFloat(t, evdAt(time.Duration(i)*time.Second), 1), evdSrcProm, subjects[i], evdSigA, nil))
	}

	for _, tc := range []struct {
		name string
		w    evidence.Window
	}{{"정방향 삽입", forward}, {"역방향 삽입", reverse}} {
		t.Run(tc.name, func(t *testing.T) {
			got := make([]string, 0, 3)
			for _, subj := range tc.w.Subjects() {
				got = append(got, subj.Key())
			}
			assertStringsEqual(t, got, want, "Subjects()")
		})
	}
}

// EVD-030: Signals()는 signal 문자열 바이트 오름차순이다.
func TestEVD030_SignalsAreSortedByString(t *testing.T) {
	signals := []model.SignalRef{evdSigC, evdSigA, evdSigB}
	want := []string{evdSigA.String(), evdSigB.String(), evdSigC.String()}

	w := mustWindow(t, evdConfig())
	for i, sig := range signals {
		w = mustAdd(t, w,
			evdRetag(evdFloat(t, evdAt(time.Duration(i)*time.Second), 1), evdSrcProm, evdSubjectA(t), sig, nil))
	}

	got, err := w.Signals(evdSubjectA(t))
	requireNoErr(t, err, "Signals")
	gotStrings := make([]string, 0, len(got))
	for _, s := range got {
		gotStrings = append(gotStrings, s.String())
	}
	assertStringsEqual(t, gotStrings, want, "Signals()")
}

// EVD-030: Sources()는 (Type, Name) 바이트 오름차순이다.
func TestEVD030_SourcesAreSortedByTypeThenName(t *testing.T) {
	sources := []model.SourceRef{evdSrcProm, evdSrcPromAlt, evdSrcGNMI, evdSrcAgent}

	w := mustWindow(t, evdConfig())
	for i, src := range sources {
		w = mustAdd(t, w,
			evdRetag(evdFloat(t, evdAt(time.Duration(i)*time.Second), 1), src, evdSubjectA(t), evdSigA, nil))
	}

	got := make([]string, 0, len(sources))
	for _, src := range w.Sources() {
		got = append(got, src.Type+"/"+src.Name)
	}
	want := []string{
		evdSrcAgent.Type + "/" + evdSrcAgent.Name,
		evdSrcGNMI.Type + "/" + evdSrcGNMI.Name,
		evdSrcProm.Type + "/" + evdSrcProm.Name,
		evdSrcPromAlt.Type + "/" + evdSrcPromAlt.Name,
	}
	assertStringsEqual(t, got, want, "Sources()")
}

// EVD-030: SeriesFor()는 3.1 시리즈 전순서 — Source.Type → Source.Name →
// Dimensions 열 — 로 반환한다.
//
// Dimensions 열의 비교는 키 오름차순 (키, 값) 쌍의 사전식 비교이고, 한쪽이
// 다른 쪽의 접두 열이면 짧은 쪽이 앞선다. 표로는 규칙이 흐려지므로 기대 순서를
// 통째로 적어 둔다.
func TestEVD030_SeriesForTotalOrder(t *testing.T) {
	type seriesKey struct {
		src  model.SourceRef
		dims map[string]string
	}

	// 기대 순서 (오름차순).
	ordered := []seriesKey{
		{evdSrcAgent, nil},
		{evdSrcGNMI, nil},
		{evdSrcProm, nil},
		{evdSrcProm, map[string]string{"a": "1"}},
		{evdSrcProm, map[string]string{"a": "1", "b": "0"}},
		{evdSrcProm, map[string]string{"a": "2"}},
		{evdSrcProm, map[string]string{"b": "0"}},
		{evdSrcPromAlt, nil},
	}

	want := make([]string, 0, len(ordered))
	for _, k := range ordered {
		want = append(want, k.src.Type+"/"+k.src.Name+"/"+evdDimensionsString(k.dims))
	}

	// 삽입 순서: 고정된 "뒤섞인" 순서와 그 역순 — 둘 다 같은 결과여야 한다.
	shuffled := []int{4, 0, 7, 2, 5, 1, 6, 3}

	build := func(t *testing.T, order []int) evidence.Window {
		t.Helper()
		w := mustWindow(t, evdConfig())
		for _, i := range order {
			k := ordered[i]
			w = mustAdd(t, w,
				evdRetag(evdFloat(t, evdAt(0), float64(i)), k.src, evdSubjectA(t), evdSigA, k.dims))
		}
		return w
	}

	reversed := make([]int, len(shuffled))
	for i, v := range shuffled {
		reversed[len(shuffled)-1-i] = v
	}

	for _, tc := range []struct {
		name  string
		order []int
	}{{"뒤섞인 삽입", shuffled}, {"그 역순 삽입", reversed}} {
		t.Run(tc.name, func(t *testing.T) {
			series, err := build(t, tc.order).SeriesFor(evdSubjectA(t), evdSigA)
			requireNoErr(t, err, "SeriesFor")
			assertStringsEqual(t, evdSeriesKeys(series), want, "SeriesFor() 전순서")
		})
	}
}

// EVD-030: Observations()는 ObservedAt 오름차순이며 삽입 순서와 무관하다.
func TestEVD030_ObservationsAreSortedByObservedAt(t *testing.T) {
	offsets := []time.Duration{30 * time.Second, 0, 20 * time.Second, 10 * time.Second}
	want := []time.Time{evdAt(0), evdAt(10 * time.Second), evdAt(20 * time.Second), evdAt(30 * time.Second)}

	w := mustWindow(t, evdConfig())
	for i, d := range offsets {
		w = mustAdd(t, w, evdFloat(t, evdAt(d), float64(i)))
	}
	assertObservedAts(t, evdDefaultSeries(t, w), want, "Observations()")
}

// EVD-031 (edge): 빈 Window 또는 해당 데이터가 없는 유효 인자는 오류가 아니라
// 빈 컬렉션이다.
func TestEVD031_EmptyQueriesAreNotErrors(t *testing.T) {
	now := evdAt(time.Minute)

	t.Run("빈 Window", func(t *testing.T) {
		assertEmptyWindow(t, mustWindow(t, evdConfig()), now, "NewWindow의 빈 Window")
	})

	t.Run("데이터가 없는 유효 인자", func(t *testing.T) {
		w := mustAdd(t, mustWindow(t, evdConfig()), evdFloat(t, evdAt(0), 1))

		signals, err := w.Signals(evdSubjectB(t))
		requireNoErr(t, err, "Signals(관측 없는 subject)")
		if len(signals) != 0 {
			t.Errorf("Signals() 길이 %d, want 0", len(signals))
		}

		series, err := w.SeriesFor(evdSubjectB(t), evdSigA)
		requireNoErr(t, err, "SeriesFor(관측 없는 subject)")
		if len(series) != 0 {
			t.Errorf("SeriesFor() 길이 %d, want 0", len(series))
		}

		series, err = w.SeriesFor(evdSubjectA(t), evdSigB)
		requireNoErr(t, err, "SeriesFor(관측 없는 signal)")
		if len(series) != 0 {
			t.Errorf("SeriesFor() 길이 %d, want 0", len(series))
		}

		last, err := w.SourceLastObserved(evdSrcGNMI)
		requireNoErr(t, err, "SourceLastObserved(관측 없는 source)")
		if !last.IsZero() {
			t.Errorf("SourceLastObserved() = %s, want zero time", evdTimeString(last))
		}

		lvl, err := w.Level(evdSubjectB(t), evdSigB, now)
		requireNoErr(t, err, "Level(관측 없는 (subject, signal))")
		if lvl != model.LevelUnknown {
			t.Errorf("Level() = %s, want Unknown", lvl.String())
		}
	})
}

// EVD-032 (edge): 시리즈 분리 — Source.Name·Dimensions 차이는 시리즈를 나누고,
// Aliases 차이와 nil/빈 Dimensions 차이는 나누지 않는다.
func TestEVD032_SeriesSeparation(t *testing.T) {
	t.Run("Source.Name만 달라도 다른 시리즈다", func(t *testing.T) {
		w := mustAdd(t, mustWindow(t, evdConfig()),
			evdRetag(evdFloat(t, evdAt(0), 1), evdSrcProm, evdSubjectA(t), evdSigA, nil),
			evdRetag(evdFloat(t, evdAt(0), 2), evdSrcPromAlt, evdSubjectA(t), evdSigA, nil),
		)
		series, err := w.SeriesFor(evdSubjectA(t), evdSigA)
		requireNoErr(t, err, "SeriesFor")
		if len(series) != 2 {
			t.Fatalf("시리즈 %d개, want 2", len(series))
		}
	})

	t.Run("Source.Type만 달라도 다른 시리즈다", func(t *testing.T) {
		gnmiSameName := model.SourceRef{Type: string(model.SourceTypeGNMI), Name: evdSrcProm.Name}
		w := mustAdd(t, mustWindow(t, evdConfig()),
			evdRetag(evdFloat(t, evdAt(0), 1), evdSrcProm, evdSubjectA(t), evdSigA, nil),
			evdRetag(evdFloat(t, evdAt(0), 2), gnmiSameName, evdSubjectA(t), evdSigA, nil),
		)
		series, err := w.SeriesFor(evdSubjectA(t), evdSigA)
		requireNoErr(t, err, "SeriesFor")
		if len(series) != 2 {
			t.Fatalf("시리즈 %d개, want 2", len(series))
		}
	})

	t.Run("Dimensions 차이는 시리즈를 나눈다", func(t *testing.T) {
		variants := []map[string]string{
			{"lane": "0"},
			{"lane": "1"},                // 값 하나 상이
			{"lane": "0", "queue": "3"},  // 키 하나 추가
			{"queue": "3"},               // 키 자체가 다름
			{"lane": "0", "queue": "30"}, // 값의 접두 관계도 구별된다
		}
		w := mustWindow(t, evdConfig())
		for i, dims := range variants {
			w = mustAdd(t, w,
				evdRetag(evdFloat(t, evdAt(0), float64(i)), evdSrcProm, evdSubjectA(t), evdSigA, dims))
		}
		series, err := w.SeriesFor(evdSubjectA(t), evdSigA)
		requireNoErr(t, err, "SeriesFor")
		if len(series) != len(variants) {
			t.Fatalf("시리즈 %d개, want %d", len(series), len(variants))
		}
	})

	t.Run("nil Dimensions와 빈 맵은 같은 시리즈다", func(t *testing.T) {
		nilDims := evdFloat(t, evdAt(0), 1)
		nilDims.Dimensions = nil
		emptyDims := evdFloat(t, evdAt(10*time.Second), 2)
		emptyDims.Dimensions = map[string]string{}

		w := mustAdd(t, mustWindow(t, evdConfig()), nilDims, emptyDims)
		s := evdDefaultSeries(t, w)
		assertObservedAts(t, s, []time.Time{evdAt(0), evdAt(10 * time.Second)}, "nil/빈 맵")
		if got := s.Dimensions(); len(got) != 0 {
			t.Errorf("Dimensions() 길이 %d, want 0", len(got))
		}
	})

	t.Run("Subject.Key()가 같고 Aliases만 다르면 같은 시리즈·같은 subject다", func(t *testing.T) {
		aliasX := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
		aliasY := mustTypedID(t, string(model.NamespaceLLDPChassisID), "aa:bb:cc:dd:ee:ff")

		o1 := evdFloat(t, evdAt(0), 1)
		o1.Subject = evdSubjectAWithAliases(t, aliasX)
		o2 := evdFloat(t, evdAt(10*time.Second), 2)
		o2.Subject = evdSubjectAWithAliases(t, aliasY)

		w := mustAdd(t, mustWindow(t, evdConfig()), o1, o2)

		if got := len(w.Subjects()); got != 1 {
			t.Errorf("Subjects() 길이 %d, want 1", got)
		}
		s := evdDefaultSeries(t, w)
		assertObservedAts(t, s, []time.Time{evdAt(0), evdAt(10 * time.Second)}, "Aliases만 다름")
	})

	t.Run("Aliases가 다른 AssetRef로 조회해도 같은 subject를 찾는다", func(t *testing.T) {
		alias := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
		o := evdFloat(t, evdAt(0), 1)
		o.Subject = evdSubjectAWithAliases(t, alias)
		w := mustAdd(t, mustWindow(t, evdConfig()), o)

		other := mustTypedID(t, string(model.NamespaceLLDPChassisID), "aa:bb")
		probe := evdSubjectAWithAliases(t, other)

		signals, err := w.Signals(probe)
		requireNoErr(t, err, "Signals(다른 Aliases로 조회)")
		if len(signals) != 1 {
			t.Errorf("Signals() 길이 %d, want 1", len(signals))
		}
		series, err := w.SeriesFor(probe, evdSigA)
		requireNoErr(t, err, "SeriesFor(다른 Aliases로 조회)")
		if len(series) != 1 {
			t.Errorf("SeriesFor() 길이 %d, want 1", len(series))
		}
	})

	t.Run("Kind가 다르면 다른 subject다", func(t *testing.T) {
		otherKind := mustAssetRef(t, model.KindPCIeFunction,
			mustTypedID(t, string(model.NamespacePCIBDF), "0000:af:00.0"))
		w := mustAdd(t, mustWindow(t, evdConfig()),
			evdRetag(evdFloat(t, evdAt(0), 1), evdSrcProm, evdSubjectA(t), evdSigA, nil),
			evdRetag(evdFloat(t, evdAt(0), 2), evdSrcProm, otherKind, evdSigA, nil),
		)
		if got := len(w.Subjects()); got != 2 {
			t.Errorf("Subjects() 길이 %d, want 2", got)
		}
	})
}

// EVD-033: SourceLastObserved는 그 source의 보존 관측 중 최대 ObservedAt이다.
func TestEVD033_SourceLastObserved(t *testing.T) {
	w := mustAdd(t, mustWindow(t, evdConfig()),
		// prometheus/prom-main — 여러 subject·signal에 걸쳐 있다.
		evdRetag(evdFloat(t, evdAt(0), 1), evdSrcProm, evdSubjectA(t), evdSigA, nil),
		evdRetag(evdFloat(t, evdAt(30*time.Second), 2), evdSrcProm, evdSubjectB(t), evdSigB, nil),
		evdRetag(evdFloat(t, evdAt(10*time.Second), 3), evdSrcProm, evdSubjectA(t), evdSigB, nil),
		// 같은 Type·다른 Name.
		evdRetag(evdFloat(t, evdAt(90*time.Second), 4), evdSrcPromAlt, evdSubjectA(t), evdSigA, nil),
		// 다른 Type.
		evdRetag(evdFloat(t, evdAt(20*time.Second), 5), evdSrcGNMI, evdSubjectA(t), evdSigA, nil),
	)

	cases := []struct {
		name string
		src  model.SourceRef
		want time.Time
	}{
		{"prometheus/prom-main", evdSrcProm, evdAt(30 * time.Second)},
		{"prometheus/prom-replica", evdSrcPromAlt, evdAt(90 * time.Second)},
		{"gnmi/gnmi-tor1", evdSrcGNMI, evdAt(20 * time.Second)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := w.SourceLastObserved(tc.src)
			requireNoErr(t, err, "SourceLastObserved")
			if !got.Equal(tc.want) {
				t.Errorf("SourceLastObserved() = %s, want %s", evdTimeString(got), evdTimeString(tc.want))
			}
		})
	}

	t.Run("보존 관측이 없는 source는 (zero time, nil)이다", func(t *testing.T) {
		got, err := w.SourceLastObserved(evdSrcAgent)
		requireNoErr(t, err, "SourceLastObserved(관측 없음)")
		if !got.IsZero() {
			t.Errorf("SourceLastObserved() = %s, want zero time", evdTimeString(got))
		}
	})
}

// EVD-034 (edge): 다른 시리즈에 대한 Add는 기존 시리즈의 조회·판정을 바꾸지 않는다.
func TestEVD034_SeriesJudgementsAreIndependent(t *testing.T) {
	now := evdAt(time.Minute)

	w := mustAdd(t, mustWindow(t, evdConfig()),
		evdFloat(t, evdAt(0), 100),
		evdFloat(t, evdAt(10*time.Second), 200),
		evdRetag(evdFloat(t, evdAt(5*time.Second), 7), evdSrcProm, evdSubjectB(t), evdSigB, nil),
	)

	// 시리즈 A는 좌표 전체로 고정한다 (prometheus·dimensions 없음) — 아래 추가
	// 케이스에는 "같은 source·다른 dimensions"가 있어서 source만으로는 3.1의
	// 시리즈를 유일하게 지목하지 못한다.
	beforeA := evdSeriesString(evdDefaultSeriesOf(t, w, evdSrcProm, nil), now, "A")
	levelB, err := w.Level(evdSubjectB(t), evdSigB, now)
	requireNoErr(t, err, "Level(B)")

	additions := []struct {
		name string
		obs  model.Observation
	}{
		{"같은 (subject, signal)의 다른 source", evdRetag(evdFloat(t, evdAt(7*time.Second), 3), evdSrcGNMI, evdSubjectA(t), evdSigA, nil)},
		{"같은 (subject, signal)의 다른 dimensions", evdRetag(evdFloat(t, evdAt(7*time.Second), 3), evdSrcProm, evdSubjectA(t), evdSigA, map[string]string{"lane": "1"})},
		{"같은 subject의 다른 signal", evdRetag(evdFloat(t, evdAt(7*time.Second), 3), evdSrcProm, evdSubjectA(t), evdSigC, nil)},
		{"다른 subject", evdRetag(evdFloat(t, evdAt(7*time.Second), 3), evdSrcProm, evdSubjectC(t), evdSigA, nil)},
	}

	for _, tc := range additions {
		t.Run(tc.name, func(t *testing.T) {
			derived := mustAdd(t, w, tc.obs)

			afterA := evdSeriesString(evdDefaultSeriesOf(t, derived, evdSrcProm, nil), now, "A")
			assertSnapshotUnchanged(t, beforeA, afterA, "시리즈 A의 조회·Fresh·Rate")

			gotB, err := derived.Level(evdSubjectB(t), evdSigB, now)
			requireNoErr(t, err, "Level(B)")
			if gotB != levelB {
				t.Errorf("다른 (subject, signal)의 Level이 변했다: %s → %s", levelB.String(), gotB.String())
			}
		})
	}
}

// 3.5: 노출 AssetRef의 선정 — 그 subject의 보존 관측 중 ObservedAt이 가장 늦은
// 것의 Subject 값이고, 같은 instant가 여럿이면 시리즈 전순서에서 앞서는 쪽이다.
func TestSpec35_ExposedAssetRefSelection(t *testing.T) {
	aliasOld := mustTypedID(t, string(model.NamespaceLLDPPortID), "오래된-alias")
	aliasNew := mustTypedID(t, string(model.NamespaceLLDPPortID), "최신-alias")

	t.Run("최신 관측의 Subject가 노출된다", func(t *testing.T) {
		o1 := evdFloat(t, evdAt(0), 1)
		o1.Subject = evdSubjectAWithAliases(t, aliasOld)
		o2 := evdRetag(evdFloat(t, evdAt(10*time.Second), 2), evdSrcGNMI,
			evdSubjectAWithAliases(t, aliasNew), evdSigA, nil)

		w := mustAdd(t, mustWindow(t, evdConfig()), o1, o2)

		subjects := w.Subjects()
		if len(subjects) != 1 {
			t.Fatalf("Subjects() 길이 %d, want 1", len(subjects))
		}
		if got := evdAliasesString(subjects[0]); got != evdAliasesString(o2.Subject) {
			t.Errorf("노출 AssetRef의 Aliases = %q, want %q (최신 관측의 것)", got, evdAliasesString(o2.Subject))
		}
	})

	t.Run("같은 instant면 시리즈 전순서에서 앞서는 쪽이다", func(t *testing.T) {
		// gnmi < prometheus (Source.Type 바이트 오름차순).
		oProm := evdRetag(evdFloat(t, evdAt(0), 1), evdSrcProm,
			evdSubjectAWithAliases(t, aliasOld), evdSigA, nil)
		oGNMI := evdRetag(evdFloat(t, evdAt(0), 2), evdSrcGNMI,
			evdSubjectAWithAliases(t, aliasNew), evdSigA, nil)

		for _, tc := range []struct {
			name string
			obs  []model.Observation
		}{
			{"prometheus 먼저 삽입", []model.Observation{oProm, oGNMI}},
			{"gnmi 먼저 삽입", []model.Observation{oGNMI, oProm}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				w := mustAdd(t, mustWindow(t, evdConfig()), tc.obs...)
				subjects := w.Subjects()
				if len(subjects) != 1 {
					t.Fatalf("Subjects() 길이 %d, want 1", len(subjects))
				}
				if got := evdAliasesString(subjects[0]); got != evdAliasesString(oGNMI.Subject) {
					t.Errorf("노출 AssetRef의 Aliases = %q, want %q (전순서에서 앞서는 gnmi 시리즈)",
						got, evdAliasesString(oGNMI.Subject))
				}
			})
		}
	})

	t.Run("Series.Subject()는 그 시리즈의 최신 관측 값이다", func(t *testing.T) {
		o1 := evdFloat(t, evdAt(0), 1)
		o1.Subject = evdSubjectAWithAliases(t, aliasOld)
		o2 := evdFloat(t, evdAt(10*time.Second), 2)
		o2.Subject = evdSubjectAWithAliases(t, aliasNew)

		w := mustAdd(t, mustWindow(t, evdConfig()), o1, o2)
		s := evdDefaultSeries(t, w)
		if got := evdAliasesString(s.Subject()); got != evdAliasesString(o2.Subject) {
			t.Errorf("Series.Subject()의 Aliases = %q, want %q", got, evdAliasesString(o2.Subject))
		}
	})
}

// 3.5: 내용 조회는 now와 무관하다 — stale 관측도 조회에는 그대로 나타난다
// (설명가능성: stale evidence도 나이와 함께 표시되어야 한다).
func TestSpec35_ContentQueriesIgnoreNow(t *testing.T) {
	w := mustAdd(t, mustWindow(t, evdConfig()),
		evdFloat(t, evdAt(0), 100),
		evdFloat(t, evdAt(10*time.Second), 200),
	)

	// MaxAge(5분)를 한참 지난 시각에도 내용 조회는 그대로다.
	staleNow := evdAt(10 * time.Hour)

	s := evdDefaultSeries(t, w)
	assertObservedAts(t, s, []time.Time{evdAt(0), evdAt(10 * time.Second)}, "stale 시점의 Observations()")
	if got := len(w.Subjects()); got != 1 {
		t.Errorf("Subjects() 길이 %d, want 1", got)
	}
	if got := len(w.Sources()); got != 1 {
		t.Errorf("Sources() 길이 %d, want 1", got)
	}
	last, err := w.SourceLastObserved(evdSrcProm)
	requireNoErr(t, err, "SourceLastObserved")
	if !last.Equal(evdAt(10 * time.Second)) {
		t.Errorf("SourceLastObserved() = %s, want %s", evdTimeString(last), evdTimeString(evdAt(10*time.Second)))
	}

	// 판정만 now를 본다.
	assertFresh(t, s, staleNow, false, "stale 시점")
	assertLevel(t, w, evdSubjectA(t), evdSigA, staleNow, model.LevelUnknown, "stale 시점")
	assertFresh(t, s, evdAt(time.Minute), true, "신선한 시점")
	assertLevel(t, w, evdSubjectA(t), evdSigA, evdAt(time.Minute), model.LevelObserved, "신선한 시점")

	// Rate도 now에 관여하지 않는다.
	r, err := s.Rate()
	requireNoErr(t, err, "Rate")
	assertRateResult(t, r, 10, 2, 0, evdAt(0), evdAt(10*time.Second), "stale 시리즈의 Rate")
}
