package tests_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

const (
	fleetTaintKey    = "infrastructure.data-path-assurance.io/gpu-path"
	fleetTaintValue  = "not-ready"
	fleetTaintEffect = "NoSchedule"
)

func fleetHex(ch byte) string { return strings.Repeat(string(ch), 64) }

func fleetFunction(t *testing.T) model.AssetRef {
	t.Helper()
	id, err := model.NewTypedID(string(model.NamespacePCIBDF), "0000:65:00.0")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := model.NewAssetRef(model.KindPCIeFunction, id)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func fleetBinding(t *testing.T, at time.Time) fleet.ObservedBinding {
	t.Helper()
	return fleet.ObservedBinding{
		Node: fleet.NodeRef{ClusterID: "cluster-a", Name: "node-a", UID: "node-uid"}, BootID: "boot-a", BDF: "0000:65:00.0",
		Function: fleetFunction(t), Claim: fleet.InventoryClaim{Vendor: "NVIDIA", UUID: "GPU-abc", Serial: "serial-a", Source: "nvidia-smi", EvidenceID: "binding-ev"},
		Source: model.SourceRef{Type: string(model.SourceTypeAgent), Name: "inventory"}, EvidenceID: "binding-ev",
		BundleRevision: "bundle-1", CollectorProfileID: "profile-1", ObservedAt: at, ExpiresAt: at.Add(time.Minute),
	}
}

func fleetReadyDevice(t *testing.T, at time.Time, uid string) fleet.DeviceDecision {
	t.Helper()
	binding := fleetBinding(t, at)
	bindingKey := "NVIDIA\x00GPU-abc\x00node-uid\x00boot-a\x000000:65:00.0"
	return fleet.DeviceDecision{
		Desired: fleet.DesiredInService, Phase: fleet.PhaseReady, Qualification: fleet.QualificationQualified,
		BindingState: fleet.BindingBound, Binding: binding, Allocation: fleet.AllocationUnknown,
		AcceptedNormalPoint: true, GraphRevision: "bundle-1",
		Coverage: []fleet.CoverageAssessment{{
			Name: "parent", PathKind: "gpu-pcie-parent", State: fleet.CoverageNormal, EvidenceIDs: []string{"path-ev"},
			ObservedAt: at, LatestObservedAt: at, ExpiresAt: at.Add(time.Minute), AssessmentSequence: 4,
		}},
		EvidenceIDs: []string{"binding-ev", "path-ev"}, EvaluatedAt: at, ValidUntil: at.Add(time.Minute),
		PolicyRevision: "policy-1", RequestID: "request-1", DeviceUID: uid, NodeUID: "node-uid", BootID: "boot-a",
		BindingKey: bindingKey, TopologyDigest: fleetHex('a'), BaselineDigest: fleetHex('b'), MetadataGeneration: 3, Session: 2,
		IntentObservedAt: at.Add(-time.Minute), ReadyWindowStartedAt: at.Add(-time.Minute), LastCompositeMin: at, LastCompositeMax: at,
		CoverageCursors: []fleet.CoverageCursor{{Name: "parent", ObservedAt: at, AssessmentSequence: 4, EvidenceDigest: fleetHex('c')}},
	}
}

// GFL-053/GFL-054/GFL-124: a complete, nonempty, internally consistent set of
// current Ready devices is eligible and yields one order-independent node point.
func TestGFL_053_054_124_AggregateNodeCompleteReadySet(t *testing.T) {
	now := fleetT0.Add(10 * time.Minute)
	a := fleetReadyDevice(t, now, "device-a")
	b := fleetReadyDevice(t, now.Add(time.Second), "device-b")
	input := fleet.AggregateNodeInput{
		Node: fleet.NodeRef{ClusterID: "cluster-a", Name: "node-a", UID: "node-uid"}, FleetUID: "fleet-uid", Selection: fleet.SelectionComplete,
		Devices: []fleet.DeviceAggregate{
			{DeviceUID: "device-b", NodeUID: "node-uid", Desired: fleet.DesiredInService, MetadataGeneration: 3, Decision: b},
			{DeviceUID: "device-a", NodeUID: "node-uid", Desired: fleet.DesiredInService, MetadataGeneration: 3, Decision: a},
		},
	}
	decision, err := fleet.AggregateNode(input, nil, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("AggregateNode: %v", err)
	}
	if decision.Qualification != fleet.QualificationQualified || decision.Eligibility != fleet.EligibilityEligible || decision.Selection != fleet.SelectionComplete || decision.DeviceCount != 2 {
		t.Fatalf("aggregate = %#v", decision)
	}
	if decision.NormalPointDigest == "" || !decision.NormalPointAt.Equal(now) || decision.AssessmentRevision == "" {
		t.Fatalf("normal point/revision missing: %#v", decision)
	}
	if !decision.ValidUntil.Equal(a.ValidUntil) {
		t.Fatalf("ValidUntil = %s, want minimum %s", decision.ValidUntil, a.ValidUntil)
	}

	reversed := input
	reversed.Devices = []fleet.DeviceAggregate{input.Devices[1], input.Devices[0]}
	again, err := fleet.AggregateNode(reversed, nil, now.Add(2*time.Second))
	if err != nil || !reflect.DeepEqual(decision, again) {
		t.Fatalf("input order changed result: %v\n%#v\n%#v", err, decision, again)
	}
}

// GFL-050/GFL-053/GFL-054/GFL-130: partial, conflicting, and empty selections
// remain Unknown and never synthesize a normal point.
func TestGFL_050_053_054_130_AggregateNodeSelectionPrecedence(t *testing.T) {
	now := fleetT0.Add(10 * time.Minute)
	for _, selection := range []fleet.SelectionState{fleet.SelectionPartial, fleet.SelectionConflict, fleet.SelectionNoDevices} {
		t.Run(selection.String(), func(t *testing.T) {
			input := fleet.AggregateNodeInput{Node: fleet.NodeRef{ClusterID: "cluster-a", Name: "node-a", UID: "node-uid"}, FleetUID: "fleet-uid", Selection: selection}
			if selection != fleet.SelectionNoDevices {
				d := fleetReadyDevice(t, now, "device-a")
				input.Devices = []fleet.DeviceAggregate{{DeviceUID: "device-a", NodeUID: "node-uid", Desired: d.Desired, MetadataGeneration: d.MetadataGeneration, Decision: d}}
			}
			got, err := fleet.AggregateNode(input, nil, now)
			if err != nil {
				t.Fatalf("AggregateNode: %v", err)
			}
			if got.Qualification != fleet.QualificationUnknown || got.Eligibility != fleet.EligibilityUnknown || got.Selection != selection || got.NormalPointDigest != "" || !got.NormalPointAt.IsZero() {
				t.Fatalf("selection %s aggregate = %#v", selection.String(), got)
			}
		})
	}
}

// GFL-053/GFL-054/GFL-124: zero ValidUntil on a Disqualified child preserves
// Disqualified; expired or future Qualified children become Unknown.
func TestGFL_053_054_124_AggregateNodeDeadlineAndChildTime(t *testing.T) {
	now := fleetT0.Add(10 * time.Minute)
	base := fleetReadyDevice(t, now, "device-a")
	cases := []struct {
		name string
		edit func(*fleet.DeviceDecision)
		want fleet.Qualification
	}{
		{"disqualified zero deadline", func(d *fleet.DeviceDecision) {
			d.Qualification = fleet.QualificationDisqualified
			d.Phase = fleet.PhaseDegraded
			d.ValidUntil = time.Time{}
			d.AcceptedNormalPoint = false
		}, fleet.QualificationDisqualified},
		{"qualified expired", func(d *fleet.DeviceDecision) { d.ValidUntil = now }, fleet.QualificationUnknown},
		{"qualified future evaluation", func(d *fleet.DeviceDecision) { d.EvaluatedAt = now.Add(time.Second) }, fleet.QualificationUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := base
			tc.edit(&d)
			input := fleet.AggregateNodeInput{Node: d.Binding.Node, FleetUID: "fleet-uid", Selection: fleet.SelectionComplete, Devices: []fleet.DeviceAggregate{{DeviceUID: d.DeviceUID, NodeUID: d.NodeUID, Desired: d.Desired, MetadataGeneration: d.MetadataGeneration, Decision: d}}}
			got, err := fleet.AggregateNode(input, nil, now)
			if err != nil {
				t.Fatalf("AggregateNode: %v", err)
			}
			if got.Qualification != tc.want || !got.ValidUntil.IsZero() || got.NormalPointDigest != "" {
				t.Fatalf("aggregate = %#v, want qualification %s with no deadline/point", got, tc.want.String())
			}
		})
	}
}

