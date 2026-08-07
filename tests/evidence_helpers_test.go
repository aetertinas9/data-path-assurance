// evidence_helpers_test.go — internal/evidence 블랙박스 테스트의 공용 헬퍼·픽스처.
// specs/evidence/spec.md (ratified v1.1) 3절의 공개 계약만 참조한다.
//
// EVD-001(표준 라이브러리와 pkg/model 외 import 금지)은 블랙박스 테스트의 사정
// 범위 밖이다 — 스펙 5절이 `make arch-check`와 verifier의
// `go list -deps ./internal/evidence`에 배정한다 (MDL-001·IDN-001과 같은 취급).
package tests

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// --- 시각 픽스처 ---
//
// EVD-002(시계 비의존)·EVD-004(결정론)를 위해 모든 시각은 고정 리터럴에서
// 파생한다. time.Now()는 EVD-007의 monotonic reading 검증에서만 쓴다 —
// 그곳에서도 판정은 "두 표현의 결과가 같다"이므로 벽시계 값에 의존하지 않는다.
var evdT0 = time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)

// evdAt은 기준 시각 evdT0에서 d만큼 떨어진 instant다.
func evdAt(d time.Duration) time.Time { return evdT0.Add(d) }

// --- Config 픽스처 (3.3) ---

const (
	evdMaxAge     = 5 * time.Minute
	evdHorizon    = time.Hour
	evdMaxSamples = 8
)

// evdConfig는 EVD-010의 유효 요건을 만족하는 기본 구성이다.
func evdConfig() evidence.Config {
	return evidence.Config{MaxAge: evdMaxAge, Horizon: evdHorizon, MaxSamples: evdMaxSamples}
}

// --- source 픽스처 (3.1) ---
//
// (Type, Name) 바이트 오름차순은 agent < gnmi < prometheus 순이다 (EVD-030).
var (
	evdSrcAgent   = model.SourceRef{Type: string(model.SourceTypeAgent), Name: "agent-node1"}
	evdSrcGNMI    = model.SourceRef{Type: string(model.SourceTypeGNMI), Name: "gnmi-tor1"}
	evdSrcProm    = model.SourceRef{Type: string(model.SourceTypePrometheus), Name: "prom-main"}
	evdSrcPromAlt = model.SourceRef{Type: string(model.SourceTypePrometheus), Name: "prom-replica"}
)

// --- subject 픽스처 ---
//
// Key() 바이트 오름차순은 NICPort/… < Pod/… < SwitchPort/… 순이다 (EVD-030).

func evdSubjectA(t *testing.T) model.AssetRef {
	t.Helper()
	return mustAssetRef(t, model.KindNICPort, mustTypedID(t, string(model.NamespacePCIBDF), "0000:af:00.0"))
}

