package tests_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

type fleetEnum interface {
	String() string
	IsValid() bool
}

// GFL-000: every declared enum exposes the specified valid values and strips
// its type prefix from String. Unknown signed values remain invalid.
func TestGFL_000_PublicEnums(t *testing.T) {
	cases := []struct {
		value fleetEnum
		want  string
	}{
		{fleet.DesiredInService, "InService"}, {fleet.DesiredMaintenance, "Maintenance"}, {fleet.DesiredRetired, "Retired"},
		{fleet.PhasePending, "Pending"}, {fleet.PhaseValidating, "Validating"}, {fleet.PhaseReady, "Ready"}, {fleet.PhaseDegraded, "Degraded"},
		{fleet.PhaseMaintenancePending, "MaintenancePending"}, {fleet.PhaseMaintenanceReady, "MaintenanceReady"}, {fleet.PhaseRetiring, "Retiring"}, {fleet.PhaseRetired, "Retired"}, {fleet.PhaseUnknown, "Unknown"},
		{fleet.QualificationQualified, "Qualified"}, {fleet.QualificationDisqualified, "Disqualified"}, {fleet.QualificationUnknown, "Unknown"},
		{fleet.EligibilityEligible, "Eligible"}, {fleet.EligibilityIneligible, "Ineligible"}, {fleet.EligibilityUnknown, "Unknown"},
		{fleet.BindingBound, "Bound"}, {fleet.BindingUnknown, "Unknown"}, {fleet.BindingConflict, "Conflict"},
		{fleet.SelectionComplete, "Complete"}, {fleet.SelectionPartial, "Partial"}, {fleet.SelectionConflict, "Conflict"}, {fleet.SelectionNoDevices, "NoDevices"},
		{fleet.CoverageNormal, "Normal"}, {fleet.CoverageMissing, "Missing"}, {fleet.CoverageUnknown, "Unknown"}, {fleet.CoverageUnsupported, "Unsupported"},
		{fleet.CompletenessUnknown, "Unknown"}, {fleet.CompletenessComplete, "Complete"}, {fleet.CompletenessPartial, "Partial"},
		{fleet.EdgeEvidenceObserved, "Observed"}, {fleet.EdgeEvidenceIntended, "Intended"}, {fleet.EdgeEvidenceInferred, "Inferred"},
		{fleet.AllocationEmpty, "Empty"}, {fleet.AllocationInUse, "InUse"}, {fleet.AllocationUnknown, "Unknown"},
		{fleet.FenceAcknowledged, "Acknowledged"}, {fleet.FenceNotAcknowledged, "NotAcknowledged"}, {fleet.FenceUnknown, "Unknown"},
		{fleet.SnapshotAccepted, "Accepted"}, {fleet.SnapshotDuplicate, "Duplicate"}, {fleet.SnapshotOutOfOrder, "OutOfOrder"}, {fleet.SnapshotGap, "Gap"}, {fleet.SnapshotWrongSession, "WrongSession"}, {fleet.SnapshotConflict, "Conflict"},
		{fleet.TrustModeLive, "Live"}, {fleet.TrustModeOffline, "Offline"},
		{fleet.TrustNVIDIAUUIDBinding, "NVIDIAUUIDBinding"}, {fleet.TrustSysfsPhysicalParent, "SysfsPhysicalParent"}, {fleet.TrustSysfsPCIeWidth, "SysfsPCIeWidth"}, {fleet.TrustPodResourcesUUIDAllocation, "PodResourcesUUIDAllocation"}, {fleet.TrustOperatorBaseline, "OperatorBaseline"}, {fleet.TrustExternalFence, "ExternalFence"},
		{fleet.AllocationNVIDIAPodResourcesUUID, "NVIDIAPodResourcesUUID"}, {fleet.AllocationUnsupported, "Unsupported"},
		{fleet.GateModeAudit, "Audit"}, {fleet.GateModeEnforce, "Enforce"}, {fleet.GateModeCleanup, "Cleanup"},
		{fleet.GateActionNone, "None"}, {fleet.GateActionAdd, "Add"}, {fleet.GateActionMaintain, "Maintain"}, {fleet.GateActionRemove, "Remove"},
		{fleet.CleanupNone, "None"}, {fleet.CleanupPending, "Pending"}, {fleet.CleanupStable, "Stable"}, {fleet.CleanupCompleted, "Completed"}, {fleet.CleanupOrphaned, "Orphaned"}, {fleet.CleanupOwnershipConflict, "OwnershipConflict"},
	}
	for _, tc := range cases {
		if !tc.value.IsValid() || tc.value.String() != tc.want {
			t.Errorf("%T(%s): IsValid=%v String=%q, want true/%q", tc.value, tc.want, tc.value.IsValid(), tc.value.String(), tc.want)
		}
	}
	invalid := []fleetEnum{
		fleet.DesiredState(-1), fleet.LifecyclePhase(-1), fleet.Qualification(-1), fleet.Eligibility(-1),
		fleet.BindingState(-1), fleet.SelectionState(-1), fleet.CoverageState(-1), fleet.SnapshotCompleteness(-1),
		fleet.EdgeEvidenceKind(-1), fleet.AllocationState(-1), fleet.FenceState(-1), fleet.SnapshotOrder(-1),
		fleet.TrustMode(-1), fleet.TrustCapability(-1), fleet.AllocationProfile(-1), fleet.GateMode(-1),
		fleet.GateAction(-1), fleet.CleanupPhase(-1),
	}
	for _, value := range invalid {
		if value.IsValid() {
			t.Errorf("unknown %T(-1) is valid", value)
		}
		_ = value.String() // Must not panic for unknown signed values.
	}
}