func fleetCleanupPolicy() fleet.CleanupPolicySnapshot {
	return fleet.CleanupPolicySnapshot{PolicyVersion: "policy-1", RequiredCoverage: []fleet.CoverageRequirement{{Name: "parent", PathKind: "gpu-pcie-parent", Required: true}}, Freshness: time.Minute, ReadyFor: time.Minute}
}

func fleetOwnership(phase fleet.CleanupPhase) fleet.GateOwnership {
	return fleet.GateOwnership{OwnerFleetUID: "fleet-uid", NodeUID: "node-uid", Key: fleetTaintKey, Value: fleetTaintValue, Effect: fleetTaintEffect, Policy: fleetCleanupPolicy(), Phase: phase}
}

func fleetGateNode(now time.Time, eligibility fleet.Eligibility, qualification fleet.Qualification, selection fleet.SelectionState) fleet.NodeDecision {
	return fleet.NodeDecision{NodeUID: "node-uid", FleetUID: "fleet-uid", Qualification: qualification, Eligibility: eligibility, Selection: selection, DeviceCount: 1, AssessmentRevision: fleetHex('d'), EvaluatedAt: now, ValidUntil: now.Add(time.Minute)}
}

// GFL-015/GFL-066/GFL-068/GFL-079/GFL-130/GFL-134: gate precedence is
// fail-closed, Audit is side-effect free, and conflict/no-device forbid a new Add.
func TestGFL_015_066_068_079_130_134_EvaluateGatePrecedence(t *testing.T) {
	now := fleetT0.Add(20 * time.Minute)
	node := fleet.NodeRef{ClusterID: "cluster-a", Name: "node-a", UID: "node-uid"}
	policy := fleetCleanupPolicy()
	cases := []struct {
		name      string
		input     fleet.GateInput
		want      fleet.GateAction
		wantOwner bool
	}{
		{"audit", fleet.GateInput{Mode: fleet.GateModeAudit, FleetUID: "fleet-uid", ControllerID: "controller-a", Node: node, Policy: policy, NodeDecision: fleetGateNode(now, fleet.EligibilityUnknown, fleet.QualificationUnknown, fleet.SelectionPartial)}, fleet.GateActionNone, false},
		{"unknown enforce adds", fleet.GateInput{Mode: fleet.GateModeEnforce, FleetUID: "fleet-uid", ControllerID: "controller-a", Node: node, Policy: policy, NodeDecision: fleetGateNode(now, fleet.EligibilityUnknown, fleet.QualificationUnknown, fleet.SelectionPartial)}, fleet.GateActionAdd, true},
		{"ineligible enforce adds", fleet.GateInput{Mode: fleet.GateModeEnforce, FleetUID: "fleet-uid", ControllerID: "controller-a", Node: node, Policy: policy, NodeDecision: fleetGateNode(now, fleet.EligibilityIneligible, fleet.QualificationQualified, fleet.SelectionComplete)}, fleet.GateActionAdd, true},
		{"fleet conflict blocks add", fleet.GateInput{Mode: fleet.GateModeEnforce, FleetUID: "fleet-uid", ControllerID: "controller-a", Node: node, FleetConflict: true, Policy: policy, NodeDecision: fleetGateNode(now, fleet.EligibilityUnknown, fleet.QualificationUnknown, fleet.SelectionPartial)}, fleet.GateActionNone, false},
		{"selection conflict blocks add", fleet.GateInput{Mode: fleet.GateModeEnforce, FleetUID: "fleet-uid", ControllerID: "controller-a", Node: node, Policy: policy, NodeDecision: fleetGateNode(now, fleet.EligibilityUnknown, fleet.QualificationUnknown, fleet.SelectionConflict)}, fleet.GateActionNone, false},
		{"no devices blocks add", fleet.GateInput{Mode: fleet.GateModeEnforce, FleetUID: "fleet-uid", ControllerID: "controller-a", Node: node, Policy: policy, NodeDecision: fleetGateNode(now, fleet.EligibilityUnknown, fleet.QualificationUnknown, fleet.SelectionNoDevices)}, fleet.GateActionNone, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fleet.EvaluateGate(tc.input, nil, now)
			if err != nil {
				t.Fatalf("EvaluateGate: %v", err)
			}
			if got.Action != tc.want {
				t.Fatalf("Action = %s, want %s (%#v)", got.Action.String(), tc.want.String(), got)
			}
			if tc.wantOwner && (got.Ownership.OwnerFleetUID != "fleet-uid" || got.Ownership.NodeUID != "node-uid" || got.Ownership.Key != fleetTaintKey || got.Ownership.Value != fleetTaintValue || got.Ownership.Effect != fleetTaintEffect) {
				t.Fatalf("Add omitted exact ownership tuple: %#v", got.Ownership)
			}
		})
	}
}

