package identity

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// asset is one registered asset: what it is, the identity it is canonically
// known by, and the other identities bound to it.
type asset struct {
	kind      model.AssetKind
	canonical identityKey
	aliases   map[identityKey]struct{}
}

// ref renders the asset as the reference every lookup returns: the registered
// kind, the canonical rendering, and every alias normalized and ordered by its
// own rendering, bytes ascending. The canonical identity is not among the
// aliases — it is not an alias of itself.
//
// The slice is built afresh on every call, so nothing a caller does to a
// returned reference can reach back into the resolver. The result satisfies
// model.AssetRef.Validate: registration established the kind and both parts of
// every identity, which is all that Validate looks at.
func (a *asset) ref() model.AssetRef {
	aliases := make([]model.TypedID, 0, len(a.aliases))
	for key := range a.aliases {
		aliases = append(aliases, key.typedID())
	}
	slices.SortFunc(aliases, func(x, y model.TypedID) int {
		return strings.Compare(x.String(), y.String())
	})
	return model.AssetRef{
		Kind:      a.kind,
		Canonical: a.canonical.String(),
		Aliases:   aliases,
	}
}

// claimedRef renders what a rejected registration asked for: the kind it
// claimed, under the identity it claimed, and no aliases, since a registration
// that never took effect brought none.
func claimedRef(kind model.AssetKind, id identityKey) model.AssetRef {
	return model.AssetRef{
		Kind:      kind,
		Canonical: id.String(),
		Aliases:   []model.TypedID{},
	}
}

// Resolver owns the identity registrations and answers lookups against them. It
// is where "one identity, at most one asset" is enforced: an operation that
// would break the invariant fails and changes nothing.
//
// A Resolver is not safe for concurrent use. Serializing access is the caller's
// business — in this system, the single-writer reducer's.
type Resolver struct {
	// assets holds every registered asset, keyed by its canonical identity.
	// Membership here is what distinguishes a canonical identity from an alias.
	assets map[identityKey]*asset
	// boundTo maps every bound identity, canonical or alias alike, to the asset
	// it names. It turns the invariant into a single lookup.
	boundTo map[identityKey]*asset
}

// NewResolver returns an empty Resolver. A Resolver must be obtained here; what
// the zero value does is not part of the contract.
func NewResolver() *Resolver {
	return &Resolver{
		assets:  make(map[identityKey]*asset),
		boundTo: make(map[identityKey]*asset),
	}
}

// RegisterAsset registers an asset of the given kind under the identity it is
// canonically known by.
//
// Registering the same kind under the same identity again does nothing and
// succeeds. Any other second claim on an identity that already resolves is a
// conflict, reported as a *Conflict with the existing binding left standing: a
// different kind under a canonical identity already taken, or an identity
// already bound as some asset's alias. The latter holds even when the kinds
// agree, because promoting an alias to a canonical identity is a merge of two
// assets by another name.
func (r *Resolver) RegisterAsset(kind model.AssetKind, canonical model.TypedID) error {
	if !kind.IsValid() {
		return invalidf("RegisterAsset: kind %s is not an asset kind", kind)
	}
	if err := checkIdentity("RegisterAsset canonical identity", canonical); err != nil {
		return err
	}
	key := keyOf(canonical)
	if bound, ok := r.boundTo[key]; ok {
		if bound.canonical == key && bound.kind == kind {
			return nil
		}
		return &Conflict{
			ID:       key.typedID(),
			Existing: bound.ref(),
			Claimed:  claimedRef(kind, key),
		}
	}
	registered := &asset{
		kind:      kind,
		canonical: key,
		aliases:   make(map[identityKey]struct{}),
	}
	r.assets[key] = registered
	r.boundTo[key] = registered
	return nil
}