func evdSubjectB(t *testing.T) model.AssetRef {
	t.Helper()
	return mustAssetRef(t, model.KindSwitchPort, mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1"))
}

func evdSubjectC(t *testing.T) model.AssetRef {
	t.Helper()
	return mustAssetRef(t, model.KindPod, mustTypedID(t, string(model.NamespaceKubernetesPodUID), "9f1c-pod-uid"))
}

// evdSubjectAWithAliases는 evdSubjectA와 Key()가 같지만 Aliases가 다른 AssetRef다
// (EVD-022·032·3.5의 노출 AssetRef 선정 검증용).
func evdSubjectAWithAliases(t *testing.T, aliases ...model.TypedID) model.AssetRef {
	t.Helper()
	return mustAssetRef(t, model.KindNICPort,
		mustTypedID(t, string(model.NamespacePCIBDF), "0000:af:00.0"), aliases...)
}

// --- signal 픽스처 (MDL-033 형식) ---
//
// 바이트 오름차순은 fabric… < pcie… < zzz… 순이다 (EVD-030).
const (
	evdSigA model.SignalRef = "fabric.port.fec.corrected_rate"
	evdSigB model.SignalRef = "pcie.link.width"
	evdSigC model.SignalRef = "zzz.last"
)

// --- 관측 빌더 ---

// evdObs는 3.4 규칙 1(유효성)을 통과하는 관측을 만든다.
// 기본 시리즈는 (evdSrcProm, evdSubjectA, evdSigA, nil Dimensions)이며,
// 각 테스트는 반환된 구조체에서 필요한 필드만 바꿔 쓴다.
func evdObs(t *testing.T, at time.Time, v model.Value) model.Observation {
	t.Helper()
	return model.Observation{
		ID:         "obs-" + evdTimeString(at),
		Source:     evdSrcProm,
		Subject:    evdSubjectA(t),
		Signal:     evdSigA,
		Value:      v,
		Unit:       "1",
		ObservedAt: at,
		ReceivedAt: at.Add(500 * time.Millisecond),
		Quality:    model.QualityGood,
	}
}

// evdFloat은 Float kind counter 관측의 축약이다.
func evdFloat(t *testing.T, at time.Time, v float64) model.Observation {
	t.Helper()
	return evdObs(t, at, model.NewFloatValue(v))
}

// evdInt은 Int kind counter 관측의 축약이다.
func evdInt(t *testing.T, at time.Time, v int64) model.Observation {
	t.Helper()
	return evdObs(t, at, model.NewIntValue(v))
}

// evdRetag는 관측의 시리즈 좌표(3.1의 (a)~(d))를 바꾼다. ID도 함께 바꿔
// 관측끼리 구별되게 한다 (ID는 동등성 판정에 관여하지 않는다 — EVD-022).
func evdRetag(o model.Observation, src model.SourceRef, subj model.AssetRef, sig model.SignalRef, dims map[string]string) model.Observation {
	o.Source = src
	o.Subject = subj
	o.Signal = sig
	o.Dimensions = dims
	o.ID = strings.Join([]string{
		o.ID, src.Type + "/" + src.Name, subj.Key(), sig.String(), evdDimensionsString(dims),
	}, "|")
	return o
}

// --- Window 조작 헬퍼 ---

func mustWindow(t *testing.T, cfg evidence.Config) evidence.Window {
	t.Helper()
	w, err := evidence.NewWindow(cfg)
	requireNoErr(t, err, "NewWindow")
	return w
}

// mustAdd는 관측들을 차례로 Add하고 오류가 없음을 단정한다.
func mustAdd(t *testing.T, w evidence.Window, obs ...model.Observation) evidence.Window {
	t.Helper()
	for i, o := range obs {
		next, err := w.Add(o)
		requireNoErr(t, err, fmt.Sprintf("Add(#%d, observedAt=%s)", i, evdTimeString(o.ObservedAt)))
		w = next
	}
	return w
}

func mustPrune(t *testing.T, w evidence.Window, cutoff time.Time) evidence.Window {
	t.Helper()
	next, err := w.Prune(cutoff)
	requireNoErr(t, err, "Prune("+evdTimeString(cutoff)+")")
	return next
}

// evdPoint는 시리즈 한 점이다 — 기준 시각 evdT0에서의 오프셋과 float64 값.
type evdPoint struct {
	d time.Duration
	v float64
}

// evdFloatSeriesWindow는 기본 시리즈에 pts를 넣은 window를 만든다.
func evdFloatSeriesWindow(t *testing.T, cfg evidence.Config, pts ...evdPoint) evidence.Window {
	t.Helper()
	w := mustWindow(t, cfg)
	for _, p := range pts {
		w = mustAdd(t, w, evdFloat(t, evdAt(p.d), p.v))
	}
	return w
}

// evdSoleSeries는 (subject, signal)의 시리즈가 정확히 하나임을 단정하고 돌려준다.
// **픽스처가 그 (subject, signal)에 시리즈를 하나만 만들 때만** 쓴다 — "하나뿐"이
// 단정의 일부이기 때문이다. 여러 시리즈가 있는 픽스처에서 그중 하나를 지목하려면
// evdSeriesAt을 써라.
func evdSoleSeries(t *testing.T, w evidence.Window, subject model.AssetRef, signal model.SignalRef) evidence.Series {
	t.Helper()
	series, err := w.SeriesFor(subject, signal)
	requireNoErr(t, err, "SeriesFor("+subject.Key()+", "+signal.String()+")")
	if len(series) != 1 {
		t.Fatalf("SeriesFor(%s, %s): 시리즈 %d개, 정확히 1개를 기대했다 (전체: %v)",
			subject.Key(), signal.String(), len(series), evdSeriesKeys(series))
	}
	return series[0]
}

// evdDefaultSeries는 기본 시리즈(evdSubjectA·evdSigA)를 꺼낸다 — evdSoleSeries와
// 같은 전제(그 (subject, signal)에 시리즈가 하나뿐)를 요구한다.
func evdDefaultSeries(t *testing.T, w evidence.Window) evidence.Series {
	t.Helper()
	return evdSoleSeries(t, w, evdSubjectA(t), evdSigA)
}

// evdSeriesAt은 3.1의 네 좌표를 **전부** 지정해 시리즈 하나를 꺼낸다 —
// (a) Source, (b) subject, (c) Signal, (d) Dimensions. 스펙 3.1이 시리즈를
// 이 넷으로 식별하므로("하나라도 다르면 다른 시리즈다") 이 지정은 픽스처가
// 나머지 좌표만 다른 시리즈를 아무리 더 만들어도 모호해지지 않는다.
// 좌표의 진부분집합(예: source만)으로 거르는 선택자는 그런 픽스처에서 시리즈를
// 유일하게 식별하지 못하므로 쓰지 않는다.
//
// dims 비교는 evdDimensionsString 기준이다 — nil 맵과 빈 맵은 같은 좌표다 (3.1 (d)).
// 일치가 정확히 1개가 아니면 실패한다: 0개는 픽스처·기대의 불일치이고, 2개 이상은
// 같은 좌표의 시리즈가 중복 존재한다는 뜻이라 구현 결함이다.
func evdSeriesAt(t *testing.T, w evidence.Window, subject model.AssetRef, signal model.SignalRef, src model.SourceRef, dims map[string]string) evidence.Series {
	t.Helper()
	series, err := w.SeriesFor(subject, signal)
	requireNoErr(t, err, "SeriesFor("+subject.Key()+", "+signal.String()+")")

	wantDims := evdDimensionsString(dims)
	var found []evidence.Series
	for _, s := range series {
		if s.Source() == src && evdDimensionsString(s.Dimensions()) == wantDims {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("(%s, %s, %s/%s, %s)의 시리즈 %d개, 정확히 1개를 기대했다 (전체: %v)",
			subject.Key(), signal.String(), src.Type, src.Name, wantDims, len(found), evdSeriesKeys(series))
	}
	return found[0]
}

// evdDefaultSeriesOf는 기본 (subject, signal)에서 좌표 (src, dims)의 시리즈를 꺼낸다.
func evdDefaultSeriesOf(t *testing.T, w evidence.Window, src model.SourceRef, dims map[string]string) evidence.Series {
	t.Helper()
	return evdSeriesAt(t, w, evdSubjectA(t), evdSigA, src, dims)
}

// evdSeriesSpec은 level·조회 테스트에서 시리즈 하나를 기술한다
// (subject·signal은 기본값 evdSubjectA·evdSigA를 쓴다).
type evdSeriesSpec struct {
	src     model.SourceRef
	dims    map[string]string
	quality model.EvidenceQuality
	at      time.Duration
	value   float64
}

// evdSpecWindow는 specs를 순서대로 Add한 window를 만든다. 같은 (src, dims)를
// 서로 다른 at으로 두 번 주면 한 시리즈에 관측 두 개가 쌓인다.
func evdSpecWindow(t *testing.T, cfg evidence.Config, specs ...evdSeriesSpec) evidence.Window {
	t.Helper()
	w := mustWindow(t, cfg)
	for i, s := range specs {
		o := evdRetag(evdFloat(t, evdAt(s.at), s.value), s.src, evdSubjectA(t), evdSigA, s.dims)
		o.Quality = s.quality
		o.ID = fmt.Sprintf("obs-spec-%02d", i)
		w = mustAdd(t, w, o)
	}
	return w
}

// evdPermutations는 obs의 모든 순열을 만든다 (EVD-004 삽입 순서 무관 검증용).
func evdPermutations(obs []model.Observation) [][]model.Observation {
	if len(obs) <= 1 {
		return [][]model.Observation{append([]model.Observation(nil), obs...)}
	}
	var out [][]model.Observation
	for i := range obs {
		rest := make([]model.Observation, 0, len(obs)-1)
		rest = append(rest, obs[:i]...)
		rest = append(rest, obs[i+1:]...)
		for _, p := range evdPermutations(rest) {
			perm := make([]model.Observation, 0, len(obs))
			perm = append(perm, obs[i])
			perm = append(perm, p...)
			out = append(out, perm)
		}
	}
	return out
}

// --- 오류 계열 헬퍼 (3.2) ---

func requireEvdConflict(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: ErrConflict 오류를 기대했으나 nil을 받았다", what)
	}
	if !errors.Is(err, evidence.ErrConflict) {
		t.Fatalf("%s: errors.Is(err, evidence.ErrConflict)가 참이어야 한다 (err=%v)", what, err)
	}
}

func requireEvdOutOfWindow(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: ErrOutOfWindow 오류를 기대했으나 nil을 받았다", what)
	}
	if !errors.Is(err, evidence.ErrOutOfWindow) {
		t.Fatalf("%s: errors.Is(err, evidence.ErrOutOfWindow)가 참이어야 한다 (err=%v)", what, err)
	}
}

