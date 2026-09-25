package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

const (
	// windowHorizon is how far back the cumulative evidence window keeps a
	// series, measured from its latest observation.
	windowHorizon = 86400 * time.Second
	// windowMaxSamples is the per-series capacity of the window: one
	// observation per frame at most.
	windowMaxSamples = 16
)

// replayResult is what the replay evaluation produced: the device and node
// decisions and the result frame state the explanation is assembled from.
type replayResult struct {
	nodeAsset model.AssetRef
	// admitted reports whether every frame was admitted; when false no frame
	// was and the result frame is frame 0.
	admitted   bool
	firstOrder fleet.SnapshotOrder
	result     int
	evaluated  time.Time
	decisions  []fleet.DeviceDecision // indexed like Replay.Devices
	bundles    []fleet.AssessmentBundle
	node       fleet.NodeDecision
	window     evidence.Window
	findings   []model.Finding
}

// ExplainFleet loads a replay from src, evaluates it with the ratified fleet
// API and returns the explanation of the requested GPU device or node.
//
// Errors from src are returned wrapped. A request naming no device (GPU) or a
// node other than the replayed one is ErrTargetNotFound. Any failure of the
// evaluation itself is ErrEvaluation.
func ExplainFleet(ctx context.Context, src ReplaySource, req Request) (Explanation, error) {
	if src == nil {
		return Explanation{}, evaluationError("no replay source")
	}
	replay, err := src.LoadReplay(ctx)
	if err != nil {
		return Explanation{}, fmt.Errorf("loading the replay: %w", err)
	}
	target := -1
	switch req.Kind {
	case TargetGPU:
		for i, d := range replay.Devices {
			if d.Device.Name == req.Name {
				target = i
				break
			}
		}
		if target < 0 {
			return Explanation{}, notFound("no device of the fleet input has the requested name")
		}
	case TargetNode:
		if replay.Node.Name != req.Name {
			return Explanation{}, notFound("the requested node is not the node of the artifact")
		}
	default:
		return Explanation{}, evaluationError("unknown request kind")
	}
	if err := ctx.Err(); err != nil {
		return Explanation{}, err
	}
	res, err := evaluateReplay(&replay)
	if err != nil {
		return Explanation{}, err
	}
	return assemble(&replay, res, req.Kind, target)
}

