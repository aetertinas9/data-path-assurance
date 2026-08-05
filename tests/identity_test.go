package tests

import (
	"reflect"
	"testing"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// MDL-010: 빈 Namespace 또는 빈 Value로 NewTypedID를 호출하면 ErrInvalid.
func TestMDL010_NewTypedIDRejectsEmptyNamespaceOrValue(t *testing.T) {
	cases := []struct {
		name      string
		namespace string
		value     string
	}{
		{"빈 namespace", "", "0000:af:00.0"},
		{"빈 value", "pci-bdf", ""},
		{"둘 다 빔", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := model.NewTypedID(tc.namespace, tc.value)
			requireErrInvalid(t, err, "NewTypedID")
			// MDL-003: 오류 시 zero value를 반환한다.
			if !reflect.DeepEqual(got, model.TypedID{}) {
				t.Errorf("오류 시 zero TypedID를 기대했으나 %#v를 받았다", got)
			}
		})
	}
}

// MDL-010(역): 비어 있지 않은 namespace·value는 받아들인다 (namespace는 열린 집합).
func TestMDL010_NewTypedIDAcceptsNonEmptyPair(t *testing.T) {
	cases := []struct {
		name      string
		namespace string
		value     string
	}{
		{"pci-bdf 상수", string(model.NamespacePCIBDF), canonicalValue},
		{"lldp-chassis-id 상수", string(model.NamespaceLLDPChassisID), "aa:bb:cc:dd:ee:ff"},
		{"openconfig-component 상수", string(model.NamespaceOpenConfigComponent), "Ethernet1/1"},
		{"kubernetes-node-uid 상수", string(model.NamespaceKubernetesNodeUID), "0f2a-node-uid"},
		{"열린 집합 - 상수 밖의 namespace", "vendor-private-ns", "some-value"},
		// 3절 서두(v1.2): "비어 있을 수 없다"는 길이 0 금지 — 공백만인 문자열은 유효.
		{"공백 한 칸도 비어 있지 않다", " ", " "},
		{"탭·개행도 비어 있지 않다", "\t", "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := model.NewTypedID(tc.namespace, tc.value)
			requireNoErr(t, err, "NewTypedID")
			if got.Namespace != tc.namespace {
				t.Errorf("Namespace = %q, want %q", got.Namespace, tc.namespace)
			}
			if got.Value != tc.value {
				t.Errorf("Value = %q, want %q", got.Value, tc.value)
			}
			// MDL-005: 생성자가 반환한 값의 Validate()는 nil이다.
			if err := got.Validate(); err != nil {
				t.Errorf("생성자 반환값의 Validate() = %v, want nil", err)
			}
		})
	}
}