func requireEvdInsufficientSamples(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: ErrInsufficientSamples 오류를 기대했으나 nil을 받았다", what)
	}
	if !errors.Is(err, evidence.ErrInsufficientSamples) {
		t.Fatalf("%s: errors.Is(err, evidence.ErrInsufficientSamples)가 참이어야 한다 (err=%v)", what, err)
	}
}

// evdErrClass는 오류를 스펙 3.2의 계열 이름으로 분류한다 (스냅샷 비교용).
func evdErrClass(err error) string {
	switch {
	case err == nil:
		return "nil"
	case errors.Is(err, model.ErrInvalid):
		return "invalid"
	case errors.Is(err, evidence.ErrConflict):
		return "conflict"
	case errors.Is(err, evidence.ErrOutOfWindow):
		return "out-of-window"
	case errors.Is(err, evidence.ErrInsufficientSamples):
		return "insufficient-samples"
	default:
		return "other"
	}
}

// --- 결정론적 렌더링 (스냅샷 비교용) ---

// evdTimeString은 시각을 instant 기준으로 렌더링한다 — Location과 monotonic
// reading은 결과에 나타나지 않는다 (EVD-007).
func evdTimeString(ts time.Time) string { return ts.UTC().Format(time.RFC3339Nano) }

// evdFloatString은 float64를 왕복 가능한 최단 표기로 렌더링한다 (EVD-008의
// "같은 바이너리 안에서 같은 입력 → 같은 PerSecond"를 비트 수준으로 비교한다).
func evdFloatString(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func evdValueString(v model.Value) string {
	switch v.Kind() {
	case model.ValueFloat:
		f, _ := v.Float()
		return "Float(" + evdFloatString(f) + ")"
	case model.ValueInt:
		i, _ := v.Int()
		return "Int(" + strconv.FormatInt(i, 10) + ")"
	case model.ValueBool:
		b, _ := v.Bool()
		return "Bool(" + strconv.FormatBool(b) + ")"
	case model.ValueString:
		s, _ := v.Str()
		return "String(" + strconv.Quote(s) + ")"
	default:
		return "Invalid"
	}
}

// evdDimensionsString은 dimensions를 키 오름차순으로 렌더링한다
// (nil 맵과 빈 맵은 같은 문자열이다 — 3.1).
func evdDimensionsString(d map[string]string) string {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+d[k])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// evdAliasesString은 Aliases를 Raw·Source까지 포함해 렌더링한다 — EVD-006의
// 방어적 복사 위반이 스냅샷에 드러나야 하기 때문이다.
func evdAliasesString(ref model.AssetRef) string {
	parts := make([]string, 0, len(ref.Aliases))
	for _, a := range ref.Aliases {
		parts = append(parts, a.String()+"|raw="+string(a.Raw)+"|source="+a.Source)
	}
	return strings.Join(parts, ";")
}

func evdObsString(o model.Observation) string {
	return strings.Join([]string{
		"id=" + o.ID,
		"src=" + o.Source.Type + "/" + o.Source.Name,
		"subj=" + o.Subject.Key(),
		"aliases=" + evdAliasesString(o.Subject),
		"sig=" + o.Signal.String(),
		"val=" + evdValueString(o.Value),
		"unit=" + o.Unit,
		"dims=" + evdDimensionsString(o.Dimensions),
		"observed=" + evdTimeString(o.ObservedAt),
		"received=" + evdTimeString(o.ReceivedAt),
		"expires=" + evdTimeString(o.ExpiresAt),
		"seq=" + strconv.FormatUint(o.Sequence, 10),
		"quality=" + o.Quality.String(),
		"digest=" + o.RawDigest,
	}, "\t")
}

// evdRateString은 RateResult를 instant 기준으로 렌더링한다 (`==` 대신 이 문자열로
// 비교한다 — 시각의 동일성은 Equal 기준이기 때문이다, EVD-007).
func evdRateString(r evidence.RateResult) string {
	return fmt.Sprintf("perSecond=%s start=%s end=%s samples=%d disc=%d",
		evdFloatString(r.PerSecond), evdTimeString(r.Start), evdTimeString(r.End),
		r.Samples, r.Discontinuities)
}

// evdSeriesString은 Series의 관측 가능한 전부를 렌더링한다.
func evdSeriesString(s evidence.Series, now time.Time, prefix string) string {
	var b strings.Builder
	src := s.Source()
	fresh, ferr := s.Fresh(now)
	fmt.Fprintf(&b, "%s src=%s/%s subj=%s aliases=%s sig=%s dims=%s fresh=%t/%s\n",
		prefix, src.Type, src.Name, s.Subject().Key(), evdAliasesString(s.Subject()),
		s.Signal().String(), evdDimensionsString(s.Dimensions()), fresh, evdErrClass(ferr))

	latest, ok := s.Latest()
	fmt.Fprintf(&b, "%s latest=%t %s\n", prefix, ok, evdObsString(latest))

	r, rerr := s.Rate()
	fmt.Fprintf(&b, "%s rate: %s err=%s\n", prefix, evdRateString(r), evdErrClass(rerr))

	for i, o := range s.Observations() {
		fmt.Fprintf(&b, "%s obs[%d] %s\n", prefix, i, evdObsString(o))
	}
	return b.String()
}

// evdSnapshot은 Window의 관측 가능한 상태 전부(모든 조회·판정 결과)를 결정론적
// 문자열로 만든다. "window가 변하지 않는다"(EVD-003·005·020·021·024·026)와
// "두 window가 구별되지 않는다"(EVD-004·034·045·058)를 한 줄로 단정하는 데 쓴다.
// now는 비-zero여야 한다 (판정 인자).
func evdSnapshot(t *testing.T, w evidence.Window, now time.Time) string {
	t.Helper()

	var b strings.Builder
	for _, src := range w.Sources() {
		last, err := w.SourceLastObserved(src)
		fmt.Fprintf(&b, "source\t%s/%s\tlast=%s\terr=%s\n",
			src.Type, src.Name, evdTimeString(last), evdErrClass(err))
	}
	for _, subj := range w.Subjects() {
		fmt.Fprintf(&b, "subject\t%s\taliases=%s\n", subj.Key(), evdAliasesString(subj))

		signals, err := w.Signals(subj)
		fmt.Fprintf(&b, "  signals\tn=%d\terr=%s\n", len(signals), evdErrClass(err))
		for _, sig := range signals {
			lvl, lerr := w.Level(subj, sig, now)
			fmt.Fprintf(&b, "  signal\t%s\tlevel=%s\terr=%s\n", sig.String(), lvl.String(), evdErrClass(lerr))

			series, serr := w.SeriesFor(subj, sig)
			fmt.Fprintf(&b, "    seriesfor\tn=%d\terr=%s\n", len(series), evdErrClass(serr))
			for i, s := range series {
				b.WriteString(evdSeriesString(s, now, fmt.Sprintf("    series[%d]", i)))
			}
		}
	}
	return b.String()
}

// --- 단정 헬퍼 ---

// assertEmptyWindow는 "빈 window와 같은 결과"(EVD-010·012·031)를 단정한다.
func assertEmptyWindow(t *testing.T, w evidence.Window, now time.Time, what string) {
	t.Helper()

	if got := w.Subjects(); len(got) != 0 {
		t.Errorf("%s: Subjects() 길이 %d, want 0 (%v)", what, len(got), got)
	}
	if got := w.Sources(); len(got) != 0 {
		t.Errorf("%s: Sources() 길이 %d, want 0 (%v)", what, len(got), got)
	}

	subj := evdSubjectA(t)
	signals, err := w.Signals(subj)
	requireNoErr(t, err, what+": Signals(유효 subject)")
	if len(signals) != 0 {
		t.Errorf("%s: Signals() 길이 %d, want 0", what, len(signals))
	}

	series, err := w.SeriesFor(subj, evdSigA)
	requireNoErr(t, err, what+": SeriesFor(유효 인자)")
	if len(series) != 0 {
		t.Errorf("%s: SeriesFor() 길이 %d, want 0", what, len(series))
	}

	lvl, err := w.Level(subj, evdSigA, now)
	requireNoErr(t, err, what+": Level(유효 인자)")
	if lvl != model.LevelUnknown {
		t.Errorf("%s: Level() = %s, want Unknown", what, lvl.String())
	}

	last, err := w.SourceLastObserved(evdSrcProm)
	requireNoErr(t, err, what+": SourceLastObserved(유효 source)")
	if !last.IsZero() {
		t.Errorf("%s: SourceLastObserved() = %s, want zero time", what, evdTimeString(last))
	}
}

// assertZeroWindow는 3.3의 zero value Window 특칙(EVD-011·012)을 단정한다 —
// 조회는 빈 window와 같고 조립은 model.ErrInvalid 계열 오류다.
func assertZeroWindow(t *testing.T, w evidence.Window, now time.Time, what string) {
	t.Helper()
	assertEmptyWindow(t, w, now, what)

	if _, err := w.Add(evdFloat(t, evdAt(0), 1)); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("%s: 구성 없는 Window의 Add = %v, want model.ErrInvalid 계열", what, err)
	}
	if _, err := w.Prune(evdAt(0)); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("%s: 구성 없는 Window의 Prune = %v, want model.ErrInvalid 계열", what, err)
	}
}

