package identity

import (
	"strings"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// ParseCanonical reverses the canonical rendering "<namespace>:<value>" that
// model.TypedID.String produces, returning a normalized identity: one carrying
// nothing but Namespace and Value.
//
// The split is at the first colon, so that a value keeps the colons of its own
// that identifiers such as PCI addresses are full of: "pci-bdf:0000:af:00.0"
// parses as namespace "pci-bdf" and value "0000:af:00.0", and "ns::v" as
// namespace "ns" and value ":v". A string with no colon at all — a display name
// such as "gpu-node-17" — or with an empty namespace or value, is not a
// canonical rendering and is rejected.
//
// The result renders back to the string it was parsed from, and a namespace it
// yields never contains a colon, so it is always usable as an identity here.
func ParseCanonical(s string) (model.TypedID, error) {
	namespace, value, found := strings.Cut(s, ":")
	if !found {
		return model.TypedID{}, invalidf("%q is not of the form <namespace>:<value>", s)
	}
	if namespace == "" {
		return model.TypedID{}, invalidf("%q has an empty namespace", s)
	}
	if value == "" {
		return model.TypedID{}, invalidf("%q has an empty value", s)
	}
	return model.TypedID{Namespace: namespace, Value: value}, nil
}

// identityKey is an identity reduced to what identity comparison looks at:
// namespace and value, byte for byte. It is what keys the resolver's maps,
// where a model.TypedID cannot go — a slice-valued Raw makes that type
// uncomparable, and Source must take no part in equality anyway.
type identityKey struct {
	namespace string
	value     string
}

// keyOf reduces an identity to its key, dropping Raw and Source.
func keyOf(id model.TypedID) identityKey {
	return identityKey{namespace: id.Namespace, value: id.Value}
}

// typedID renders the key as a normalized identity: Namespace and Value only,
// with no Raw and no Source.
func (k identityKey) typedID() model.TypedID {
	return model.TypedID{Namespace: k.namespace, Value: k.value}
}

// String renders the key the way the model renders the identity it stands for.
func (k identityKey) String() string {
	return k.typedID().String()
}

// checkIdentity reports whether id may be used as an identity here: valid by
// the model's own rules, and with a colon-free namespace so that the canonical
// rendering can be split back apart at the first colon. role names the argument
// in the error message.
func checkIdentity(role string, id model.TypedID) error {
	if err := id.Validate(); err != nil {
		return invalidf("%s: %s", role, err)
	}
	if strings.Contains(id.Namespace, ":") {
		return invalidf("%s: namespace %q contains a colon", role, id.Namespace)
	}
	return nil
}