// RegisterAlias binds alias to the asset registered under canonical.
//
// canonical must be an asset's canonical identity. Reaching an asset through
// one of its aliases is refused, because a chain of aliases would then decide
// what an asset is. Binding an identity that already resolves to the same asset
// does nothing and succeeds — that includes the asset's own canonical identity,
// which never joins the aliases. Binding one that resolves to a different asset
// is a conflict, reported as a *Conflict with the existing binding left
// standing; repeating the call reports the same conflict again.
func (r *Resolver) RegisterAlias(canonical, alias model.TypedID) error {
	if err := checkIdentity("RegisterAlias canonical identity", canonical); err != nil {
		return err
	}
	if err := checkIdentity("RegisterAlias alias", alias); err != nil {
		return err
	}
	canonicalKey := keyOf(canonical)
	claimant, ok := r.assets[canonicalKey]
	if !ok {
		return fmt.Errorf("%w: RegisterAlias: %s is not the canonical identity of a registered asset",
			ErrNotRegistered, canonicalKey)
	}
	aliasKey := keyOf(alias)
	if bound, ok := r.boundTo[aliasKey]; ok {
		if bound == claimant {
			return nil
		}
		return &Conflict{
			ID:       aliasKey.typedID(),
			Existing: bound.ref(),
			Claimed:  claimant.ref(),
		}
	}
	claimant.aliases[aliasKey] = struct{}{}
	r.boundTo[aliasKey] = claimant
	return nil
}

// Resolve returns a reference to the asset id names, whether id is that asset's
// canonical identity or one of its aliases. Raw and Source are ignored here as
// they are wherever identities are compared.
func (r *Resolver) Resolve(id model.TypedID) (model.AssetRef, error) {
	if err := checkIdentity("Resolve", id); err != nil {
		return model.AssetRef{}, err
	}
	key := keyOf(id)
	bound, ok := r.boundTo[key]
	if !ok {
		return model.AssetRef{}, fmt.Errorf("%w: Resolve: %s resolves to no asset", ErrNotRegistered, key)
	}
	return bound.ref(), nil
}

// ResolveAll resolves a set of identities asserted to name one and the same
// asset, and returns that asset. Repeats are fine, and canonical identities may
// be mixed with aliases; a set of one behaves exactly as Resolve does.
//
// An empty set says nothing, and is an argument violation. Otherwise every
// identity is checked before any is resolved and every one is resolved before
// the results are compared, so that a set which is both unresolvable and
// self-contradicting reports the missing registration rather than the
// disagreement.
func (r *Resolver) ResolveAll(ids []model.TypedID) (model.AssetRef, error) {
	if len(ids) == 0 {
		return model.AssetRef{}, invalidf("ResolveAll: no identities given")
	}
	for i, id := range ids {
		if err := checkIdentity(fmt.Sprintf("ResolveAll ids[%d]", i), id); err != nil {
			return model.AssetRef{}, err
		}
	}
	for i, id := range ids {
		if _, ok := r.boundTo[keyOf(id)]; !ok {
			return model.AssetRef{}, fmt.Errorf("%w: ResolveAll: ids[%d] %s resolves to no asset",
				ErrNotRegistered, i, keyOf(id))
		}
	}
	first := r.boundTo[keyOf(ids[0])]
	for i, id := range ids {
		if bound := r.boundTo[keyOf(id)]; bound != first {
			return model.AssetRef{}, fmt.Errorf("%w: ResolveAll: ids[0] %s names %s but ids[%d] %s names %s",
				ErrAmbiguous, keyOf(ids[0]), first.canonical, i, keyOf(id), bound.canonical)
		}
	}
	return first.ref(), nil
}

// Assets returns a reference to every registered asset, ordered by Key, bytes
// ascending. Distinct assets have distinct canonical identities and so distinct
// keys, which leaves no ties for the order to be arbitrary about. An empty
// resolver returns an empty slice.
func (r *Resolver) Assets() []model.AssetRef {
	refs := make([]model.AssetRef, 0, len(r.assets))
	for _, registered := range r.assets {
		refs = append(refs, registered.ref())
	}
	slices.SortFunc(refs, func(x, y model.AssetRef) int {
		return strings.Compare(x.Key(), y.Key())
	})
	return refs
}
