// identity_helpers_test.go — internal/identity 블랙박스 테스트의 공용 헬퍼·픽스처.
// specs/identity/spec.md (ratified v1.0) 3절의 공개 계약만 참조한다.
// (기존 identity_test.go는 pkg/model의 식별 타입(MDL-0xx) 테스트다 — 별개다.)
package tests

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// --- 픽스처 ---
//
// 값 자체에는 의미가 없다. 서로 다른 identity를 만들기 위한 것이며,
// 모두 3.1을 만족한다(Namespace에 콜론 없음, 양쪽 비어 있지 않음).

// idnCanonicalA는 canonicalTypedID와 같은 pci-bdf identity다.
func idnCanonicalA(t *testing.T) model.TypedID {
	t.Helper()
	return mustTypedID(t, string(model.NamespacePCIBDF), canonicalValue)
}

func idnCanonicalB(t *testing.T) model.TypedID {
	t.Helper()
	return mustTypedID(t, string(model.NamespacePCIBDF), "0000:af:00.1")
}

func idnCanonicalC(t *testing.T) model.TypedID {
	t.Helper()
	return mustTypedID(t, string(model.NamespaceKubernetesNodeUID), "0f2a-node-uid")
}

func idnAliasPort(t *testing.T) model.TypedID {
	t.Helper()
	return mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
}

func idnAliasChassis(t *testing.T) model.TypedID {
	t.Helper()
	return mustTypedID(t, string(model.NamespaceLLDPChassisID), "aa:bb:cc:dd:ee:ff")
}

func idnAliasPod(t *testing.T) model.TypedID {
	t.Helper()
	return mustTypedID(t, string(model.NamespaceKubernetesPodUID), "9f1c-pod-uid")
}

// idnUnregistered는 어떤 테스트에서도 등록하지 않는 유효한 identity다.
func idnUnregistered(t *testing.T) model.TypedID {
	t.Helper()
	return mustTypedID(t, string(model.NamespaceOpenConfigComponent), "Port-Channel99")
}

// idnCase는 identity 표 테스트의 한 줄이다.
type idnCase struct {
	name string
	id   model.TypedID
}

// invalidIdentities는 3.1의 유효성을 위반하는 TypedID 목록이다 —
// (a) model.TypedID.Validate()가 nil이 아니거나, (b) Namespace에 콜론이 있다.
// IDN-003·021·031·042·044(b)가 공유한다.
func invalidIdentities() []idnCase {
	return []idnCase{
		{"zero value", model.TypedID{}},
		{"Namespace가 빔", model.TypedID{Value: canonicalValue}},
		{"Value가 빔", model.TypedID{Namespace: string(model.NamespacePCIBDF)}},
		{"Namespace·Value 둘 다 빔 (Raw·Source만 있음)", model.TypedID{Raw: []byte{0x01}, Source: "sysfs"}},
		// 3.1 (b): 이 패키지만의 추가 제약 — model 계약으로는 유효한 값들이다.
		{"Namespace에 콜론", model.TypedID{Namespace: "pci:bdf", Value: canonicalValue}},
		{"Namespace가 콜론 하나", model.TypedID{Namespace: ":", Value: "v"}},
		{"Namespace가 콜론으로 끝남", model.TypedID{Namespace: "ns:", Value: "v"}},
		{"Namespace가 콜론으로 시작함", model.TypedID{Namespace: ":ns", Value: "v"}},
		{"Namespace·Value 둘 다 콜론 포함", model.TypedID{Namespace: "a:b", Value: "c:d"}},
	}
}

// --- 단정 헬퍼 ---

// assertNormalized는 3.1의 "정규화된 TypedID"(Raw nil, Source 빈 문자열)를 단정한다.
func assertNormalized(t *testing.T, id model.TypedID, what string) {
	t.Helper()
	if id.Raw != nil {
		t.Errorf("%s: 정규화된 TypedID의 Raw는 nil이어야 한다 (got %#v)", what, id.Raw)
	}
	if id.Source != "" {
		t.Errorf("%s: 정규화된 TypedID의 Source는 빈 문자열이어야 한다 (got %q)", what, id.Source)
	}
}

// aliasStrings는 AssetRef.Aliases를 String() 목록으로 만든다 (순서 보존).
func aliasStrings(ref model.AssetRef) []string {
	out := make([]string, 0, len(ref.Aliases))
	for _, a := range ref.Aliases {
		out = append(out, a.String())
	}
	return out
}

