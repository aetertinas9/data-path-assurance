package evidence

import (
	"bytes"
	"maps"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// The helpers below implement the defensive copying this package does at both
// ends: on the way in, so that a caller mutating what it handed to Add cannot
// reach the window afterwards; on the way out, so that a caller mutating what a
// query returned cannot either. Copies are deep — down to an alias's Raw bytes
// — and a nil slice or map is copied as nil, so a copy cannot be told from its
// original by nil-ness alone.

func cloneObservation(o model.Observation) model.Observation {
	c := o
	c.Subject = cloneAssetRef(o.Subject)
	c.Dimensions = maps.Clone(o.Dimensions)
	return c
}

func cloneObservations(obs []model.Observation) []model.Observation {
	c := make([]model.Observation, len(obs))
	for i, o := range obs {
		c[i] = cloneObservation(o)
	}
	return c
}

func cloneAssetRef(a model.AssetRef) model.AssetRef {
	c := a
	c.Aliases = cloneTypedIDs(a.Aliases)
	return c
}

func cloneTypedIDs(ids []model.TypedID) []model.TypedID {
	if ids == nil {
		return nil
	}
	c := make([]model.TypedID, len(ids))
	for i, id := range ids {
		c[i] = id
		c[i].Raw = bytes.Clone(id.Raw)
	}
	return c
}
