// identity_parse_test.go — ParseCanonical (specs/identity/spec.md 3.2, IDN-010~014).
package tests

import (
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/identity"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// parseCase는 ParseCanonical이 수용해야 하는 한 줄이다.
type parseCase struct {
	name      string
	in        string
	namespace string
	value     string
}

// acceptedCanonicalStrings는 IDN-010·012가 수용을 요구하는 문자열 코퍼스다.
// IDN-013(문자열 왕복)도 같은 코퍼스를 쓴다.
func acceptedCanonicalStrings() []parseCase {
	return []parseCase{
		{"스펙 예시 (value에 콜론 3개)", canonicalString, "pci-bdf", canonicalValue},
		{"콜론 1개", "kubernetes-pod-uid:9f1c", "kubernetes-pod-uid", "9f1c"},
		{"value에 슬래시", "lldp-port-id:Ethernet1/1", "lldp-port-id", "Ethernet1/1"},
		{"value가 MAC 주소", "lldp-chassis-id:aa:bb:cc:dd:ee:ff", "lldp-chassis-id", "aa:bb:cc:dd:ee:ff"},
		{"value가 콜론으로 시작 (ns::v)", "ns::v", "ns", ":v"},
		{"value가 콜론으로 끝남", "ns:v:", "ns", "v:"},
		{"value가 콜론 하나", "ns::", "ns", ":"},
		{"value가 콜론 둘", "ns:::", "ns", "::"},
		{"한 글자씩", "a:b", "a", "b"},
		{"namespace가 공백 한 칸", " : ", " ", " "},
		{"value에 공백", "ns:a b", "ns", "a b"},
		{"value 앞뒤 공백 보존", "ns: a ", "ns", " a "},
		{"대문자 보존", "PCI-BDF:0000:AF:00.0", "PCI-BDF", "0000:AF:00.0"},
		{"비-ASCII", "한글:값:콜론", "한글", "값:콜론"},
		{"value에 탭", "ns:\t", "ns", "\t"},
	}
}

// IDN-010: "<namespace>:<value>" 형식을 첫 번째 콜론에서 분리해 정규화된
// TypedID(Raw nil, Source 빈 문자열)와 nil을 반환한다.
func TestIDN010_ParseCanonicalSplitsAtFirstColon(t *testing.T) {
	for _, tc := range acceptedCanonicalStrings() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := identity.ParseCanonical(tc.in)
			requireNoErr(t, err, "ParseCanonical("+tc.in+")")

			if got.Namespace != tc.namespace {
				t.Errorf("Namespace = %q, want %q", got.Namespace, tc.namespace)
			}
			if got.Value != tc.value {
				t.Errorf("Value = %q, want %q", got.Value, tc.value)
			}
			// 3.1: 결과는 정규화되어 있다.
			assertNormalized(t, got, "ParseCanonical 결과")
			// 결과는 유효한 TypedID다.
			requireNoErr(t, got.Validate(), "ParseCanonical 결과의 Validate()")
		})
	}
}

// IDN-011: 빈 문자열·콜론 없는 문자열·":"·":value"·"namespace:"는
// zero TypedID와 model.ErrInvalid 계열 오류다.
func TestIDN011_ParseCanonicalRejectsMalformedStrings(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"빈 문자열", ""},
		{"콜론 없는 표시 이름", "gpu-node-17"},
		{"콜론 없는 문자열 (숫자)", "0000af000"},
		{"콜론뿐", ":"},
		{"namespace가 빔", ":value"},
		{"namespace가 빔 (value에 콜론)", ":0000:af:00.0"},
		{"value가 빔", "namespace:"},
		{"value가 빔 (스펙 예시 namespace)", "pci-bdf:"},
		{"공백만 (콜론 없음)", "   "},
		{"콜론 없는 비-ASCII", "한글"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := identity.ParseCanonical(tc.in)
			requireErrInvalid(t, err, "ParseCanonical("+tc.in+")")
			assertZeroTypedID(t, got, "ParseCanonical("+tc.in+")")
		})
	}
}

// IDN-012 (edge): 구분자가 value에 포함되어도 첫 콜론 뒤 전체가 Value다.
func TestIDN012_ParseCanonicalKeepsColonsInValue(t *testing.T) {
	t.Run("스펙 예시 pci-bdf", func(t *testing.T) {
		got, err := identity.ParseCanonical("pci-bdf:0000:af:00.0")
		requireNoErr(t, err, "ParseCanonical")
		if got.Namespace != "pci-bdf" {
			t.Errorf("Namespace = %q, want %q", got.Namespace, "pci-bdf")
		}
		if got.Value != "0000:af:00.0" {
			t.Errorf("Value = %q, want %q", got.Value, "0000:af:00.0")
		}
	})

	t.Run("콜론으로 시작하는 value ns::v", func(t *testing.T) {
		got, err := identity.ParseCanonical("ns::v")
		requireNoErr(t, err, "ParseCanonical")
		if got.Namespace != "ns" {
			t.Errorf("Namespace = %q, want %q", got.Namespace, "ns")
		}
		if got.Value != ":v" {
			t.Errorf("Value = %q, want %q", got.Value, ":v")
		}
	})

	// 첫 콜론에서만 분리하므로, 콜론 개수가 늘어도 Namespace는 변하지 않는다.
	t.Run("콜론이 늘어도 namespace는 그대로", func(t *testing.T) {
		for _, in := range []string{"ns:v", "ns:v:w", "ns:v:w:x", "ns::::"} {
			got, err := identity.ParseCanonical(in)
			requireNoErr(t, err, "ParseCanonical("+in+")")
			if got.Namespace != "ns" {
				t.Errorf("ParseCanonical(%q).Namespace = %q, want %q", in, got.Namespace, "ns")
			}
			if want := in[len("ns:"):]; got.Value != want {
				t.Errorf("ParseCanonical(%q).Value = %q, want %q", in, got.Value, want)
			}
		}
	})
}

