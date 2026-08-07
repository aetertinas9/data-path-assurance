// evidence_level_test.go — evidence level 판정 (specs/evidence/spec.md 3.7,
// EVD-070~077).
package tests

import (
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// 판정 기준 시각과 신선/stale 관측의 오프셋 (MaxAge = 5분).
const (
	evdLevelFresh = 0                 // deadline = now + 4분 → 신선
	evdLevelStale = -10 * time.Minute // deadline = now − 6분 → stale
)

var evdLevelNow = evdAt(time.Minute)

// EVD-070: (subject, signal)에 시리즈가 없으면 (LevelUnknown, nil)이다 —
// 오류가 아니다.
func TestEVD070_NoSeriesIsUnknown(t *testing.T) {
	t.Run("빈 Window", func(t *testing.T) {
		w := mustWindow(t, evdConfig())
		assertLevel(t, w, evdSubjectA(t), evdSigA, evdLevelNow, model.LevelUnknown, "빈 Window")
	})

	t.Run("다른 subject·signal에만 관측이 있는 Window", func(t *testing.T) {
		w := mustAdd(t, mustWindow(t, evdConfig()),
			evdRetag(evdFloat(t, evdAt(0), 1), evdSrcProm, evdSubjectB(t), evdSigB, nil))
		assertLevel(t, w, evdSubjectA(t), evdSigA, evdLevelNow, model.LevelUnknown, "관측 없는 (A, sigA)")
		assertLevel(t, w, evdSubjectA(t), evdSigB, evdLevelNow, model.LevelUnknown, "subject만 다름")
		assertLevel(t, w, evdSubjectB(t), evdSigA, evdLevelNow, model.LevelUnknown, "signal만 다름")
	})

	t.Run("Prune으로 전부 사라진 (subject, signal)", func(t *testing.T) {
		w := mustAdd(t, mustWindow(t, evdConfig()), evdFloat(t, evdAt(0), 1))
		assertLevel(t, w, evdSubjectA(t), evdSigA, evdLevelNow, model.LevelObserved, "Prune 전")
		w = mustPrune(t, w, evdAt(time.Hour))
		assertLevel(t, w, evdSubjectA(t), evdSigA, evdLevelNow, model.LevelUnknown, "Prune 후")
	})
}

// EVD-071 (edge): 모든 시리즈의 최신 관측이 stale이면 LevelUnknown이다 —
// 과거에 아무리 풍부했어도 신선한 evidence가 없으면 Unknown이다.
func TestEVD071_AllStaleIsDemotedToUnknown(t *testing.T) {
	cases := []struct {
		name  string
		specs []evdSeriesSpec
	}{
		{"stale 시리즈 하나", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelStale, quality: model.QualityGood},
		}},
		{"서로 다른 Type 3종이 전부 stale", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelStale, quality: model.QualityGood},
			{src: evdSrcGNMI, at: evdLevelStale, quality: model.QualityGood},
			{src: evdSrcAgent, at: evdLevelStale, quality: model.QualityGood},
		}},
		{"관측이 많아도 최신이 stale이면 Unknown", []evdSeriesSpec{
			{src: evdSrcProm, at: -30 * time.Minute, quality: model.QualityGood},
			{src: evdSrcProm, at: -20 * time.Minute, quality: model.QualityGood},
			{src: evdSrcProm, at: evdLevelStale, quality: model.QualityGood},
			{src: evdSrcGNMI, at: -25 * time.Minute, quality: model.QualityGood},
			{src: evdSrcGNMI, at: evdLevelStale, quality: model.QualityGood},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := evdSpecWindow(t, evdConfig(), tc.specs...)
			assertLevel(t, w, evdSubjectA(t), evdSigA, evdLevelNow, model.LevelUnknown, tc.name)

			// 내용은 남아 있다 (설명가능성) — 판정만 유보된다.
			series, err := w.SeriesFor(evdSubjectA(t), evdSigA)
			requireNoErr(t, err, "SeriesFor")
			if len(series) == 0 {
				t.Errorf("%s: stale 관측이 조회에서도 사라졌다", tc.name)
			}
			for _, s := range series {
				assertFresh(t, s, evdLevelNow, false, "stale 시리즈["+s.Source().Name+"]")
			}
		})
	}
}