// GFL-000: every public DTO can be independently validated even when it was
// assembled without a constructor.
func TestGFL_000_AllPublicStructsExposeValidate(t *testing.T) {
	values := []any{
		fleet.NodeRef{}, fleet.InventoryClaim{}, fleet.ObservedBinding{}, fleet.SnapshotCursor{}, fleet.SnapshotEnvelope{}, fleet.DeviceRef{}, fleet.EdgeProvenance{},
		fleet.CoverageRequirement{}, fleet.CoverageAssessment{}, fleet.TrustedSource{}, fleet.CollectorTrustProfile{}, fleet.FenceTrustProfile{}, fleet.WorkloadRef{},
		fleet.AllocationEntry{}, fleet.AllocationBatch{}, fleet.FenceAcknowledgement{}, fleet.Policy{}, fleet.Intent{}, fleet.AssessmentBundle{}, fleet.CoverageCursor{},
		fleet.DeviceDecision{}, fleet.DeviceAggregate{}, fleet.AggregateNodeInput{}, fleet.NodeDecision{}, fleet.CleanupPolicySnapshot{}, fleet.GateOwnership{}, fleet.GateInput{}, fleet.GateDecision{},
	}
	for _, value := range values {
		validator, ok := value.(interface{ Validate() error })
		if !ok {
			t.Errorf("%T does not expose Validate() error", value)
			continue
		}
		_ = validator.Validate()
	}
}