// evaluateReplay runs snapshot admission, the per-frame bundles and the
// device evaluation chain, and the node aggregation.
func evaluateReplay(r *Replay) (*replayResult, error) {
	n := len(r.Frames)
	if n == 0 {
		return nil, evaluationError("the replay has no frame")
	}
	res := &replayResult{
		nodeAsset: model.AssetRef{Kind: model.KindKubernetesNode, Canonical: model.NamespaceKubernetesNodeUID + ":" + r.Node.UID},
	}
	partition, err := graph.PartitionFor(res.nodeAsset)
	if err != nil {
		return nil, evaluationError("the node asset anchors no partition")
	}

	// Admission chain: stop at the first frame that is not accepted.
	cursors := make([]fleet.SnapshotCursor, n)
	var previous *fleet.SnapshotCursor
	accepted := 0
	for k := range r.Frames {
		c, order, err := fleet.AdmitSnapshot(r.CollectorTrust.Session, previous, r.Frames[k].Envelope)
		if err != nil {
			return nil, evaluationError("snapshot admission returned an error")
		}
		if k == 0 {
			res.firstOrder = order
		}
		if order != fleet.SnapshotAccepted {
			break
		}
		cursors[k] = c
		previous = &cursors[k]
		accepted++
	}
	switch accepted {
	case n:
		res.admitted = true
		res.result = n - 1
	case 0:
		res.result = 0
	default:
		return nil, evaluationError("only part of the replay was admitted")
	}

	cfg := evidence.Config{MaxAge: r.Policy.Freshness, Horizon: windowHorizon, MaxSamples: windowMaxSamples}
	window, err := evidence.NewWindow(cfg)
	if err != nil {
		return nil, evaluationError("the evidence window configuration was rejected")
	}
	var decisions []*fleet.DeviceDecision
	for k := 0; k <= res.result; k++ {
		frame := &r.Frames[k]
		now := frame.Envelope.ObservedAt
		for _, o := range frame.Observations {
			if window, err = window.Add(o); err != nil {
				return nil, evaluationError("the evidence window rejected an observation")
			}
		}
		topology, err := frameTopology(partition, cfg, frame)
		if err != nil {
			return nil, err
		}
		findings := pcie.EvaluateLinkWidth(window, now)
		admitted := cursors[k]
		if !res.admitted {
			e := frame.Envelope
			admitted = fleet.SnapshotCursor{NodeUID: e.NodeUID, BootID: e.BootID, PayloadDigest: e.PayloadDigest, BundleRevision: e.BundleRevision, Session: e.Session, Sequence: e.Sequence, Completeness: e.Completeness, ObservedAt: e.ObservedAt, Baseline: false}
		}
		template := fleet.AssessmentBundle{
			Policy:                r.Policy,
			Snapshot:              frame.Envelope,
			Admitted:              admitted,
			GraphRevision:         frame.Envelope.BundleRevision,
			WindowRevision:        frame.Envelope.BundleRevision,
			TopologyDigest:        topologyDigest(partition, frame),
			BaselineDigest:        baselineDigest(partition, window),
			Topology:              topology,
			Window:                window,
			Provenance:            frame.Provenance,
			Bindings:              frame.Bindings,
			Findings:              findings,
			FindingsEvaluatedAt:   now,
			FindingsGraphRevision: frame.Envelope.BundleRevision,
			CollectorTrust:        r.CollectorTrust,
		}
		next := make([]*fleet.DeviceDecision, len(r.Devices))
		bundles := make([]fleet.AssessmentBundle, len(r.Devices))
		for i, intent := range r.Devices {
			bundle := template
			bundle.Intent = intent
			var prev *fleet.DeviceDecision
			if k > 0 {
				prev = decisions[i]
			}
			d, err := fleet.EvaluateDevice(bundle, prev, now)
			if err != nil {
				return nil, evaluationError("device evaluation returned an error")
			}
			next[i] = &d
			bundles[i] = bundle
		}
		decisions = next
		if k == res.result {
			res.window = window
			res.findings = findings
			res.bundles = bundles
			res.evaluated = now
		}
	}
	res.decisions = make([]fleet.DeviceDecision, len(r.Devices))
	for i, d := range decisions {
		res.decisions[i] = *d
	}

	aggregates := make([]fleet.DeviceAggregate, len(r.Devices))
	for i, intent := range r.Devices {
		aggregates[i] = fleet.DeviceAggregate{
			DeviceUID:          intent.Device.UID,
			NodeUID:            r.Node.UID,
			Desired:            intent.Desired,
			MetadataGeneration: intent.MetadataGeneration,
			Decision:           res.decisions[i],
		}
	}
	slices.SortFunc(aggregates, func(a, b fleet.DeviceAggregate) int { return strings.Compare(a.DeviceUID, b.DeviceUID) })
	selection := fleet.SelectionComplete
	if len(aggregates) == 0 {
		selection = fleet.SelectionNoDevices
	}
	res.node, err = fleet.AggregateNode(fleet.AggregateNodeInput{
		Node:      fleet.NodeRef{ClusterID: r.ClusterID, Name: r.Node.Name, UID: r.Node.UID},
		FleetUID:  r.FleetUID,
		Selection: selection,
		Devices:   aggregates,
	}, nil, res.evaluated)
	if err != nil {
		return nil, evaluationError("node aggregation returned an error")
	}
	return res, nil
}

// frameTopology builds the topology snapshot of one frame from that frame's
// payload alone.
func frameTopology(partition model.PartitionKey, cfg evidence.Config, f *Frame) (*graph.Snapshot, error) {
	state, err := graph.NewState(partition, cfg)
	if err != nil {
		return nil, evaluationError("the topology state could not be created")
	}
	resync, err := graph.NewResync(partition, f.Envelope.Sequence, f.Assets, f.Edges)
	if err != nil {
		return nil, evaluationError("the frame topology was rejected")
	}
	state, _, err = state.Apply(resync)
	if err != nil {
		return nil, evaluationError("the frame topology could not be applied")
	}
	return state.Snapshot(), nil
}