// assertRefShape는 3.4 "반환되는 model.AssetRef의 형태"를 통째로 단정한다.
// wantAliases는 String() 바이트 단위 오름차순으로 기대되는 alias 목록이다.
func assertRefShape(t *testing.T, ref model.AssetRef, wantKind model.AssetKind, wantCanonical model.TypedID, wantAliases []string, what string) {
	t.Helper()

	if ref.Kind != wantKind {
		t.Errorf("%s: Kind = %q, want %q", what, ref.Kind.String(), wantKind.String())
	}
	if ref.Canonical != wantCanonical.String() {
		t.Errorf("%s: Canonical = %q, want %q", what, ref.Canonical, wantCanonical.String())
	}

	got := aliasStrings(ref)
	if len(got) != len(wantAliases) {
		t.Fatalf("%s: Aliases = %v, want %v", what, got, wantAliases)
	}
	for i := range got {
		if got[i] != wantAliases[i] {
			t.Errorf("%s: Aliases[%d] = %q, want %q (전체 %v)", what, i, got[i], wantAliases[i], got)
		}
	}
	// 3.4: canonical identity 자신은 Aliases에 포함되지 않는다.
	for _, a := range got {
		if a == wantCanonical.String() {
			t.Errorf("%s: canonical identity %q가 Aliases에 들어 있다", what, a)
		}
	}
	// 3.4: Aliases는 String() 바이트 단위 오름차순으로 정렬되어 있다.
	if !sort.StringsAreSorted(got) {
		t.Errorf("%s: Aliases가 String() 오름차순이 아니다: %v", what, got)
	}
	// 3.1: 반환된 alias 원소는 정규화되어 있다.
	for i, a := range ref.Aliases {
		assertNormalized(t, a, fmt.Sprintf("%s Aliases[%d]", what, i))
	}
	// 3.4: 반환된 AssetRef의 Validate()는 nil이다.
	if err := ref.Validate(); err != nil {
		t.Errorf("%s: Validate() = %v, want nil", what, err)
	}
}

// assertSameRef는 두 AssetRef가 3.4 형태 기준으로 동일함을 단정한다 —
// Aliases는 순서까지 같아야 한다. Aliases의 nil/빈 슬라이스 구분은 계약이
// 아니므로(3.4) 길이 0끼리는 같은 것으로 본다.
func assertSameRef(t *testing.T, got, want model.AssetRef, what string) {
	t.Helper()

	if got.Kind != want.Kind || got.Canonical != want.Canonical {
		t.Errorf("%s: (Kind, Canonical) = (%q, %q), want (%q, %q)",
			what, got.Kind.String(), got.Canonical, want.Kind.String(), want.Canonical)
	}
	g, w := aliasStrings(got), aliasStrings(want)
	if len(g) != len(w) {
		t.Fatalf("%s: Aliases = %v, want %v", what, g, w)
	}
	for i := range g {
		if g[i] != w[i] {
			t.Errorf("%s: Aliases[%d] = %q, want %q", what, i, g[i], w[i])
		}
	}
}

// assertZeroRef는 3.3 "오류 시 값 반환 위치에는 zero value"를 단정한다.
func assertZeroRef(t *testing.T, ref model.AssetRef, what string) {
	t.Helper()
	var zero model.AssetRef
	if ref.Kind != zero.Kind || ref.Canonical != zero.Canonical || len(ref.Aliases) != 0 {
		t.Errorf("%s: 오류 시 zero AssetRef를 기대했으나 %#v를 받았다", what, ref)
	}
}

// assertZeroTypedID는 오류 시 zero TypedID 반환을 단정한다.
func assertZeroTypedID(t *testing.T, id model.TypedID, what string) {
	t.Helper()
	if id.Namespace != "" || id.Value != "" || id.Raw != nil || id.Source != "" {
		t.Errorf("%s: 오류 시 zero TypedID를 기대했으나 %#v를 받았다", what, id)
	}
}

// --- 오류 계열 헬퍼 (3.3) ---

func requireErrNotRegistered(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: ErrNotRegistered 오류를 기대했으나 nil을 받았다", what)
	}
	if !errors.Is(err, identity.ErrNotRegistered) {
		t.Fatalf("%s: errors.Is(err, identity.ErrNotRegistered)가 참이어야 한다 (err=%v)", what, err)
	}
}

func requireErrAmbiguous(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: ErrAmbiguous 오류를 기대했으나 nil을 받았다", what)
	}
	if !errors.Is(err, identity.ErrAmbiguous) {
		t.Fatalf("%s: errors.Is(err, identity.ErrAmbiguous)가 참이어야 한다 (err=%v)", what, err)
	}
}