// GFL-001/GFL-002/GFL-003/GFL-008/GFL-009/GFL-014/GFL-078: public validation
// preserves raw identity bytes and rejects only the specified malformed forms.
func TestGFL_001_002_003_008_009_014_016_078_PublicValidation(t *testing.T) {
	validNode := fleet.NodeRef{ClusterID: " cluster ", Name: "Node-A", UID: " UID-A "}
	if err := validNode.Validate(); err != nil {
		t.Fatalf("raw nonempty node strings rejected: %v", err)
	}
	for _, value := range []interface{ Validate() error }{
		fleet.NodeRef{Name: "n", UID: "u"},
		fleet.NodeRef{ClusterID: "c", UID: "u"},
		fleet.NodeRef{ClusterID: "c", Name: "n"},
		fleet.DeviceRef{Name: "gpu"},
		fleet.DeviceRef{UID: "gpu-uid"},
		fleet.InventoryClaim{Vendor: "NVIDIA", UUID: "uuid", Source: "inventory"},
		fleet.InventoryClaim{Vendor: "NVIDIA", UUID: "uuid", EvidenceID: "ev"},
		fleet.InventoryClaim{Vendor: "NVIDIA", Source: "inventory", EvidenceID: "ev"},
		fleet.CoverageRequirement{Name: "x", PathKind: "invented", Required: true},
		fleet.SnapshotEnvelope{NodeUID: "u", BootID: "b", PayloadDigest: "d", BundleRevision: "r", Session: 1, Completeness: fleet.CompletenessUnknown, ObservedAt: time.Unix(1, 0)},
	} {
		if err := value.Validate(); !errors.Is(err, fleet.ErrInvalidInput) {
			t.Errorf("%T.Validate error = %v, want ErrInvalidInput", value, err)
		}
	}

	profile := fleet.CollectorTrustProfile{
		ID: "offline:fixture", Mode: fleet.TrustModeOffline, ClusterID: "cluster", NodeUID: "node", Session: 1,
	}
	if err := profile.Validate(); err != nil {
		t.Fatalf("valid offline trust profile rejected: %v", err)
	}
	profile.Session = 0
	if err := profile.Validate(); !errors.Is(err, fleet.ErrInvalidInput) {
		t.Fatalf("offline zero session error = %v, want ErrInvalidInput", err)
	}
}

// GFL-007/GFL-008: NewPolicy validates durations and duplicate names, sorts a
// defensive copy, and does not share nested input or output slices.
func TestGFL_007_008_NewPolicyDefensiveCopyAndDeterminism(t *testing.T) {
	requirements := []fleet.CoverageRequirement{
		{Name: "z", PathKind: "gpu-pcie-root", Required: true},
		{Name: "a", PathKind: "gpu-pcie-parent", Required: true},
	}
	input := fleet.Policy{Revision: "policy-1", RequiredCoverage: requirements, Freshness: time.Minute, ReadyFor: time.Minute}
	first, err := fleet.NewPolicy(input)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	second, err := fleet.NewPolicy(input)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("same policy input was not deterministic: %v / %#v %#v", err, first, second)
	}
	if got := []string{first.RequiredCoverage[0].Name, first.RequiredCoverage[1].Name}; !reflect.DeepEqual(got, []string{"a", "z"}) {
		t.Fatalf("coverage order = %v, want [a z]", got)
	}
	requirements[0].Name = "mutated-input"
	first.RequiredCoverage[0].Name = "mutated-output"
	third, err := fleet.NewPolicy(input)
	if err != nil {
		t.Fatalf("NewPolicy after output mutation: %v", err)
	}
	if third.RequiredCoverage[0].Name != "a" {
		t.Fatalf("policy shared nested storage: %#v", third.RequiredCoverage)
	}

	invalid := []fleet.Policy{
		{Revision: "p", RequiredCoverage: []fleet.CoverageRequirement{{Name: "a", PathKind: "gpu-pcie-root", Required: true}}, Freshness: 0, ReadyFor: time.Second},
		{Revision: "p", RequiredCoverage: []fleet.CoverageRequirement{{Name: "a", PathKind: "gpu-pcie-root", Required: true}}, Freshness: time.Second, ReadyFor: 0},
		{Revision: "p", RequiredCoverage: []fleet.CoverageRequirement{{Name: "a", PathKind: "gpu-pcie-root"}, {Name: "a", PathKind: "gpu-pcie-parent"}}, Freshness: time.Second, ReadyFor: time.Second},
	}
	for _, policy := range invalid {
		got, err := fleet.NewPolicy(policy)
		if !errors.Is(err, fleet.ErrInvalidInput) || !reflect.DeepEqual(got, fleet.Policy{}) {
			t.Errorf("invalid policy returned %#v, %v; want zero/ErrInvalidInput", got, err)
		}
	}
}