// GFL-068/GFL-124: an existing owned gate is maintained when an otherwise
// successful cached decision is expired; expiry clears release continuity.
func TestGFL_068_124_EvaluateGateExpiredDecisionMaintains(t *testing.T) {
	now := fleetT0.Add(20 * time.Minute)
	ownership := fleetOwnership(fleet.CleanupNone)
	nodeDecision := fleetGateNode(now.Add(-time.Minute), fleet.EligibilityEligible, fleet.QualificationQualified, fleet.SelectionComplete)
	nodeDecision.ValidUntil = now
	input := fleet.GateInput{Mode: fleet.GateModeEnforce, FleetUID: "fleet-uid", ControllerID: "controller-a", Node: fleet.NodeRef{ClusterID: "cluster-a", Name: "node-a", UID: "node-uid"}, Policy: fleetCleanupPolicy(), NodeDecision: nodeDecision, Ownership: ownership}
	previous := fleet.GateDecision{Action: fleet.GateActionMaintain, Ownership: ownership, Reason: "Validating", EvaluatedAt: now.Add(-time.Minute)}
	previous.Ownership.RecoveryStartedAt = now.Add(-10 * time.Minute)
	previous.Ownership.LastRecoveryPointAt = now.Add(-time.Minute)
	previous.Ownership.RecoveryControllerID = "controller-a"
	previous.Ownership.LastRecoveryDigest = fleetHex('e')
	got, err := fleet.EvaluateGate(input, &previous, now)
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if got.Action != fleet.GateActionMaintain || !got.Ownership.RecoveryStartedAt.IsZero() || !got.Ownership.LastRecoveryPointAt.IsZero() || got.Ownership.LastRecoveryDigest != "" {
		t.Fatalf("expired decision did not maintain/reset continuity: %#v", got)
	}
}

