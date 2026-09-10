package tests_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

func fleetAsset(t *testing.T, kind model.AssetKind, namespace, value string) model.AssetRef {
	t.Helper()
	id, err := model.NewTypedID(namespace, value)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := model.NewAssetRef(kind, id)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func fleetEdge(t *testing.T, from, to model.AssetRef) graph.Edge {
	t.Helper()
	edge, err := graph.NewEdge(from, to, model.RelLocatedIn, model.OriginObserved)
	if err != nil {
		t.Fatal(err)
	}
	return edge
}

func fleetBundle(t *testing.T, at time.Time, sequence uint64, desired fleet.DesiredState) fleet.AssessmentBundle {
	t.Helper()
	nodeAsset := fleetAsset(t, model.KindKubernetesNode, string(model.NamespaceKubernetesNodeUID), "node-uid")
	function := fleetFunction(t)
	root := fleetAsset(t, model.KindPCIeRootPort, string(model.NamespacePCIBDF), "0000:64:00.0")
	toRoot := fleetEdge(t, function, root)
	toNode := fleetEdge(t, root, nodeAsset)
	partition, err := graph.PartitionFor(nodeAsset)
	if err != nil {
		t.Fatal(err)
	}
	state, err := graph.NewState(partition, evidence.Config{MaxAge: time.Minute, Horizon: time.Hour, MaxSamples: 16})
	if err != nil {
		t.Fatal(err)
	}
	resync, err := graph.NewResync(partition, sequence, []model.AssetRef{function, root, nodeAsset}, []graph.Edge{toRoot, toNode})
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = state.Apply(resync)
	if err != nil {
		t.Fatal(err)
	}
	revision := "bundle-1"
	if sequence > 0 {
		revision = "bundle-2"
	}
	source := model.SourceRef{Type: string(model.SourceTypeAgent), Name: "collector"}
	snapshot := fleet.SnapshotEnvelope{
		NodeUID: "node-uid", BootID: "boot-a", PayloadDigest: fleetHex('9'), BundleRevision: revision,
		Session: 2, Sequence: sequence, Completeness: fleet.CompletenessComplete, ObservedAt: at,
	}
	claim := fleet.InventoryClaim{Vendor: "NVIDIA", UUID: "GPU-abc", Serial: "serial-a", Source: "nvidia-smi", EvidenceID: "binding-ev"}
	binding := fleetBinding(t, at)
	binding.BundleRevision = revision
	binding.Source = source
	return fleet.AssessmentBundle{
		Policy: fleet.Policy{Revision: "policy-1", RequiredCoverage: []fleet.CoverageRequirement{
			{Name: "parent", PathKind: "gpu-pcie-parent", Required: true},
			{Name: "root", PathKind: "gpu-pcie-root", Required: true},
		}, Freshness: time.Minute, ReadyFor: 30 * time.Second},
		Intent: fleet.Intent{
			Device: fleet.DeviceRef{Name: "gpu-a", UID: "device-a"}, Node: fleet.NodeRef{ClusterID: "cluster-a", Name: "node-a", UID: "node-uid"},
			Desired: desired, RequestID: "request-1", MetadataGeneration: 3, ObservedAt: at.Add(-time.Minute), Claim: claim,
		},
		Snapshot: snapshot,
		Admitted: fleet.SnapshotCursor{
			NodeUID: snapshot.NodeUID, BootID: snapshot.BootID, PayloadDigest: snapshot.PayloadDigest, BundleRevision: snapshot.BundleRevision,
			Session: snapshot.Session, Sequence: snapshot.Sequence, Completeness: snapshot.Completeness, ObservedAt: snapshot.ObservedAt, Baseline: true,
		},
		GraphRevision: revision, WindowRevision: revision, TopologyDigest: fleetHex('a'), BaselineDigest: fleetHex('b'),
		Topology: state.Snapshot(), Window: state.Snapshot().Window(),
		Provenance: []fleet.EdgeProvenance{
			{Edge: toRoot, EvidenceID: "path-parent", BundleRevision: revision, CollectorProfileID: "profile-1", Kind: fleet.EdgeEvidenceObserved, Source: source, ObservedAt: at, ExpiresAt: at.Add(time.Minute)},
			{Edge: toNode, EvidenceID: "path-root", BundleRevision: revision, CollectorProfileID: "profile-1", Kind: fleet.EdgeEvidenceObserved, Source: source, ObservedAt: at, ExpiresAt: at.Add(time.Minute)},
		},
		Bindings: []fleet.ObservedBinding{binding}, FindingsEvaluatedAt: at, FindingsGraphRevision: revision,
		CollectorTrust: fleet.CollectorTrustProfile{
			ID: "profile-1", Mode: fleet.TrustModeLive, ClusterID: "cluster-a", NodeUID: "node-uid", Session: 2,
			Sources: []fleet.TrustedSource{
				{Capability: fleet.TrustNVIDIAUUIDBinding, Source: source},
				{Capability: fleet.TrustSysfsPhysicalParent, Source: source},
			},
		},
	}
}

// GFL-004/GFL-011/GFL-012/GFL-015/GFL-017/GFL-020/GFL-023/GFL-041/GFL-120/GFL-136:
// the first complete, admitted, trusted normal bundle binds the exact GPU and
// starts at Validating with one newly accepted composite normal point.
func TestGFL_004_011_012_015_017_020_023_041_120_127_136_FirstNormalBundleValidates(t *testing.T) {
	at := fleetT0.Add(10 * time.Minute)
	decision, err := fleet.EvaluateDevice(fleetBundle(t, at, 0, fleet.DesiredInService), nil, at)
	if err != nil {
		t.Fatalf("EvaluateDevice: %v", err)
	}
	if decision.BindingState != fleet.BindingBound || decision.Phase != fleet.PhaseValidating || decision.Qualification != fleet.QualificationUnknown || !decision.AcceptedNormalPoint {
		t.Fatalf("first normal decision = %#v", decision)
	}
	if len(decision.Coverage) != 2 || decision.Coverage[0].Name != "parent" || decision.Coverage[1].Name != "root" {
		t.Fatalf("coverage not byte-sorted/complete: %#v", decision.Coverage)
	}
	for _, coverage := range decision.Coverage {
		if coverage.State != fleet.CoverageNormal || len(coverage.EvidenceIDs) == 0 {
			t.Errorf("coverage = %#v, want Normal with evidence", coverage)
		}
	}
}

// GFL-007/GFL-041/GFL-042/GFL-124: cached evidence cannot advance the ready
// window; a second fresh composite point with strictly newer per-coverage times
// can complete ReadyFor without graph revision itself resetting continuity.
func TestGFL_007_041_042_124_DeviceReadyRequiresNewCompositePoints(t *testing.T) {
	firstAt := fleetT0.Add(10 * time.Minute)
	firstBundle := fleetBundle(t, firstAt, 0, fleet.DesiredInService)
	first, err := fleet.EvaluateDevice(firstBundle, nil, firstAt)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := fleet.EvaluateDevice(firstBundle, &first, firstAt.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if cached.AcceptedNormalPoint || cached.Phase == fleet.PhaseReady {
		t.Fatalf("cached bundle advanced Ready: %#v", cached)
	}
	secondAt := firstAt.Add(30 * time.Second)
	second, err := fleet.EvaluateDevice(fleetBundle(t, secondAt, 1, fleet.DesiredInService), &cached, secondAt)
	if err != nil {
		t.Fatal(err)
	}
	if !second.AcceptedNormalPoint || second.Phase != fleet.PhaseReady || second.Qualification != fleet.QualificationQualified || second.ValidUntil.IsZero() {
		t.Fatalf("fresh continuous bundle did not reach Ready: %#v", second)
	}
	readyDeadline := second.ValidUntil
	readyCached, err := fleet.EvaluateDevice(fleetBundle(t, secondAt, 1, fleet.DesiredInService), &second, secondAt.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if readyCached.Phase != fleet.PhaseReady || readyCached.Qualification != fleet.QualificationQualified || readyCached.AcceptedNormalPoint || !readyCached.ValidUntil.Equal(readyDeadline) {
		t.Fatalf("fresh cached Ready changed continuity/deadline: %#v", readyCached)
	}
	expired, err := fleet.EvaluateDevice(fleetBundle(t, secondAt, 1, fleet.DesiredInService), &readyCached, readyDeadline)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Phase == fleet.PhaseReady || expired.Qualification == fleet.QualificationQualified || expired.AcceptedNormalPoint {
		t.Fatalf("expired cached bundle remained Ready: %#v", expired)
	}

	// Returned slices are copies: mutating them cannot corrupt previous opaque continuity.
	second.Coverage[0].EvidenceIDs[0] = "caller-mutated"
	again, err := fleet.EvaluateDevice(fleetBundle(t, secondAt.Add(30*time.Second), 2, fleet.DesiredInService), &cached, secondAt.Add(30*time.Second))
	if err != nil || len(again.Coverage) == 0 || again.Coverage[0].EvidenceIDs[0] == "caller-mutated" {
		t.Fatalf("decision shared nested result storage: %v / %#v", err, again)
	}
}

// GFL-011/GFL-024/GFL-040/GFL-122: partial or unadmitted snapshots are Unknown
// and cannot produce normal points or lifecycle completion.
func TestGFL_011_024_040_122_PartialAndUnadmittedSnapshotsStayUnknown(t *testing.T) {
	at := fleetT0.Add(10 * time.Minute)
	cases := []struct {
		name       string
		edit       func(*fleet.AssessmentBundle)
		wantReason string
	}{
		{"partial", func(b *fleet.AssessmentBundle) {
			b.Snapshot.Completeness = fleet.CompletenessPartial
			b.Admitted.Completeness = fleet.CompletenessPartial
		}, ""},
		{"not admitted", func(b *fleet.AssessmentBundle) { b.Admitted.Baseline = false }, "UnadmittedSnapshot"},
		{"admitted digest mismatch", func(b *fleet.AssessmentBundle) { b.Admitted.PayloadDigest = "different" }, "UnadmittedSnapshot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := fleetBundle(t, at, 0, fleet.DesiredInService)
			tc.edit(&bundle)
			got, err := fleet.EvaluateDevice(bundle, nil, at)
			if err != nil {
				t.Fatalf("EvaluateDevice: %v", err)
			}
			if got.Qualification != fleet.QualificationUnknown || got.Phase != fleet.PhasePending || got.AcceptedNormalPoint {
				t.Fatalf("decision = %#v, want Pending/Unknown with no point", got)
			}
			if tc.wantReason != "" && got.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// GFL-004/GFL-012/GFL-013/GFL-137: missing trust capability and future
// observations stay Unknown and do not start continuity.
func TestGFL_004_012_013_137_IdentityTrustAndFuturePrecedence(t *testing.T) {
	at := fleetT0.Add(10 * time.Minute)
	untrusted := fleetBundle(t, at, 0, fleet.DesiredInService)
	untrusted.CollectorTrust.Sources = untrusted.CollectorTrust.Sources[1:]
	got, err := fleet.EvaluateDevice(untrusted, nil, at)
	if err != nil || got.BindingState != fleet.BindingUnknown || got.Qualification != fleet.QualificationUnknown || got.Reason != "UntrustedSource" || got.AcceptedNormalPoint {
		t.Fatalf("untrusted identity = %#v/%v", got, err)
	}

	future := fleetBundle(t, at.Add(time.Minute), 0, fleet.DesiredInService)
	got, err = fleet.EvaluateDevice(future, nil, at)
	if err != nil || got.Qualification != fleet.QualificationUnknown || got.Reason != "FutureObservation" || got.AcceptedNormalPoint {
		t.Fatalf("future observation = %#v/%v", got, err)
	}
}

// GFL-009/GFL-021/GFL-031/GFL-040: unsupported GPU/NIC adjacency and a width
// requirement without an operator or physical-link baseline cannot become Normal.
func TestGFL_009_021_031_040_UnsupportedAndUnprovenCoverage(t *testing.T) {
	at := fleetT0.Add(10 * time.Minute)
	cases := []struct {
		name     string
		pathKind string
		addWidth bool
	}{
		{"unsupported nic shared ancestor", "gpu-nic-shared-ancestor", false},
		{"width has capability but no baseline", "gpu-pcie-link-width-normal", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := fleetBundle(t, at, 0, fleet.DesiredInService)
			bundle.Policy.RequiredCoverage = []fleet.CoverageRequirement{{Name: "required", PathKind: tc.pathKind, Required: true}}
			if tc.addWidth {
				source := bundle.CollectorTrust.Sources[0].Source
				bundle.CollectorTrust.Sources = append(bundle.CollectorTrust.Sources, fleet.TrustedSource{Capability: fleet.TrustSysfsPCIeWidth, Source: source})
			}
			got, err := fleet.EvaluateDevice(bundle, nil, at)
			if err != nil {
				t.Fatalf("EvaluateDevice: %v", err)
			}
			if got.Qualification == fleet.QualificationQualified || got.Phase == fleet.PhaseReady || got.AcceptedNormalPoint || len(got.Coverage) != 1 || got.Coverage[0].State == fleet.CoverageNormal {
				t.Fatalf("unproven coverage became normal/ready: %#v", got)
			}
		})
	}
}

// GFL-022/GFL-023/GFL-040: intended sidecars and expired observed provenance
// are not fresh observed qualification evidence.
func TestGFL_022_023_040_ProvenanceKindAndExpiry(t *testing.T) {
	at := fleetT0.Add(10 * time.Minute)
	cases := []struct {
		name string
		edit func(*fleet.AssessmentBundle)
	}{
		{"intended", func(b *fleet.AssessmentBundle) { b.Provenance[0].Kind = fleet.EdgeEvidenceIntended }},
		{"expired at boundary", func(b *fleet.AssessmentBundle) {
			b.Provenance[0].ObservedAt = at.Add(-time.Second)
			b.Provenance[0].ExpiresAt = at
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := fleetBundle(t, at, 0, fleet.DesiredInService)
			tc.edit(&bundle)
			got, err := fleet.EvaluateDevice(bundle, nil, at)
			if err != nil {
				t.Fatalf("EvaluateDevice: %v", err)
			}
			if got.Qualification == fleet.QualificationQualified || got.Phase == fleet.PhaseReady || got.AcceptedNormalPoint {
				t.Fatalf("non-fresh observed provenance qualified: %#v", got)
			}
		})
	}

	t.Run("impossible expiry order is invalid", func(t *testing.T) {
		bundle := fleetBundle(t, at, 0, fleet.DesiredInService)
		bundle.Provenance[0].ExpiresAt = bundle.Provenance[0].ObservedAt
		got, err := fleet.EvaluateDevice(bundle, nil, at)
		if !errors.Is(err, fleet.ErrInvalidInput) || !reflect.DeepEqual(got, fleet.DeviceDecision{}) {
			t.Fatalf("got %#v/%v, want zero/ErrInvalidInput", got, err)
		}
	})
}

// GFL-051/GFL-122: stale finding evaluation invalidates required coverage and
// cannot be treated as proof that no active finding exists.
func TestGFL_051_122_StaleFindingEvaluationIsUnknown(t *testing.T) {
	at := fleetT0.Add(10 * time.Minute)
	bundle := fleetBundle(t, at, 0, fleet.DesiredInService)
	bundle.FindingsEvaluatedAt = at.Add(-bundle.Policy.Freshness)
	got, err := fleet.EvaluateDevice(bundle, nil, at)
	if err != nil {
		t.Fatalf("EvaluateDevice: %v", err)
	}
	if got.Qualification != fleet.QualificationUnknown || got.Phase == fleet.PhaseReady || got.AcceptedNormalPoint {
		t.Fatalf("stale finding evaluation was accepted: %#v", got)
	}
}

// GFL-040/GFL-051/GFL-121: a fresh active path finding attached to the current
// graph revision takes precedence over otherwise normal coverage.
func TestGFL_040_051_121_ActivePathFindingDegradesDevice(t *testing.T) {
	at := fleetT0.Add(10 * time.Minute)
	finding, err := model.NewFinding(model.Finding{
		ID: "finding-width", Type: model.FindingPCIeLinkWidthDegraded, Scope: []model.AssetRef{fleetFunction(t)},
		Severity: model.SeverityWarning, Confidence: model.ConfidenceHigh, State: model.StateActive,
		Evidence:  []model.EvidenceRef{{ObservationID: "width-ev", Summary: "current width below expected"}},
		FirstSeen: at.Add(-time.Minute), LastSeen: at, Explanation: "PCIe width is degraded",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("linked current evidence degrades", func(t *testing.T) {
		bundle := fleetBundle(t, at, 0, fleet.DesiredInService)
		observation, obsErr := model.NewObservation(model.Observation{
			ID: "width-ev", Source: bundle.Provenance[0].Source, Subject: fleetFunction(t), Signal: model.SignalRef("pcie.link.width"),
			Value: model.NewIntValue(8), Unit: "lanes", ObservedAt: at, ReceivedAt: at, ExpiresAt: at.Add(time.Minute), Quality: model.QualityDegraded,
		})
		if obsErr != nil {
			t.Fatal(obsErr)
		}
		bundle = fleetBundleWithObservation(t, bundle, observation)
		bundle.Findings = []model.Finding{finding}
		got, evalErr := fleet.EvaluateDevice(bundle, nil, at)
		if evalErr != nil {
			t.Fatalf("EvaluateDevice: %v", evalErr)
		}
		if got.Phase != fleet.PhaseDegraded || got.Qualification != fleet.QualificationDisqualified || len(got.FindingIDs) != 1 || got.FindingIDs[0] != "finding-width" || got.AcceptedNormalPoint {
			t.Fatalf("active finding decision = %#v", got)
		}
	})

	t.Run("unlinked finding is unknown", func(t *testing.T) {
		bundle := fleetBundle(t, at, 0, fleet.DesiredInService)
		bundle.Findings = []model.Finding{finding}
		got, evalErr := fleet.EvaluateDevice(bundle, nil, at)
		if evalErr != nil {
			t.Fatalf("EvaluateDevice: %v", evalErr)
		}
		if got.Phase == fleet.PhaseDegraded || got.Qualification != fleet.QualificationUnknown || got.AcceptedNormalPoint {
			t.Fatalf("unlinked finding was treated as current bad evidence: %#v", got)
		}
	})
}

func fleetBundleWithObservation(t *testing.T, bundle fleet.AssessmentBundle, observation model.Observation) fleet.AssessmentBundle {
	t.Helper()
	partition := bundle.Topology.Partition()
	state, err := graph.NewState(partition, evidence.Config{MaxAge: time.Minute, Horizon: time.Hour, MaxSamples: 16})
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
	sequence := bundle.Snapshot.Sequence + 1
	appendEvent, err := graph.NewObservationAppend(partition, sequence, observation)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = state.Apply(appendEvent)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Topology = state.Snapshot()
	bundle.Window = bundle.Topology.Window()
	bundle.Snapshot.Sequence = sequence
	bundle.Admitted.Sequence = sequence
	bundle.Snapshot.PayloadDigest = fleetHex('7')
	bundle.Admitted.PayloadDigest = bundle.Snapshot.PayloadDigest
	return bundle
}

func fleetCompletionBundle(t *testing.T, at time.Time, desired fleet.DesiredState) fleet.AssessmentBundle {
	t.Helper()
	bundle := fleetBundle(t, at, 0, desired)
	source := model.SourceRef{Type: string(model.SourceTypeAgent), Name: "podresources"}
	bundle.CollectorTrust.Sources = append(bundle.CollectorTrust.Sources, fleet.TrustedSource{Capability: fleet.TrustPodResourcesUUIDAllocation, Source: source})
	bundle.Allocation = &fleet.AllocationBatch{
		NodeUID: "node-uid", BootID: "boot-a", BundleRevision: bundle.GraphRevision, EvidenceDigest: fleetHex('8'), Session: 2, Sequence: 0,
		ObservedAt: at, ExpiresAt: at.Add(time.Minute), Complete: true, EvidenceRefs: []string{"allocation-ev"},
		Profile: fleet.AllocationNVIDIAPodResourcesUUID, CollectorProfileID: "profile-1",
	}
	fenceSource := model.SourceRef{Type: string(model.SourceTypeAgent), Name: "maintenance"}
	bundle.FenceTrust = &fleet.FenceTrustProfile{ID: "fence-profile", ClusterID: "cluster-a", Issuer: "maintenance", Source: fenceSource}
	bundle.Fence = &fleet.FenceAcknowledgement{
		Node: bundle.Intent.Node, DeviceUID: bundle.Intent.Device.UID, BootID: bundle.Snapshot.BootID, Session: bundle.Snapshot.Session,
		RequestID: bundle.Intent.RequestID, EvidenceID: "fence-ev", Source: fenceSource, MetadataGeneration: bundle.Intent.MetadataGeneration,
		State: fleet.FenceAcknowledged, ObservedAt: at, ExpiresAt: at.Add(time.Minute), TrustProfileID: "fence-profile",
	}
	return bundle
}

// GFL-018/GFL-043/GFL-044/GFL-046/GFL-047/GFL-085/GFL-087/GFL-123/GFL-125:
// Maintenance and Retired completion require the same current request's fresh,
// trusted fence plus a complete allocation-empty batch.
func TestGFL_018_019_043_044_046_047_085_087_088_123_125_138_LifecycleCompletion(t *testing.T) {
	at := fleetT0.Add(10 * time.Minute)
	for _, tc := range []struct {
		desired fleet.DesiredState
		pending fleet.LifecyclePhase
		ready   fleet.LifecyclePhase
	}{
		{fleet.DesiredMaintenance, fleet.PhaseMaintenancePending, fleet.PhaseMaintenanceReady},
		{fleet.DesiredRetired, fleet.PhaseRetiring, fleet.PhaseRetired},
	} {
		t.Run(tc.desired.String(), func(t *testing.T) {
			pending, err := fleet.EvaluateDevice(fleetBundle(t, at, 0, tc.desired), nil, at)
			if err != nil || pending.Phase != tc.pending {
				t.Fatalf("without completion evidence = %#v/%v, want %s", pending, err, tc.pending.String())
			}
			completeBundle := fleetCompletionBundle(t, at, tc.desired)
			complete, err := fleet.EvaluateDevice(completeBundle, nil, at)
			if err != nil || complete.Phase != tc.ready || complete.Allocation != fleet.AllocationEmpty {
				t.Fatalf("with empty allocation/fence = %#v/%v, want %s/Empty", complete, err, tc.ready.String())
			}
			mismatch := fleetCompletionBundle(t, at, tc.desired)
			mismatch.Fence.RequestID = "old-request"
			blocked, err := fleet.EvaluateDevice(mismatch, nil, at)
			if err != nil || blocked.Phase != tc.pending {
				t.Fatalf("old request fence completed lifecycle: %#v/%v", blocked, err)
			}
			untrusted := fleetCompletionBundle(t, at, tc.desired)
			untrusted.Fence.Source.Name = "untrusted-maintenance"
			blocked, err = fleet.EvaluateDevice(untrusted, nil, at)
			if err != nil || blocked.Phase != tc.pending || blocked.Reason != "UntrustedSource" {
				t.Fatalf("untrusted fence completed lifecycle: %#v/%v", blocked, err)
			}
			mismatchedAllocation := fleetCompletionBundle(t, at, tc.desired)
			mismatchedAllocation.Allocation.Sequence++
			blocked, err = fleet.EvaluateDevice(mismatchedAllocation, nil, at)
			if err != nil || blocked.Phase != tc.pending {
				t.Fatalf("mismatched allocation completed lifecycle: %#v/%v", blocked, err)
			}
		})
	}
}

func fleetMatchingAllocationEntry(at time.Time, suffix string) fleet.AllocationEntry {
	return fleet.AllocationEntry{
		ResourceName: "nvidia.com/gpu", DeviceID: "GPU-abc",
		Workload: fleet.WorkloadRef{Namespace: "default", Name: "pod-" + suffix, UID: "pod-uid-" + suffix, Container: "worker", CreatedAt: at},
	}
}

// GFL-018/GFL-043/GFL-044/GFL-085/GFL-086/GFL-087/GFL-123: one or repeated
// entries for the exact bound UUID are InUse. Unsupported, partial, stale, or
// pre-intent allocation evidence and an expired fence cannot complete lifecycle.
func TestGFL_018_043_044_085_086_087_123_AllocationBoundaries(t *testing.T) {
	at := fleetT0.Add(10 * time.Minute)

	t.Run("one and repeated bound UUID entries are in use", func(t *testing.T) {
		for _, entries := range [][]fleet.AllocationEntry{
			{fleetMatchingAllocationEntry(at, "a")},
			{fleetMatchingAllocationEntry(at, "a"), fleetMatchingAllocationEntry(at, "b")},
		} {
			bundle := fleetCompletionBundle(t, at, fleet.DesiredMaintenance)
			bundle.Allocation.Entries = entries
			got, err := fleet.EvaluateDevice(bundle, nil, at)
			if err != nil {
				t.Fatalf("EvaluateDevice: %v", err)
			}
			if got.Allocation != fleet.AllocationInUse || got.Phase != fleet.PhaseMaintenancePending {
				t.Fatalf("matching entries = %#v", got)
			}
		}
	})

	cases := []struct {
		name string
		edit func(*fleet.AssessmentBundle)
	}{
		{"unsupported profile", func(b *fleet.AssessmentBundle) { b.Allocation.Profile = fleet.AllocationUnsupported }},
		{"unsupported resource", func(b *fleet.AssessmentBundle) {
			entry := fleetMatchingAllocationEntry(at, "a")
			entry.ResourceName = "vendor.example/gpu"
			b.Allocation.Entries = []fleet.AllocationEntry{entry}
		}},
		{"partial batch", func(b *fleet.AssessmentBundle) { b.Allocation.Complete = false }},
		{"stale batch", func(b *fleet.AssessmentBundle) {
			b.Allocation.ObservedAt = at.Add(-time.Second)
			b.Allocation.ExpiresAt = at
		}},
		{"pre-intent batch", func(b *fleet.AssessmentBundle) {
			b.Policy.Freshness = 2 * time.Minute
			b.Allocation.ObservedAt = b.Intent.ObservedAt.Add(-time.Second)
			b.Allocation.ExpiresAt = at.Add(time.Minute)
		}},
		{"expired fence", func(b *fleet.AssessmentBundle) {
			b.Fence.ObservedAt = at.Add(-time.Second)
			b.Fence.ExpiresAt = at
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := fleetCompletionBundle(t, at, fleet.DesiredMaintenance)
			tc.edit(&bundle)
			got, err := fleet.EvaluateDevice(bundle, nil, at)
			if err != nil {
				t.Fatalf("EvaluateDevice: %v", err)
			}
			if got.Phase == fleet.PhaseMaintenanceReady {
				t.Fatalf("insufficient allocation/fence completed lifecycle: %#v", got)
			}
		})
	}
}

// GFL-042/GFL-045/GFL-047/GFL-124: a new InService request after maintenance
// cannot reuse the old request's completed lifecycle or ready continuity.
func TestGFL_042_045_047_124_NewInServiceRequestStartsNewWindow(t *testing.T) {
	at := fleetT0.Add(10 * time.Minute)
	maintenance, err := fleet.EvaluateDevice(fleetCompletionBundle(t, at, fleet.DesiredMaintenance), nil, at)
	if err != nil || maintenance.Phase != fleet.PhaseMaintenanceReady {
		t.Fatalf("maintenance setup = %#v/%v", maintenance, err)
	}
	newRequest := fleetBundle(t, at.Add(time.Second), 1, fleet.DesiredInService)
	newRequest.Intent.RequestID = "request-2"
	newRequest.Intent.MetadataGeneration = 4
	newRequest.Intent.ObservedAt = at.Add(time.Second)
	got, err := fleet.EvaluateDevice(newRequest, &maintenance, at.Add(time.Second))
	if err != nil {
		t.Fatalf("EvaluateDevice: %v", err)
	}
	if got.Phase != fleet.PhaseValidating || got.Qualification == fleet.QualificationQualified || !got.AcceptedNormalPoint || got.RequestID != "request-2" || got.MetadataGeneration != 4 {
		t.Fatalf("new request reused old completion/continuity: %#v", got)
	}
}
