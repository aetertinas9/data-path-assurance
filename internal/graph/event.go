package graph

import (
	"slices"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// Event is one thing that happened to one partition's graph, carrying the
// partition it happened to and the number its producer gave it.
//
// The six implementations below are the whole set, and the set is closed: an
// Event defined outside this package is refused by [State.Apply] and by
// [Reducer.Offer], because a state that cannot enumerate what may reach it
// cannot promise what it does with everything that does. All six are opaque —
// a constructor establishes the invariants and accessors read them back, so an
// event that exists is an event that made sense.
//
// A sequence number is any uint64. Zero and 2^64-1 are ordinary numbers here,
// not sentinels, and "the next one" is uint64 arithmetic, so the number after
// 2^64-1 is 0.
type Event interface {
	Partition() model.PartitionKey
	Sequence() uint64
}

// validateEvent reports whether ev is a well-formed event of this package: not
// nil, not something another package implemented, and not a zero value that
// never went through a constructor.
func validateEvent(ev Event) error {
	switch e := ev.(type) {
	case nil:
		return invalidf("event is nil")
	case Resync:
		return e.validate()
	case AssetUpsert:
		return e.validate()
	case AssetRemove:
		return e.validate()
	case EdgeUpsert:
		return e.validate()
	case EdgeRemove:
		return e.validate()
	case ObservationAppend:
		return e.validate()
	default:
		return invalidf("%T is not an event of this package", ev)
	}
}

// Resync carries a partition's whole topology as of one moment. Applying it
// replaces what was stored, whatever the sequence numbers say, and establishes
// the baseline every delta is counted from. It is what a producer sends when it
// starts and periodically thereafter, and it is the only way out of a gap.
//
// The evidence window is not part of it: replacing a topology does not erase
// what was observed there.
type Resync struct {
	partition model.PartitionKey
	sequence  uint64
	assets    []model.AssetRef
	edges     []Edge
}

// NewResync returns the snapshot event for partition p at sequence seq.
//
// Every asset must be valid and no two of them may be the same asset; likewise
// every edge must be valid and no two may share an identity. Two claims about
// one thing in one snapshot are ambiguous, and merging them quietly is the kind
// of guess this system does not make. An edge whose endpoint is not among the
// assets is fine — it may point at a remote asset, or at one this partition
// never lists. An empty snapshot is fine too: it establishes the baseline of an
// empty partition.
func NewResync(p model.PartitionKey, seq uint64, assets []model.AssetRef, edges []Edge) (Resync, error) {
	e := Resync{
		partition: p,
		sequence:  seq,
		assets:    cloneAssetRefs(assets),
		edges:     cloneEdges(edges),
	}
	slices.SortFunc(e.assets, compareAssetRefs)
	slices.SortFunc(e.edges, compareEdges)
	if err := e.validate(); err != nil {
		return Resync{}, err
	}
	return e, nil
}

// Partition returns the partition the snapshot is of.
func (e Resync) Partition() model.PartitionKey { return e.partition }

// Sequence returns the number the producer gave the snapshot.
func (e Resync) Sequence() uint64 { return e.sequence }

// Assets returns the snapshot's assets, ordered by Key, bytes ascending.
func (e Resync) Assets() []model.AssetRef { return cloneAssetRefs(e.assets) }

// Edges returns the snapshot's edges in edge order.
func (e Resync) Edges() []Edge { return cloneEdges(e.edges) }

// validate checks the snapshot's parts. Duplicates are found by adjacency,
// which holds because the constructor sorts before it validates and both orders
// are total over distinct identities.
func (e Resync) validate() error {
	if !e.partition.IsValid() {
		return invalidf("Resync: partition key is empty")
	}
	for i, a := range e.assets {
		if err := a.Validate(); err != nil {
			return invalidf("Resync assets[%d]: %s", i, err)
		}
		if i > 0 && e.assets[i-1].Key() == a.Key() {
			return invalidf("Resync assets: %s is claimed twice in one snapshot", a.Key())
		}
	}
	for i, edge := range e.edges {
		if err := edge.Validate(); err != nil {
			return invalidf("Resync edges[%d]: %s", i, err)
		}
		if i > 0 && keyOf(e.edges[i-1]) == keyOf(edge) {
			return invalidf("Resync edges: %s is claimed twice in one snapshot", edge.label())
		}
	}
	return nil
}

// AssetUpsert adds an asset to a partition, or replaces what was stored for the
// asset of that key.
type AssetUpsert struct {
	partition model.PartitionKey
	sequence  uint64
	asset     model.AssetRef
}

// NewAssetUpsert returns the upsert of asset in partition p at sequence seq.
func NewAssetUpsert(p model.PartitionKey, seq uint64, asset model.AssetRef) (AssetUpsert, error) {
	e := AssetUpsert{partition: p, sequence: seq, asset: cloneAssetRef(asset)}
	if err := e.validate(); err != nil {
		return AssetUpsert{}, err
	}
	return e, nil
}

// Partition returns the partition the asset belongs to.
func (e AssetUpsert) Partition() model.PartitionKey { return e.partition }

// Sequence returns the number the producer gave the event.
func (e AssetUpsert) Sequence() uint64 { return e.sequence }

// Asset returns the asset to store.
func (e AssetUpsert) Asset() model.AssetRef { return cloneAssetRef(e.asset) }

func (e AssetUpsert) validate() error {
	return validateAssetEvent("AssetUpsert", e.partition, e.asset)
}

// AssetRemove removes an asset from a partition, and with it every edge that
// named the asset at either end.
type AssetRemove struct {
	partition model.PartitionKey
	sequence  uint64
	asset     model.AssetRef
}

// NewAssetRemove returns the removal of asset from partition p at sequence seq.
func NewAssetRemove(p model.PartitionKey, seq uint64, asset model.AssetRef) (AssetRemove, error) {
	e := AssetRemove{partition: p, sequence: seq, asset: cloneAssetRef(asset)}
	if err := e.validate(); err != nil {
		return AssetRemove{}, err
	}
	return e, nil
}

// Partition returns the partition the asset is removed from.
func (e AssetRemove) Partition() model.PartitionKey { return e.partition }

// Sequence returns the number the producer gave the event.
func (e AssetRemove) Sequence() uint64 { return e.sequence }

// Asset returns the asset to remove.
func (e AssetRemove) Asset() model.AssetRef { return cloneAssetRef(e.asset) }

func (e AssetRemove) validate() error {
	return validateAssetEvent("AssetRemove", e.partition, e.asset)
}

// validateAssetEvent checks the two parts the asset events share.
func validateAssetEvent(name string, p model.PartitionKey, asset model.AssetRef) error {
	if !p.IsValid() {
		return invalidf("%s: partition key is empty", name)
	}
	if err := asset.Validate(); err != nil {
		return invalidf("%s: %s", name, err)
	}
	return nil
}

// EdgeUpsert adds an edge to a partition, or replaces what was stored for the
// edge of that identity.
type EdgeUpsert struct {
	partition model.PartitionKey
	sequence  uint64
	edge      Edge
}

// NewEdgeUpsert returns the upsert of edge in partition p at sequence seq.
func NewEdgeUpsert(p model.PartitionKey, seq uint64, edge Edge) (EdgeUpsert, error) {
	e := EdgeUpsert{partition: p, sequence: seq, edge: edge.clone()}
	if err := e.validate(); err != nil {
		return EdgeUpsert{}, err
	}
	return e, nil
}

// Partition returns the partition the edge is stored in.
func (e EdgeUpsert) Partition() model.PartitionKey { return e.partition }

// Sequence returns the number the producer gave the event.
func (e EdgeUpsert) Sequence() uint64 { return e.sequence }

// Edge returns the edge to store.
func (e EdgeUpsert) Edge() Edge { return e.edge.clone() }

func (e EdgeUpsert) validate() error {
	return validateEdgeEvent("EdgeUpsert", e.partition, e.edge)
}

// EdgeRemove removes the edge of one identity from a partition.
type EdgeRemove struct {
	partition model.PartitionKey
	sequence  uint64
	edge      Edge
}

// NewEdgeRemove returns the removal of edge from partition p at sequence seq.
func NewEdgeRemove(p model.PartitionKey, seq uint64, edge Edge) (EdgeRemove, error) {
	e := EdgeRemove{partition: p, sequence: seq, edge: edge.clone()}
	if err := e.validate(); err != nil {
		return EdgeRemove{}, err
	}
	return e, nil
}

// Partition returns the partition the edge is removed from.
func (e EdgeRemove) Partition() model.PartitionKey { return e.partition }

// Sequence returns the number the producer gave the event.
func (e EdgeRemove) Sequence() uint64 { return e.sequence }

// Edge returns the edge to remove. Only its identity decides what is removed.
func (e EdgeRemove) Edge() Edge { return e.edge.clone() }

func (e EdgeRemove) validate() error {
	return validateEdgeEvent("EdgeRemove", e.partition, e.edge)
}

// validateEdgeEvent checks the two parts the edge events share.
func validateEdgeEvent(name string, p model.PartitionKey, edge Edge) error {
	if !p.IsValid() {
		return invalidf("%s: partition key is empty", name)
	}
	if err := edge.Validate(); err != nil {
		return invalidf("%s: %s", name, err)
	}
	return nil
}

// ObservationAppend offers one observation to a partition's evidence window.
//
// What the window makes of it is decided when the event is applied, not when it
// is built: a reading the model calls valid is a valid event even if it holds
// an infinity or a NaN, which the window is the one to refuse.
type ObservationAppend struct {
	partition   model.PartitionKey
	sequence    uint64
	observation model.Observation
}

// NewObservationAppend returns the append of o to partition p's window at
// sequence seq.
func NewObservationAppend(p model.PartitionKey, seq uint64, o model.Observation) (ObservationAppend, error) {
	e := ObservationAppend{partition: p, sequence: seq, observation: cloneObservation(o)}
	if err := e.validate(); err != nil {
		return ObservationAppend{}, err
	}
	return e, nil
}

// Partition returns the partition whose window the observation is offered to.
func (e ObservationAppend) Partition() model.PartitionKey { return e.partition }

// Sequence returns the number the producer gave the event.
func (e ObservationAppend) Sequence() uint64 { return e.sequence }

// Observation returns the observation to append.
func (e ObservationAppend) Observation() model.Observation {
	return cloneObservation(e.observation)
}

func (e ObservationAppend) validate() error {
	if !e.partition.IsValid() {
		return invalidf("ObservationAppend: partition key is empty")
	}
	if err := e.observation.Validate(); err != nil {
		return invalidf("ObservationAppend: %s", err)
	}
	return nil
}