// GFL-015/GFL-069/GFL-076/GFL-135: cleanup never removes on cold start and
// changing controller identity discards persisted recovery continuity.
func TestGFL_015_069_076_135_EvaluateGateCleanupColdStartAndControllerChange(t *testing.T) {
	now := fleetT0.Add(20 * time.Minute)
	ownership := fleetOwnership(fleet.CleanupStable)
	ownership.CleanupRequestedAt = now.Add(-20 * time.Minute)
	ownership.RecoveryStartedAt = now.Add(-10 * time.Minute)
	ownership.LastRecoveryPointAt = now.Add(-time.Minute)
	ownership.RecoveryControllerID = "old-controller"
	ownership.LastRecoveryDigest = fleetHex('e')
	nodeDecision := fleetGateNode(now, fleet.EligibilityIneligible, fleet.QualificationQualified, fleet.SelectionComplete)
	nodeDecision.NormalPointAt = now
	nodeDecision.NormalPointDigest = fleetHex('f')
	input := fleet.GateInput{Mode: fleet.GateModeCleanup, FleetUID: "fleet-uid", ControllerID: "new-controller", Node: fleet.NodeRef{ClusterID: "cluster-a", Name: "node-a", UID: "node-uid"}, NodeDecision: nodeDecision, Ownership: ownership}
	got, err := fleet.EvaluateGate(input, nil, now)
	if err != nil {
		t.Fatalf("EvaluateGate cold cleanup: %v", err)
	}
	if got.Action == fleet.GateActionRemove || got.Ownership.RecoveryControllerID != "new-controller" || !got.Ownership.RecoveryStartedAt.Equal(now) {
		t.Fatalf("cold/controller-changed cleanup reused persisted continuity: %#v", got)
	}
}