// IDN-013 (edge, 문자열 왕복): ParseCanonical이 수용한 모든 s에 대해
// parsed.String() == s.
func TestIDN013_ParseCanonicalRoundTripsString(t *testing.T) {
	for _, tc := range acceptedCanonicalStrings() {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := identity.ParseCanonical(tc.in)
			requireNoErr(t, err, "ParseCanonical("+tc.in+")")
			if got := parsed.String(); got != tc.in {
				t.Errorf("parsed.String() = %q, want %q", got, tc.in)
			}
		})
	}
}

// IDN-013 (edge, TypedID 왕복): Namespace에 콜론이 없는 유효한 TypedID t에 대해
// ParseCanonical(t.String())은 t와 Equal이다.
func TestIDN013_ParseCanonicalRoundTripsTypedID(t *testing.T) {
	cases := []struct {
		name      string
		namespace string
		value     string
	}{
		{"pci-bdf 예시", string(model.NamespacePCIBDF), canonicalValue},
		{"콜론 없는 value", string(model.NamespaceKubernetesPodUID), "9f1c-pod-uid"},
		{"value가 콜론으로 시작", "ns", ":v"},
		{"value가 콜론 하나", "ns", ":"},
		{"value가 콜론으로 끝남", "ns", "v:"},
		{"공백만인 namespace·value", " ", " "},
		{"탭·개행", "\t", "\n"},
		{"대문자", "PCI-BDF", "0000:AF:00.0"},
		{"비-ASCII", "한글", "값:콜론"},
		{"value에 슬래시", string(model.NamespaceLLDPPortID), "Ethernet1/1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := mustTypedID(t, tc.namespace, tc.value)

			parsed, err := identity.ParseCanonical(original.String())
			requireNoErr(t, err, "ParseCanonical("+original.String()+")")

			if !parsed.Equal(original) {
				t.Errorf("왕복 후 Equal이 거짓이다: %#v vs %#v", parsed, original)
			}
			if !original.Equal(parsed) {
				t.Errorf("왕복 후 Equal이 대칭이 아니다: %#v vs %#v", original, parsed)
			}
			assertNormalized(t, parsed, "왕복 결과")
		})
	}
}

// IDN-013 (edge): Raw·Source가 채워진 TypedID도 왕복하면 같은 identity가 된다 —
// String()이 Namespace·Value만으로 결정되므로 왕복 결과는 정규화된 값이다.
func TestIDN013_RoundTripDropsRawAndSource(t *testing.T) {
	original := model.TypedID{
		Namespace: string(model.NamespaceLLDPChassisID),
		Value:     "aa:bb:cc:dd:ee:ff",
		Raw:       []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff},
		Source:    "lldp",
	}
	requireNoErr(t, original.Validate(), "픽스처의 Validate()")

	parsed, err := identity.ParseCanonical(original.String())
	requireNoErr(t, err, "ParseCanonical")

	if !parsed.Equal(original) {
		t.Errorf("Raw·Source가 있어도 Equal이어야 한다: %#v vs %#v", parsed, original)
	}
	assertNormalized(t, parsed, "왕복 결과")
}

// IDN-014: Resolver가 반환한 AssetRef의 Canonical은 다시 파싱되고, 그 결과를
// Resolve하면 같은 asset(같은 Key())이 나온다.
func TestIDN014_ResolverCanonicalIsReparsable(t *testing.T) {
	r := identity.NewResolver()

	cA, cB, cC := idnCanonicalA(t), idnCanonicalB(t), idnCanonicalC(t)
	mustRegisterAsset(t, r, model.KindNICPort, cA)
	mustRegisterAsset(t, r, model.KindPCIeFunction, cB)
	mustRegisterAsset(t, r, model.KindKubernetesNode, cC)
	mustRegisterAlias(t, r, cA, idnAliasPort(t))
	mustRegisterAlias(t, r, cA, idnAliasChassis(t))

	refs := r.Assets()
	if len(refs) != 3 {
		t.Fatalf("len(Assets()) = %d, want 3", len(refs))
	}
	for _, ref := range refs {
		t.Run(ref.Key(), func(t *testing.T) {
			parsed, err := identity.ParseCanonical(ref.Canonical)
			requireNoErr(t, err, "ParseCanonical("+ref.Canonical+")")
			assertNormalized(t, parsed, "ParseCanonical 결과")

			got, err := r.Resolve(parsed)
			requireNoErr(t, err, "Resolve(ParseCanonical 결과)")
			if got.Key() != ref.Key() {
				t.Errorf("Key() = %q, want %q", got.Key(), ref.Key())
			}
			assertSameRef(t, got, ref, "왕복 해석 결과")
		})
	}

	// alias로 얻은 AssetRef의 Canonical도 같은 성질을 가진다.
	viaAlias := mustResolve(t, r, idnAliasPort(t))
	parsed, err := identity.ParseCanonical(viaAlias.Canonical)
	requireNoErr(t, err, "ParseCanonical(alias 경유 Canonical)")
	if !parsed.Equal(cA) {
		t.Errorf("alias 경유 Canonical의 파싱 결과 = %#v, want %#v", parsed, cA)
	}
}