// MDL-011: String()은 "<Namespace>:<Value>"이다.
func TestMDL011_TypedIDString(t *testing.T) {
	cases := []struct {
		name      string
		namespace string
		value     string
		want      string
	}{
		{"스펙 예시", "pci-bdf", "0000:af:00.0", canonicalString},
		{"value에 콜론 없음", "kubernetes-pod-uid", "9f1c", "kubernetes-pod-uid:9f1c"},
		{"value에 슬래시", "lldp-port-id", "Ethernet1/1", "lldp-port-id:Ethernet1/1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := mustTypedID(t, tc.namespace, tc.value)
			if got := id.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// MDL-011: 손으로 조립한 TypedID(Raw·Source 포함)도 같은 문자열을 낸다 —
// String()은 Namespace·Value만으로 결정된다.
func TestMDL011_TypedIDStringIgnoresRawAndSource(t *testing.T) {
	id := model.TypedID{
		Namespace: "pci-bdf",
		Value:     canonicalValue,
		Raw:       []byte{0x00, 0xaf, 0x00},
		Source:    "sysfs",
	}
	if got := id.String(); got != canonicalString {
		t.Errorf("String() = %q, want %q", got, canonicalString)
	}
}

// MDL-012: Equal은 Namespace·Value만 비교하고 Raw·Source는 무시한다.
func TestMDL012_TypedIDEqualIgnoresRawAndSource(t *testing.T) {
	a := model.TypedID{Namespace: "pci-bdf", Value: canonicalValue, Raw: []byte("A"), Source: "sysfs"}
	b := model.TypedID{Namespace: "pci-bdf", Value: canonicalValue, Raw: []byte("B-다름"), Source: "gnmi"}
	c := model.TypedID{Namespace: "pci-bdf", Value: canonicalValue}

	if !a.Equal(b) {
		t.Errorf("Raw·Source만 다른 두 TypedID의 Equal이 참이어야 한다: %#v vs %#v", a, b)
	}
	if !b.Equal(a) {
		t.Errorf("Equal은 대칭이어야 한다")
	}
	if !a.Equal(c) {
		t.Errorf("Raw·Source가 비어 있어도 Equal이 참이어야 한다")
	}
	if !a.Equal(a) {
		t.Errorf("Equal은 반사적이어야 한다")
	}
}

// MDL-012: Namespace 또는 Value가 다르면 Equal은 거짓이다.
func TestMDL012_TypedIDEqualDistinguishesNamespaceAndValue(t *testing.T) {
	base := model.TypedID{Namespace: "pci-bdf", Value: canonicalValue}
	cases := []struct {
		name  string
		other model.TypedID
	}{
		{"namespace 다름", model.TypedID{Namespace: "lldp-port-id", Value: canonicalValue}},
		{"value 다름", model.TypedID{Namespace: "pci-bdf", Value: "0000:af:00.1"}},
		{"둘 다 다름", model.TypedID{Namespace: "lldp-port-id", Value: "Ethernet1/1"}},
		{"namespace와 value가 뒤바뀜", model.TypedID{Namespace: canonicalValue, Value: "pci-bdf"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if base.Equal(tc.other) {
				t.Errorf("Equal이 거짓이어야 한다: %#v vs %#v", base, tc.other)
			}
			if tc.other.Equal(base) {
				t.Errorf("Equal이 거짓이어야 한다 (대칭): %#v vs %#v", tc.other, base)
			}
		})
	}
}

// MDL-013: 무효한 Kind·canonical·alias는 ErrInvalid.
func TestMDL013_NewAssetRefRejectsInvalidInput(t *testing.T) {
	var zeroKind model.AssetKind
	valid := model.TypedID{Namespace: "pci-bdf", Value: canonicalValue}
	invalid := model.TypedID{Namespace: "", Value: ""}

	cases := []struct {
		name      string
		kind      model.AssetKind
		canonical model.TypedID
		aliases   []model.TypedID
	}{
		{"zero value Kind", zeroKind, valid, nil},
		{"canonical이 zero TypedID", model.KindNICPort, model.TypedID{}, nil},
		{"canonical의 Namespace가 빔", model.KindNICPort, model.TypedID{Value: canonicalValue}, nil},
		{"canonical의 Value가 빔", model.KindNICPort, model.TypedID{Namespace: "pci-bdf"}, nil},
		{"alias 하나가 무효", model.KindNICPort, valid, []model.TypedID{invalid}},
		{"유효 alias 뒤에 무효 alias", model.KindNICPort, valid, []model.TypedID{
			{Namespace: "lldp-port-id", Value: "Ethernet1/1"},
			{Namespace: "lldp-chassis-id", Value: ""},
		}},
		{"무효 alias 뒤에 유효 alias", model.KindNICPort, valid, []model.TypedID{
			{Namespace: "", Value: "Ethernet1/1"},
			{Namespace: "lldp-chassis-id", Value: "aa:bb"},
		}},
		{"Kind와 canonical 둘 다 무효", zeroKind, model.TypedID{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := model.NewAssetRef(tc.kind, tc.canonical, tc.aliases...)
			requireErrInvalid(t, err, "NewAssetRef")
			// MDL-003: 오류 시 zero value.
			if !reflect.DeepEqual(got, model.AssetRef{}) {
				t.Errorf("오류 시 zero AssetRef를 기대했으나 %#v를 받았다", got)
			}
		})
	}
}

// MDL-013(역) + 3.1: Canonical 필드에는 canonical.String()이 저장된다.
func TestMDL013_NewAssetRefStoresCanonicalString(t *testing.T) {
	canonical := canonicalTypedID(t)
	alias1 := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")
	alias2 := mustTypedID(t, string(model.NamespaceLLDPChassisID), "aa:bb:cc:dd:ee:ff")

	ref, err := model.NewAssetRef(model.KindNICPort, canonical, alias1, alias2)
	requireNoErr(t, err, "NewAssetRef")

	if ref.Kind != model.KindNICPort {
		t.Errorf("Kind = %v, want KindNICPort", ref.Kind.String())
	}
	if ref.Canonical != canonical.String() {
		t.Errorf("Canonical = %q, want %q", ref.Canonical, canonical.String())
	}
	if ref.Canonical != canonicalString {
		t.Errorf("Canonical = %q, want %q", ref.Canonical, canonicalString)
	}
	if len(ref.Aliases) != 2 {
		t.Fatalf("len(Aliases) = %d, want 2", len(ref.Aliases))
	}
	if !ref.Aliases[0].Equal(alias1) || !ref.Aliases[1].Equal(alias2) {
		t.Errorf("Aliases 내용이 보존되지 않았다: %#v", ref.Aliases)
	}
	// MDL-005: 생성자 반환값의 Validate()는 nil.
	if err := ref.Validate(); err != nil {
		t.Errorf("생성자 반환값의 Validate() = %v, want nil", err)
	}
}

// MDL-013(역): alias가 하나도 없어도 유효하다 (Aliases는 선택).
func TestMDL013_NewAssetRefAcceptsZeroAliases(t *testing.T) {
	ref, err := model.NewAssetRef(model.KindKubernetesNode,
		mustTypedID(t, string(model.NamespaceKubernetesNodeUID), "0f2a-node-uid"))
	requireNoErr(t, err, "NewAssetRef(무 alias)")
	if len(ref.Aliases) != 0 {
		t.Errorf("len(Aliases) = %d, want 0", len(ref.Aliases))
	}
	if err := ref.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// MDL-013(역): v0.1의 모든 AssetKind로 AssetRef를 만들 수 있다.
func TestMDL013_NewAssetRefAcceptsEveryV01Kind(t *testing.T) {
	kinds := []model.AssetKind{
		model.KindSite, model.KindRow, model.KindRack, model.KindKubernetesNode,
		model.KindPCIeRootPort, model.KindPCIeSwitch, model.KindPCIeFunction,
		model.KindNICPort, model.KindVF, model.KindEthernetSwitch, model.KindSwitchPort,
		model.KindTransceiver, model.KindPhysicalLink, model.KindPod, model.KindContainer,
	}
	canonical := canonicalTypedID(t)
	for _, kind := range kinds {
		t.Run(kind.String(), func(t *testing.T) {
			ref, err := model.NewAssetRef(kind, canonical)
			requireNoErr(t, err, "NewAssetRef("+kind.String()+")")
			if ref.Kind != kind {
				t.Errorf("Kind = %q, want %q", ref.Kind.String(), kind.String())
			}
		})
	}
}

// MDL-014: Key()는 "<Kind>/<Canonical>"이다.
func TestMDL014_AssetRefKeyFormat(t *testing.T) {
	ref := mustAssetRef(t, model.KindNICPort, canonicalTypedID(t))
	want := "NICPort/" + canonicalString
	if got := ref.Key(); got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
}

// MDL-014: 같은 Kind·canonical이면 같은 Key, 다르면 다른 Key.
func TestMDL014_AssetRefKeyIdentity(t *testing.T) {
	canonical := canonicalTypedID(t)
	other := mustTypedID(t, string(model.NamespacePCIBDF), "0000:af:00.1")

	a := mustAssetRef(t, model.KindNICPort, canonical)
	sameKindSameCanonical := mustAssetRef(t, model.KindNICPort, canonical)
	differentKind := mustAssetRef(t, model.KindSwitchPort, canonical)
	differentCanonical := mustAssetRef(t, model.KindNICPort, other)

	if a.Key() != sameKindSameCanonical.Key() {
		t.Errorf("같은 Kind·canonical의 Key가 달랐다: %q vs %q", a.Key(), sameKindSameCanonical.Key())
	}
	if a.Key() == differentKind.Key() {
		t.Errorf("Kind가 다른데 Key가 같았다: %q", a.Key())
	}
	if a.Key() == differentCanonical.Key() {
		t.Errorf("canonical이 다른데 Key가 같았다: %q", a.Key())
	}
	if differentKind.Key() == differentCanonical.Key() {
		t.Errorf("서로 다른 AssetRef의 Key가 같았다: %q", differentKind.Key())
	}
}

// MDL-014: Alias 집합이 달라도 Kind·Canonical이 같으면 Key는 같다.
func TestMDL014_AssetRefKeyIgnoresAliases(t *testing.T) {
	canonical := canonicalTypedID(t)
	alias := mustTypedID(t, string(model.NamespaceLLDPPortID), "Ethernet1/1")

	withAlias := mustAssetRef(t, model.KindNICPort, canonical, alias)
	withoutAlias := mustAssetRef(t, model.KindNICPort, canonical)

	if withAlias.Key() != withoutAlias.Key() {
		t.Errorf("alias 유무로 Key가 달라졌다: %q vs %q", withAlias.Key(), withoutAlias.Key())
	}
}

// 3.1: namespace 상수 값 (열린 집합의 권장 값).
func TestSpec31_NamespaceConstantValues(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"NamespacePCIBDF", string(model.NamespacePCIBDF), "pci-bdf"},
		{"NamespaceLLDPChassisID", string(model.NamespaceLLDPChassisID), "lldp-chassis-id"},
		{"NamespaceLLDPPortID", string(model.NamespaceLLDPPortID), "lldp-port-id"},
		{"NamespaceOpenConfigComponent", string(model.NamespaceOpenConfigComponent), "openconfig-component"},
		{"NamespaceKubernetesNodeUID", string(model.NamespaceKubernetesNodeUID), "kubernetes-node-uid"},
		{"NamespaceKubernetesPodUID", string(model.NamespaceKubernetesPodUID), "kubernetes-pod-uid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}
