package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// liveAssessor evaluates the bundles of a LiveBundleSource with the ratified
// fleet API and keeps, per node, the decisions of its last successful call as
// the next call's previous state (cold start = no memory).
//
// Ownership and lock order: locks serializes the AssessNode and ForgetNode
// calls of one node UID for their whole duration (calls for different nodes
// run in parallel, and the source is called under its node's lock only); mu
// guards the nodes map and is held only around map reads and writes, never
// across a call out of the package. Lock order: locks[uid] before mu. The
// memory lives in the process only and is never restored from anywhere.
type liveAssessor struct {
	source LiveBundleSource
	locks  liveNodeLocks

	mu    sync.Mutex
	nodes map[string]*liveNodeMemory // node UID -> that node's continuity state
}

// liveNodeMemory is everything remembered about one node: the device
// decisions by device UID and the node decision of one fleet.
type liveNodeMemory struct {
	devices  map[string]fleet.DeviceDecision
	fleetUID string
	node     fleet.NodeDecision
}

// NewLiveAssessor returns the NodeAssessor that evaluates the bundles of
// source. It is goroutine-safe. A nil source behaves like
// NewNoObservationSource(). See GKA-032 for the rules AssessNode follows.
func NewLiveAssessor(source LiveBundleSource) NodeAssessor {
	if source == nil {
		source = NewNoObservationSource()
	}
	return &liveAssessor{
		source: source,
		locks:  liveNodeLocks{entries: map[string]*liveNodeLock{}},
		nodes:  map[string]*liveNodeMemory{},
	}
}

// AssessNode implements NodeAssessor.
func (a *liveAssessor) AssessNode(ctx context.Context, req NodeAssessmentRequest) (NodeAssessment, error) {
	uid := req.Node.UID
	release := a.locks.acquire(uid)
	defer release()

	set, err := a.source.NodeBundles(ctx, LiveBundleQuery{
		Node:    req.Node,
		Policy:  liveClonePolicy(req.Policy),
		Intents: slices.Clone(req.Intents),
		Now:     req.Now,
	})
	if errors.Is(err, ErrNoObservation) {
		a.forgetLocked(uid)
		return NodeAssessment{}, nil
	}
	if err != nil {
		return NodeAssessment{}, fmt.Errorf("app: live bundle source: %w", err)
	}
	if len(set.Devices) == 0 {
		a.forgetLocked(uid)
		return NodeAssessment{}, nil
	}

	// Rule (b): validate the whole set before anything is evaluated.
	intents := make(map[string]fleet.Intent, len(req.Intents))
	for _, in := range req.Intents {
		intents[in.Device.UID] = in
	}
	first := set.Devices[0].Bundle
	seen := make(map[string]struct{}, len(set.Devices))
	for i, d := range set.Devices {
		if _, ok := intents[d.DeviceUID]; !ok {
			return NodeAssessment{}, fmt.Errorf("app: live bundle %d: device is not among the request intents: %w", i, fleet.ErrInvalidInput)
		}
		if _, dup := seen[d.DeviceUID]; dup {
			return NodeAssessment{}, fmt.Errorf("app: live bundle %d: device appears more than once: %w", i, fleet.ErrInvalidInput)
		}
		seen[d.DeviceUID] = struct{}{}
		b := d.Bundle
		switch {
		case b.Snapshot.NodeUID != uid:
			return NodeAssessment{}, fmt.Errorf("app: live bundle %d: snapshot belongs to another node: %w", i, fleet.ErrInvalidInput)
		case b.GraphRevision != first.GraphRevision:
			return NodeAssessment{}, fmt.Errorf("app: live bundle %d: graph revision differs from the other bundles: %w", i, fleet.ErrInvalidInput)
		case b.Snapshot.Completeness != first.Snapshot.Completeness:
			return NodeAssessment{}, fmt.Errorf("app: live bundle %d: snapshot completeness differs from the other bundles: %w", i, fleet.ErrInvalidInput)
		case b.Snapshot.Session != first.Snapshot.Session:
			return NodeAssessment{}, fmt.Errorf("app: live bundle %d: snapshot session differs from the other bundles: %w", i, fleet.ErrInvalidInput)
		}
	}

	// The result and the memory follow device UID order.
	bundles := slices.Clone(set.Devices)
	slices.SortFunc(bundles, func(x, y LiveDeviceBundle) int { return strings.Compare(x.DeviceUID, y.DeviceUID) })

	prevDevices, prevNode := a.previous(uid, req.FleetUID, bundles)

	// Rules (c) and (d): the request's policy and intent replace the source's;
	// previous is the remembered decision of the same (node, device).
	decisions := make([]fleet.DeviceDecision, len(bundles))
	for i, d := range bundles {
		bundle := d.Bundle
		bundle.Policy = req.Policy
		bundle.Intent = intents[d.DeviceUID]
		dec, err := fleet.EvaluateDevice(bundle, prevDevices[d.DeviceUID], req.Now)
		if err != nil {
			return NodeAssessment{}, fmt.Errorf("app: evaluate device: %w", err)
		}
		decisions[i] = dec
	}

	// Rule (e): aggregate the node. A complete selection with fewer decisions
	// than intents is a partial one.
	selection := req.Selection
	if selection == fleet.SelectionComplete && len(decisions) < len(req.Intents) {
		selection = fleet.SelectionPartial
	}
	aggregates := make([]fleet.DeviceAggregate, len(decisions))
	for i, dec := range decisions {
		in := intents[bundles[i].DeviceUID]
		aggregates[i] = fleet.DeviceAggregate{
			DeviceUID:          bundles[i].DeviceUID,
			NodeUID:            uid,
			Desired:            in.Desired,
			MetadataGeneration: in.MetadataGeneration,
			Decision:           dec,
		}
	}
	node, err := fleet.AggregateNode(fleet.AggregateNodeInput{
		Node:      req.Node,
		FleetUID:  req.FleetUID,
		Selection: selection,
		Devices:   aggregates,
	}, prevNode, req.Now)
	if err != nil {
		return NodeAssessment{}, fmt.Errorf("app: aggregate node: %w", err)
	}

	// Rule (f): every call succeeded, so the node's memory is replaced by this
	// call's decisions (devices not in this call are dropped).
	memory := &liveNodeMemory{
		devices:  make(map[string]fleet.DeviceDecision, len(decisions)),
		fleetUID: req.FleetUID,
		node:     node,
	}
	out := NodeAssessment{
		Devices: make([]DeviceAssessment, len(decisions)),
		Node:    &node,
		Observation: &NodeObservation{
			GraphRevision: first.GraphRevision,
			Completeness:  first.Snapshot.Completeness,
		},
	}
	for i, dec := range decisions {
		memory.devices[bundles[i].DeviceUID] = liveCloneDecision(dec)
		out.Devices[i] = DeviceAssessment{
			Decision:   liveCloneDecision(dec),
			Findings:   liveCloneFindings(bundles[i].Findings),
			Evidence:   liveCloneEvidence(bundles[i].Evidence),
			Allocation: liveCloneAllocation(bundles[i].Allocation),
		}
	}
	a.mu.Lock()
	a.nodes[uid] = memory
	a.mu.Unlock()
	return out, nil
}