// EVD-072 (edge): 신선한 시리즈가 하나 이상이면 결과는 Observed 또는
// Corroborated다 — 신선한 evidence는 어떤 경로로도 Unknown으로 강등되지 않는다.
func TestEVD072_FreshEvidenceIsNeverDemotedToUnknown(t *testing.T) {
	cases := []struct {
		name  string
		specs []evdSeriesSpec
	}{
		{"신선한 Good 하나", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood},
		}},
		{"신선한 Degraded 하나", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityDegraded},
		}},
		{"신선한 Unknown 하나", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityUnknown},
		}},
		{"신선한 것 하나 + stale 다수", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcGNMI, at: evdLevelStale, quality: model.QualityGood},
			{src: evdSrcAgent, at: evdLevelStale, quality: model.QualityGood},
		}},
		{"신선한 Degraded 하나 + stale Good 다수", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityDegraded},
			{src: evdSrcGNMI, at: evdLevelStale, quality: model.QualityGood},
		}},
		{"신선한 Type 2종", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcGNMI, at: evdLevelFresh, quality: model.QualityGood},
		}},
		{"값이 0인 신선한 관측", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood, value: 0},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := evdSpecWindow(t, evdConfig(), tc.specs...)
			got, err := w.Level(evdSubjectA(t), evdSigA, evdLevelNow)
			requireNoErr(t, err, "Level")
			if got == model.LevelUnknown {
				t.Errorf("%s: 신선한 evidence가 있는데 Unknown으로 강등되었다", tc.name)
			}
			if got != model.LevelObserved && got != model.LevelCorroborated {
				t.Errorf("%s: Level = %s, want Observed 또는 Corroborated", tc.name, got.String())
			}
			assertLevelNeverInferredOrOutOfScope(t, got, tc.name)
		})
	}
}

// EVD-073: 신선한 시리즈가 전부 같은 Source.Type이면 — Name·dimensions가
// 여럿이어도 — LevelObserved다.
func TestEVD073_SameSourceTypeIsObserved(t *testing.T) {
	cases := []struct {
		name  string
		specs []evdSeriesSpec
	}{
		{"같은 Type·같은 Name 하나", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood},
		}},
		{"같은 Type·다른 Name 2개", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcPromAlt, at: evdLevelFresh, quality: model.QualityGood},
		}},
		{"같은 Type·같은 Name·다른 dimensions 3개", []evdSeriesSpec{
			{src: evdSrcProm, dims: map[string]string{"lane": "0"}, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcProm, dims: map[string]string{"lane": "1"}, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcProm, dims: map[string]string{"lane": "2"}, at: evdLevelFresh, quality: model.QualityGood},
		}},
		{"같은 Type · 다른 Type은 stale", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcPromAlt, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcGNMI, at: evdLevelStale, quality: model.QualityGood},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := evdSpecWindow(t, evdConfig(), tc.specs...)
			assertLevel(t, w, evdSubjectA(t), evdSigA, evdLevelNow, model.LevelObserved, tc.name)
		})
	}
}

// EVD-074: corroboration 자격 시리즈의 서로 다른 Source.Type 수가 2 이상이면
// Corroborated다 (결정 지점 ③ — 같은 Type의 다른 Name은 독립으로 세지 않는다).
func TestEVD074_CorroborationCountsDistinctSourceTypes(t *testing.T) {
	cases := []struct {
		name  string
		specs []evdSeriesSpec
		want  model.EvidenceLevel
	}{
		{"prometheus + gnmi", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcGNMI, at: evdLevelFresh, quality: model.QualityGood},
		}, model.LevelCorroborated},
		{"agent + gnmi (LLDP 양방향 assurance)", []evdSeriesSpec{
			{src: evdSrcAgent, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcGNMI, at: evdLevelFresh, quality: model.QualityGood},
		}, model.LevelCorroborated},
		{"Type 3종", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcGNMI, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcAgent, at: evdLevelFresh, quality: model.QualityGood},
		}, model.LevelCorroborated},
		{"같은 Type의 다른 Name 2개뿐", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcPromAlt, at: evdLevelFresh, quality: model.QualityGood},
		}, model.LevelObserved},
		{"같은 Type 2개 + 다른 Type 1개", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcPromAlt, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcGNMI, at: evdLevelFresh, quality: model.QualityGood},
		}, model.LevelCorroborated},
		{"둘째 Type이 stale이면 Observed", []evdSeriesSpec{
			{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcGNMI, at: evdLevelStale, quality: model.QualityGood},
		}, model.LevelObserved},
		{"Type은 같고 dimensions만 다른 여러 시리즈", []evdSeriesSpec{
			{src: evdSrcProm, dims: map[string]string{"lane": "0"}, at: evdLevelFresh, quality: model.QualityGood},
			{src: evdSrcProm, dims: map[string]string{"lane": "1"}, at: evdLevelFresh, quality: model.QualityGood},
		}, model.LevelObserved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := evdSpecWindow(t, evdConfig(), tc.specs...)
			assertLevel(t, w, evdSubjectA(t), evdSigA, evdLevelNow, tc.want, tc.name)
		})
	}
}

