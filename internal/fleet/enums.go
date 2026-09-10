package fleet

import "strconv"

type DesiredState int

const (
	DesiredInService DesiredState = iota + 1
	DesiredMaintenance
	DesiredRetired
)

type LifecyclePhase int

const (
	PhasePending LifecyclePhase = iota + 1
	PhaseValidating
	PhaseReady
	PhaseDegraded
	PhaseMaintenancePending
	PhaseMaintenanceReady
	PhaseRetiring
	PhaseRetired
	PhaseUnknown
)

type Qualification int

const (
	QualificationQualified Qualification = iota + 1
	QualificationDisqualified
	QualificationUnknown
)

type Eligibility int

const (
	EligibilityEligible Eligibility = iota + 1
	EligibilityIneligible
	EligibilityUnknown
)

type BindingState int

const (
	BindingBound BindingState = iota + 1
	BindingUnknown
	BindingConflict
)

type SelectionState int

const (
	SelectionComplete SelectionState = iota + 1
	SelectionPartial
	SelectionConflict
	SelectionNoDevices
)

type CoverageState int

const (
	CoverageNormal CoverageState = iota + 1
	CoverageMissing
	CoverageUnknown
	CoverageUnsupported
)

type SnapshotCompleteness int

const (
	CompletenessUnknown SnapshotCompleteness = iota
	CompletenessComplete
	CompletenessPartial
)

type EdgeEvidenceKind int

const (
	EdgeEvidenceObserved EdgeEvidenceKind = iota + 1
	EdgeEvidenceIntended
	EdgeEvidenceInferred
)

type AllocationState int

const (
	AllocationEmpty AllocationState = iota + 1
	AllocationInUse
	AllocationUnknown
)

type FenceState int

const (
	FenceAcknowledged FenceState = iota + 1
	FenceNotAcknowledged
	FenceUnknown
)

type SnapshotOrder int

const (
	SnapshotAccepted SnapshotOrder = iota + 1
	SnapshotDuplicate
	SnapshotOutOfOrder
	SnapshotGap
	SnapshotWrongSession
	SnapshotConflict
)

type TrustMode int

const (
	TrustModeLive TrustMode = iota + 1
	TrustModeOffline
)

type TrustCapability int

const (
	TrustNVIDIAUUIDBinding TrustCapability = iota + 1
	TrustSysfsPhysicalParent
	TrustSysfsPCIeWidth
	TrustPodResourcesUUIDAllocation
	TrustOperatorBaseline
	TrustExternalFence
)

type AllocationProfile int

const (
	AllocationNVIDIAPodResourcesUUID AllocationProfile = iota + 1
	AllocationUnsupported
)

type GateMode int

const (
	GateModeAudit GateMode = iota + 1
	GateModeEnforce
	GateModeCleanup
)

type GateAction int

const (
	GateActionNone GateAction = iota + 1
	GateActionAdd
	GateActionMaintain
	GateActionRemove
)

type CleanupPhase int

const (
	CleanupNone CleanupPhase = iota + 1
	CleanupPending
	CleanupStable
	CleanupCompleted
	CleanupOrphaned
	CleanupOwnershipConflict
)

func enumString(v, first int, names []string, typ string) string {
	i := v - first
	if i >= 0 && i < len(names) {
		return names[i]
	}
	return typ + "(" + strconv.Itoa(v) + ")"
}
func enumValid(v, first int, names []string) bool { return v >= first && v-first < len(names) }

var desiredNames = []string{"InService", "Maintenance", "Retired"}

func (v DesiredState) String() string { return enumString(int(v), 1, desiredNames, "DesiredState") }
func (v DesiredState) IsValid() bool  { return enumValid(int(v), 1, desiredNames) }

var phaseNames = []string{"Pending", "Validating", "Ready", "Degraded", "MaintenancePending", "MaintenanceReady", "Retiring", "Retired", "Unknown"}

func (v LifecyclePhase) String() string { return enumString(int(v), 1, phaseNames, "LifecyclePhase") }
func (v LifecyclePhase) IsValid() bool  { return enumValid(int(v), 1, phaseNames) }

var qualificationNames = []string{"Qualified", "Disqualified", "Unknown"}

func (v Qualification) String() string {
	return enumString(int(v), 1, qualificationNames, "Qualification")
}
func (v Qualification) IsValid() bool { return enumValid(int(v), 1, qualificationNames) }

var eligibilityNames = []string{"Eligible", "Ineligible", "Unknown"}

