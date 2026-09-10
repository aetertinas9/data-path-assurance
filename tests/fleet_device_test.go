package tests_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// GFL-000/GFL-008/GFL-015/GFL-078/GFL-136: EvaluateDevice validates its own
// public input and any nonnil previous decision, returns no partial result, and
// never treats a zero previous pointer as cold start.
func TestGFL_000_008_015_078_136_EvaluateDeviceRejectsMalformedInput(t *testing.T) {
	now := fleetT0.Add(time.Minute)
	valid := fleetBundle(t, now, 0, fleet.DesiredInService)
	cases := []struct {
		name     string
		bundle   fleet.AssessmentBundle
		previous *fleet.DeviceDecision
		evalNow  time.Time
	}{
		{"zero bundle", fleet.AssessmentBundle{}, nil, now},
		{"zero now", valid, nil, time.Time{}},
		{"zero previous", valid, &fleet.DeviceDecision{}, now},
		{"partial previous", valid, &fleet.DeviceDecision{DeviceUID: "device-a"}, now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fleet.EvaluateDevice(tc.bundle, tc.previous, tc.evalNow)
			if !errors.Is(err, fleet.ErrInvalidInput) || !reflect.DeepEqual(got, fleet.DeviceDecision{}) {
				t.Fatalf("got %#v/%v, want zero/ErrInvalidInput", got, err)
			}
		})
	}
}

// GFL-000/GFL-078: union-like decision DTOs require a binding only in Bound,
// allow a zero ValidUntil for non-qualified outcomes, and reject partial state.
func TestGFL_000_078_DeviceDecisionUnionValidation(t *testing.T) {
	unknown := fleet.DeviceDecision{
		Desired: fleet.DesiredInService, Phase: fleet.PhasePending, Qualification: fleet.QualificationUnknown,
		BindingState: fleet.BindingUnknown, Allocation: fleet.AllocationUnknown, Reason: "UntrustedSource", EvaluatedAt: fleetT0,
		PolicyRevision: "policy-1", RequestID: "request-1", DeviceUID: "device-a", NodeUID: "node-uid", BootID: "boot-a",
		TopologyDigest: fleetHex('a'), BaselineDigest: fleetHex('b'), MetadataGeneration: 1, Session: 1, IntentObservedAt: fleetT0,
	}
	if err := unknown.Validate(); err != nil {
		t.Fatalf("valid Unknown decision rejected: %v", err)
	}
	badUnknown := unknown
	badUnknown.Binding = fleetBinding(t, fleetT0)
	if err := badUnknown.Validate(); !errors.Is(err, fleet.ErrInvalidInput) {
		t.Fatalf("Unknown with nonzero binding error = %v, want ErrInvalidInput", err)
	}
	badQualified := unknown
	badQualified.Qualification = fleet.QualificationQualified
	badQualified.Phase = fleet.PhaseReady
	if err := badQualified.Validate(); !errors.Is(err, fleet.ErrInvalidInput) {
		t.Fatalf("Qualified with zero ValidUntil/binding error = %v, want ErrInvalidInput", err)
	}
}

// GFL-010: continuity digests are lowercase SHA-256 hex and BindingKey is the
// exact raw vendor/UUID/node/boot/BDF tuple separated by NUL bytes.
func TestGFL_010_DeviceDecisionContinuityEncodingValidation(t *testing.T) {
	decision := fleetReadyDevice(t, fleetT0.Add(time.Minute), "device-a")
	if err := decision.Validate(); err != nil {
		t.Fatalf("valid continuity state rejected: %v", err)
	}
	if decision.BindingKey != "NVIDIA\x00GPU-abc\x00node-uid\x00boot-a\x000000:65:00.0" {
		t.Fatalf("BindingKey = %q", decision.BindingKey)
	}
	for _, edit := range []func(*fleet.DeviceDecision){
		func(d *fleet.DeviceDecision) { d.TopologyDigest = "short" },
		func(d *fleet.DeviceDecision) { d.BaselineDigest = fleetHex('A') },
		func(d *fleet.DeviceDecision) { d.CoverageCursors[0].EvidenceDigest = fleetHex('g') },
	} {
		bad := decision
		bad.CoverageCursors = append([]fleet.CoverageCursor(nil), decision.CoverageCursors...)
		edit(&bad)
		if err := bad.Validate(); !errors.Is(err, fleet.ErrInvalidInput) {
			t.Errorf("invalid continuity encoding error = %v, want ErrInvalidInput", err)
		}
	}
}