// assertLevel은 Level 판정을 단정하고, 덤으로 EVD-076(미반환 level)을 매번 검사한다.
func assertLevel(t *testing.T, w evidence.Window, subject model.AssetRef, signal model.SignalRef, now time.Time, want model.EvidenceLevel, what string) {
	t.Helper()
	got, err := w.Level(subject, signal, now)
	requireNoErr(t, err, what+": Level")
	if got != want {
		t.Errorf("%s: Level = %s, want %s", what, got.String(), want.String())
	}
	assertLevelNeverInferredOrOutOfScope(t, got, what)
}

// assertLevelNeverInferredOrOutOfScope는 EVD-076을 단정한다.
func assertLevelNeverInferredOrOutOfScope(t *testing.T, got model.EvidenceLevel, what string) {
	t.Helper()
	if got == model.LevelInferred || got == model.LevelOutOfScope {
		t.Errorf("%s: EVD-076 위반 — Level이 %s를 반환했다", what, got.String())
	}
}

// assertFresh는 Series.Fresh(now)를 단정한다.
func assertFresh(t *testing.T, s evidence.Series, now time.Time, want bool, what string) {
	t.Helper()
	got, err := s.Fresh(now)
	requireNoErr(t, err, what+": Fresh")
	if got != want {
		t.Errorf("%s: Fresh(%s) = %t, want %t", what, evdTimeString(now), got, want)
	}
}

