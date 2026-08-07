package graph

import "github.com/aetertinas9/data-path-assurance/pkg/model"

// The helpers below implement the defensive copying this package performs at
// both ends: a constructor copies what it is handed, and an accessor, a lookup
// or a transition copies what it hands out. Between the two, values are stored
// as they are and never mutated, which is what lets a state and the snapshots
// derived from it share them.
//
// Unlike the model's copiers, the ones returning a collection never return nil:
// a collection handed out is a collection, empty or not, and nil-ness is not
// something a caller is asked to read meaning into.

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// cloneTypedIDs copies the aliases and the raw bytes each of them kept. A nil
// slice is copied as nil, so that an asset reference survives the round trip
// looking exactly as it did.
func cloneTypedIDs(ids []model.TypedID) []model.TypedID {
	if ids == nil {
		return nil
	}
	c := make([]model.TypedID, len(ids))
	for i, id := range ids {
		c[i] = id
		c[i].Raw = cloneBytes(id.Raw)
	}
	return c
}

func cloneAssetRef(a model.AssetRef) model.AssetRef {
	c := a
	c.Aliases = cloneTypedIDs(a.Aliases)
	return c
}

// cloneAssetRefs copies every reference in refs. The result is never nil.
func cloneAssetRefs(refs []model.AssetRef) []model.AssetRef {
	c := make([]model.AssetRef, len(refs))
	for i, r := range refs {
		c[i] = cloneAssetRef(r)
	}
	return c
}

// cloneEdges copies every edge in edges, endpoints and all. The result is never
// nil.
func cloneEdges(edges []Edge) []Edge {
	c := make([]Edge, len(edges))
	for i, e := range edges {
		c[i] = e.clone()
	}
	return c
}

func cloneObservation(o model.Observation) model.Observation {
	c := o
	c.Subject = cloneAssetRef(o.Subject)
	c.Dimensions = cloneStringMap(o.Dimensions)
	return c
}