// GFL-068/GFL-069/GFL-076/GFL-134: release requires distinct, increasing,
// fresh node normal points. Enforce uses ReadyFor; Cleanup uses max(5m, ReadyFor).
func TestGFL_068_069_076_134_EvaluateGateReleaseContinuity(t *testing.T) {
	base := fleetT0.Add(30 * time.Minute)
	node := fleet.NodeRef{ClusterID: "cluster-a", Name: "node-a", UID: "node-uid"}

	t.Run("enforce ready window", func(t *testing.T) {
		ownership := fleetOwnership(fleet.CleanupNone)
		var previous *fleet.GateDecision
		for i := 0; i <= 1; i++ {
			now := base.Add(time.Duration(i) * time.Minute)
			point := fleetGateNode(now, fleet.EligibilityEligible, fleet.QualificationQualified, fleet.SelectionComplete)
			point.NormalPointAt = now
			point.NormalPointDigest = fleetHex(byte('e' + i))
			input := fleet.GateInput{Mode: fleet.GateModeEnforce, FleetUID: "fleet-uid", ControllerID: "controller-a", Node: node, Policy: fleetCleanupPolicy(), NodeDecision: point, Ownership: ownership}
			got, err := fleet.EvaluateGate(input, previous, now)
			if err != nil {
				t.Fatalf("point %d: %v", i, err)
			}
			if i == 0 && got.Action == fleet.GateActionRemove {
				t.Fatalf("cold start removed gate: %#v", got)
			}
			if i == 1 && got.Action != fleet.GateActionRemove {
				t.Fatalf("ReadyFor continuity did not remove gate: %#v", got)
			}
			previous = &got
			ownership = got.Ownership
		}
	})

	t.Run("cleanup minimum five minutes", func(t *testing.T) {
		ownership := fleetOwnership(fleet.CleanupPending)
		ownership.CleanupRequestedAt = base.Add(-time.Minute)
		var previous *fleet.GateDecision
		for i := 0; i <= 5; i++ {
			now := base.Add(time.Duration(i) * time.Minute)
			point := fleetGateNode(now, fleet.EligibilityIneligible, fleet.QualificationQualified, fleet.SelectionComplete)
			point.NormalPointAt = now
			point.NormalPointDigest = fleetHex(byte('a' + i))
			input := fleet.GateInput{Mode: fleet.GateModeCleanup, FleetUID: "fleet-uid", ControllerID: "controller-a", Node: node, NodeDecision: point, Ownership: ownership}
			got, err := fleet.EvaluateGate(input, previous, now)
			if err != nil {
				t.Fatalf("point %d: %v", i, err)
			}
			if i < 5 && got.Action == fleet.GateActionRemove {
				t.Fatalf("cleanup removed before five minutes at point %d: %#v", i, got)
			}
			if i == 5 && got.Action != fleet.GateActionRemove {
				t.Fatalf("five-minute cleanup continuity did not remove: %#v", got)
			}
			previous = &got
			ownership = got.Ownership
		}
	})
}

// GFL-053: duplicate device UIDs and outer/inner identity mismatches are invalid
// rather than silently aggregated.
func TestGFL_053_AggregateNodeRejectsDuplicateAndMismatchedDevices(t *testing.T) {
	now := fleetT0.Add(10 * time.Minute)
	d := fleetReadyDevice(t, now, "device-a")
	base := fleet.AggregateNodeInput{Node: d.Binding.Node, FleetUID: "fleet-uid", Selection: fleet.SelectionComplete}
	cases := []struct {
		name    string
		devices []fleet.DeviceAggregate
	}{
		{"duplicate UID", []fleet.DeviceAggregate{{DeviceUID: "device-a", NodeUID: "node-uid", Desired: d.Desired, MetadataGeneration: d.MetadataGeneration, Decision: d}, {DeviceUID: "device-a", NodeUID: "node-uid", Desired: d.Desired, MetadataGeneration: d.MetadataGeneration, Decision: d}}},
		{"outer inner UID mismatch", []fleet.DeviceAggregate{{DeviceUID: "other", NodeUID: "node-uid", Desired: d.Desired, MetadataGeneration: d.MetadataGeneration, Decision: d}}},
		{"wrong node", []fleet.DeviceAggregate{{DeviceUID: "device-a", NodeUID: "other-node", Desired: d.Desired, MetadataGeneration: d.MetadataGeneration, Decision: d}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			input.Devices = tc.devices
			got, err := fleet.AggregateNode(input, nil, now)
			if !errors.Is(err, fleet.ErrInvalidInput) || !reflect.DeepEqual(got, fleet.NodeDecision{}) {
				t.Fatalf("got %#v/%v, want zero/ErrInvalidInput", got, err)
			}
		})
	}
}

// GFL-015/GFL-079/GFL-136: a nonnil partially populated previous gate decision
// and ownership mismatches are invalid and return a zero decision.
func TestGFL_015_079_136_EvaluateGateRejectsInvalidPrevious(t *testing.T) {
	now := fleetT0.Add(20 * time.Minute)
	input := fleet.GateInput{Mode: fleet.GateModeEnforce, FleetUID: "fleet-uid", ControllerID: "controller-a", Node: fleet.NodeRef{ClusterID: "cluster-a", Name: "node-a", UID: "node-uid"}, Policy: fleetCleanupPolicy(), NodeDecision: fleetGateNode(now, fleet.EligibilityUnknown, fleet.QualificationUnknown, fleet.SelectionPartial)}
	for _, previous := range []*fleet.GateDecision{{}, {Action: fleet.GateActionMaintain, Ownership: fleetOwnership(fleet.CleanupNone), Reason: "Unknown", EvaluatedAt: now}} {
		got, err := fleet.EvaluateGate(input, previous, now)
		if !errors.Is(err, fleet.ErrInvalidInput) || !reflect.DeepEqual(got, fleet.GateDecision{}) {
			t.Errorf("invalid previous returned %#v/%v, want zero/ErrInvalidInput", got, err)
		}
	}
}