// EVD-075 (edge): Quality 소비 — QualityDegraded는 corroboration에 기여하지
// 않지만 Unknown 강등 사유도 아니다. QualityUnknown은 자격이 있다.
func TestEVD075_QualityConsumption(t *testing.T) {
	cases := []struct {
		name  string
		specs []evdSeriesSpec
		want  model.EvidenceLevel
	}{
		{
			"(a) 서로 다른 Type 2개가 모두 Degraded → Observed",
			[]evdSeriesSpec{
				{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityDegraded},
				{src: evdSrcGNMI, at: evdLevelFresh, quality: model.QualityDegraded},
			},
			model.LevelObserved,
		},
		{
			"(b) Unknown + Good, 서로 다른 Type → Corroborated",
			[]evdSeriesSpec{
				{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityUnknown},
				{src: evdSrcGNMI, at: evdLevelFresh, quality: model.QualityGood},
			},
			model.LevelCorroborated,
		},
		{
			"(b') Unknown + Unknown, 서로 다른 Type → Corroborated",
			[]evdSeriesSpec{
				{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityUnknown},
				{src: evdSrcGNMI, at: evdLevelFresh, quality: model.QualityUnknown},
			},
			model.LevelCorroborated,
		},
		{
			"(c) 신선한 Degraded 하나뿐 → Observed",
			[]evdSeriesSpec{
				{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityDegraded},
			},
			model.LevelObserved,
		},
		{
			"Degraded 1 + Good 1 (Type 상이) → 자격 Type이 하나뿐이라 Observed",
			[]evdSeriesSpec{
				{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityDegraded},
				{src: evdSrcGNMI, at: evdLevelFresh, quality: model.QualityGood},
			},
			model.LevelObserved,
		},
		{
			"Degraded 1 + Good 2 (Type 3종) → 자격 Type 2종이라 Corroborated",
			[]evdSeriesSpec{
				{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityDegraded},
				{src: evdSrcGNMI, at: evdLevelFresh, quality: model.QualityGood},
				{src: evdSrcAgent, at: evdLevelFresh, quality: model.QualityGood},
			},
			model.LevelCorroborated,
		},
		{
			"같은 Type 안에서 Degraded와 Good이 섞여도 Type 수는 1 → Observed",
			[]evdSeriesSpec{
				{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityDegraded},
				{src: evdSrcPromAlt, at: evdLevelFresh, quality: model.QualityGood},
			},
			model.LevelObserved,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := evdSpecWindow(t, evdConfig(), tc.specs...)
			assertLevel(t, w, evdSubjectA(t), evdSigA, evdLevelNow, tc.want, tc.name)
		})
	}
}

