package v1alpha1

// FleetMode is the enforcement mode of a GPUFleet. It exists only in the API;
// the domain core does not know it.
//
// +kubebuilder:validation:Enum=Audit;Enforce
type FleetMode string

const (
	FleetModeAudit   FleetMode = "Audit"
	FleetModeEnforce FleetMode = "Enforce"
)

// DesiredState is the lifecycle state an operator asks a GPUDevice to reach.
//
// +kubebuilder:validation:Enum=InService;Maintenance;Retired
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=256
type DesiredState string

const (
	DesiredStateInService   DesiredState = "InService"
	DesiredStateMaintenance DesiredState = "Maintenance"
	DesiredStateRetired     DesiredState = "Retired"
)

// Qualification is the path qualification verdict of a device or node.
//
// +kubebuilder:validation:Enum=Qualified;Disqualified;Unknown
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=256
type Qualification string

const (
	QualificationQualified    Qualification = "Qualified"
	QualificationDisqualified Qualification = "Disqualified"
	QualificationUnknown      Qualification = "Unknown"
)

// LifecyclePhase is the lifecycle phase a GPUDevice is observed in.
//
// +kubebuilder:validation:Enum=Pending;Validating;Ready;Degraded;MaintenancePending;MaintenanceReady;Retiring;Retired;Unknown
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=256
type LifecyclePhase string

const (
	LifecyclePhasePending            LifecyclePhase = "Pending"
	LifecyclePhaseValidating         LifecyclePhase = "Validating"
	LifecyclePhaseReady              LifecyclePhase = "Ready"
	LifecyclePhaseDegraded           LifecyclePhase = "Degraded"
	LifecyclePhaseMaintenancePending LifecyclePhase = "MaintenancePending"
	LifecyclePhaseMaintenanceReady   LifecyclePhase = "MaintenanceReady"
	LifecyclePhaseRetiring           LifecyclePhase = "Retiring"
	LifecyclePhaseRetired            LifecyclePhase = "Retired"
	LifecyclePhaseUnknown            LifecyclePhase = "Unknown"
)

// BindingState is the state of the binding between a GPUDevice claim and the
// observed hardware.
//
// +kubebuilder:validation:Enum=Bound;Unknown;Conflict
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=256
type BindingState string

const (
	BindingStateBound    BindingState = "Bound"
	BindingStateUnknown  BindingState = "Unknown"
	BindingStateConflict BindingState = "Conflict"
)

// AllocationState tells whether workloads currently use a device.
//
// +kubebuilder:validation:Enum=Empty;InUse;Unknown
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=256
type AllocationState string

const (
	AllocationStateEmpty   AllocationState = "Empty"
	AllocationStateInUse   AllocationState = "InUse"
	AllocationStateUnknown AllocationState = "Unknown"
)

// CoverageState is the state of one required evidence coverage item.
//
// +kubebuilder:validation:Enum=Normal;Missing;Unknown;Unsupported
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=256
type CoverageState string

const (
	CoverageStateNormal      CoverageState = "Normal"
	CoverageStateMissing     CoverageState = "Missing"
	CoverageStateUnknown     CoverageState = "Unknown"
	CoverageStateUnsupported CoverageState = "Unsupported"
)

// SnapshotCompleteness tells how complete the evidence snapshot of a node is.
//
// +kubebuilder:validation:Enum=Unknown;Complete;Partial
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=256
type SnapshotCompleteness string

const (
	SnapshotCompletenessUnknown  SnapshotCompleteness = "Unknown"
	SnapshotCompletenessComplete SnapshotCompleteness = "Complete"
	SnapshotCompletenessPartial  SnapshotCompleteness = "Partial"
)

// CleanupPhase is the phase of the gate cleanup of a node.
//
// +kubebuilder:validation:Enum=None;Pending;Stable;Completed;Orphaned;OwnershipConflict
type CleanupPhase string

const (
	CleanupPhaseNone              CleanupPhase = "None"
	CleanupPhasePending           CleanupPhase = "Pending"
	CleanupPhaseStable            CleanupPhase = "Stable"
	CleanupPhaseCompleted         CleanupPhase = "Completed"
	CleanupPhaseOrphaned          CleanupPhase = "Orphaned"
	CleanupPhaseOwnershipConflict CleanupPhase = "OwnershipConflict"
)

// Condition types published in status.conditions of the three resources. The
// value is the name without the ConditionType prefix.
const (
	ConditionTypeFleetReady      = "FleetReady"
	ConditionTypeDeviceQualified = "DeviceQualified"
	ConditionTypeLifecycleReady  = "LifecycleReady"
	ConditionTypeEvidenceFresh   = "EvidenceFresh"
	ConditionTypeIdentityBound   = "IdentityBound"
	ConditionTypeAllocationKnown = "AllocationKnown"
	ConditionTypeNodeEligible    = "NodeEligible"
	ConditionTypeGateOwned       = "GateOwned"
	ConditionTypeCleanupReady    = "CleanupReady"
)

// Condition reasons: the closed vocabulary of 22 values every published
// condition draws from. The value is the name without the Reason prefix.
const (
	ReasonReady                  = "Ready"
	ReasonValidating             = "Validating"
	ReasonDegraded               = "Degraded"
	ReasonCoverageMissing        = "CoverageMissing"
	ReasonEvidenceStale          = "EvidenceStale"
	ReasonIdentityConflict       = "IdentityConflict"
	ReasonUIDMismatch            = "UIDMismatch"
	ReasonAllocationUnknown      = "AllocationUnknown"
	ReasonFenceMissing           = "FenceMissing"
	ReasonConflict               = "Conflict"
	ReasonNoMatchingDevices      = "NoMatchingDevices"
	ReasonPartialSnapshot        = "PartialSnapshot"
	ReasonBundleMismatch         = "BundleMismatch"
	ReasonFutureObservation      = "FutureObservation"
	ReasonUntrustedSource        = "UntrustedSource"
	ReasonUnadmittedSnapshot     = "UnadmittedSnapshot"
	ReasonTargetCapacityExceeded = "TargetCapacityExceeded"
	ReasonCleanupPending         = "CleanupPending"
	ReasonCleanupStable          = "CleanupStable"
	ReasonCleanupCompleted       = "CleanupCompleted"
	ReasonOwnershipConflict      = "OwnershipConflict"
	ReasonInternalError          = "InternalError"
)

// Coverage path kinds accepted in requiredCoverage[].pathKind and reported in
// coverage[].pathKind.
const (
	PathKindGPUPCIeParent          = "gpu-pcie-parent"
	PathKindGPUPCIeRoot            = "gpu-pcie-root"
	PathKindGPUPCIeLinkWidthNormal = "gpu-pcie-link-width-normal"
	PathKindGPUNICSharedAncestor   = "gpu-nic-shared-ancestor"
	PathKindNICLLDPRemote          = "nic-lldp-remote"
)

// NodeConditionTypeGPUFleetReady is the type of the custom condition the
// controller publishes on Node objects.
const NodeConditionTypeGPUFleetReady = "DataPathGPUFleetReady"
