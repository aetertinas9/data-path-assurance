package model

import "strings"

// Well-known identity namespaces. The set is open: any non-empty namespace is
// accepted, and these constants only name the ones v0.1 adapters agree on.
const (
	NamespacePCIBDF              = "pci-bdf"
	NamespaceLLDPChassisID       = "lldp-chassis-id"
	NamespaceLLDPPortID          = "lldp-port-id"
	NamespaceOpenConfigComponent = "openconfig-component"
	NamespaceKubernetesNodeUID   = "kubernetes-node-uid"
	NamespaceKubernetesPodUID    = "kubernetes-pod-uid"
)

// TypedID is a namespaced identifier. It is the only way to name an asset:
// there is deliberately no API that turns a display name (an interface alias,
// a hostname) into an identity.
//
// Raw keeps the bytes the identifier was decoded from, and Source records which
// collector produced it; both are optional and neither participates in
// equality.
type TypedID struct {
	Namespace string
	Value     string
	Raw       []byte
	Source    string
}

// NewTypedID returns a TypedID for the given namespace and value.
//
// Raw and Source are not settable here; a value that carries them is assembled
// as a struct literal and re-checked with Validate.
func NewTypedID(namespace, value string) (TypedID, error) {
	id := TypedID{Namespace: namespace, Value: value}
	if err := id.Validate(); err != nil {
		return TypedID{}, err
	}
	return id, nil
}

// Validate reports whether the identifier carries both of its required parts.
func (t TypedID) Validate() error {
	if t.Namespace == "" {
		return invalidf("TypedID.Namespace is empty")
	}
	if t.Value == "" {
		return invalidf("TypedID.Value is empty")
	}
	return nil
}

// String renders the identifier as "<Namespace>:<Value>", for example
// "pci-bdf:0000:af:00.0".
func (t TypedID) String() string {
	return t.Namespace + ":" + t.Value
}

// Equal compares identity only: two TypedIDs with the same Namespace and Value
// are equal however they were decoded and whoever reported them.
func (t TypedID) Equal(other TypedID) bool {
	return t.Namespace == other.Namespace && t.Value == other.Value
}

func (t TypedID) clone() TypedID {
	c := t
	c.Raw = cloneBytes(t.Raw)
	return c
}

// isTypedIDString recognizes a rendering of nonempty Namespace and Value.
// Either part may contain colons, so the rendering need not have a unique
// split; any colon with nonempty strings on both sides is sufficient.
func isTypedIDString(s string) bool {
	return len(s) >= 3 && strings.Contains(s[1:len(s)-1], ":")
}

// AssetKind classifies what an asset is. The zero value is not a kind.
type AssetKind int

// The asset kinds v0.1 models, from site down to container.
const (
	KindSite AssetKind = iota + 1
	KindRow
	KindRack
	KindKubernetesNode
	KindPCIeRootPort
	KindPCIeSwitch
	KindPCIeFunction
	KindNICPort
	KindVF
	KindEthernetSwitch
	KindSwitchPort
	KindTransceiver
	KindPhysicalLink
	KindPod
	KindContainer
)

var assetKindNames = []string{
	"Site",
	"Row",
	"Rack",
	"KubernetesNode",
	"PCIeRootPort",
	"PCIeSwitch",
	"PCIeFunction",
	"NICPort",
	"VF",
	"EthernetSwitch",
	"SwitchPort",
	"Transceiver",
	"PhysicalLink",
	"Pod",
	"Container",
}

// String returns the kind's name without the "Kind" prefix, e.g. "PCIeRootPort".
func (k AssetKind) String() string {
	return enumName("AssetKind", int(k), int(KindSite), assetKindNames)
}

// IsValid reports whether k is one of the enumerated kinds. The zero value is
// not.
func (k AssetKind) IsValid() bool {
	return enumInRange(int(k), int(KindSite), len(assetKindNames))
}

// AssetRef points at one asset. Canonical is the string form of the asset's
// chosen canonical TypedID; Aliases are the other identifiers the same asset is
// known by.
type AssetRef struct {
	Kind      AssetKind
	Canonical string
	Aliases   []TypedID
}

// NewAssetRef builds a reference to an asset of the given kind, identified by
// canonical and additionally known by aliases. The reference stores
// canonical.String().
func NewAssetRef(kind AssetKind, canonical TypedID, aliases ...TypedID) (AssetRef, error) {
	if !kind.IsValid() {
		return AssetRef{}, invalidf("AssetRef.Kind %s is not an asset kind", kind)
	}
	if err := canonical.Validate(); err != nil {
		return AssetRef{}, invalidf("AssetRef canonical identifier: %s", err)
	}
	for i, alias := range aliases {
		if err := alias.Validate(); err != nil {
			return AssetRef{}, invalidf("AssetRef.Aliases[%d]: %s", i, err)
		}
	}
	return AssetRef{
		Kind:      kind,
		Canonical: canonical.String(),
		Aliases:   cloneTypedIDs(aliases),
	}, nil
}

// Validate reports whether the kind is enumerated, Canonical is the rendering
// of a valid TypedID, and every alias is itself valid.
func (a AssetRef) Validate() error {
	if !a.Kind.IsValid() {
		return invalidf("AssetRef.Kind %s is not an asset kind", a.Kind)
	}
	if !isTypedIDString(a.Canonical) {
		return invalidf("AssetRef.Canonical %q is not of the form <namespace>:<value>", a.Canonical)
	}
	for i, alias := range a.Aliases {
		if err := alias.Validate(); err != nil {
			return invalidf("AssetRef.Aliases[%d]: %s", i, err)
		}
	}
	return nil
}

// Key renders the reference as "<Kind>/<Canonical>". Two references to the same
// kind and canonical identifier share a key; references differing in either do
// not.
func (a AssetRef) Key() string {
	return a.Kind.String() + "/" + a.Canonical
}

func (a AssetRef) clone() AssetRef {
	c := a
	c.Aliases = cloneTypedIDs(a.Aliases)
	return c
}