// assertRateResult는 RateResult 전 필드를 단정한다. PerSecond는 비트 동일을
// 요구한다 — 인자로 주는 기대값은 이진수로 정확히 표현되는 값만 쓴다 (EVD-008).
func assertRateResult(t *testing.T, got evidence.RateResult, wantPerSecond float64, wantSamples, wantDisc int, wantStart, wantEnd time.Time, what string) {
	t.Helper()
	if got.PerSecond != wantPerSecond {
		t.Errorf("%s: PerSecond = %s, want %s", what, evdFloatString(got.PerSecond), evdFloatString(wantPerSecond))
	}
	if got.Samples != wantSamples {
		t.Errorf("%s: Samples = %d, want %d", what, got.Samples, wantSamples)
	}
	if got.Discontinuities != wantDisc {
		t.Errorf("%s: Discontinuities = %d, want %d", what, got.Discontinuities, wantDisc)
	}
	// EVD-007: 시각 결과 값의 동일성은 Equal(instant) 기준이다.
	if !got.Start.Equal(wantStart) {
		t.Errorf("%s: Start = %s, want %s", what, evdTimeString(got.Start), evdTimeString(wantStart))
	}
	if !got.End.Equal(wantEnd) {
		t.Errorf("%s: End = %s, want %s", what, evdTimeString(got.End), evdTimeString(wantEnd))
	}
	assertRateInvariants(t, got, what)
}