// EVD-075 (edge) · 3.7 규칙 2: 판정은 시리즈의 최신 관측만 본다 — 과거 관측의
// Quality는 corroboration 자격에 영향을 주지 않는다.
func TestEVD075_OnlyLatestObservationQualityMatters(t *testing.T) {
	t.Run("최신이 Degraded면 과거가 Good이어도 자격이 없다", func(t *testing.T) {
		w := evdSpecWindow(t, evdConfig(),
			evdSeriesSpec{src: evdSrcProm, at: -time.Minute, quality: model.QualityGood, value: 1},
			evdSeriesSpec{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityDegraded, value: 2},
			evdSeriesSpec{src: evdSrcGNMI, at: evdLevelFresh, quality: model.QualityGood, value: 3},
		)
		assertLevel(t, w, evdSubjectA(t), evdSigA, evdLevelNow, model.LevelObserved,
			"최신 Degraded")
	})

	t.Run("최신이 Good이면 과거가 Degraded여도 자격이 있다", func(t *testing.T) {
		w := evdSpecWindow(t, evdConfig(),
			evdSeriesSpec{src: evdSrcProm, at: -time.Minute, quality: model.QualityDegraded, value: 1},
			evdSeriesSpec{src: evdSrcProm, at: evdLevelFresh, quality: model.QualityGood, value: 2},
			evdSeriesSpec{src: evdSrcGNMI, at: evdLevelFresh, quality: model.QualityGood, value: 3},
		)
		assertLevel(t, w, evdSubjectA(t), evdSigA, evdLevelNow, model.LevelCorroborated,
			"최신 Good")
	})
}

// EVD-076: Level은 어떤 입력에서도 LevelInferred·LevelOutOfScope를 반환하지
// 않는다 (결정 지점 ④).
func TestEVD076_LevelNeverReturnsInferredOrOutOfScope(t *testing.T) {
	qualities := []model.EvidenceQuality{model.QualityUnknown, model.QualityGood, model.QualityDegraded}
	sources := []model.SourceRef{evdSrcProm, evdSrcPromAlt, evdSrcGNMI, evdSrcAgent}
	offsets := []time.Duration{evdLevelFresh, evdLevelStale, -time.Minute, 30 * time.Second}

	// 단일 시리즈 전수.
	for _, q := range qualities {
		for _, src := range sources {
			for _, at := range offsets {
				w := evdSpecWindow(t, evdConfig(), evdSeriesSpec{src: src, at: at, quality: q})
				got, err := w.Level(evdSubjectA(t), evdSigA, evdLevelNow)
				requireNoErr(t, err, "Level")
				assertLevelNeverInferredOrOutOfScope(t, got, "단일 시리즈 전수")
			}
		}
	}

	// 두 시리즈 조합 전수.
	for _, qA := range qualities {
		for _, qB := range qualities {
			for _, srcB := range sources {
				for _, atB := range offsets {
					w := evdSpecWindow(t, evdConfig(),
						evdSeriesSpec{src: evdSrcProm, at: evdLevelFresh, quality: qA},
						evdSeriesSpec{src: srcB, at: atB, quality: qB, dims: map[string]string{"lane": "1"}},
					)
					got, err := w.Level(evdSubjectA(t), evdSigA, evdLevelNow)
					requireNoErr(t, err, "Level")
					assertLevelNeverInferredOrOutOfScope(t, got, "두 시리즈 조합 전수")
				}
			}
		}
	}

	// 시리즈가 없는 경우와 오류 경로.
	empty := mustWindow(t, evdConfig())
	got, err := empty.Level(evdSubjectA(t), evdSigA, evdLevelNow)
	requireNoErr(t, err, "Level(빈 Window)")
	assertLevelNeverInferredOrOutOfScope(t, got, "빈 Window")

	got, _ = empty.Level(evdSubjectA(t), evdSigA, zeroTime)
	assertLevelNeverInferredOrOutOfScope(t, got, "zero now (오류 경로)")

	var zeroWindow evidence.Window
	got, err = zeroWindow.Level(evdSubjectA(t), evdSigA, evdLevelNow)
	requireNoErr(t, err, "Level(zero Window)")
	assertLevelNeverInferredOrOutOfScope(t, got, "zero Window")
}