// requireConflict는 3.3·IDN-070의 충돌 오류 계약을 통째로 검사하고 상세를 돌려준다.
func requireConflict(t *testing.T, err error, what string) *identity.Conflict {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: 충돌 오류를 기대했으나 nil을 받았다", what)
	}
	if !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("%s: errors.Is(err, identity.ErrConflict)가 참이어야 한다 (err=%v)", what, err)
	}
	var c *identity.Conflict
	if !errors.As(err, &c) {
		t.Fatalf("%s: errors.As(err, &c)(c *identity.Conflict)가 참이어야 한다 (err=%v)", what, err)
	}
	if c == nil {
		t.Fatalf("%s: errors.As가 nil *Conflict를 채웠다", what)
	}
	msg := err.Error()
	if msg == "" {
		t.Errorf("%s: 충돌 오류의 Error()는 비어 있지 않아야 한다", what)
	}
	if !strings.Contains(msg, c.ID.String()) {
		t.Errorf("%s: Error() = %q, ID.String() %q를 포함해야 한다", what, msg, c.ID.String())
	}
	// 3.3: Conflict.ID는 정규화된 identity다.
	assertNormalized(t, c.ID, what+"의 Conflict.ID")
	return c
}

// --- Resolver 조작 헬퍼 ---

func mustRegisterAsset(t *testing.T, r *identity.Resolver, kind model.AssetKind, canonical model.TypedID) {
	t.Helper()
	requireNoErr(t, r.RegisterAsset(kind, canonical),
		fmt.Sprintf("RegisterAsset(%s, %s)", kind.String(), canonical.String()))
}

func mustRegisterAlias(t *testing.T, r *identity.Resolver, canonical, alias model.TypedID) {
	t.Helper()
	requireNoErr(t, r.RegisterAlias(canonical, alias),
		fmt.Sprintf("RegisterAlias(%s, %s)", canonical.String(), alias.String()))
}

func mustResolve(t *testing.T, r *identity.Resolver, id model.TypedID) model.AssetRef {
	t.Helper()
	ref, err := r.Resolve(id)
	requireNoErr(t, err, "Resolve("+id.String()+")")
	return ref
}

// errClass는 오류를 스펙 3.3의 계열 이름으로 분류한다 (스냅샷 비교용).
func errClass(err error) string {
	switch {
	case err == nil:
		return "nil"
	case errors.Is(err, model.ErrInvalid):
		return "invalid"
	case errors.Is(err, identity.ErrNotRegistered):
		return "not-registered"
	case errors.Is(err, identity.ErrAmbiguous):
		return "ambiguous"
	case errors.Is(err, identity.ErrConflict):
		return "conflict"
	default:
		return "other"
	}
}

// resolverSnapshot은 Resolver의 관측 가능한 상태 전부(Assets()와 probe들의
// Resolve 결과)를 결정론적 문자열로 만든다. "상태를 바꾸지 않는다"(IDN-003·005·
// 021·022·031·032·033·034·035·044·052·053)를 한 줄로 단정하는 데 쓴다.
func resolverSnapshot(t *testing.T, r *identity.Resolver, probes ...model.TypedID) string {
	t.Helper()

	var b strings.Builder
	for _, ref := range r.Assets() {
		b.WriteString("asset\t")
		b.WriteString(ref.Key())
		for _, a := range ref.Aliases {
			b.WriteString("\talias=")
			b.WriteString(a.String())
			if a.Raw != nil {
				b.WriteString("\t(raw!=nil)")
			}
			if a.Source != "" {
				b.WriteString("\t(source=" + a.Source + ")")
			}
		}
		b.WriteString("\n")
	}
	for _, p := range probes {
		ref, err := r.Resolve(p)
		b.WriteString("resolve\t")
		b.WriteString(p.String())
		b.WriteString("\t")
		if err != nil {
			b.WriteString("err=" + errClass(err))
		} else {
			b.WriteString(ref.Key() + "\taliases=" + strings.Join(aliasStrings(ref), ","))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// assertSnapshotUnchanged는 연산 전후의 스냅샷이 같은지 단정한다.
func assertSnapshotUnchanged(t *testing.T, before, after string, what string) {
	t.Helper()
	if before != after {
		t.Errorf("%s: 상태가 변했다\n--- before ---\n%s--- after ---\n%s", what, before, after)
	}
}

// --- Conflict finding 픽스처 ---

// idnConflictEvidence는 IDN-060의 "Validate() nil인 EvidenceRef 1개 이상"을 만족한다.
func idnConflictEvidence() []model.EvidenceRef {
	return []model.EvidenceRef{
		{ObservationID: "obs-idn-0001", Summary: "같은 identity를 두 asset이 주장했다"},
	}
}

// idnValidConflict는 IDN-060의 "유효한 Conflict"다 —
// Existing·Claimed의 Validate()가 nil이고 ID가 유효하다.
func idnValidConflict(t *testing.T) identity.Conflict {
	t.Helper()
	disputed := idnCanonicalA(t)
	return identity.Conflict{
		ID:       disputed,
		Existing: mustAssetRef(t, model.KindNICPort, disputed, idnAliasPort(t)),
		Claimed:  mustAssetRef(t, model.KindPCIeFunction, disputed),
	}
}