// assertRateInvariants는 3.6 규칙 6의 결과 불변식을 단정한다.
//
// 포화(v1.1)에서 오탐하지 않는다 — math.MaxFloat64는 유한·비음수이므로 아래
// 어떤 검사에도 걸리지 않는다. PerSecond의 **상한**은 계약이 아니므로(포화값
// 자체가 합법적인 반환값이다) 여기에 상한 검사를 추가해서는 안 된다.
func assertRateInvariants(t *testing.T, got evidence.RateResult, what string) {
	t.Helper()
	if math.IsNaN(got.PerSecond) {
		t.Errorf("%s: PerSecond가 NaN이다", what)
	}
	if math.IsInf(got.PerSecond, 0) {
		t.Errorf("%s: PerSecond가 ±Inf다 (%v)", what, got.PerSecond)
	}
	if got.PerSecond < 0 {
		t.Errorf("%s: PerSecond가 음수다 (%s)", what, evdFloatString(got.PerSecond))
	}
	if got.Samples < 2 {
		t.Errorf("%s: 성공한 Rate의 Samples = %d, want >= 2", what, got.Samples)
	}
	if got.Discontinuities < 0 {
		t.Errorf("%s: Discontinuities = %d, want >= 0", what, got.Discontinuities)
	}
	// 3.6 규칙 5: 분모는 항상 양수다.
	if !got.End.After(got.Start) {
		t.Errorf("%s: End(%s)가 Start(%s)보다 뒤여야 한다 (간격 0 차단)",
			what, evdTimeString(got.End), evdTimeString(got.Start))
	}
}