// ForgetNode implements NodeAssessor.
func (a *liveAssessor) ForgetNode(nodeUID string) {
	release := a.locks.acquire(nodeUID)
	defer release()
	a.forgetLocked(nodeUID)
}

// forgetLocked drops one node's memory. The caller holds that node's lock.
func (a *liveAssessor) forgetLocked(nodeUID string) {
	a.mu.Lock()
	delete(a.nodes, nodeUID)
	a.mu.Unlock()
}

// previous returns copies of the remembered decisions a call starts from: the
// device decisions of the bundles' devices and the node decision of fleetUID.
// A decision that does not validate is never handed on as previous. The caller
// holds the node's lock.
func (a *liveAssessor) previous(nodeUID, fleetUID string, bundles []LiveDeviceBundle) (map[string]*fleet.DeviceDecision, *fleet.NodeDecision) {
	a.mu.Lock()
	memory := a.nodes[nodeUID]
	var devices map[string]*fleet.DeviceDecision
	var node *fleet.NodeDecision
	if memory != nil {
		devices = make(map[string]*fleet.DeviceDecision, len(bundles))
		for _, b := range bundles {
			if dec, ok := memory.devices[b.DeviceUID]; ok {
				c := liveCloneDecision(dec)
				devices[b.DeviceUID] = &c
			}
		}
		if memory.fleetUID == fleetUID {
			n := memory.node
			node = &n
		}
	}
	a.mu.Unlock()
	for uid, dec := range devices {
		if dec.Validate() != nil {
			delete(devices, uid)
		}
	}
	if node != nil && node.Validate() != nil {
		node = nil
	}
	return devices, node
}

// liveNodeLocks hands out one mutex per node UID and forgets a mutex once no
// caller holds or waits for it, so the table does not grow with the number of
// nodes ever seen.
type liveNodeLocks struct {
	mu      sync.Mutex
	entries map[string]*liveNodeLock // guarded by mu
}

type liveNodeLock struct {
	mu   sync.Mutex
	refs int // holders and waiters; guarded by liveNodeLocks.mu
}

// acquire blocks until it holds the lock of key and returns the release
// function, which must be called exactly once.
func (l *liveNodeLocks) acquire(key string) func() {
	l.mu.Lock()
	e := l.entries[key]
	if e == nil {
		e = &liveNodeLock{}
		l.entries[key] = e
	}
	e.refs++
	l.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		l.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(l.entries, key)
		}
		l.mu.Unlock()
	}
}
