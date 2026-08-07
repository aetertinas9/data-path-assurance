package graph

import (
	"slices"
	"strings"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// Edge is one directed relation between two assets: From is the subject of the
// relation and To its object, so "A UPSTREAM_OF B" is the edge from A to B.
//
// An edge is identified by four things at once: From.Key(), To.Key(), Relation
// and Origin. Two edges agreeing on all four are the same edge however their
// endpoints are aliased; two differing in any of them are two edges, and they
// coexist. That Origin takes part is what lets what was observed and what was
// intended stand side by side, for a rule to compare.
//
// Nothing about an edge is inferred. The reverse edge is not created, the
// endpoints are not created, and which direction carries which relation is the
// producer's convention, not this package's.
type Edge struct {
	From     model.AssetRef
	To       model.AssetRef
	Relation model.EdgeRelation
	Origin   model.EdgeOrigin
}

// NewEdge returns the edge relating from to to, validated and copied
// defensively. Mutating the aliases reachable from either endpoint afterwards
// does not affect the returned edge, whose Validate is nil.
func NewEdge(from, to model.AssetRef, rel model.EdgeRelation, origin model.EdgeOrigin) (Edge, error) {
	e := Edge{From: from, To: to, Relation: rel, Origin: origin}
	if err := e.Validate(); err != nil {
		return Edge{}, err
	}
	return e.clone(), nil
}

// Validate reports the first way in which the edge is not one. It is applied
// alike to an edge from [NewEdge] and to one assembled as a struct literal, so
// that a value that never went through the constructor is held to the same
// rules.
//
// Both endpoints must be valid assets, and they must be two: an edge from a
// thing to itself says nothing, and endpoints differing only in their aliases
// are one thing. Relation and Origin must both be enumerated; neither zero
// value is.
//
// Two relations further fix their origin. INTENDED_TO_CONNECT is what an intent
// document declares and SHARES_FAILURE_DOMAIN_WITH is what the system worked
// out, so neither can have come from anywhere else. Every other pairing is left
// open, including a relation an intent document declares about a link — an
// edge's origin says who claimed it, not what kind of claim it is.
func (e Edge) Validate() error {
	if err := e.From.Validate(); err != nil {
		return invalidf("Edge.From: %s", err)
	}
	if err := e.To.Validate(); err != nil {
		return invalidf("Edge.To: %s", err)
	}
	if e.From.Key() == e.To.Key() {
		return invalidf("Edge relates %s to itself", e.From.Key())
	}
	if !e.Relation.IsValid() {
		return invalidf("Edge.Relation %s is not an edge relation", e.Relation)
	}
	if !e.Origin.IsValid() {
		return invalidf("Edge.Origin %s is not an edge origin", e.Origin)
	}
	switch {
	case e.Relation == model.RelIntendedToConnect && e.Origin != model.OriginIntended:
		return invalidf("Edge.Relation %s is declared intent, so Edge.Origin cannot be %s",
			e.Relation, e.Origin)
	case e.Relation == model.RelSharesFailureDomainWith && e.Origin != model.OriginDerived:
		return invalidf("Edge.Relation %s is worked out, so Edge.Origin cannot be %s",
			e.Relation, e.Origin)
	}
	return nil
}

// clone copies the edge and both endpoints' aliases.
func (e Edge) clone() Edge {
	c := e
	c.From = cloneAssetRef(e.From)
	c.To = cloneAssetRef(e.To)
	return c
}

// label renders the edge for a diagnostic message. It is not a contract, and it
// is deliberately not an exported String: the public surface of an edge is its
// fields, its constructor and its validation.
func (e Edge) label() string {
	return e.From.Key() + " -" + e.Relation.String() + "/" + e.Origin.String() + "-> " + e.To.Key()
}

// edgeKey is an edge's identity in a form a map can key on: the two endpoint
// keys and the two enumerations, and nothing else an endpoint happens to carry.
type edgeKey struct {
	from     string
	to       string
	relation model.EdgeRelation
	origin   model.EdgeOrigin
}

func keyOf(e Edge) edgeKey {
	return edgeKey{
		from:     e.From.Key(),
		to:       e.To.Key(),
		relation: e.Relation,
		origin:   e.Origin,
	}
}

// compareEdges is the total order edges are returned in everywhere: by From's
// key, then To's, then the relation's wire name, then the origin's name, each
// compared bytes ascending.
//
// Distinct identities compare distinct, because the four components of the
// order are the four the identity is made of and each enumerated value renders
// to its own name. That leaves no tie for the order to be arbitrary about.
func compareEdges(x, y Edge) int {
	if c := strings.Compare(x.From.Key(), y.From.Key()); c != 0 {
		return c
	}
	if c := strings.Compare(x.To.Key(), y.To.Key()); c != 0 {
		return c
	}
	if c := strings.Compare(x.Relation.String(), y.Relation.String()); c != 0 {
		return c
	}
	return strings.Compare(x.Origin.String(), y.Origin.String())
}

// compareAssetRefs orders asset references by Key, bytes ascending. Two
// references to one asset share a key, and this package never holds two of
// them at once.
func compareAssetRefs(x, y model.AssetRef) int {
	return strings.Compare(x.Key(), y.Key())
}

// sortedEdges returns the stored edges in edge order. The values are the stored
// ones, so a caller handing them out clones them first.
func sortedEdges(m map[edgeKey]Edge) []Edge {
	edges := make([]Edge, 0, len(m))
	for _, e := range m {
		edges = append(edges, e)
	}
	slices.SortFunc(edges, compareEdges)
	return edges
}

// sortedAssets returns the stored assets by Key, bytes ascending. The values
// are the stored ones, so a caller handing them out clones them first.
func sortedAssets(m map[string]model.AssetRef) []model.AssetRef {
	assets := make([]model.AssetRef, 0, len(m))
	for _, a := range m {
		assets = append(assets, a)
	}
	slices.SortFunc(assets, compareAssetRefs)
	return assets
}

// PartitionFor derives the key of the partition anchored at anchor.
//
// A partition is anchored at exactly one asset, and only a Kubernetes node or
// an Ethernet switch anchors one: those are the two things the fabric is
// divided into. The key is the anchor's own Key, which is never empty, so the
// key is always valid.
//
// Which partition any other asset or observation belongs to is not decided
// here. That takes knowledge of the whole fabric, which one partition's state
// does not have, so it is the caller's — the adapters' — business, and this
// package only checks that an event arriving at a partition says it is for that
// partition.
func PartitionFor(anchor model.AssetRef) (model.PartitionKey, error) {
	if err := anchor.Validate(); err != nil {
		return "", invalidf("PartitionFor: %s", err)
	}
	switch anchor.Kind {
	case model.KindKubernetesNode, model.KindEthernetSwitch:
		return model.PartitionKey(anchor.Key()), nil
	default:
		return "", invalidf("PartitionFor: %s is a %s, which anchors no partition",
			anchor.Key(), anchor.Kind)
	}
}