// assertRateNear는 PerSecond를 상대 오차 relTol 안에서 단정한다. 분모가 초
// 미만이거나 값이 2⁶⁴ 근방이라 이진수 반올림이 개입하는 경우에 쓴다
// (EVD-008: PerSecond는 IEEE 754 반올림 수준의 차이가 허용된다).
func assertRateNear(t *testing.T, got, want, relTol float64, what string) {
	t.Helper()
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Errorf("%s: PerSecond = %s, want 유한한 %s", what, evdFloatString(got), evdFloatString(want))
		return
	}
	scale := math.Abs(want)
	if scale == 0 {
		scale = 1
	}
	if diff := math.Abs(got - want); diff/scale > relTol {
		t.Errorf("%s: PerSecond = %s, want %s (상대 오차 %g 초과)",
			what, evdFloatString(got), evdFloatString(want), relTol)
	}
}

// assertZeroRateResult는 오류 시 zero RateResult 반환(3.2·EVD-050·059)을 단정한다.
func assertZeroRateResult(t *testing.T, got evidence.RateResult, what string) {
	t.Helper()
	if got.PerSecond != 0 || !got.Start.IsZero() || !got.End.IsZero() || got.Samples != 0 || got.Discontinuities != 0 {
		t.Errorf("%s: 오류 시 zero RateResult를 기대했으나 %+v를 받았다", what, got)
	}
}

// assertObservedAts는 시리즈의 Observations()가 기대한 instant 열과 같은지
// 단정한다 (EVD-025·026·027·030).
func assertObservedAts(t *testing.T, s evidence.Series, want []time.Time, what string) {
	t.Helper()
	got := s.Observations()
	if len(got) != len(want) {
		t.Fatalf("%s: Observations() 길이 %d, want %d (got=%s)", what, len(got), len(want), evdObservedAtList(got))
	}
	for i := range got {
		if !got[i].ObservedAt.Equal(want[i]) {
			t.Errorf("%s: Observations()[%d].ObservedAt = %s, want %s (전체 %s)",
				what, i, evdTimeString(got[i].ObservedAt), evdTimeString(want[i]), evdObservedAtList(got))
		}
	}
}

func evdObservedAtList(obs []model.Observation) string {
	parts := make([]string, 0, len(obs))
	for _, o := range obs {
		parts = append(parts, evdTimeString(o.ObservedAt))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// evdSeriesKeys는 SeriesFor 결과를 순서 보존 문자열 목록으로 만든다 (EVD-030).
func evdSeriesKeys(series []evidence.Series) []string {
	out := make([]string, 0, len(series))
	for _, s := range series {
		src := s.Source()
		out = append(out, src.Type+"/"+src.Name+"/"+evdDimensionsString(s.Dimensions()))
	}
	return out
}

func assertStringsEqual(t *testing.T, got, want []string, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: 길이 %d, want %d\n got=%v\nwant=%v", what, len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s: [%d] = %q, want %q\n got=%v\nwant=%v", what, i, got[i], want[i], got, want)
		}
	}
}
