package graph

import (
	"slices"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// Snapshot is one partition's graph as it stood at one point of application:
// what was stored, how far the stream had been followed, and the evidence
// window of the same moment. Reading is done through snapshots and only through
// them.
//
// A Snapshot is immutable. Nothing a state or a reducer does afterwards reaches
// one that has been handed out, so any number of goroutines may share it, and
// what it answers now it answers later.
//
// Topology and window are always the same moment. A rule reading both out of
// one snapshot cannot end up comparing a topology from one instant against
// evidence from another, because there is no way to ask for them separately.
//
// A nil *Snapshot answers as an empty one does. Every method below is safe on
// it, so a caller need not guard a snapshot it was handed.
type Snapshot struct {
	partition model.PartitionKey
	// assets and edges are the state's own maps. They are safe to share
	// because a state never writes to a map it has published — an application
	// builds new ones.
	assets   map[string]model.AssetRef
	edges    map[edgeKey]Edge
	window   evidence.Window
	last     uint64
	baseline bool
	synced   bool
}

// Partition returns the partition the snapshot is of. An empty snapshot returns
// the zero key.
func (s *Snapshot) Partition() model.PartitionKey {
	if s == nil {
		return ""
	}
	return s.partition
}

// Sequence returns the number of the last delta taken and whether a baseline
// has been established at all. Without a baseline the answer is (0, false), and
// the number means nothing.
//
// A gap does not undo the baseline: the number stays at the last delta actually
// taken, and it is [Snapshot.Synced] that reports the state is no longer
// following the stream.
func (s *Snapshot) Sequence() (uint64, bool) {
	if s == nil || !s.baseline {
		return 0, false
	}
	return s.last, true
}

// Synced reports whether the snapshot has a baseline and has seen no gap since
// it. Only a synced state takes topology deltas; an unsynced one is waiting for
// the snapshot that heals it.
func (s *Snapshot) Synced() bool {
	return s != nil && s.synced
}

// Assets returns every stored asset as it was last stored, ordered by Key,
// bytes ascending.
//
// Only assets that were upserted or listed in a snapshot appear. An edge does
// not create its endpoints, so an asset named only by an edge is not here.
func (s *Snapshot) Assets() []model.AssetRef {
	if s == nil {
		return []model.AssetRef{}
	}
	assets := sortedAssets(s.assets)
	for i := range assets {
		assets[i] = cloneAssetRef(assets[i])
	}
	return assets
}

// Asset returns the stored asset ref names, looked up by Key, as it was last
// stored. An asset that is not stored comes back as the zero AssetRef beside a
// nil error: a stored asset is always a valid one, so the zero value says
// "none" unambiguously.
//
// A ref the model calls invalid is an argument violation.
func (s *Snapshot) Asset(ref model.AssetRef) (model.AssetRef, error) {
	if err := ref.Validate(); err != nil {
		return model.AssetRef{}, invalidf("Asset: %s", err)
	}
	if s == nil {
		return model.AssetRef{}, nil
	}
	stored, ok := s.assets[ref.Key()]
	if !ok {
		return model.AssetRef{}, nil
	}
	return cloneAssetRef(stored), nil
}

// Edges returns every stored edge as it was last stored, in edge order.
func (s *Snapshot) Edges() []Edge {
	if s == nil {
		return []Edge{}
	}
	edges := sortedEdges(s.edges)
	for i := range edges {
		edges[i] = edges[i].clone()
	}
	return edges
}

// EdgesFrom returns the stored edges whose From names ref, matched by Key, in
// edge order.
//
// The lookup does not require ref to be a stored asset. An edge may point at an
// asset in another partition, or at one this partition never listed, and it is
// found here all the same.
func (s *Snapshot) EdgesFrom(ref model.AssetRef) ([]Edge, error) {
	if err := ref.Validate(); err != nil {
		return nil, invalidf("EdgesFrom: %s", err)
	}
	return s.edgesAt(ref.Key(), true), nil
}

// EdgesTo returns the stored edges whose To names ref, matched by Key, in edge
// order. It is [Snapshot.EdgesFrom] read the other way round.
func (s *Snapshot) EdgesTo(ref model.AssetRef) ([]Edge, error) {
	if err := ref.Validate(); err != nil {
		return nil, invalidf("EdgesTo: %s", err)
	}
	return s.edgesAt(ref.Key(), false), nil
}

// edgesAt collects the stored edges naming key at one end, in edge order.
func (s *Snapshot) edgesAt(key string, from bool) []Edge {
	matched := make([]Edge, 0)
	if s == nil {
		return matched
	}
	for k, edge := range s.edges {
		end := k.to
		if from {
			end = k.from
		}
		if end == key {
			matched = append(matched, edge.clone())
		}
	}
	slices.SortFunc(matched, compareEdges)
	return matched
}

// Window returns the partition's evidence window as of this snapshot's point of
// application. An empty snapshot returns the zero Window, which answers every
// query as an empty window does and cannot be assembled into.
func (s *Snapshot) Window() evidence.Window {
	if s == nil {
		return evidence.Window{}
	}
	return s.window
}