// GFL-018/GFL-043/GFL-044/GFL-046/GFL-078/GFL-085/GFL-087/GFL-123/GFL-125:
// optional allocation and fence inputs are valid pending facts. Their DTOs
// enforce complete, fresh, correlated structures before evaluators can use them.
func TestGFL_018_043_044_046_078_085_087_123_125_AllocationFenceValidation(t *testing.T) {
	at := fleetT0.Add(time.Minute)
	batch := fleet.AllocationBatch{
		NodeUID: "node-uid", BootID: "boot-a", BundleRevision: "bundle-1", EvidenceDigest: fleetHex('a'),
		Session: 2, Sequence: 4, ObservedAt: at, ExpiresAt: at.Add(time.Minute), Complete: true,
		EvidenceRefs: []string{"allocation-ev"}, Profile: fleet.AllocationNVIDIAPodResourcesUUID, CollectorProfileID: "profile-1",
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("complete empty allocation batch rejected: %v", err)
	}
	partial := batch
	partial.Complete = false
	if err := partial.Validate(); err != nil {
		t.Fatalf("structurally valid partial allocation batch rejected: %v", err)
	}
	staleShape := batch
	staleShape.ExpiresAt = staleShape.ObservedAt
	if err := staleShape.Validate(); !errors.Is(err, fleet.ErrInvalidInput) {
		t.Fatalf("non-increasing allocation expiry error = %v, want ErrInvalidInput", err)
	}

	fenceSource := fleetBinding(t, at).Source
	trust := fleet.FenceTrustProfile{ID: "fence-profile", ClusterID: "cluster-a", Issuer: "maintenance", Source: fenceSource}
	ack := fleet.FenceAcknowledgement{
		Node: fleet.NodeRef{ClusterID: "cluster-a", Name: "node-a", UID: "node-uid"}, DeviceUID: "device-a", BootID: "boot-a", Session: 2,
		RequestID: "request-1", EvidenceID: "fence-ev", Source: fenceSource, MetadataGeneration: 3,
		State: fleet.FenceAcknowledged, ObservedAt: at, ExpiresAt: at.Add(time.Minute), TrustProfileID: "fence-profile",
	}
	if err := trust.Validate(); err != nil {
		t.Fatalf("FenceTrustProfile: %v", err)
	}
	if err := ack.Validate(); err != nil {
		t.Fatalf("FenceAcknowledgement: %v", err)
	}
	badAck := ack
	badAck.ExpiresAt = badAck.ObservedAt
	if err := badAck.Validate(); !errors.Is(err, fleet.ErrInvalidInput) {
		t.Fatalf("non-increasing fence expiry error = %v, want ErrInvalidInput", err)
	}
}

// GFL-007: an evaluator neither rewrites caller-owned nested input nor exposes
// result slices that remain linked to those inputs.
func TestGFL_007_EvaluateDeviceCopiesNestedInputAndOutput(t *testing.T) {
	at := fleetT0.Add(time.Minute)
	bundle := fleetBundle(t, at, 0, fleet.DesiredInService)
	wantRequirement := bundle.Policy.RequiredCoverage[0]
	wantEvidence := bundle.Provenance[0].EvidenceID
	decision, err := fleet.EvaluateDevice(bundle, nil, at)
	if err != nil {
		t.Fatalf("EvaluateDevice: %v", err)
	}
	if bundle.Policy.RequiredCoverage[0] != wantRequirement || bundle.Provenance[0].EvidenceID != wantEvidence {
		t.Fatalf("evaluator mutated caller input: %#v / %#v", bundle.Policy.RequiredCoverage, bundle.Provenance)
	}
	if len(decision.Coverage) == 0 || len(decision.Coverage[0].EvidenceIDs) == 0 {
		t.Fatalf("fixture produced no nested result: %#v", decision)
	}
	bundle.Policy.RequiredCoverage[0].Name = "mutated-after-call"
	bundle.Provenance[0].EvidenceID = "mutated-after-call"
	if decision.Coverage[0].Name == "mutated-after-call" || decision.Coverage[0].EvidenceIDs[0] == "mutated-after-call" {
		t.Fatalf("result shares nested evaluator input: %#v", decision.Coverage)
	}
}
