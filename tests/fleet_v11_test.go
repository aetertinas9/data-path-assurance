package tests_test

import (
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

func fleetObservedEdge(t *testing.T, from, to model.AssetRef, relation model.EdgeRelation) graph.Edge {
	t.Helper()
	edge, err := graph.NewEdge(from, to, relation, model.OriginObserved)
	if err != nil {
		t.Fatal(err)
	}
	return edge
}

func fleetReplaceTopology(t *testing.T, bundle fleet.AssessmentBundle, assets []model.AssetRef, edges []graph.Edge) fleet.AssessmentBundle {
	t.Helper()
	partition := bundle.Topology.Partition()
	state, err := graph.NewState(partition, evidence.Config{MaxAge: time.Minute, Horizon: time.Hour, MaxSamples: 64})
	if err != nil {
		t.Fatal(err)
	}
	resync, err := graph.NewResync(partition, bundle.Snapshot.Sequence, assets, edges)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = state.Apply(resync)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Topology = state.Snapshot()
	bundle.Window = bundle.Topology.Window()
	source := bundle.CollectorTrust.Sources[0].Source
	bundle.Provenance = make([]fleet.EdgeProvenance, len(edges))
	for i, edge := range edges {
		bundle.Provenance[i] = fleet.EdgeProvenance{
			Edge: edge, EvidenceID: "topology-ev-" + string(rune('a'+i)), BundleRevision: bundle.GraphRevision,
			CollectorProfileID: bundle.CollectorTrust.ID, Kind: fleet.EdgeEvidenceObserved, Source: source,
			ObservedAt: bundle.Snapshot.ObservedAt, ExpiresAt: bundle.Snapshot.ObservedAt.Add(time.Minute),
		}
	}
	return bundle
}

func fleetTopologyAssets(t *testing.T) (model.AssetRef, model.AssetRef, model.AssetRef) {
	t.Helper()
	function := fleetFunction(t)
	root := fleetAsset(t, model.KindPCIeRootPort, string(model.NamespacePCIBDF), "0000:64:00.0")
	node := fleetAsset(t, model.KindKubernetesNode, string(model.NamespaceKubernetesNodeUID), "node-uid")
	return function, root, node
}

func fleetAssertNoNormalPoint(t *testing.T, bundle fleet.AssessmentBundle, now time.Time) {
	t.Helper()
	decision, err := fleet.EvaluateDevice(bundle, nil, now)
	if err != nil {
		t.Fatalf("EvaluateDevice: %v", err)
	}
	if decision.AcceptedNormalPoint || decision.Phase == fleet.PhaseReady || decision.Qualification == fleet.QualificationQualified {
		t.Fatalf("invalid physical proof became normal/Ready: %#v", decision)
	}
}

// GFL-020A: direct child-to-root and finite child-to-switch-to-root
// RelLocatedIn chains with exact observed provenance are valid physical paths.
func TestGFL_020A_DirectAndSwitchObservedContainment(t *testing.T) {
	at := fleetT0.Add(50 * time.Minute)
	direct, err := fleet.EvaluateDevice(fleetBundle(t, at, 0, fleet.DesiredInService), nil, at)
	if err != nil || !direct.AcceptedNormalPoint {
		t.Fatalf("direct path = %#v/%v", direct, err)
	}

	function, root, node := fleetTopologyAssets(t)
	switchAsset := fleetAsset(t, model.KindPCIeSwitch, string(model.NamespacePCIBDF), "0000:63:00.0")
	edges := []graph.Edge{
		fleetObservedEdge(t, function, switchAsset, model.RelLocatedIn),
		fleetObservedEdge(t, switchAsset, root, model.RelLocatedIn),
		fleetObservedEdge(t, root, node, model.RelLocatedIn),
	}
	bundle := fleetReplaceTopology(t, fleetBundle(t, at, 0, fleet.DesiredInService), []model.AssetRef{function, switchAsset, root, node}, edges)
	decision, err := fleet.EvaluateDevice(bundle, nil, at)
	if err != nil || !decision.AcceptedNormalPoint || decision.Coverage[0].State != fleet.CoverageNormal || decision.Coverage[1].State != fleet.CoverageNormal {
		t.Fatalf("switch chain = %#v/%v", decision, err)
	}
}

// GFL-020A/GFL-028: wrong direction/relation, cycles, multiple parents, and
// foreign-node paths are not physical proof for the affected bound function.
func TestGFL_020A_028_InvalidObservedContainmentIsUnknown(t *testing.T) {
	at := fleetT0.Add(50 * time.Minute)
	function, root, node := fleetTopologyAssets(t)
	otherRoot := fleetAsset(t, model.KindPCIeRootPort, string(model.NamespacePCIBDF), "0000:62:00.0")
	switchAsset := fleetAsset(t, model.KindPCIeSwitch, string(model.NamespacePCIBDF), "0000:63:00.0")
	foreignNode := fleetAsset(t, model.KindKubernetesNode, string(model.NamespaceKubernetesNodeUID), "other-node-uid")
	cases := []struct {
		name   string
		assets []model.AssetRef
		edges  []graph.Edge
	}{
		{"wrong direction", []model.AssetRef{function, root, node}, []graph.Edge{fleetObservedEdge(t, root, function, model.RelLocatedIn), fleetObservedEdge(t, root, node, model.RelLocatedIn)}},
		{"wrong relation", []model.AssetRef{function, root, node}, []graph.Edge{fleetObservedEdge(t, function, root, model.RelConnectedTo), fleetObservedEdge(t, root, node, model.RelLocatedIn)}},
		{"cycle", []model.AssetRef{function, switchAsset, root, node}, []graph.Edge{fleetObservedEdge(t, function, switchAsset, model.RelLocatedIn), fleetObservedEdge(t, switchAsset, function, model.RelLocatedIn), fleetObservedEdge(t, switchAsset, root, model.RelLocatedIn), fleetObservedEdge(t, root, node, model.RelLocatedIn)}},
		{"multiple parents", []model.AssetRef{function, root, otherRoot, node}, []graph.Edge{fleetObservedEdge(t, function, root, model.RelLocatedIn), fleetObservedEdge(t, function, otherRoot, model.RelLocatedIn), fleetObservedEdge(t, root, node, model.RelLocatedIn), fleetObservedEdge(t, otherRoot, node, model.RelLocatedIn)}},
		{"foreign node", []model.AssetRef{function, root, foreignNode}, []graph.Edge{fleetObservedEdge(t, function, root, model.RelLocatedIn), fleetObservedEdge(t, root, foreignNode, model.RelLocatedIn)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := fleetReplaceTopology(t, fleetBundle(t, at, 0, fleet.DesiredInService), tc.assets, tc.edges)
			fleetAssertNoNormalPoint(t, bundle, at)
		})
	}
}

// GFL-020A/GFL-028: a contradiction on an unrelated function does not suppress
// the independently valid bound-device path.
func TestGFL_020A_028_UnrelatedContradictionPreservesValidDevice(t *testing.T) {
	at := fleetT0.Add(50 * time.Minute)
	function, root, node := fleetTopologyAssets(t)
	otherFunction := fleetAsset(t, model.KindPCIeFunction, string(model.NamespacePCIBDF), "0000:66:00.0")
	switchAsset := fleetAsset(t, model.KindPCIeSwitch, string(model.NamespacePCIBDF), "0000:63:00.0")
	edges := []graph.Edge{
		fleetObservedEdge(t, function, root, model.RelLocatedIn), fleetObservedEdge(t, root, node, model.RelLocatedIn),
		fleetObservedEdge(t, otherFunction, switchAsset, model.RelLocatedIn), fleetObservedEdge(t, switchAsset, otherFunction, model.RelLocatedIn),
	}
	bundle := fleetReplaceTopology(t, fleetBundle(t, at, 0, fleet.DesiredInService), []model.AssetRef{function, root, node, otherFunction, switchAsset}, edges)
	decision, err := fleet.EvaluateDevice(bundle, nil, at)
	if err != nil || !decision.AcceptedNormalPoint {
		t.Fatalf("unrelated contradiction suppressed valid device: %#v/%v", decision, err)
	}
}

// GFL-041A: empty and optional-only coverage policies are valid but cannot
// create path qualification, cursors, a ready window, or Ready.
func TestGFL_041A_NoRequiredCoverageCannotCreateReady(t *testing.T) {
	at := fleetT0.Add(50 * time.Minute)
	policies := []fleet.Policy{
		{Revision: "empty", Freshness: time.Minute, ReadyFor: time.Minute},
		{Revision: "optional", RequiredCoverage: []fleet.CoverageRequirement{{Name: "optional", PathKind: "gpu-nic-shared-ancestor", Required: false}}, Freshness: time.Minute, ReadyFor: time.Minute},
	}
	for _, raw := range policies {
		t.Run(raw.Revision, func(t *testing.T) {
			policy, err := fleet.NewPolicy(raw)
			if err != nil {
				t.Fatalf("NewPolicy: %v", err)
			}
			bundle := fleetBundle(t, at, 0, fleet.DesiredInService)
			bundle.Policy = policy
			decision, err := fleet.EvaluateDevice(bundle, nil, at)
			if err != nil {
				t.Fatalf("EvaluateDevice: %v", err)
			}
			if decision.Qualification != fleet.QualificationUnknown || decision.AcceptedNormalPoint || decision.Phase == fleet.PhaseReady || len(decision.CoverageCursors) != 0 || !decision.ReadyWindowStartedAt.IsZero() {
				t.Fatalf("no-required policy created readiness: %#v", decision)
			}
		})
	}
}

// GFL-041A/GFL-043/GFL-046: an empty required set keeps path qualification
// Unknown but does not block independently satisfied Maintenance/Retired completion.
func TestGFL_041A_NoRequiredCoveragePreservesLifecycleCompletion(t *testing.T) {
	at := fleetT0.Add(50 * time.Minute)
	for _, tc := range []struct {
		desired fleet.DesiredState
		phase   fleet.LifecyclePhase
	}{{fleet.DesiredMaintenance, fleet.PhaseMaintenanceReady}, {fleet.DesiredRetired, fleet.PhaseRetired}} {
		bundle := fleetCompletionBundle(t, at, tc.desired)
		bundle.Policy = fleet.Policy{Revision: "empty", Freshness: time.Minute, ReadyFor: time.Minute}
		decision, err := fleet.EvaluateDevice(bundle, nil, at)
		if err != nil || decision.Phase != tc.phase || decision.Qualification != fleet.QualificationUnknown || decision.AcceptedNormalPoint {
			t.Fatalf("%s no-required completion = %#v/%v", tc.desired.String(), decision, err)
		}
	}
}

// GFL-017/GFL-041/GFL-041A: missing or unsupported optional coverage does not
// block readiness when every Required=true coverage remains Normal.
func TestGFL_017_041_041A_OptionalCoverageDoesNotBlockReady(t *testing.T) {
	firstAt := fleetT0.Add(50 * time.Minute)
	firstBundle := fleetBundle(t, firstAt, 0, fleet.DesiredInService)
	firstBundle.Policy.RequiredCoverage = append(firstBundle.Policy.RequiredCoverage, fleet.CoverageRequirement{Name: "optional-nic", PathKind: "gpu-nic-shared-ancestor", Required: false})
	first, err := fleet.EvaluateDevice(firstBundle, nil, firstAt)
	if err != nil || !first.AcceptedNormalPoint {
		t.Fatalf("first optional point = %#v/%v", first, err)
	}
	secondAt := firstAt.Add(30 * time.Second)
	secondBundle := fleetBundle(t, secondAt, 1, fleet.DesiredInService)
	secondBundle.Policy.RequiredCoverage = append(secondBundle.Policy.RequiredCoverage, fleet.CoverageRequirement{Name: "optional-nic", PathKind: "gpu-nic-shared-ancestor", Required: false})
	second, err := fleet.EvaluateDevice(secondBundle, &first, secondAt)
	if err != nil || second.Phase != fleet.PhaseReady || second.Qualification != fleet.QualificationQualified {
		t.Fatalf("optional coverage blocked Ready: %#v/%v", second, err)
	}
}

func fleetReadyContinuity(t *testing.T, at time.Time) fleet.DeviceDecision {
	t.Helper()
	first, err := fleet.EvaluateDevice(fleetBundle(t, at, 0, fleet.DesiredInService), nil, at)
	if err != nil {
		t.Fatal(err)
	}
	secondAt := at.Add(30 * time.Second)
	second, err := fleet.EvaluateDevice(fleetBundle(t, secondAt, 1, fleet.DesiredInService), &first, secondAt)
	if err != nil || second.Phase != fleet.PhaseReady {
		t.Fatalf("ready setup = %#v/%v", second, err)
	}
	return second
}

// GFL-002/GFL-015/GFL-042: a new DeviceUID starts independently and cannot
// inherit the previous object's ready continuity.
func TestGFL_002_015_042_NewDeviceUIDCannotReuseContinuity(t *testing.T) {
	at := fleetT0.Add(50 * time.Minute)
	_ = fleetReadyContinuity(t, at)
	newBundle := fleetBundle(t, at.Add(time.Minute), 2, fleet.DesiredInService)
	newBundle.Intent.Device = fleet.DeviceRef{Name: "gpu-b", UID: "device-b"}
	decision, err := fleet.EvaluateDevice(newBundle, nil, at.Add(time.Minute))
	if err != nil || decision.Phase != fleet.PhaseValidating || !decision.AcceptedNormalPoint || decision.DeviceUID != "device-b" {
		t.Fatalf("new DeviceUID reused continuity: %#v/%v", decision, err)
	}
}

// GFL-042/GFL-045/GFL-047: changed policy and request accept qualifying
// post-intent evidence only as the first point of a new window.
func TestGFL_042_045_047_ChangedPolicyAndRequestStartNewWindow(t *testing.T) {
	start := fleetT0.Add(50 * time.Minute)
	ready := fleetReadyContinuity(t, start)
	at := start.Add(time.Minute)
	bundle := fleetBundle(t, at, 2, fleet.DesiredInService)
	bundle.Policy.Revision = "policy-2"
	bundle.Intent.RequestID = "request-2"
	bundle.Intent.MetadataGeneration = 4
	bundle.Intent.ObservedAt = at
	decision, err := fleet.EvaluateDevice(bundle, &ready, at)
	if err != nil || decision.Phase != fleet.PhaseValidating || !decision.AcceptedNormalPoint || !decision.ReadyWindowStartedAt.Equal(at) {
		t.Fatalf("changed policy/request did not start a new first point: %#v/%v", decision, err)
	}
}

func fleetWidthDimensions(root, peer model.AssetRef, peerKind, provenance string) map[string]string {
	return map[string]string{
		pcie.DimensionRootCanonical:      root.Canonical,
		pcie.DimensionPeerCanonical:      peer.Canonical,
		pcie.DimensionPeerKind:           peerKind,
		pcie.DimensionExpectedProvenance: provenance,
	}
}

func fleetWidthObservation(t *testing.T, id string, subject model.AssetRef, source model.SourceRef, signal model.SignalRef, value model.Value, at, expires time.Time, dimensions map[string]string) model.Observation {
	t.Helper()
	observation, err := model.NewObservation(model.Observation{
		ID: id, Source: source, Subject: subject, Signal: signal, Value: value, Unit: pcie.UnitLinkWidth,
		Dimensions: dimensions, ObservedAt: at, ReceivedAt: at, ExpiresAt: expires, Quality: model.QualityGood,
	})
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func fleetBundleWithObservations(t *testing.T, bundle fleet.AssessmentBundle, observations ...model.Observation) fleet.AssessmentBundle {
	t.Helper()
	partition := bundle.Topology.Partition()
	state, err := graph.NewState(partition, evidence.Config{MaxAge: time.Minute, Horizon: time.Hour, MaxSamples: 64})
	if err != nil {
		t.Fatal(err)
	}
	resync, err := graph.NewResync(partition, bundle.Snapshot.Sequence, bundle.Topology.Assets(), bundle.Topology.Edges())
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = state.Apply(resync)
	if err != nil {
		t.Fatal(err)
	}
	sequence := bundle.Snapshot.Sequence
	for _, observation := range observations {
		sequence++
		event, eventErr := graph.NewObservationAppend(partition, sequence, observation)
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		state, _, eventErr = state.Apply(event)
		if eventErr != nil {
			t.Fatal(eventErr)
		}
	}
	bundle.Topology = state.Snapshot()
	bundle.Window = bundle.Topology.Window()
	bundle.Snapshot.Sequence = sequence
	bundle.Admitted.Sequence = sequence
	bundle.Snapshot.PayloadDigest = fleetHex('6')
	bundle.Admitted.PayloadDigest = bundle.Snapshot.PayloadDigest
	return bundle
}

func fleetWidthBundle(t *testing.T, now time.Time, observations ...model.Observation) fleet.AssessmentBundle {
	t.Helper()
	bundle := fleetBundle(t, now, 0, fleet.DesiredInService)
	bundle.Policy.RequiredCoverage = []fleet.CoverageRequirement{{Name: "width", PathKind: "gpu-pcie-link-width-normal", Required: true}}
	source := bundle.Provenance[0].Source
	bundle.CollectorTrust.Sources = append(bundle.CollectorTrust.Sources, fleet.TrustedSource{Capability: fleet.TrustSysfsPCIeWidth, Source: source})
	return fleetBundleWithObservations(t, bundle, observations...)
}

// GFL-009/GFL-031: the ratified PCIe current/expected schema produces Normal
// only for a fresh explicit current>=expected pair on the physical peer.
func TestGFL_009_031_PCIeWidthNormalMeasurement(t *testing.T) {
	now := fleetT0.Add(50 * time.Minute)
	function, root, _ := fleetTopologyAssets(t)
	source := model.SourceRef{Type: string(model.SourceTypeAgent), Name: "collector"}
	dims := fleetWidthDimensions(root, root, model.KindPCIeRootPort.String(), pcie.ProvenanceAdjacentCapabilityMin)
	current := fleetWidthObservation(t, "width-current", function, source, pcie.SignalLinkWidthCurrent, model.NewIntValue(16), now, now.Add(time.Minute), dims)
	expected := fleetWidthObservation(t, "width-expected", function, source, pcie.SignalLinkWidthExpected, model.NewIntValue(16), now, now.Add(time.Minute), dims)
	bundle := fleetWidthBundle(t, now, current, expected)
	decision, err := fleet.EvaluateDevice(bundle, nil, now)
	if err != nil || !decision.AcceptedNormalPoint || len(decision.Coverage) != 1 || decision.Coverage[0].State != fleet.CoverageNormal {
		t.Fatalf("explicit normal pair = %#v/%v", decision, err)
	}
}

// GFL-009/GFL-031: degraded, missing, malformed, or stale latest pairs cannot
// be promoted to Normal or fall back to an older successful pair.
func TestGFL_009_031_PCIeWidthNonNormalMeasurements(t *testing.T) {
	now := fleetT0.Add(50 * time.Minute)
	function, root, _ := fleetTopologyAssets(t)
	source := model.SourceRef{Type: string(model.SourceTypeAgent), Name: "collector"}
	validDims := fleetWidthDimensions(root, root, model.KindPCIeRootPort.String(), pcie.ProvenanceAdjacentCapabilityMin)
	pair := func(at, expires time.Time, currentValue, expectedValue model.Value, dims map[string]string) []model.Observation {
		return []model.Observation{
			fleetWidthObservation(t, "current-"+at.String(), function, source, pcie.SignalLinkWidthCurrent, currentValue, at, expires, dims),
			fleetWidthObservation(t, "expected-"+at.String(), function, source, pcie.SignalLinkWidthExpected, expectedValue, at, expires, dims),
		}
	}
	extraDim := fleetWidthDimensions(root, root, model.KindPCIeRootPort.String(), pcie.ProvenanceAdjacentCapabilityMin)
	extraDim["extra"] = "value"
	wrongPeer := fleetWidthDimensions(root, root, model.KindPCIeSwitch.String(), pcie.ProvenanceAdjacentCapabilityMin)
	badProvenance := fleetWidthDimensions(root, root, model.KindPCIeRootPort.String(), "learned-current")
	cases := []struct {
		name         string
		observations []model.Observation
	}{
		{"current below expected", pair(now, now.Add(time.Minute), model.NewIntValue(8), model.NewIntValue(16), validDims)},
		{"missing expected", []model.Observation{fleetWidthObservation(t, "current-only", function, source, pcie.SignalLinkWidthCurrent, model.NewIntValue(16), now, now.Add(time.Minute), validDims)}},
		{"fractional scalar", pair(now, now.Add(time.Minute), model.NewFloatValue(8.5), model.NewIntValue(16), validDims)},
		{"extra dimension", pair(now, now.Add(time.Minute), model.NewIntValue(16), model.NewIntValue(16), extraDim)},
		{"peer kind mismatch", pair(now, now.Add(time.Minute), model.NewIntValue(16), model.NewIntValue(16), wrongPeer)},
		{"invalid expected provenance", pair(now, now.Add(time.Minute), model.NewIntValue(16), model.NewIntValue(16), badProvenance)},
		{"stale latest does not fall back", append(pair(now.Add(-30*time.Second), now.Add(time.Minute), model.NewIntValue(16), model.NewIntValue(16), validDims), pair(now.Add(-time.Second), now, model.NewIntValue(16), model.NewIntValue(16), validDims)...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fleetAssertNoNormalPoint(t, fleetWidthBundle(t, now, tc.observations...), now)
		})
	}
}

// GFL-085/GFL-086: a supported matching entry cannot hide a relevant
// unsupported resource or a matching workload observed outside its lifetime.
func TestGFL_085_086_MixedAllocationEntriesRemainUnknown(t *testing.T) {
	at := fleetT0.Add(50 * time.Minute)
	valid := fleetMatchingAllocationEntry(at, "valid")
	unsupported := fleetMatchingAllocationEntry(at, "unsupported")
	unsupported.ResourceName = "vendor.example/gpu"
	invalidLifetime := fleetMatchingAllocationEntry(at.Add(time.Second), "future")
	for _, tc := range []struct {
		name    string
		entries []fleet.AllocationEntry
	}{
		{"supported then unsupported", []fleet.AllocationEntry{valid, unsupported}},
		{"unsupported then supported", []fleet.AllocationEntry{unsupported, valid}},
		{"supported then invalid workload lifetime", []fleet.AllocationEntry{valid, invalidLifetime}},
		{"invalid workload lifetime then supported", []fleet.AllocationEntry{invalidLifetime, valid}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := fleetCompletionBundle(t, at, fleet.DesiredMaintenance)
			bundle.Allocation.Entries = tc.entries
			decision, err := fleet.EvaluateDevice(bundle, nil, at)
			if err != nil {
				t.Fatalf("EvaluateDevice: %v", err)
			}
			if decision.Allocation != fleet.AllocationUnknown || decision.Phase == fleet.PhaseMaintenanceReady {
				t.Fatalf("mixed invalid batch reported success: %#v", decision)
			}
		})
	}
}

// GFL-019/GFL-043: a supplied but untrusted fence remains an explicit
// unsuccessful completion input.
func TestGFL_019_043_UntrustedFenceIsNotSuccessful(t *testing.T) {
	at := fleetT0.Add(50 * time.Minute)
	bundle := fleetCompletionBundle(t, at, fleet.DesiredMaintenance)
	bundle.Fence.Source.Name = "untrusted-maintenance"
	decision, err := fleet.EvaluateDevice(bundle, nil, at)
	if err != nil || decision.Phase == fleet.PhaseMaintenanceReady || decision.Reason != "UntrustedSource" {
		t.Fatalf("untrusted fence reported success: %#v/%v", decision, err)
	}
}