// EVD-077 (edge): corroboration은 값의 일치를 판정하지 않는다 — 서로 모순되는
// 값을 보고해도 Corroborated다 (값 모순은 룰의 재료다).
func TestEVD077_ConflictingValuesStillCorroborate(t *testing.T) {
	sigPeer := model.SignalRef("fabric.port.lldp.peer_chassis")

	build := func(t *testing.T, agentValue, gnmiValue model.Value) evidence.Window {
		t.Helper()
		w := mustWindow(t, evdConfig())

		oAgent := evdObs(t, evdAt(evdLevelFresh), agentValue)
		oAgent = evdRetag(oAgent, evdSrcAgent, evdSubjectA(t), sigPeer, nil)
		oGNMI := evdObs(t, evdAt(evdLevelFresh), gnmiValue)
		oGNMI = evdRetag(oGNMI, evdSrcGNMI, evdSubjectA(t), sigPeer, nil)

		return mustAdd(t, w, oAgent, oGNMI)
	}

	t.Run("서로 다른 peer 문자열", func(t *testing.T) {
		w := build(t, model.NewStringValue("switch-a:Ethernet1/1"), model.NewStringValue("switch-b:Ethernet9/9"))
		assertLevel(t, w, evdSubjectA(t), sigPeer, evdLevelNow, model.LevelCorroborated, "값 모순")
	})

	t.Run("같은 peer 문자열 (일치해도 같은 판정)", func(t *testing.T) {
		w := build(t, model.NewStringValue("switch-a:Ethernet1/1"), model.NewStringValue("switch-a:Ethernet1/1"))
		assertLevel(t, w, evdSubjectA(t), sigPeer, evdLevelNow, model.LevelCorroborated, "값 일치")
	})

	t.Run("kind가 서로 다른 값", func(t *testing.T) {
		w := build(t, model.NewBoolValue(true), model.NewFloatValue(0))
		assertLevel(t, w, evdSubjectA(t), sigPeer, evdLevelNow, model.LevelCorroborated, "kind 모순")
	})
}

// 3.7: Level은 (subject, signal)마다 독립이다 — 한쪽의 corroboration이 다른
// (subject, signal)의 판정에 새어 나가지 않는다.
func TestSpec37_LevelIsPerSubjectAndSignal(t *testing.T) {
	w := mustWindow(t, evdConfig())
	// (A, sigA): Type 2종 → Corroborated.
	w = mustAdd(t, w,
		evdRetag(evdFloat(t, evdAt(evdLevelFresh), 1), evdSrcProm, evdSubjectA(t), evdSigA, nil),
		evdRetag(evdFloat(t, evdAt(evdLevelFresh), 2), evdSrcGNMI, evdSubjectA(t), evdSigA, nil),
	)
	// (A, sigB): Type 1종 → Observed.
	w = mustAdd(t, w,
		evdRetag(evdFloat(t, evdAt(evdLevelFresh), 3), evdSrcProm, evdSubjectA(t), evdSigB, nil))
	// (B, sigA): stale → Unknown.
	w = mustAdd(t, w,
		evdRetag(evdFloat(t, evdAt(evdLevelStale), 4), evdSrcProm, evdSubjectB(t), evdSigA, nil))

	assertLevel(t, w, evdSubjectA(t), evdSigA, evdLevelNow, model.LevelCorroborated, "(A, sigA)")
	assertLevel(t, w, evdSubjectA(t), evdSigB, evdLevelNow, model.LevelObserved, "(A, sigB)")
	assertLevel(t, w, evdSubjectB(t), evdSigA, evdLevelNow, model.LevelUnknown, "(B, sigA)")
	assertLevel(t, w, evdSubjectB(t), evdSigB, evdLevelNow, model.LevelUnknown, "(B, sigB) — 관측 없음")
	assertLevel(t, w, evdSubjectC(t), evdSigA, evdLevelNow, model.LevelUnknown, "(C, sigA) — subject 없음")
}

// 3.7: Level은 Aliases가 다른 AssetRef로 조회해도 같은 subject를 찾는다
// (조회 공통 규약 — Key() 기준).
func TestSpec37_LevelMatchesSubjectByKey(t *testing.T) {
	alias := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
	o := evdFloat(t, evdAt(evdLevelFresh), 1)
	o.Subject = evdSubjectAWithAliases(t, alias)
	w := mustAdd(t, mustWindow(t, evdConfig()), o)

	other := mustTypedID(t, string(model.NamespaceLLDPChassisID), "aa:bb")
	assertLevel(t, w, evdSubjectAWithAliases(t, other), evdSigA, evdLevelNow,
		model.LevelObserved, "다른 Aliases로 조회")
	assertLevel(t, w, evdSubjectA(t), evdSigA, evdLevelNow,
		model.LevelObserved, "Aliases 없이 조회")
}
