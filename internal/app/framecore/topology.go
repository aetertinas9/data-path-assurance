package framecore

import (
	"errors"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// The reasons Topology can fail. Topology returns exactly one of them, bare:
// the underlying error is dropped, because the graph errors quote asset keys
// that come from a payload and so must not reach a log or a response.
var (
	// ErrTopologyState reports that the topology state could not be created.
	ErrTopologyState = errors.New("framecore: the topology state could not be created")
	// ErrTopologyRejected reports that the frame topology was rejected.
	ErrTopologyRejected = errors.New("framecore: the frame topology was rejected")
	// ErrTopologyApply reports that the frame topology could not be applied.
	ErrTopologyApply = errors.New("framecore: the frame topology could not be applied")
)

// Topology builds the topology snapshot of one frame from that frame's
// assets and edges alone, as a resync of the partition at the frame's
// sequence.
func Topology(partition model.PartitionKey, cfg evidence.Config, sequence uint64, assets []model.AssetRef, edges []graph.Edge) (*graph.Snapshot, error) {
	state, err := graph.NewState(partition, cfg)
	if err != nil {
		return nil, ErrTopologyState
	}
	resync, err := graph.NewResync(partition, sequence, assets, edges)
	if err != nil {
		return nil, ErrTopologyRejected
	}
	state, _, err = state.Apply(resync)
	if err != nil {
		return nil, ErrTopologyApply
	}
	return state.Snapshot(), nil
}