func (v Eligibility) String() string { return enumString(int(v), 1, eligibilityNames, "Eligibility") }
func (v Eligibility) IsValid() bool  { return enumValid(int(v), 1, eligibilityNames) }

var bindingNames = []string{"Bound", "Unknown", "Conflict"}

func (v BindingState) String() string { return enumString(int(v), 1, bindingNames, "BindingState") }
func (v BindingState) IsValid() bool  { return enumValid(int(v), 1, bindingNames) }

var selectionNames = []string{"Complete", "Partial", "Conflict", "NoDevices"}

func (v SelectionState) String() string {
	return enumString(int(v), 1, selectionNames, "SelectionState")
}
func (v SelectionState) IsValid() bool { return enumValid(int(v), 1, selectionNames) }

var coverageNames = []string{"Normal", "Missing", "Unknown", "Unsupported"}

func (v CoverageState) String() string { return enumString(int(v), 1, coverageNames, "CoverageState") }
func (v CoverageState) IsValid() bool  { return enumValid(int(v), 1, coverageNames) }

var completenessNames = []string{"Unknown", "Complete", "Partial"}

func (v SnapshotCompleteness) String() string {
	return enumString(int(v), 0, completenessNames, "SnapshotCompleteness")
}
func (v SnapshotCompleteness) IsValid() bool { return enumValid(int(v), 0, completenessNames) }

var edgeEvidenceNames = []string{"Observed", "Intended", "Inferred"}

func (v EdgeEvidenceKind) String() string {
	return enumString(int(v), 1, edgeEvidenceNames, "EdgeEvidenceKind")
}
func (v EdgeEvidenceKind) IsValid() bool { return enumValid(int(v), 1, edgeEvidenceNames) }

var allocationNames = []string{"Empty", "InUse", "Unknown"}

func (v AllocationState) String() string {
	return enumString(int(v), 1, allocationNames, "AllocationState")
}
func (v AllocationState) IsValid() bool { return enumValid(int(v), 1, allocationNames) }

var fenceNames = []string{"Acknowledged", "NotAcknowledged", "Unknown"}

func (v FenceState) String() string { return enumString(int(v), 1, fenceNames, "FenceState") }
func (v FenceState) IsValid() bool  { return enumValid(int(v), 1, fenceNames) }

var snapshotOrderNames = []string{"Accepted", "Duplicate", "OutOfOrder", "Gap", "WrongSession", "Conflict"}

func (v SnapshotOrder) String() string {
	return enumString(int(v), 1, snapshotOrderNames, "SnapshotOrder")
}
func (v SnapshotOrder) IsValid() bool { return enumValid(int(v), 1, snapshotOrderNames) }

var trustModeNames = []string{"Live", "Offline"}

func (v TrustMode) String() string { return enumString(int(v), 1, trustModeNames, "TrustMode") }
func (v TrustMode) IsValid() bool  { return enumValid(int(v), 1, trustModeNames) }

var trustCapabilityNames = []string{"NVIDIAUUIDBinding", "SysfsPhysicalParent", "SysfsPCIeWidth", "PodResourcesUUIDAllocation", "OperatorBaseline", "ExternalFence"}

func (v TrustCapability) String() string {
	return enumString(int(v), 1, trustCapabilityNames, "TrustCapability")
}
func (v TrustCapability) IsValid() bool { return enumValid(int(v), 1, trustCapabilityNames) }

var allocationProfileNames = []string{"NVIDIAPodResourcesUUID", "Unsupported"}

func (v AllocationProfile) String() string {
	return enumString(int(v), 1, allocationProfileNames, "AllocationProfile")
}
func (v AllocationProfile) IsValid() bool { return enumValid(int(v), 1, allocationProfileNames) }

var gateModeNames = []string{"Audit", "Enforce", "Cleanup"}

func (v GateMode) String() string { return enumString(int(v), 1, gateModeNames, "GateMode") }
func (v GateMode) IsValid() bool  { return enumValid(int(v), 1, gateModeNames) }

var gateActionNames = []string{"None", "Add", "Maintain", "Remove"}

func (v GateAction) String() string { return enumString(int(v), 1, gateActionNames, "GateAction") }
func (v GateAction) IsValid() bool  { return enumValid(int(v), 1, gateActionNames) }

var cleanupNames = []string{"None", "Pending", "Stable", "Completed", "Orphaned", "OwnershipConflict"}

func (v CleanupPhase) String() string { return enumString(int(v), 1, cleanupNames, "CleanupPhase") }
func (v CleanupPhase) IsValid() bool  { return enumValid(int(v), 1, cleanupNames) }
